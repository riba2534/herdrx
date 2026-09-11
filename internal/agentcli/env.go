package agentcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Environment 封装受控端 CLI 运行所需的系统环境与依赖接口，支持测试依赖注入
type Environment struct {
	ConfigDir           string
	RuntimeDir          string
	StateDir            string
	DataDir             string
	HomeDir             string
	HerdrBin            string
	LookPath            func(file string) (string, error)
	CommandRunner       func(name string, args ...string) ([]byte, error)
	ServiceRunner       ServiceRunner
	UpdateHealthTimeout time.Duration // Test harness override; production uses 30 seconds.
}

// limitedWriter 流式截断写入器，防止恶意或异常子进程无限制输出耗尽内存
type limitedWriter struct {
	buf   bytes.Buffer
	limit int64
	over  bool
}

func (w *limitedWriter) Write(p []byte) (n int, err error) {
	rem := w.limit - int64(w.buf.Len())
	if rem <= 0 {
		w.over = true
		return len(p), nil // 丢弃超额数据
	}
	if int64(len(p)) > rem {
		w.buf.Write(p[:rem])
		w.over = true
		return len(p), nil
	}
	return w.buf.Write(p)
}

// RunBoundedCommand 执行具有严格超时和流式内存上限的命令，超量或超时时坚决终止子进程
func RunBoundedCommand(ctx context.Context, timeout time.Duration, maxBytes int64, name string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	lw := &limitedWriter{limit: maxBytes}
	cmd.Stdout = lw
	cmd.Stderr = lw

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return lw.buf.Bytes(), errors.New("command execution timed out")
	}
	if lw.over {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return lw.buf.Bytes(), fmt.Errorf("command output exceeded maximum limit of %d bytes", maxBytes)
	}
	return lw.buf.Bytes(), err
}

// DefaultEnv 返回当前运行时的真实环境依赖，默认命令执行器接入 3s 超时与 1MB 流式上限
func DefaultEnv() Environment {
	home, _ := os.UserHomeDir()
	// XDG_CONFIG_HOME 必须优先于平台惯例：os.UserConfigDir() 在 macOS 上直接
	// 忽略它，会让 --config 之外的路径覆盖失效，也让隔离测试落到真实用户目录。
	configRoot := xdgDirectory("XDG_CONFIG_HOME", "")
	if configRoot == "" {
		configRoot, _ = os.UserConfigDir()
	}
	if configRoot == "" {
		configRoot = filepath.Join(home, ".config")
	}

	stateDir := filepath.Join(xdgDirectory("XDG_STATE_HOME", filepath.Join(home, ".local", "state")), "herdrx")
	runtimeDir := xdgDirectory("XDG_RUNTIME_DIR", "")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(stateDir, "run")
	} else {
		runtimeDir = filepath.Join(runtimeDir, "herdrx")
	}

	return Environment{
		ConfigDir:  filepath.Join(configRoot, "herdrx"),
		RuntimeDir: runtimeDir,
		StateDir:   stateDir,
		DataDir:    filepath.Join(xdgDirectory("XDG_DATA_HOME", filepath.Join(home, ".local", "share")), "herdrx"),
		HomeDir:    home,
		HerdrBin:   "",
		LookPath:   exec.LookPath,
		CommandRunner: func(name string, args ...string) ([]byte, error) {
			return RunBoundedCommand(context.Background(), 3*time.Second, 1<<20, name, args...)
		},
		ServiceRunner: NewRealServiceRunner(),
	}
}

func xdgDirectory(name, fallback string) string {
	if path := os.Getenv(name); filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return fallback
}
