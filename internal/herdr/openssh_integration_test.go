//go:build unix

package herdr

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/store"
	"golang.org/x/crypto/ssh"
)

func TestOpenSSHIsolatedLifecycle(t *testing.T) {
	binary, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("system OpenSSH unavailable")
	}
	// Short paths also fit macOS's Unix socket limit. Everything on the fake
	// remote host, including its identity and Herdr sockets, lives here.
	dir, err := os.MkdirTemp("", "hx-live-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	home := filepath.Join(dir, "home")
	herdrDir := filepath.Join(home, ".config", "herdr")
	if err := os.MkdirAll(herdrDir, 0700); err != nil {
		t.Fatal(err)
	}
	apiPath := filepath.Join(herdrDir, "herdr.sock")
	terminalPath := filepath.Join(herdrDir, "herdr-client.sock")
	fixtureUnixSocket(t, apiPath, func(conn net.Conn) {
		var request struct{ Method string }
		if json.NewDecoder(conn).Decode(&request) != nil {
			return
		}
		if request.Method != "session.snapshot" {
			_ = json.NewEncoder(conn).Encode(map[string]any{"error": map[string]string{"code": "forbidden", "message": "fixture only allows snapshots"}})
			return
		}
		_ = json.NewEncoder(conn).Encode(map[string]any{"id": "herdrx", "result": map[string]any{"snapshot": Snapshot{Version: "fixture", Protocol: 22}}})
	})
	fixtureUnixSocket(t, terminalPath, func(conn net.Conn) { _, _ = io.Copy(conn, conn) })

	clientKey, err := fixtureSSHKey()
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := ssh.MarshalPrivateKey(clientKey, "isolated test")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "identity")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(privateKey), 0600); err != nil {
		t.Fatal(err)
	}
	hostKey, err := fixtureSSHKey()
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	fixture := startOpenSSHFixture(t, home, clientSigner.PublicKey(), hostSigner)
	_, port, err := net.SplitHostPort(fixture.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	knownHosts := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(knownHosts, []byte("[127.0.0.1]:"+port+" "+string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config")
	settings := fmt.Sprintf("Host fixture\n HostName 127.0.0.1\n Port %s\n User fixture\n IdentityFile %s\n IdentitiesOnly yes\n UserKnownHostsFile %s\n GlobalKnownHostsFile /dev/null\n", port, keyPath, knownHosts)
	if err := os.WriteFile(config, []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	wrapper := filepath.Join(dir, "ssh")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+quote(binary)+" -F "+quote(config)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	endpoint, err := DialOpenSSHEndpoint(ctx, wrapper, SSHOptions{Host: store.Host{Transport: "ssh", Hostname: "fixture", AuthMethod: "system_ssh"}})
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	transport := endpoint.client.(*openSSHTransport)
	if endpoint.socketPath != apiPath {
		t.Fatalf("discovered socket = %q, want %q", endpoint.socketPath, apiPath)
	}
	session, err := transport.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	output, err := session.Output("printf fixture-output")
	_ = session.Close()
	if err != nil || string(output) != "fixture-output" {
		t.Fatalf("multiplexed Output = %q: %v", output, err)
	}
	snapshot, err := endpoint.Snapshot(ctx)
	if err != nil || snapshot.Version != "fixture" || snapshot.Protocol != 22 {
		t.Fatalf("forwarded snapshot = %+v: %v", snapshot, err)
	}
	terminal, err := endpoint.OpenTerminalSocket(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	_ = terminal.SetDeadline(time.Now().Add(5 * time.Second))
	message := []byte("same independent terminal\x00中文")
	if _, err := terminal.Write(message); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(message))
	if _, err := io.ReadFull(terminal, reply); err != nil || !bytes.Equal(reply, message) {
		t.Fatalf("native socket forwarding = %q: %v", reply, err)
	}
	// Exceed an SSH channel window to exercise stdin flow control and EOF,
	// preserving arbitrary bytes rather than only a short textual payload.
	image := bytes.Repeat([]byte{0, 1, 255, 13, 10, 128}, 400000)
	imagePath, err := endpoint.StageImage(ctx, "png", bytes.NewReader(image))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(imagePath) != filepath.Join(home, ".cache", "herdrx", "staging") {
		t.Fatalf("image escaped the fixture: %q", imagePath)
	}
	stored, err := os.ReadFile(imagePath)
	if err != nil || !bytes.Equal(stored, image) {
		t.Fatalf("image transfer changed %d bytes: %v", len(image), err)
	}
	info, err := os.Stat(imagePath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("staged image permissions: %v, %v", info, err)
	}
	if fixture.authenticated.Load() != 1 {
		t.Fatalf("operations opened %d SSH connections instead of one master", fixture.authenticated.Load())
	}
	if err := endpoint.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(transport.dir); !os.IsNotExist(err) {
		t.Fatalf("master socket directory survived Close: %v", err)
	}
	if _, err := terminal.Read(make([]byte, 1)); err == nil {
		t.Fatal("Close left the terminal access connection open")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("Close did not disconnect the terminal access connection")
	}
	if _, err := endpoint.Snapshot(ctx); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed endpoint accepted a request: %v", err)
	}
	// Bypass NewSession's closed-context guard. Give fallback a reachable
	// destination and valid credentials so only the mux fail-closed policy
	// prevents a fresh connection, rather than a missing alias or identity.
	if err := transport.command(ctx, "-p", port, "-l", "fixture", "-i", keyPath,
		"-o", "IdentitiesOnly=yes", "-o", "UserKnownHostsFile="+knownHosts,
		"-o", "StrictHostKeyChecking=yes", "--", "127.0.0.1", "printf fixture-output").Run(); err == nil {
		t.Fatal("dead master silently reauthenticated")
	}
	if fixture.accepted.Load() != 1 || fixture.authenticated.Load() != 1 {
		t.Fatalf("dead master made another connection: accepted=%d authenticated=%d", fixture.accepted.Load(), fixture.authenticated.Load())
	}
	// The independent remote service and both sockets outlive access cleanup.
	local := &LocalEndpoint{SocketPath: apiPath}
	if snapshot, err := local.Snapshot(ctx); err != nil || snapshot.Version != "fixture" {
		t.Fatalf("Close stopped the independent Herdr API: %+v, %v", snapshot, err)
	}
	direct, err := net.DialTimeout("unix", terminalPath, time.Second)
	if err != nil {
		t.Fatalf("Close stopped the independent terminal socket: %v", err)
	}
	defer direct.Close()
	_ = direct.SetDeadline(time.Now().Add(time.Second))
	if _, err := direct.Write(message); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(direct, reply); err != nil || !bytes.Equal(reply, message) {
		t.Fatalf("remote terminal did not survive access cleanup: %q, %v", reply, err)
	}
}

func fixtureSSHKey() (ed25519.PrivateKey, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	return private, err
}

func fixtureUnixSocket(t *testing.T, path string, serve func(net.Conn)) {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			stop := context.AfterFunc(t.Context(), func() { _ = conn.Close() })
			serve(conn)
			stop()
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
}

type openSSHFixture struct {
	listener      net.Listener
	accepted      atomic.Int32
	authenticated atomic.Int32
}

func startOpenSSHFixture(t *testing.T, home string, key ssh.PublicKey, host ssh.Signer) *openSSHFixture {
	t.Helper()
	config := &ssh.ServerConfig{PublicKeyCallback: func(metadata ssh.ConnMetadata, candidate ssh.PublicKey) (*ssh.Permissions, error) {
		if metadata.User() != "fixture" || !bytes.Equal(candidate.Marshal(), key.Marshal()) {
			return nil, fmt.Errorf("unknown fixture identity")
		}
		return nil, nil
	}}
	config.AddHostKey(host)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &openSSHFixture{listener: listener}
	var connections []net.Conn
	var active sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections = append(connections, conn)
			fixture.accepted.Add(1)
			active.Add(1)
			go func() {
				defer active.Done()
				defer conn.Close()
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer server.Close()
				fixture.authenticated.Add(1)
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					active.Add(1)
					go func() {
						defer active.Done()
						serveOpenSSHFixtureChannel(t.Context(), channel, home)
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
		for _, conn := range connections {
			_ = conn.Close()
		}
		active.Wait()
	})
	return fixture
}

func serveOpenSSHFixtureChannel(ctx context.Context, incoming ssh.NewChannel, home string) {
	switch incoming.ChannelType() {
	case "direct-streamlocal@openssh.com":
		var request struct {
			Path     string
			Reserved string
			Port     uint32
		}
		dir := filepath.Join(home, ".config", "herdr")
		if ssh.Unmarshal(incoming.ExtraData(), &request) != nil || (request.Path != filepath.Join(dir, "herdr.sock") && request.Path != filepath.Join(dir, "herdr-client.sock")) {
			_ = incoming.Reject(ssh.Prohibited, "fixture socket only")
			return
		}
		local, err := net.Dial("unix", request.Path)
		if err != nil {
			_ = incoming.Reject(ssh.ConnectionFailed, "fixture socket unavailable")
			return
		}
		defer local.Close()
		channel, requests, err := incoming.Accept()
		if err != nil {
			return
		}
		defer channel.Close()
		go ssh.DiscardRequests(requests)
		go func() { _, _ = io.Copy(local, channel); _ = local.Close() }()
		_, _ = io.Copy(channel, local)
	case "session":
		channel, requests, err := incoming.Accept()
		if err != nil {
			return
		}
		defer channel.Close()
		for request := range requests {
			var payload struct{ Command string }
			if request.Type != "exec" || ssh.Unmarshal(request.Payload, &payload) != nil || !fixtureSSHCommandAllowed(payload.Command) {
				_ = request.Reply(false, nil)
				continue
			}
			_ = request.Reply(true, nil)
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", payload.Command)
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "XDG_CACHE_HOME=" + filepath.Join(home, ".cache")}
			cmd.Dir = home
			cmd.Stdin, cmd.Stdout, cmd.Stderr = channel, channel, channel.Stderr()
			cmd.WaitDelay = time.Second
			status := uint32(0)
			if err := cmd.Run(); err != nil {
				status = 1
			}
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
			return
		}
	default:
		_ = incoming.Reject(ssh.UnknownChannelType, "unsupported fixture channel")
	}
}

func fixtureSSHCommandAllowed(command string) bool {
	if command == `sh -c 'printf "%s\n%s\n" "$HOME" "${XDG_CONFIG_HOME:-}"'` || command == "printf fixture-output" {
		return true
	}
	// Run the actual staging shell, but accept only its fixed grammar and a
	// generated PNG basename. The fixture offers no general shell endpoint.
	filename := regexp.MustCompile(`img_[0-9]+_[0-9a-f]{16}\.png`).FindString(command)
	return filename != "" && strings.ReplaceAll(command, filename, "fixture.png") == `sh -c 'dir="${XDG_CACHE_HOME:-$HOME/.cache}/herdrx/staging"; mkdir -p "$dir" && cat > "$dir/fixture.png" && chmod 600 "$dir/fixture.png" && printf "%s/%s\n" "$dir" "fixture.png"'`
}
