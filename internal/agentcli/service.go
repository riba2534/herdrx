package agentcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type ServiceRunner interface {
	CheckLinger(username string) (bool, error)
	DaemonReload() error
	EnableAndStart(serviceName string) error
	Stop(serviceName string) error
	Restart(serviceName string) error
	DisableAndStop(serviceName string) error
	WriteUnitFile(path string, content []byte) error
	RemoveUnitFile(path string) error
}

type RealServiceRunner struct{}

func NewRealServiceRunner() ServiceRunner {
	return &RealServiceRunner{}
}

func (r *RealServiceRunner) CheckLinger(username string) (bool, error) {
	out, err := RunBoundedCommand(context.Background(), 5*time.Second, 64<<10, "loginctl", "show-user", username, "--property=Linger")
	if err != nil {
		return false, err
	}
	return strings.Contains(string(out), "Linger=yes"), nil
}

func (r *RealServiceRunner) DaemonReload() error {
	return runSystemctl("daemon-reload")
}

func (r *RealServiceRunner) EnableAndStart(serviceName string) error {
	return runSystemctl("enable", "--now", serviceName)
}

func (r *RealServiceRunner) Stop(serviceName string) error {
	return runSystemctl("stop", serviceName)
}

func (r *RealServiceRunner) Restart(serviceName string) error {
	return runSystemctl("restart", serviceName)
}

func (r *RealServiceRunner) DisableAndStop(serviceName string) error {
	return runSystemctl("disable", "--now", serviceName)
}

func (r *RealServiceRunner) IsActive(serviceName string) (bool, error) {
	out, err := RunBoundedCommand(context.Background(), 5*time.Second, 64<<10, "systemctl", "--user", "show", "--property=LoadState,ActiveState", serviceName)
	if err != nil {
		return false, fmt.Errorf("inspect legacy service: %w: %s", err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "ActiveState=") {
			state := strings.TrimPrefix(line, "ActiveState=")
			return state != "inactive" && state != "failed", nil
		}
	}
	return false, errors.New("systemctl did not report the legacy service state")
}

func runSystemctl(args ...string) error {
	out, err := RunBoundedCommand(context.Background(), 35*time.Second, 64<<10, "systemctl", append([]string{"--user"}, args...)...)
	if err != nil {
		return fmt.Errorf("systemctl: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (r *RealServiceRunner) WriteUnitFile(path string, content []byte) error {
	cleanPath := filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cleanPath), ".unit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, cleanPath); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(cleanPath))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (r *RealServiceRunner) RemoveUnitFile(path string) error {
	cleanPath := filepath.Clean(path)
	err := os.Remove(cleanPath)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

type InstallResult struct {
	UnitPath      string `json:"unit_path"`
	LingerEnabled bool   `json:"linger_enabled"`
	LingerWarning string `json:"linger_warning,omitempty"`
}

// GenerateSystemdUnit 构造固定绝对路径与环境变量注入的 systemd user unit 文件
func GenerateSystemdUnit(binaryPath, configPath string, runtimeDirs ...string) string {
	currentPath := os.Getenv("PATH")
	command := systemdArgument(binaryPath) + " serve --config " + systemdArgument(configPath)
	if len(runtimeDirs) > 0 && runtimeDirs[0] != "" {
		command += " --runtime-dir " + systemdArgument(runtimeDirs[0])
	}
	return `[Unit]
Description=herdrx remote access agent
After=network.target

[Service]
Type=simple
ExecStart=` + command + `
Restart=always
RestartSec=3
TimeoutStopSec=10
NoNewPrivileges=true
Environment=` + strconv.Quote(strings.ReplaceAll("PATH="+currentPath, "%", "%%")) + `

[Install]
WantedBy=default.target
`
}

func systemdArgument(value string) string {
	return strconv.Quote(strings.NewReplacer("%", "%%", "$", "$$").Replace(value))
}

func serviceUnitPath(env Environment) string {
	root := filepath.Join(env.HomeDir, ".config")
	if env.ConfigDir != "" {
		root = filepath.Dir(env.ConfigDir)
	}
	return filepath.Join(root, "systemd", "user", "herdrx.service")
}

// os.Executable resolves symlinks on Linux. Prefer the stable entrypoint that
// actually refers to this executable, so subsequent service restarts upgrade.
func resolveServiceExecutable(env Environment, executable string) string {
	actual, err := os.Stat(executable)
	if err != nil {
		return executable
	}
	candidates := []string{filepath.Join(env.HomeDir, ".local", "bin", "herdrx")}
	if len(os.Args) > 0 {
		invoked := os.Args[0]
		if !strings.ContainsRune(invoked, os.PathSeparator) {
			if path, err := exec.LookPath(invoked); err == nil {
				invoked = path
			}
		}
		if path, err := filepath.Abs(invoked); err == nil {
			candidates = append(candidates, path)
		}
	}
	for _, candidate := range candidates {
		if fi, err := os.Stat(candidate); err == nil && os.SameFile(actual, fi) {
			return candidate
		}
	}
	return executable
}

func InstallService(env Environment, binaryPath, configPath string) (*InstallResult, error) {
	runner := env.ServiceRunner
	if runner == nil {
		runner = NewRealServiceRunner()
	}

	absBin, err := filepath.Abs(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("resolve binary path: %w", err)
	}
	absCfg, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	if env.RuntimeDir != "" {
		env.RuntimeDir, err = filepath.Abs(env.RuntimeDir)
		if err != nil {
			return nil, fmt.Errorf("resolve runtime path: %w", err)
		}
	}

	// 检查当前用户 linger 状态
	u, err := user.Current()
	var lingerOk bool
	if err == nil {
		lingerOk, _ = runner.CheckLinger(u.Username)
	}

	var warning string
	if !lingerOk {
		username := "your-user"
		if u != nil {
			username = u.Username
		}
		warning = fmt.Sprintf("当前用户未开启 loginctl linger，SSH 登出后后台服务可能会被系统终止。建议执行: loginctl enable-linger %s", username)
	}

	absBin = resolveServiceExecutable(env, absBin)
	unitContent := GenerateSystemdUnit(absBin, absCfg, env.RuntimeDir)
	unitPath := serviceUnitPath(env)
	unitChanged := false
	if existing, err := os.ReadFile(unitPath); err == nil {
		if !strings.Contains(string(existing), "Description=herdrx remote access agent") {
			return nil, fmt.Errorf("refusing to overwrite custom unit at %s", unitPath)
		}
		unitChanged = string(existing) != unitContent
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read service unit: %w", err)
	}

	if err := runner.WriteUnitFile(unitPath, []byte(unitContent)); err != nil {
		return nil, fmt.Errorf("write systemd unit: %w", err)
	}
	if err := runner.DaemonReload(); err != nil {
		return nil, fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if err := runner.EnableAndStart("herdrx.service"); err != nil {
		return nil, fmt.Errorf("systemctl enable --now: %w", err)
	}
	if unitChanged {
		if err := runner.Restart("herdrx.service"); err != nil {
			return nil, fmt.Errorf("restart changed unit: %w", err)
		}
	}

	result := &InstallResult{
		UnitPath:      unitPath,
		LingerEnabled: lingerOk,
		LingerWarning: warning,
	}
	if _, ok := runner.(*RealServiceRunner); ok {
		sock := filepath.Join(env.RuntimeDir, "control.sock")
		var status Readiness
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		callErr := clientCallIPCContext(ctx, sock, "GET", "/ready", absCfg, nil, &status)
		cancel()
		if !unitChanged && (callErr != nil || status.Version != Version) {
			if err := runner.Restart("herdrx.service"); err != nil {
				return result, fmt.Errorf("restart installed binary: %w", err)
			}
		}
		cfg, err := SafeLoadConfig(absCfg)
		if err != nil {
			return result, err
		}
		expected, err := readiness(&cfg, absCfg)
		if err != nil {
			return result, err
		}
		if err := waitForReadiness(env, expected, absCfg, 30*time.Second); err != nil {
			return result, err
		}
	}
	return result, nil
}

func UninstallService(env Environment) error {
	runner := env.ServiceRunner
	if runner == nil {
		runner = NewRealServiceRunner()
	}

	unitPath := serviceUnitPath(env)
	if err := runner.DisableAndStop("herdrx.service"); err != nil {
		return fmt.Errorf("stop and disable service: %w", err)
	}
	if err := runner.RemoveUnitFile(unitPath); err != nil {
		return fmt.Errorf("remove unit file: %w", err)
	}
	if err := runner.DaemonReload(); err != nil {
		return fmt.Errorf("daemon-reload after uninstall: %w", err)
	}
	return nil
}
