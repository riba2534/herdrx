package tunnel

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/agent"
	"github.com/riba2534/herdrx/internal/secure"
	"golang.org/x/crypto/ssh"
)

func permanentSSHFixture(t *testing.T, active bool) (*PermanentTailcatServer, *mockConfigStore, func() (*ssh.Client, error)) {
	t.Helper()
	clientPrivate, clientPublic, err := secure.GenerateSSHKey("isolated-client")
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.ParsePrivateKey(clientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	hostPrivate, _, err := secure.GenerateSSHKey("isolated-host")
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.ParsePrivateKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	status := "prepared"
	if active {
		status = "active"
	}
	store := &mockConfigStore{cfg: agent.Config{Paired: active, Binding: agent.BindingConfig{
		Status: status, FormalSSHPublicKey: string(clientPublic), Epoch: 7,
		PreparedExpiresAt: time.Now().Add(time.Minute),
	}}}
	server := &PermanentTailcatServer{
		state: NewTwoPhaseState(store), agentID: "isolated-agent", hostSigner: hostSigner,
		activeConns: make(map[net.Conn]struct{}),
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close(); _ = server.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.handleSSH(conn)
		}
	}()
	clientConfig := &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.FixedHostKey(hostSigner.PublicKey()), Timeout: 3 * time.Second,
	}
	return server, store, func() (*ssh.Client, error) {
		return ssh.Dial("tcp", listener.Addr().String(), clientConfig)
	}
}

func isolatedClientSocket(t *testing.T) ([]byte, *atomic.Int32) {
	t.Helper()
	// A short temporary XDG path avoids sockaddr_un limits without accessing
	// the test runner's actual Herdr configuration or existing sessions.
	dir, err := os.MkdirTemp("", "herdrx-tunnel-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "herdr", "sessions", "isolated", "herdr-client.sock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepts := new(atomic.Int32)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	return ssh.Marshal(struct {
		Path     string
		Reserved string
		Port     uint32
	}{Path: path}), accepts
}

func requireGeometryAccess(t *testing.T, client *ssh.Client, allowed bool) {
	t.Helper()
	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("open geometry session: %v", err)
	}
	defer session.Close()
	// The PID is deliberately absent. Authorization is checked at Start,
	// independently of terminal geometry lookup (covered by helper tests).
	err = session.Start("herdrx-terminal-geometry 1073741824")
	if allowed && err != nil {
		t.Fatalf("active geometry command rejected before execution: %v", err)
	}
	if !allowed && err == nil {
		t.Error("unauthorized geometry command reached execution")
	}
	if err == nil {
		_ = session.Wait()
	}
}

func requireClientSocketDenied(t *testing.T, client *ssh.Client, payload []byte) {
	t.Helper()
	if stream, _, err := client.OpenChannel("direct-streamlocal@openssh.com", payload); err == nil {
		_ = stream.Close()
		t.Error("unauthorized client socket stream was accepted")
	}
}

func openClientEcho(t *testing.T, client *ssh.Client, payload []byte) ssh.Channel {
	t.Helper()
	stream, requests, err := client.OpenChannel("direct-streamlocal@openssh.com", payload)
	if err != nil {
		t.Fatalf("active client socket stream rejected: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	go ssh.DiscardRequests(requests)
	if _, err := stream.Write([]byte("once")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(stream, buf); err != nil || string(buf) != "once" {
		t.Fatalf("forwarded client socket echo: %q, %v", buf, err)
	}
	return stream
}

func TestPermanentClientSocketAndGeometryCannotUpgradePreparedConnection(t *testing.T) {
	payload, accepts := isolatedClientSocket(t)
	_, store, dial := permanentSSHFixture(t, false)
	prepared, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	requireClientSocketDenied(t, prepared, payload)
	requireGeometryAccess(t, prepared, false)
	if _, err := store.Update(func(cfg *agent.Config) error {
		cfg.Binding.Status, cfg.Paired = "active", true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	requireClientSocketDenied(t, prepared, payload)
	requireGeometryAccess(t, prepared, false)
	if accepts.Load() != 0 {
		t.Fatal("prepared authentication reached the client socket backend")
	}
	active, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	openClientEcho(t, active, payload)
	requireGeometryAccess(t, active, true)
	if accepts.Load() != 1 {
		t.Fatalf("unexpected backend stream count: %d", accepts.Load())
	}
}

func TestPermanentClientAccessRequiresCurrentActiveBinding(t *testing.T) {
	for _, change := range []string{"unpaired", "epoch", "revoked"} {
		t.Run(change, func(t *testing.T) {
			payload, accepts := isolatedClientSocket(t)
			server, store, dial := permanentSSHFixture(t, true)
			client, err := dial()
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			stream := openClientEcho(t, client, payload)
			requireGeometryAccess(t, client, true)
			if _, err := store.Update(func(cfg *agent.Config) error {
				switch change {
				case "unpaired":
					cfg.Paired = false
				case "epoch":
					cfg.Binding.Epoch++
				case "revoked":
					cfg.Binding.Status, cfg.Paired, cfg.Revoked = "revoked", false, true
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			requireClientSocketDenied(t, client, payload)
			requireGeometryAccess(t, client, false)
			if ok, _, err := client.SendRequest("keepalive@herdrx", true, nil); err != nil || ok {
				t.Fatalf("expired authorization keepalive: ok=%v err=%v", ok, err)
			}
			if accepts.Load() != 1 {
				t.Errorf("expired authorization opened extra backend streams: %d", accepts.Load())
			}
			fresh, err := dial()
			if change == "epoch" {
				if err != nil {
					t.Fatalf("fresh epoch authentication failed: %v", err)
				}
				_ = fresh.Close()
			} else if err == nil {
				_ = fresh.Close()
				t.Error("inactive binding allowed a fresh SSH authentication")
			}
			// The production unpair lifecycle persists revocation and closes the
			// permanent server. That close must also release an existing stream.
			if err := server.Close(); err != nil {
				t.Fatal(err)
			}
			closed := make(chan error, 1)
			go func() { _, err := stream.Read(make([]byte, 1)); closed <- err }()
			select {
			case err := <-closed:
				if err == nil {
					t.Fatal("closed permanent server left a client stream readable")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("closed permanent server retained a client socket stream")
			}
		})
	}
}
