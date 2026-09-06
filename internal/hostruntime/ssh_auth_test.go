package hostruntime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"golang.org/x/crypto/ssh"
)

// Exercise the actual vault -> shared credential -> SSH handshake, including
// encrypted imports and certificates, against a disposable local SSH server.
func TestSSHManagedKeyAndPasswordHandshake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := secure.OpenVault(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CreateUser(ctx, store.User{ID: "owner", Email: "owner@example.test", Role: "user", DisplayName: "Fixture", PasswordHash: "unused"}); err != nil {
		t.Fatal(err)
	}
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(private, "fixture", []byte("import-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	material := secure.SSHKeyMaterial{PrivateKey: string(pem.EncodeToMemory(block)), Passphrase: "import-passphrase"}
	signer, _, err := material.Parse()
	if err != nil {
		t.Fatal(err)
	}
	serverPrivate, _, _ := secure.GenerateSSHKey("")
	serverKey, _ := ssh.ParsePrivateKey(serverPrivate)
	caPrivate, _, _ := secure.GenerateSSHKey("")
	ca, _ := ssh.ParsePrivateKey(caPrivate)
	cert := &ssh.Certificate{Key: signer.PublicKey(), CertType: ssh.UserCert, ValidPrincipals: []string{"custom-user"}, ValidAfter: uint64(time.Now().Add(-time.Minute).Unix()), ValidBefore: uint64(time.Now().Add(time.Hour).Unix())}
	if err = cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	checker := ssh.CertChecker{IsUserAuthority: func(key ssh.PublicKey) bool { return string(key.Marshal()) == string(ca.PublicKey().Marshal()) }}
	config := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if meta.User() != "custom-user" || string(password) != "fixture-password" {
				return nil, fmt.Errorf("unexpected login")
			}
			return nil, nil
		},
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != "custom-user" {
				return nil, fmt.Errorf("custom SSH username was not used")
			}
			if _, ok := key.(*ssh.Certificate); ok {
				return checker.Authenticate(meta, key)
			}
			if string(key.Marshal()) != string(signer.PublicKey().Marshal()) {
				return nil, fmt.Errorf("incorrect client key")
			}
			return nil, nil
		},
	}
	config.AddHostKey(serverKey)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
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
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					for request := range requests {
						if request.Type == "exec" {
							_ = request.Reply(true, nil)
							_, _ = io.WriteString(channel, "/workspace/fixture\n\n")
							_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
							_ = channel.Close()
							break
						} else {
							_ = request.Reply(false, nil)
						}
					}
				}
			}()
		}
	}()
	factory := &Factory{Store: db, Vault: vault}
	defer func() { factory.Close(); listener.Close(); workers.Wait() }()
	port, _ := strconv.Atoi(strings.Split(listener.Addr().String(), ":")[1])
	for _, mode := range []string{"saved_key", "certificate", "password"} {
		kind := mode
		secret := []byte("fixture-password")
		if mode != "password" {
			kind = "saved_key"
			m := material
			if mode == "certificate" {
				m.Certificate = string(ssh.MarshalAuthorizedKey(cert))
			}
			secret, _ = json.Marshal(m)
		}
		id := "key-" + mode
		encrypted, err := SealCredential(vault, "owner", id, kind, secret)
		if err != nil {
			t.Fatal(err)
		}
		credential := store.Credential{ID: id, OwnerID: "owner", Kind: kind, Ciphertext: encrypted}
		if kind == "saved_key" {
			err = db.CreateSSHKey(ctx, store.SSHKey{ID: id, OwnerID: "owner", Name: mode, PublicKey: string(ssh.MarshalAuthorizedKey(signer.PublicKey())), Fingerprint: ssh.FingerprintSHA256(signer.PublicKey()), Algorithm: signer.PublicKey().Type()}, credential)
		} else {
			err = db.CreateCredential(ctx, credential)
		}
		if err != nil {
			t.Fatal(err)
		}
		for i := range 2 {
			host := store.Host{ID: fmt.Sprintf("%s-%d", mode, i), OwnerID: "owner", Name: mode, Transport: "ssh", Hostname: "127.0.0.1", Port: port, Username: "custom-user", AuthMethod: kind, CredentialID: id, HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(serverKey.PublicKey())))}
			if i == 0 {
				host.HostKey = ""
			}
			if err = db.CreateHost(ctx, host); err != nil {
				t.Fatal(err)
			}
			host, err = db.HostByID(ctx, "owner", host.ID)
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				untrusted := host
				untrusted.HostKey = ""
				handle, err := factory.openUncached(ctx, untrusted)
				var unknown *herdr.UnknownHostKeyError
				if handle != nil || !errors.As(err, &unknown) {
					t.Fatalf("first SSH contact must return a nil interface and fingerprint: %T %v", handle, err)
				}
				if _, err = factory.Open(ctx, untrusted); !errors.As(err, &unknown) {
					t.Fatalf("pooled first SSH contact: %v", err)
				}
				pending, err := db.HostByID(ctx, "owner", host.ID)
				if err != nil || pending.PendingHostKey != strings.TrimSpace(string(ssh.MarshalAuthorizedKey(serverKey.PublicKey()))) {
					t.Fatal("first contact did not persist the expected pending key", err)
				}
				if err := db.TrustHostKey(ctx, "owner", host.ID, pending.PendingHostKey); err != nil {
					t.Fatal(err)
				}
				factory.CloseHost(host.ID)
				host, err = db.HostByID(ctx, "owner", host.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			handle, err := factory.Open(ctx, host)
			if err != nil {
				t.Fatalf("%s handshake: %v", mode, err)
			}
			handle.Close()
			factory.CloseHost(host.ID)
		}
	}
	// An authentication failure after a verified host key must also leave the
	// process and pool usable, without a typed nil endpoint to clean up.
	badID := "bad-password"
	encrypted, err := SealCredential(vault, "owner", badID, "password", []byte("incorrect-password"))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CreateCredential(ctx, store.Credential{ID: badID, OwnerID: "owner", Kind: "password", Ciphertext: encrypted}); err != nil {
		t.Fatal(err)
	}
	badHost := store.Host{ID: badID, OwnerID: "owner", Name: "Bad password", Transport: "ssh", Hostname: "127.0.0.1", Port: port, Username: "custom-user", AuthMethod: "password", CredentialID: badID, HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(serverKey.PublicKey())))}
	if endpoint, err := factory.openUncached(ctx, badHost); err == nil || endpoint != nil {
		t.Fatalf("authentication failure returned endpoint %T: %v", endpoint, err)
	}
	if endpoint, err := factory.Open(ctx, badHost); err == nil || endpoint != nil {
		t.Fatalf("pooled authentication failure returned endpoint %T: %v", endpoint, err)
	}
	if stats := factory.Stats(); stats.Connections != 0 || stats.Pending != 0 {
		t.Fatal("failed handshake retained a live pool entry", stats)
	}
}
