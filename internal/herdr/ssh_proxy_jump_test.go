package herdr

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"golang.org/x/crypto/ssh"
)

func TestParseProxyJump(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    ProxyJumpSpec
		wantErr bool
	}{
		{in: "jump.example:2222", want: ProxyJumpSpec{Host: "jump.example", Port: 2222}},
		{in: "alice@jump.example", want: ProxyJumpSpec{User: "alice", Host: "jump.example", Port: 22}},
		{in: "alice@jump.example:22", want: ProxyJumpSpec{User: "alice", Host: "jump.example", Port: 22}},
		{in: "jump", want: ProxyJumpSpec{Host: "jump", Port: 22}},
		{in: "[2001:db8::1]:22", want: ProxyJumpSpec{Host: "2001:db8::1", Port: 22}},
		{in: "bob@[2001:db8::1]:2200", want: ProxyJumpSpec{User: "bob", Host: "2001:db8::1", Port: 2200}},
		{in: "a,b", wantErr: true},
		{in: "user@host:port", wantErr: true},
		{in: "", wantErr: true},
		{in: "user@", wantErr: true},
		{in: "bad host", wantErr: true},
	}
	for _, tc := range cases {
		got, err := ParseProxyJump(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%q: expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("%q: got %+v want %+v", tc.in, got, tc.want)
		}
	}
}

func TestDialSSHEndpointViaProxyJump(t *testing.T) {
	ctx := t.Context()
	clientPrivate, clientPublic, err := secure.GenerateSSHKey("herdrx-test")
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := ssh.ParsePrivateKey(clientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	targetHostPrivate, _, err := secure.GenerateSSHKey("")
	if err != nil {
		t.Fatal(err)
	}
	targetHostKey, err := ssh.ParsePrivateKey(targetHostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	jumpHostPrivate, _, err := secure.GenerateSSHKey("")
	if err != nil {
		t.Fatal(err)
	}
	jumpHostKey, err := ssh.ParsePrivateKey(jumpHostPrivate)
	if err != nil {
		t.Fatal(err)
	}

	targetConfig := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != "target-user" {
				return nil, fmt.Errorf("unexpected target user %q", meta.User())
			}
			if string(key.Marshal()) != string(clientKey.PublicKey().Marshal()) {
				return nil, fmt.Errorf("unexpected target client key")
			}
			return nil, nil
		},
	}
	targetConfig.AddHostKey(targetHostKey)
	targetListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetListener.Close()
	var workers sync.WaitGroup
	serveSSHSessions(t, &workers, targetListener, targetConfig, handleDiscoverySession)

	jumpConfig := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != "jump-user" {
				return nil, fmt.Errorf("unexpected jump user %q", meta.User())
			}
			if string(key.Marshal()) != string(clientKey.PublicKey().Marshal()) {
				return nil, fmt.Errorf("unexpected jump client key")
			}
			return nil, nil
		},
	}
	jumpConfig.AddHostKey(jumpHostKey)
	jumpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer jumpListener.Close()
	serveSSHJump(t, &workers, jumpListener, jumpConfig)
	t.Cleanup(func() {
		_ = jumpListener.Close()
		_ = targetListener.Close()
		workers.Wait()
	})

	targetPort := mustPort(t, targetListener.Addr().String())
	jumpPort := mustPort(t, jumpListener.Addr().String())
	hostKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(targetHostKey.PublicKey())))
	endpoint, err := DialSSHEndpoint(ctx, SSHOptions{
		Host: store.Host{
			Transport:  "ssh",
			Hostname:   "127.0.0.1",
			Port:       targetPort,
			Username:   "target-user",
			AuthMethod: "private_key",
			HostKey:    hostKey,
			ProxyJump:  fmt.Sprintf("jump-user@127.0.0.1:%d", jumpPort),
		},
		Secret:  clientPrivate,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial via jump: %v", err)
	}
	defer endpoint.Close()
	if !strings.HasSuffix(endpoint.socketPath, "/herdr/herdr.sock") {
		t.Fatalf("unexpected socket path %q", endpoint.socketPath)
	}
	_ = clientPublic
}

func TestDialSSHEndpointViaProxyJumpReusesTargetUser(t *testing.T) {
	ctx := t.Context()
	clientPrivate, _, err := secure.GenerateSSHKey("herdrx-test")
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := ssh.ParsePrivateKey(clientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	targetHostPrivate, _, _ := secure.GenerateSSHKey("")
	targetHostKey, _ := ssh.ParsePrivateKey(targetHostPrivate)
	jumpHostPrivate, _, _ := secure.GenerateSSHKey("")
	jumpHostKey, _ := ssh.ParsePrivateKey(jumpHostPrivate)

	authOK := func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != "shared-user" || string(key.Marshal()) != string(clientKey.PublicKey().Marshal()) {
			return nil, fmt.Errorf("auth failed for %q", meta.User())
		}
		return nil, nil
	}
	targetConfig := &ssh.ServerConfig{PublicKeyCallback: authOK}
	targetConfig.AddHostKey(targetHostKey)
	jumpConfig := &ssh.ServerConfig{PublicKeyCallback: authOK}
	jumpConfig.AddHostKey(jumpHostKey)

	targetListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetListener.Close()
	jumpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer jumpListener.Close()
	var workers sync.WaitGroup
	serveSSHSessions(t, &workers, targetListener, targetConfig, handleDiscoverySession)
	serveSSHJump(t, &workers, jumpListener, jumpConfig)
	t.Cleanup(func() {
		_ = jumpListener.Close()
		_ = targetListener.Close()
		workers.Wait()
	})

	endpoint, err := DialSSHEndpoint(ctx, SSHOptions{
		Host: store.Host{
			Transport:  "ssh",
			Hostname:   "127.0.0.1",
			Port:       mustPort(t, targetListener.Addr().String()),
			Username:   "shared-user",
			AuthMethod: "private_key",
			HostKey:    strings.TrimSpace(string(ssh.MarshalAuthorizedKey(targetHostKey.PublicKey()))),
			ProxyJump:  "127.0.0.1:" + strconv.Itoa(mustPort(t, jumpListener.Addr().String())),
		},
		Secret:  clientPrivate,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial via jump without jump user: %v", err)
	}
	_ = endpoint.Close()
}

func mustPort(t *testing.T, addr string) int {
	t.Helper()
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func serveSSHSessions(t *testing.T, workers *sync.WaitGroup, listener net.Listener, config *ssh.ServerConfig, handle func(ssh.Channel, <-chan *ssh.Request)) {
	t.Helper()
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
				sshConn, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer sshConn.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if incoming.ChannelType() != "session" {
						_ = incoming.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					channel, reqs, err := incoming.Accept()
					if err != nil {
						return
					}
					handle(channel, reqs)
				}
			}()
		}
	}()
}

func handleDiscoverySession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for request := range requests {
		if request.Type != "exec" {
			_ = request.Reply(false, nil)
			continue
		}
		_ = request.Reply(true, nil)
		_, _ = io.WriteString(channel, "/tmp/fixture-home\n\n")
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		return
	}
}

func serveSSHJump(t *testing.T, workers *sync.WaitGroup, listener net.Listener, config *ssh.ServerConfig) {
	t.Helper()
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
				sshConn, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer sshConn.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if incoming.ChannelType() != "direct-tcpip" {
						_ = incoming.Reject(ssh.UnknownChannelType, "jump only allows direct-tcpip")
						continue
					}
					var payload struct {
						Host string
						Port uint32
						Orig string
						OPrt uint32
					}
					if err := ssh.Unmarshal(incoming.ExtraData(), &payload); err != nil {
						_ = incoming.Reject(ssh.ConnectionFailed, "bad direct-tcpip")
						continue
					}
					upstream, err := net.DialTimeout("tcp", net.JoinHostPort(payload.Host, strconv.Itoa(int(payload.Port))), 5*time.Second)
					if err != nil {
						_ = incoming.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					channel, reqs, err := incoming.Accept()
					if err != nil {
						_ = upstream.Close()
						return
					}
					go ssh.DiscardRequests(reqs)
					go func() { _, _ = io.Copy(upstream, channel); _ = upstream.Close() }()
					_, _ = io.Copy(channel, upstream)
					_ = channel.Close()
					_ = upstream.Close()
				}
			}()
		}
	}()
}

func TestDialSSHEndpointRejectsInvalidProxyJump(t *testing.T) {
	_, err := DialSSHEndpoint(context.Background(), SSHOptions{
		Host: store.Host{Hostname: "127.0.0.1", Port: 22, Username: "u", AuthMethod: "password", ProxyJump: "a,b"},
		Secret: []byte("x"),
	})
	if err == nil || !strings.Contains(err.Error(), "proxy_jump") {
		t.Fatalf("expected proxy_jump validation error, got %v", err)
	}
}
