package agentcli

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPreflight_Missing(t *testing.T) {
	env := Environment{
		LookPath: func(file string) (string, error) {
			return "", errors.New("not found in path")
		},
	}
	res := RunPreflight(env)
	if res.Status != PreflightMissing {
		t.Fatalf("expected status missing, got %s", res.Status)
	}
	if res.Suggestion == "" {
		t.Fatalf("expected suggestion with official docs link")
	}
}

// TestPreflight_BinTrueRejected 严格验证 /bin/true 绝不可被误判为有效 Herdr
func TestPreflight_BinTrueRejected(t *testing.T) {
	// macOS 只有 /usr/bin/true，Linux 通常是 /bin/true；按实际存在的位置取，
	// 否则这条断言会退化成「文件不存在」而不再检验兼容性判定。
	truePath := ""
	for _, candidate := range []string{"/bin/true", "/usr/bin/true"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			truePath = candidate
			break
		}
	}
	if truePath == "" {
		t.Skip("no true(1) binary available")
	}
	env := Environment{
		HerdrBin: truePath,
		CommandRunner: func(name string, args ...string) ([]byte, error) {
			// true 无论什么参数都退出码 0 且输出为空
			return []byte{}, nil
		},
	}
	res := RunPreflight(env)
	if res.Status != PreflightIncompatible {
		t.Fatalf("expected %s to be rejected as incompatible, got status: %s (details: %s)", truePath, res.Status, res.Details)
	}
}

// TestPreflight_SchemaMissingRequiredMethods 验证 schema 缺少核心接口时报 incompatible
func TestPreflight_SchemaMissingRequiredMethods(t *testing.T) {
	tempDir := t.TempDir()
	herdrBin := filepath.Join(tempDir, "herdr")
	_ = os.WriteFile(herdrBin, []byte("#!/bin/sh\nexit 0\n"), 0o755)

	env := Environment{
		HerdrBin: herdrBin,
		HomeDir:  tempDir,
		CommandRunner: func(name string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "--version" {
				return []byte("herdr 0.8.2\n"), nil
			}
			if len(args) >= 3 && args[0] == "api" && args[1] == "schema" {
				// 返回合法 JSON 但没有包含所需核心方法
				fakeSchema := `{"schemas":{"request":{"oneOf":[{"properties":{"method":{"const":"unrelated.method"}}}]}}}`
				return []byte(fakeSchema), nil
			}
			return nil, nil
		},
	}

	res := RunPreflight(env)
	if res.Status != PreflightIncompatible {
		t.Fatalf("expected incompatible when missing core methods, got: %s", res.Status)
	}
}

// TestPreflight_SocketDisconnected 验证即使 status 输出 running 但 socket 不通时判定为 not_running
func TestPreflight_SocketDisconnected(t *testing.T) {
	tempDir := t.TempDir()
	herdrBin := filepath.Join(tempDir, "herdr")
	_ = os.WriteFile(herdrBin, []byte("#!/bin/sh\nexit 0\n"), 0o755)

	nonExistentSocket := filepath.Join(tempDir, "non_existent.sock")

	env := Environment{
		HerdrBin: herdrBin,
		HomeDir:  tempDir,
		CommandRunner: func(name string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "--version" {
				return []byte("herdr 0.8.2\n"), nil
			}
			if len(args) >= 3 && args[0] == "api" && args[1] == "schema" {
				fakeSchema := `{"schemas":{"request":{"oneOf":[{"properties":{"method":{"const":"workspace.list"}}}]}}}`
				return []byte(fakeSchema), nil
			}
			if len(args) >= 2 && args[0] == "status" && args[1] == "server" {
				return []byte("status: running\nversion: 0.8.2\nsocket: " + nonExistentSocket + "\n"), nil
			}
			return nil, nil
		},
	}

	res := RunPreflight(env)
	if res.Status != PreflightNotRunning {
		t.Fatalf("expected status not_running when socket cannot be connected, got %s (details: %s)", res.Status, res.Details)
	}
	if res.ServerRunning {
		t.Fatalf("ServerRunning must be false")
	}
}

// TestPreflight_FullSuccess 验证真实多行 status 输出与真实运行 socket 时通过
func TestPreflight_FullSuccess(t *testing.T) {
	tempDir := t.TempDir()
	herdrBin := filepath.Join(tempDir, "herdr with space")
	_ = os.WriteFile(herdrBin, []byte("#!/bin/sh\nexit 0\n"), 0o755)

	realSocket := filepath.Join(tempDir, "herdr.sock")
	ln, err := net.Listen("unix", realSocket)
	if err != nil {
		t.Fatalf("listen mock unix socket: %v", err)
	}
	defer ln.Close()

	env := Environment{
		HerdrBin: herdrBin,
		HomeDir:  tempDir,
		CommandRunner: func(name string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "--version" {
				return []byte("herdr 0.8.2\n"), nil
			}
			if len(args) >= 3 && args[0] == "api" && args[1] == "schema" {
				fakeSchema := `{"schemas":{"request":{"oneOf":[{"properties":{"method":{"const":"session.snapshot"}}}]}}}`
				return []byte(fakeSchema), nil
			}
			if len(args) >= 2 && args[0] == "status" && args[1] == "server" {
				return []byte("status: running\nversion: 0.8.2\nsocket: " + realSocket + "\n"), nil
			}
			return nil, nil
		},
	}

	res := RunPreflight(env)
	if res.Status != PreflightOK {
		t.Fatalf("expected status ok, got %s (details: %s)", res.Status, res.Details)
	}
	if !res.ServerRunning || res.SocketPath != realSocket {
		t.Fatalf("unexpected preflight result: %+v", res)
	}
}
