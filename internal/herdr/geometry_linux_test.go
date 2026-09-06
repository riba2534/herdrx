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
	"strconv"
	"strings"
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
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python 3 is required for basic SSH geometry")
	}
	dir := t.TempDir()
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
				if json.NewDecoder(conn).Decode(&request) != nil || request.Method != "pane.process_info" || request.Params.PaneID != "w1:p1" {
					return
				}
				_ = json.NewEncoder(conn).Encode(map[string]any{"id": "herdrx", "result": map[string]any{"process_info": map[string]any{"pane_id": "w1:p1", "shell_pid": pid}}})
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
						serveGeometrySSH(conn, config, dir, pid)
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
		})
	}
	actual, err := pty.GetsizeFull(terminal)
	if err != nil || actual.Rows != 47 || actual.Cols != 65 || actual.X != 520 || actual.Y != 752 {
		t.Fatalf("size changed: %+v %v", actual, err)
	}
}

// Basic SSH fixture executes only the fixed geometry reader generated by the
// product, alongside socket forwarding. It has no arbitrary shell endpoint.
func serveGeometrySSH(conn net.Conn, config *ssh.ServerConfig, dir string, pid int) {
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
					switch payload.Command {
					case `sh -c 'printf "%s\n%s\n" "$HOME" "${XDG_CONFIG_HOME:-}"'`:
						output = []byte(dir + "\n" + dir + "\n")
					case fmt.Sprintf("python3 -c '%s' %d", remoteGeometryProgram, pid):
						output, runErr = exec.Command("python3", "-c", remoteGeometryProgram, strconv.Itoa(pid)).Output()
					default:
						_ = request.Reply(false, nil)
						return
					}
					_ = request.Reply(true, nil)
					_, _ = stream.Write(output)
					status := uint32(0)
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
