package agentcli

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/riba2534/herdrx/internal/herdrpaths"
)

// 受控端启动时自动拉起默认会话的 Herdr。
//
// Herdr 必须独立于本服务运行：herdrx 升级、重启或卸载都不能结束 Herdr、pane 和其中
// 的任务。Linux 上用 `systemd-run --user --scope` 把 Herdr 放进用户 systemd 的独立
// scope，脱离 herdrx.service 的 cgroup；没有可用的用户 systemd 时用新会话启动
// （与 Herdr 客户端自己拉起服务端的方式相同）。若本进程处在 systemd 服务里却无法
// 创建独立 scope，就不拉起——那样启动的 Herdr 会随 herdrx 服务重启一起被结束。
//
// 只在没有 Herdr 应答且 socket 不存在或拒绝连接时拉起；已在运行或状态不明的 Herdr
// 一律不动。设置 HERDRX_HERDR_AUTOSTART=0 可关闭。

const herdrAutostartEnv = "HERDRX_HERDR_AUTOSTART"

type herdrAutostart struct {
	logger     *slog.Logger
	socket     string
	lookPath   func(string) (string, error)
	getenv     func(string) string
	probe      func(context.Context, ...string) error // runs a short command, nil on success
	launch     func(*exec.Cmd) error
	dial       func(string) error
	goos       string
	readyAfter time.Duration
}

func newHerdrAutostart(env Environment, logger *slog.Logger) (*herdrAutostart, error) {
	dir, err := herdrpaths.HerdrDir()
	if err != nil {
		return nil, err
	}
	lookPath := env.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	return &herdrAutostart{
		logger:   logger,
		socket:   filepath.Join(dir, "herdr.sock"),
		lookPath: lookPath,
		getenv:   os.Getenv,
		probe: func(ctx context.Context, args ...string) error {
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			return exec.CommandContext(ctx, args[0], args[1:]...).Run()
		},
		launch: func(cmd *exec.Cmd) error {
			if err := cmd.Start(); err != nil {
				return err
			}
			// Reap the launcher; with --scope it is the Herdr server itself.
			go func() { _ = cmd.Wait() }()
			return nil
		},
		dial: func(path string) error {
			conn, err := net.DialTimeout("unix", path, time.Second)
			if err == nil {
				_ = conn.Close()
			}
			return err
		},
		goos:       runtime.GOOS,
		readyAfter: 10 * time.Second,
	}, nil
}

// autostartHerdr is the production hook wired into DefaultEnv.
func autostartHerdr(ctx context.Context, env Environment, logger *slog.Logger) {
	starter, err := newHerdrAutostart(env, logger)
	if err != nil {
		logger.Warn("herdr autostart skipped", "reason", err.Error())
		return
	}
	starter.run(ctx)
}

func (a *herdrAutostart) disabled() bool {
	switch strings.ToLower(strings.TrimSpace(a.getenv(herdrAutostartEnv))) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

func (a *herdrAutostart) run(ctx context.Context) {
	if a.disabled() {
		a.logger.Info("herdr autostart disabled", "env", herdrAutostartEnv)
		return
	}
	err := a.dial(a.socket)
	if err == nil {
		return
	}
	if !errors.Is(err, syscall.ENOENT) && !errors.Is(err, syscall.ECONNREFUSED) {
		// Permission problems or anything unexpected: never touch a server we cannot see.
		a.logger.Warn("herdr autostart skipped", "socket", a.socket, "reason", err.Error())
		return
	}
	herdrBin, err := a.lookPath("herdr")
	if err != nil {
		for _, candidate := range []string{filepath.Join(a.getenv("HOME"), ".local", "bin", "herdr"), "/usr/local/bin/herdr"} {
			if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				herdrBin, err = candidate, nil
				break
			}
		}
	}
	if err != nil {
		a.logger.Info("herdr autostart skipped", "reason", "herdr is not installed")
		return
	}
	if !filepath.IsAbs(herdrBin) {
		a.logger.Info("herdr autostart skipped", "reason", "herdr is not installed")
		return
	}
	cmd, method, reason := a.command(ctx, herdrBin)
	if cmd == nil {
		a.logger.Warn("herdr autostart skipped", "reason", reason)
		return
	}
	if err := a.launch(cmd); err != nil {
		a.logger.Warn("herdr autostart failed", "method", method, "error", err.Error())
		return
	}
	deadline := time.Now().Add(a.readyAfter)
	for time.Now().Before(deadline) {
		if a.dial(a.socket) == nil {
			a.logger.Info("herdr started", "method", method, "socket", a.socket)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
	a.logger.Warn("herdr did not become ready", "method", method, "socket", a.socket)
}

// command builds the detached launch. The server starts through the user's
// login shell so it sees the same PATH and locale as a terminal login.
func (a *herdrAutostart) command(ctx context.Context, herdrBin string) (*exec.Cmd, string, string) {
	shell := a.loginShell()
	server := []string{shell, "-lc", `exec "$0" "$@"`, herdrBin, "server"}
	if a.goos == "linux" {
		if systemdRun, err := a.lookPath("systemd-run"); err == nil {
			if systemctl, err := a.lookPath("systemctl"); err == nil && a.probe(ctx, systemctl, "--user", "show-environment") == nil {
				unit := "herdrx-herdr-default-" + strconv.FormatInt(time.Now().Unix(), 10)
				args := append([]string{"--user", "--scope", "--quiet", "--collect", "--unit=" + unit, "--"}, server...)
				return a.detached(exec.Command(systemdRun, args...)), "systemd", ""
			}
		}
		if a.getenv("INVOCATION_ID") != "" {
			return nil, "", "running under systemd without a usable user manager; Herdr would share this service's lifetime"
		}
	}
	return a.detached(exec.Command(server[0], server[1:]...)), "setsid", ""
}

func (a *herdrAutostart) loginShell() string {
	shell := a.getenv("SHELL")
	switch filepath.Base(shell) {
	case "bash", "zsh", "sh", "dash", "ksh", "mksh":
		if filepath.IsAbs(shell) {
			return shell
		}
	}
	return "/bin/sh"
}

// detached gives the launch its own session and no inherited stdio, so neither
// a terminal hangup nor the service manager's process-group cleanup reaches it.
func (a *herdrAutostart) detached(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.Dir = "/"
	if home := a.getenv("HOME"); filepath.IsAbs(home) {
		cmd.Dir = home
	}
	return cmd
}
