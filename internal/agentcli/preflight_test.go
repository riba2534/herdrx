package agentcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/riba2534/herdrx/internal/testpaths"
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
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request map[string]any
		if json.NewDecoder(conn).Decode(&request) == nil && request["method"] == "ping" {
			_ = json.NewEncoder(conn).Encode(map[string]any{"result": map[string]any{"type": "pong", "version": "0.8.2", "protocol": 20}})
		}
	}()

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
	if !res.DaemonVerified || res.DaemonVersion != "0.8.2" || res.DaemonProtocol != 20 {
		t.Fatalf("live identity missing: %+v", res)
	}
}

func TestPreflightSeparatesInstalledAndLiveProtocol(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		cliProtocol, liveProtocol int
		liveVersion               string
		want                      PreflightStatus
	}{
		{"different-product-compatible", 22, 22, "0.9.1", PreflightOK},
		{"protocol-mismatch-keeps-json-access", 20, 22, "0.9.1", PreflightOK},
		{"missing-live-identity", 22, 0, "", PreflightOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := testpaths.ShortTempDir(t)
			binary, socket := filepath.Join(dir, "herdr"), filepath.Join(dir, "api.sock")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				var request map[string]any
				if json.NewDecoder(conn).Decode(&request) != nil {
					return
				}
				if request["method"] != "ping" {
					return
				}
				_ = json.NewEncoder(conn).Encode(map[string]any{"result": map[string]any{"type": "pong", "version": tc.liveVersion, "protocol": tc.liveProtocol}})
			}()
			env := Environment{HerdrBin: binary, HomeDir: dir, CommandRunner: func(_ string, args ...string) ([]byte, error) {
				switch args[0] {
				case "--version":
					return []byte("herdr 0.9.0"), nil
				case "api":
					return []byte(fmt.Sprintf(`{"protocol":%d,"schemas":{"request":{"oneOf":[{"properties":{"method":{"const":"session.snapshot"}}}]}}}`, tc.cliProtocol)), nil
				case "status":
					return []byte("status: running\nversion: 0.9.0\nsocket: " + socket + "\n"), nil
				default:
					return nil, fmt.Errorf("unexpected command %q", args)
				}
			}}
			result := RunPreflight(env)
			if result.Status != tc.want || result.CLIProtocol != tc.cliProtocol || result.Version != "herdr 0.9.0" || result.DaemonVersion != tc.liveVersion || result.DaemonProtocol != tc.liveProtocol {
				t.Fatalf("identity conflated: %+v", result)
			}
			if result.DaemonVerified != (tc.liveProtocol > 0) {
				t.Fatalf("fabricated daemon verification: %+v", result)
			}
			if tc.liveProtocol == 0 && result.TerminalProtocolMatch != nil {
				t.Fatal("incomplete daemon identity became a compatibility claim")
			}
			if tc.liveProtocol > 0 && (result.TerminalProtocolMatch == nil || *result.TerminalProtocolMatch != (tc.cliProtocol == tc.liveProtocol)) {
				t.Fatalf("incorrect private protocol diagnostic: %+v", result)
			}
		})
	}
}
