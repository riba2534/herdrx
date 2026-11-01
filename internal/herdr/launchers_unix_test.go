//go:build linux || darwin

package herdr

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/agent"
	"github.com/riba2534/herdrx/internal/launchers"
	"github.com/riba2534/herdrx/internal/launchers/launcherstest"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/riba2534/herdrx/internal/testpaths"
	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
)

// serveLoginShellSSH answers exec requests like sshd: the command runs through
// /bin/sh -c in a new session, with stdout and stderr on the channel.
func serveLoginShellSSH(conn net.Conn, config *ssh.ServerConfig) {
	server, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer server.Close()
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		go func(newChannel ssh.NewChannel) {
			channel, requests, err := newChannel.Accept()
			if err != nil {
				return
			}
			defer channel.Close()
			for request := range requests {
				var payload struct{ Command string }
				if request.Type != "exec" || ssh.Unmarshal(request.Payload, &payload) != nil {
					_ = request.Reply(false, nil)
					continue
				}
				_ = request.Reply(true, nil)
				command := exec.Command("/bin/sh", "-c", payload.Command)
				command.Stdout, command.Stderr = channel, channel.Stderr()
				command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				status := uint32(0)
				var exit *exec.ExitError
				if err := command.Run(); errors.As(err, &exit) {
					status = uint32(exit.ExitCode())
				} else if err != nil {
					status = 255
				}
				buffer := make([]byte, 4)
				binary.BigEndian.PutUint32(buffer, status)
				_, _ = channel.SendRequest("exit-status", false, buffer)
				return
			}
		}(newChannel)
	}
}

// dialLauncherEndpoint connects an SSHEndpoint of the given transport to a
// basic SSH server or to the real access daemon.
func dialLauncherEndpoint(t *testing.T, transport string) *SSHEndpoint {
	t.Helper()
	private, public, err := secure.GenerateSSHKey("launcher-client")
	if err != nil {
		t.Fatal(err)
	}
	hostPrivate, _, err := secure.GenerateSSHKey("launcher-host")
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := ssh.ParsePrivateKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serve := func(conn net.Conn) {}
	if transport == "tailcat" {
		agentConfig := agent.Config{Version: 1, Node: *tailcat.NewPrivateKey(), Paired: true, SSHHostPrivate: string(hostPrivate), AuthorizedSSHKey: strings.TrimSpace(string(public))}
		agentConfig.AllowedNodeKey = string(agentConfig.Node.Public.Addr())
		configPath := filepath.Join(testpaths.ShortTempDir(t), "agent.json")
		if err := agent.Save(configPath, agentConfig); err != nil {
			t.Fatal(err)
		}
		daemon, err := agent.NewSSHServer(configPath, agentConfig)
		if err != nil {
			t.Fatal(err)
		}
		serve = daemon.Handle
	} else {
		config := &ssh.ServerConfig{NoClientAuth: true}
		config.AddHostKey(hostKey)
		serve = func(conn net.Conn) { serveLoginShellSSH(conn, config) }
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serve(conn)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	host := store.Host{Transport: transport, Hostname: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, Username: "test", AuthMethod: "private_key", HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostKey.PublicKey())))}
	endpoint, err := DialSSHEndpoint(ctx, SSHOptions{Host: host, Secret: private})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = endpoint.Close() })
	return endpoint
}

func TestLaunchersOverEachTransportReturnNamesAndFlagsOnly(t *testing.T) {
	launcherstest.RequireZsh(t)
	home := launcherstest.Isolate(t, launcherstest.Zshrc)
	endpoints := map[string]LauncherEndpoint{
		"local":   &LocalEndpoint{},
		"ssh":     dialLauncherEndpoint(t, "ssh"),
		"tailcat": dialLauncherEndpoint(t, "tailcat"),
	}
	for name, endpoint := range endpoints {
		t.Run(name, func(t *testing.T) {
			result, err := endpoint.Launchers(context.Background())
			if err != nil || !reflect.DeepEqual(result, launchers.Found(launcherstest.Expected)) {
				t.Fatalf("result %+v err %v", result, err)
			}
			encoded := fmt.Sprintf("%+v", result)
			for _, secret := range launcherstest.Secrets {
				if strings.Contains(encoded, secret) {
					t.Fatalf("result leaked %q", secret)
				}
			}
			launcherstest.AssertNoMarkers(t, home)
		})
	}
	t.Run("without zsh", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		for name, endpoint := range endpoints {
			result, err := endpoint.Launchers(context.Background())
			if err != nil || result.Supported || result.Reason != launchers.ReasonShellUnavailable {
				t.Fatalf("%s: result %+v err %v", name, result, err)
			}
		}
	})
}
