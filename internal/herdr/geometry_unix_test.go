//go:build linux || darwin

package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/riba2534/herdrx/internal/agent"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
)

func TestGeometryOnLocalSSHAndTailcatEndpoints(t *testing.T) {
	// macOS Unix socket paths have a small limit; use a short isolated root.
	dir, err := os.MkdirTemp("/tmp", "hxg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_CONFIG_HOME", dir)
	root := filepath.Join(dir, "herdr")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sleep", "60")
	terminal, err := pty.StartWithSize(child, &pty.Winsize{Rows: 47, Cols: 65, X: 520, Y: 752})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait(); _ = terminal.Close() })
	pid := child.Process.Pid
	// 像素宽度不是列数的整数倍，受控端会拒绝读取并在 stderr 说明原因。
	inexactChild := exec.Command("sleep", "60")
	inexactTerminal, err := pty.StartWithSize(inexactChild, &pty.Winsize{Rows: 47, Cols: 65, X: 521, Y: 752})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inexactChild.Process.Kill(); _ = inexactChild.Wait(); _ = inexactTerminal.Close() })
	// w1:p3 只在基础 SSH fixture 中模拟远端缺少 python3。
	pids := map[string]int{"w1:p1": pid, "w1:p2": inexactChild.Process.Pid, "w1:p3": os.Getpid()}
	apiListener, err := net.Listen("unix", filepath.Join(root, "herdr.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer apiListener.Close()
	go func() {
		for {
			conn, err := apiListener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var request struct {
					Method string `json:"method"`
					Params struct {
						PaneID string `json:"pane_id"`
					} `json:"params"`
				}
				if json.NewDecoder(conn).Decode(&request) != nil || request.Method != "pane.process_info" || pids[request.Params.PaneID] == 0 {
					return
				}
				_ = json.NewEncoder(conn).Encode(map[string]any{"id": "herdrx", "result": map[string]any{"process_info": map[string]any{"pane_id": request.Params.PaneID, "shell_pid": pids[request.Params.PaneID]}}})
			}()
		}
	}()
	clientListener, err := net.Listen("unix", filepath.Join(root, "herdr-client.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer clientListener.Close()
	go func() {
		for {
			conn, err := clientListener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	private, public, err := secure.GenerateSSHKey("geometry-client")
	if err != nil {
		t.Fatal(err)
	}
	hostPrivate, _, err := secure.GenerateSSHKey("geometry-host")
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.ParsePrivateKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	allowed, _, _, _, err := ssh.ParseAuthorizedKey(public)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) != string(allowed.Marshal()) {
			return nil, fmt.Errorf("wrong test key")
		}
		return nil, nil
	}}
	config.AddHostKey(key)
	agentConfig := agent.Config{Version: 1, Node: *tailcat.NewPrivateKey(), Paired: true, SSHHostPrivate: string(hostPrivate), AuthorizedSSHKey: strings.TrimSpace(string(public))}
	agentConfig.AllowedNodeKey = string(agentConfig.Node.Public.Addr())
	configPath := filepath.Join(dir, "agent.json")
	if err := agent.Save(configPath, agentConfig); err != nil {
		t.Fatal(err)
	}
	agentServer, err := agent.NewSSHServer(configPath, agentConfig)
	if err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{"local", "ssh", "tailcat"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "ssh" {
				if _, err := exec.LookPath("python3"); err != nil {
					t.Skip("Python 3 is required for basic SSH geometry")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var endpoint NativeScrollEndpoint
			if mode == "local" {
				endpoint = &LocalEndpoint{SocketPath: filepath.Join(root, "herdr.sock")}
			} else {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				go func() {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					if mode == "tailcat" {
						agentServer.Handle(conn)
					} else {
						serveGeometrySSH(conn, config, dir, pids)
					}
				}()
				port := listener.Addr().(*net.TCPAddr).Port
				value, err := DialSSHEndpoint(ctx, SSHOptions{Host: store.Host{Transport: mode, Hostname: "127.0.0.1", Port: port, Username: "test", AuthMethod: "private_key", HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key.PublicKey())))}, Secret: private})
				if err != nil {
					t.Fatal(err)
				}
				defer value.Close()
				endpoint = value
			}
			geometry, err := endpoint.TerminalGeometry(ctx, "w1:p1")
			if err != nil || geometry != (TerminalGeometry{Cols: 65, Rows: 47, CellWidthPx: 8, CellHeightPx: 16}) {
				t.Fatalf("geometry=%+v err=%v", geometry, err)
			}
			conn, err := endpoint.OpenTerminalSocket(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stop()
			if _, err := conn.Write([]byte("same-terminal")); err != nil {
				t.Fatal(err)
			}
			reply := make([]byte, 13)
			if _, err := io.ReadFull(conn, reply); err != nil || string(reply) != "same-terminal" {
				t.Fatalf("client socket forwarding: %q %v", reply, err)
			}
			if _, err := endpoint.TerminalGeometry(ctx, "../../123"); err == nil {
				t.Fatal("invalid pane accepted")
			}
			// 远端给出的原因必须到达用户，且不能被说成是 CLI 版本或 Python 问题。
			if _, err := endpoint.TerminalGeometry(ctx, "w1:p2"); err == nil || !strings.Contains(err.Error(), "terminal size cannot be preserved exactly") || strings.Contains(err.Error(), "herdrx CLI") || strings.Contains(err.Error(), "Python 3") {
				t.Fatalf("inexact terminal err=%v", err)
			}
			// 读取程序没能运行时，stderr 不是读取失败的原因，保留原提示。
			if mode == "ssh" {
				if _, err := endpoint.TerminalGeometry(ctx, "w1:p3"); err == nil || !strings.Contains(err.Error(), "已安装 Python 3") {
					t.Fatalf("missing python err=%v", err)
				}
			}
		})
	}
	actual, err := pty.GetsizeFull(terminal)
	if err != nil || actual.Rows != 47 || actual.Cols != 65 || actual.X != 520 || actual.Y != 752 {
		t.Fatalf("size changed: %+v %v", actual, err)
	}
}

// Basic SSH fixture executes only the fixed geometry reader generated by the
// product, alongside socket forwarding. It has no arbitrary shell endpoint.
func serveGeometrySSH(conn net.Conn, config *ssh.ServerConfig, dir string, pids map[string]int) {
	server, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer server.Close()
	go ssh.DiscardRequests(requests)
	for channel := range channels {
		go func(channel ssh.NewChannel) {
			switch channel.ChannelType() {
			case "direct-streamlocal@openssh.com":
				var payload struct {
					Path     string
					Reserved string
					Port     uint32
				}
				if ssh.Unmarshal(channel.ExtraData(), &payload) != nil || (payload.Path != filepath.Join(dir, "herdr", "herdr.sock") && payload.Path != filepath.Join(dir, "herdr", "herdr-client.sock")) {
					_ = channel.Reject(ssh.Prohibited, "bad path")
					return
				}
				local, err := net.Dial("unix", payload.Path)
				if err != nil {
					_ = channel.Reject(ssh.ConnectionFailed, "missing fixture socket")
					return
				}
				defer local.Close()
				stream, requests, err := channel.Accept()
				if err != nil {
					return
				}
				defer stream.Close()
				go ssh.DiscardRequests(requests)
				go func() { _, _ = io.Copy(local, stream); _ = local.Close() }()
				_, _ = io.Copy(stream, local)
			case "session":
				stream, requests, err := channel.Accept()
				if err != nil {
					return
				}
				defer stream.Close()
				for request := range requests {
					var payload struct{ Command string }
					if request.Type != "exec" || ssh.Unmarshal(request.Payload, &payload) != nil {
						_ = request.Reply(false, nil)
						continue
					}
					var output []byte
					var runErr error
					geometryPID := 0
					for _, pid := range pids {
						if payload.Command == fmt.Sprintf("python3 -c '%s' %d", remoteGeometryProgram, pid) {
							geometryPID = pid
						}
					}
					status := uint32(0)
					switch {
					case geometryPID == pids["w1:p3"]:
						_ = request.Reply(true, nil)
						_, _ = stream.Stderr().Write([]byte("sh: 1: python3: not found\n"))
						_, _ = stream.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{127}))
						return
					case payload.Command == `sh -c 'printf "%s\n%s\n" "$HOME" "${XDG_CONFIG_HOME:-}"'`:
						output = []byte(dir + "\n" + dir + "\n")
					case geometryPID != 0:
						output, runErr = exec.Command("python3", "-c", remoteGeometryProgram, strconv.Itoa(geometryPID)).Output()
					default:
						_ = request.Reply(false, nil)
						return
					}
					_ = request.Reply(true, nil)
					_, _ = stream.Write(output)
					// 与真实 sshd 一致，远端程序的 stderr 走扩展数据通道。
					if exitErr, ok := runErr.(*exec.ExitError); ok {
						_, _ = stream.Stderr().Write(exitErr.Stderr)
					}
					if runErr != nil {
						status = 1
					}
					_, _ = stream.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
					return
				}
			default:
				_ = channel.Reject(ssh.UnknownChannelType, "unsupported")
			}
		}(channel)
	}
}

// The program travels inside single quotes through the remote login shell. A
// single quote would end that argument early and run the rest as shell words.
func TestRemoteGeometryProgramFitsSingleQuotedShellArgument(t *testing.T) {
	if strings.Contains(remoteGeometryProgram, "'") {
		t.Fatal("remote geometry program must not contain single quotes")
	}
}

// A process outside any terminal session must be rejected rather than reporting
// some other terminal's size. Setsid keeps the child from inheriting the test
// runner's controlling terminal, so the case also holds on a developer's TTY.
func TestRemoteGeometryProgramRejectsProcessWithoutTerminal(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python 3 is required for basic SSH geometry")
	}
	child := exec.Command("sleep", "60")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	output, err := exec.Command("python3", "-c", remoteGeometryProgram, strconv.Itoa(child.Process.Pid)).CombinedOutput()
	if err == nil {
		t.Fatalf("geometry of a process without a terminal = %s, want an error", output)
	}
	if runtime.GOOS == "darwin" && !strings.Contains(string(output), "process has no controlling terminal") {
		t.Fatalf("darwin error = %s, want the missing controlling terminal reason", output)
	}
}
