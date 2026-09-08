package agent

import (
	"bytes"
	"encoding/base64"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/testpaths"
	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
)

func setupTestSSH(t *testing.T, paired bool, revoked bool) (string, *SSHServer, *ssh.ClientConfig, ssh.PublicKey, string) {
	t.Helper()

	clientPrivPEM, clientAuthKey, err := secure.GenerateSSHKey("client-key")
	if err != nil {
		t.Fatalf("generate client SSH key: %v", err)
	}
	clientSigner, err := ssh.ParsePrivateKey(clientPrivPEM)
	if err != nil {
		t.Fatalf("parse client private key: %v", err)
	}

	hostPrivPEM, hostAuthKey, err := secure.GenerateSSHKey("host-key")
	if err != nil {
		t.Fatalf("generate host SSH key: %v", err)
	}
	hostPubKey, _, _, _, err := ssh.ParseAuthorizedKey(hostAuthKey)
	if err != nil {
		t.Fatalf("parse host public key: %v", err)
	}

	tempDir := testpaths.ShortTempDir(t)
	configPath := filepath.Join(tempDir, "config.json")

	node := tailcat.NewPrivateKey()
	token, _ := secure.Token(16)
	tokenHash := base64.RawStdEncoding.EncodeToString(secure.TokenHash(token))

	cfg := Config{
		Version:          1,
		Node:             *node,
		AllowedNodeKey:   string(node.Public.Addr()),
		AuthorizedSSHKey: strings.TrimSpace(string(clientAuthKey)),
		SSHHostPrivate:   string(hostPrivPEM),
		PublicURL:        "http://127.0.0.1:8080",
		SetupID:          "test-setup-id",
		PairTokenHash:    tokenHash,
		PairExpiresAt:    time.Now().Add(10 * time.Minute),
		Paired:           paired,
		Revoked:          revoked,
	}

	if err := Save(configPath, cfg); err != nil {
		t.Fatalf("save initial config: %v", err)
	}

	server, err := NewSSHServer(configPath, cfg)
	if err != nil {
		t.Fatalf("new ssh server: %v", err)
	}

	// 强制使用 FixedHostKey 进行主机公钥 pinning 验证，禁止 InsecureIgnoreHostKey
	clientConfig := &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.FixedHostKey(hostPubKey),
		Timeout:         3 * time.Second,
	}

	return configPath, server, clientConfig, hostPubKey, token
}

func dialLocalTCP(t *testing.T, server *SSHServer, clientConfig *ssh.ClientConfig) (*ssh.Client, error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.Handle(conn)
		}
	}()

	return ssh.Dial("tcp", listener.Addr().String(), clientConfig)
}

func TestStreamLocal_PairedGateControlAndPass(t *testing.T) {
	tempDir := testpaths.ShortTempDir(t)
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	herdrDir := filepath.Join(tempDir, "herdr")
	if err := os.MkdirAll(herdrDir, 0o700); err != nil {
		t.Fatalf("mkdir herdr: %v", err)
	}
	mockSocketPath := filepath.Join(herdrDir, "herdr.sock")
	unixListener, err := net.Listen("unix", mockSocketPath)
	if err != nil {
		t.Fatalf("listen unix mock socket: %v", err)
	}
	defer unixListener.Close()

	var acceptCount int32
	go func() {
		for {
			c, err := unixListener.Accept()
			if err != nil {
				return
			}
			atomic.AddInt32(&acceptCount, 1)
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn) // echo
			}(c)
		}
	}()

	type streamLocalPayload struct {
		SocketPath string
		Reserved0  string
		Reserved1  uint32
	}
	reqPayload := ssh.Marshal(streamLocalPayload{SocketPath: mockSocketPath})

	// 未配对连接请求合法的 Herdr socket：断言直接在 SSH 层 Reject，且后端零连接
	{
		configPath, server, clientConfig, _, _ := setupTestSSH(t, false, false)
		_ = configPath
		client, err := dialLocalTCP(t, server, clientConfig)
		if err != nil {
			t.Fatalf("dial client: %v", err)
		}
		defer client.Close()

		_, _, err = client.OpenChannel("direct-streamlocal@openssh.com", reqPayload)
		if err == nil {
			t.Fatalf("expected streamlocal to be rejected for unpaired agent")
		}
		if !strings.Contains(err.Error(), "requires an active paired agent") && !strings.Contains(err.Error(), "prohibited") {
			t.Fatalf("unexpected streamlocal reject error: %v", err)
		}
		if count := atomic.LoadInt32(&acceptCount); count != 0 {
			t.Fatalf("backend socket must not accept any connection when unpaired, got %d", count)
		}
	}

	// 正式已配对连接（新建连接）：断言 streamlocal 转发成功打通并可进行双向数据交互（功能不退化）
	{
		_, server, clientConfig, _, _ := setupTestSSH(t, true, false)
		client, err := dialLocalTCP(t, server, clientConfig)
		if err != nil {
			t.Fatalf("dial client: %v", err)
		}
		defer client.Close()

		ch, reqs, err := client.OpenChannel("direct-streamlocal@openssh.com", reqPayload)
		if err != nil {
			t.Fatalf("expected streamlocal to succeed for paired agent, got: %v", err)
		}
		defer ch.Close()
		go ssh.DiscardRequests(reqs)

		testMsg := []byte("herdr-ping-test")
		if _, err := ch.Write(testMsg); err != nil {
			t.Fatalf("write to forwarded channel: %v", err)
		}
		buf := make([]byte, len(testMsg))
		if _, err := io.ReadFull(ch, buf); err != nil {
			t.Fatalf("read from forwarded channel: %v", err)
		}
		if !bytes.Equal(buf, testMsg) {
			t.Fatalf("echo mismatch: got %s, want %s", string(buf), string(testMsg))
		}
		if count := atomic.LoadInt32(&acceptCount); count != 1 {
			t.Fatalf("expected 1 backend connection for paired agent, got %d", count)
		}
	}
}

// TestUnpairedExecDenied_HerdrStageImageAndShell 确认在未配对时，所有白名单命令在协议层（Session.Start）即被拒绝
func TestUnpairedExecDenied_HerdrStageImageAndShell(t *testing.T) {
	_, server, clientConfig, _, _ := setupTestSSH(t, false, false)
	client, err := dialLocalTCP(t, server, clientConfig)
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer client.Close()

	// 1. herdr terminal session observe 严格在 Session.Start 协议层拒绝建立 exec 通道
	{
		sess, err := client.NewSession()
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		err = sess.Start("herdr terminal session observe s1 --cols 80 --rows 24")
		if err == nil {
			_ = sess.Close()
			t.Fatalf("expected herdr terminal observe to fail at protocol level (Session.Start) when unpaired")
		}
		_ = sess.Close()
	}

	// 2. herdrx-stage-image 在协议层拒绝
	{
		sess, err := client.NewSession()
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		err = sess.Start("herdrx-stage-image png")
		if err == nil {
			_ = sess.Close()
			t.Fatalf("expected stage-image to fail at protocol level (Session.Start) when unpaired")
		}
		_ = sess.Close()
	}

	// 3. uname -sm 在协议层拒绝
	{
		sess, err := client.NewSession()
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		err = sess.Start("uname -sm")
		if err == nil {
			_ = sess.Close()
			t.Fatalf("expected uname to fail at protocol level (Session.Start) when unpaired")
		}
		_ = sess.Close()
	}

	// 4. config-path (sh -c ...) 在协议层拒绝
	{
		sess, err := client.NewSession()
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		err = sess.Start("sh -c 'printf \"%s\\n%s\\n\" \"$HOME\" \"${XDG_CONFIG_HOME:-}\"'")
		if err == nil {
			_ = sess.Close()
			t.Fatalf("expected config-path to fail at protocol level (Session.Start) when unpaired")
		}
		_ = sess.Close()
	}

	// 5. PTY 与 Shell 模式拒绝
	{
		sess, err := client.NewSession()
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		if err := sess.RequestPty("xterm", 80, 24, nil); err == nil {
			t.Fatalf("expected pty-req to fail on unpaired agent")
		}
		if err := sess.Shell(); err == nil {
			t.Fatalf("expected shell to fail on unpaired agent")
		}
		_ = sess.Close()
	}
}

func TestPairingRoleFixed_NoInPlaceEscalation(t *testing.T) {
	configPath, server, clientConfig, _, validToken := setupTestSSH(t, false, false)

	client, err := dialLocalTCP(t, server, clientConfig)
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer client.Close()

	// 1. 错误 token 尝试配对：返回非零并报错，配置依然 unpaired
	{
		sess, err := client.NewSession()
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		err = sess.Run("confirm-pair wrong-token-xyz")
		if err == nil {
			t.Fatalf("expected wrong token confirm-pair to fail")
		}
		_ = sess.Close()

		cfg, err := Load(configPath)
		if err != nil || cfg.Paired {
			t.Fatalf("config must remain unpaired after invalid token")
		}
	}

	// 2. 正确 token 配对成功（且测试固定以 - 和 _ 前缀的真实合法 base64url token）
	{
		for _, customToken := range []string{validToken, "-dash-prefix-token-12345", "_under_prefix_token_67890"} {
			// 将当前配置重置为该 customToken
			cfg, err := Load(configPath)
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			cfg.Paired = false
			cfg.PairTokenHash = base64.RawStdEncoding.EncodeToString(secure.TokenHash(customToken))
			cfg.PairExpiresAt = time.Now().Add(10 * time.Minute)
			if err := Save(configPath, cfg); err != nil {
				t.Fatalf("save config with custom token: %v", err)
			}

			sess, err := client.NewSession()
			if err != nil {
				t.Fatalf("create session: %v", err)
			}
			var stdout bytes.Buffer
			sess.Stdout = &stdout
			if err := sess.Run("confirm-pair " + customToken); err != nil {
				t.Fatalf("confirm-pair with token %q failed: %v", customToken, err)
			}
			if !strings.Contains(stdout.String(), "paired") {
				t.Fatalf("expected output 'paired', got %s", stdout.String())
			}
			_ = sess.Close()

			cfgAfter, err := Load(configPath)
			if err != nil || !cfgAfter.Paired {
				t.Fatalf("expected config to be paired after confirming %q", customToken)
			}
		}
	}

	// 3. 核心断言：在同一 SSH 连接中，配对成功后仍然不得原地升级执行 uname（Session.Start 协议层拒绝）
	{
		sess, err := client.NewSession()
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		err = sess.Start("uname -sm")
		if err == nil {
			_ = sess.Close()
			t.Fatalf("in-place privilege escalation must be denied at protocol level on the pairing connection")
		}
		_ = sess.Close()
	}

	// 4. 新建正式 SSH 连接：认证时刻已是 Paired，正常放行 uname
	{
		formalClient, err := dialLocalTCP(t, server, clientConfig)
		if err != nil {
			t.Fatalf("dial formal client: %v", err)
		}
		defer formalClient.Close()

		sess, err := formalClient.NewSession()
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		var stdout bytes.Buffer
		sess.Stdout = &stdout
		if err := sess.Run("uname -sm"); err != nil {
			t.Fatalf("formal client uname failed: %v", err)
		}
		if len(strings.TrimSpace(stdout.String())) == 0 {
			t.Fatalf("expected uname output")
		}
		_ = sess.Close()
	}
}

// TestPairingRoleFixed_ConcurrentPairingRace 验证角色在 PublicKeyCallback 写入 Permissions，
// 并发配对修改磁盘无法影响正在完成握手的连接角色
func TestPairingRoleFixed_ConcurrentPairingRace(t *testing.T) {
	configPath, server, clientConfig, _, validToken := setupTestSSH(t, false, false)

	// 包装 PublicKeyCallback：在认证检查通过后、握手完成前故意修改磁盘 config.Paired = true
	origCallback := server.config.PublicKeyCallback
	server.config.PublicKeyCallback = func(connMetadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		perms, err := origCallback(connMetadata, key)
		if err != nil {
			return nil, err
		}
		// 模拟并发竞态：另一个连接在此时将磁盘配置置为 Paired=true
		cfg, _ := Load(configPath)
		cfg.Paired = true
		_ = Save(configPath, cfg)
		return perms, nil
	}

	client, err := dialLocalTCP(t, server, clientConfig)
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer client.Close()

	// 断言：由于认证时刻写入的 Permissions 角色为 unpaired，该连接依然是 pairing-only，禁止执行 uname
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	err = sess.Start("uname -sm")
	if err == nil {
		_ = sess.Close()
		t.Fatalf("connection must be locked to unpaired role from Permissions, uname must fail at protocol level")
	}
	_ = sess.Close()

	// 验证其依然可以正常运行 confirm-pair
	sess2, err := client.NewSession()
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	var stdout bytes.Buffer
	sess2.Stdout = &stdout
	if err := sess2.Run("confirm-pair " + validToken); err != nil {
		t.Fatalf("confirm-pair must still succeed on pairing connection: %v", err)
	}
	_ = sess2.Close()
}

func TestRevocationClosesActiveConnectionAndBlocksNewHandshake(t *testing.T) {
	configPath, server, clientConfig, _, _ := setupTestSSH(t, true, false)

	// 1. 建立存活连接
	client, err := dialLocalTCP(t, server, clientConfig)
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer client.Close()

	// 验证撤销前连接处于健康状态
	{
		sess, err := client.NewSession()
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		if err := sess.Run("uname -sm"); err != nil {
			t.Fatalf("pre-revocation uname failed: %v", err)
		}
		_ = sess.Close()
	}

	// 2. 触发撤销
	if err := Unpair(configPath); err != nil {
		t.Fatalf("unpair: %v", err)
	}

	// 3. 等待 watchRevocation 监测到撤销并关闭底层连接
	connClosed := make(chan error, 1)
	go func() {
		connClosed <- client.Wait()
	}()

	select {
	case err := <-connClosed:
		t.Logf("connection closed by server upon revocation as expected: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatalf("client connection was not closed within timeout after revocation")
	}

	// 4. 尝试发起全新 SSH 握手：必须在握手层明确被拒绝
	newClient, err := dialLocalTCP(t, server, clientConfig)
	if err == nil {
		newClient.Close()
		t.Fatalf("new handshake must fail after agent revocation")
	}
	if !strings.Contains(err.Error(), "revoked") && !strings.Contains(err.Error(), "handshake") {
		t.Fatalf("expected revocation handshake failure, got: %v", err)
	}
}

func TestClientSocketPairingRoleAndRevocation(t *testing.T) {
	for _, suffix := range []string{"herdr-client.sock", "sessions/isolated/herdr-client.sock"} {
		t.Run(suffix, func(t *testing.T) {
			// Keep the Unix path below sockaddr_un's limit, and isolate XDG from
			// every real Herdr session on the machine running this test.
			dir := testpaths.ShortTempDir(t)
			t.Setenv("XDG_CONFIG_HOME", dir)
			path := filepath.Join(dir, "herdr", suffix)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			var accepts atomic.Int32
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
			payload := ssh.Marshal(struct {
				Path     string
				Reserved string
				Port     uint32
			}{Path: path})
			configPath, server, clientConfig, _, token := setupTestSSH(t, false, false)
			pairing, err := dialLocalTCP(t, server, clientConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer pairing.Close()
			deny := func(client *ssh.Client) {
				t.Helper()
				if ch, _, err := client.OpenChannel("direct-streamlocal@openssh.com", payload); err == nil {
					_ = ch.Close()
					t.Fatal("unauthorized client socket stream was accepted")
				}
				sess, err := client.NewSession()
				if err != nil {
					return // A revoked SSH connection may already be closed.
				}
				defer sess.Close()
				if err := sess.Start("herdrx-terminal-geometry 1073741824"); err == nil {
					t.Fatal("unauthorized terminal geometry command was accepted")
				}
			}
			deny(pairing)
			if err := confirmPair(configPath, token); err != nil {
				t.Fatal(err)
			}
			deny(pairing) // Pairing cannot upgrade an already authenticated connection.
			if accepts.Load() != 0 {
				t.Fatal("pairing-only connection reached a backend client socket")
			}
			formal, err := dialLocalTCP(t, server, clientConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer formal.Close()
			stream, requests, err := formal.OpenChannel("direct-streamlocal@openssh.com", payload)
			if err != nil {
				t.Fatalf("paired client socket stream: %v", err)
			}
			defer stream.Close()
			go ssh.DiscardRequests(requests)
			if _, err := stream.Write([]byte("once")); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 4)
			if _, err := io.ReadFull(stream, buf); err != nil || string(buf) != "once" {
				t.Fatalf("client socket echo: %q, %v", buf, err)
			}
			sess, err := formal.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			if err := sess.Start("herdrx-terminal-geometry 1073741824"); err != nil {
				t.Fatalf("paired geometry command rejected before execution: %v", err)
			}
			_ = sess.Wait() // Nonexistent PID; geometry success is tested separately.
			_ = sess.Close()
			if err := Unpair(configPath); err != nil {
				t.Fatal(err)
			}
			deny(formal) // New channels must fail even before the revocation ticker runs.
			closed := make(chan error, 1)
			go func() { _, err := stream.Read(make([]byte, 1)); closed <- err }()
			select {
			case err := <-closed:
				if err == nil {
					t.Fatal("revocation left the existing client stream readable")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("revocation did not close the existing client socket stream")
			}
			if client, err := dialLocalTCP(t, server, clientConfig); err == nil {
				_ = client.Close()
				t.Fatal("revocation allowed a new SSH handshake")
			}
			if accepts.Load() != 1 {
				t.Fatalf("unauthorized attempts opened extra backend streams: %d", accepts.Load())
			}
		})
	}
}

// TestHerdrBinCustomPathExec_WithSpaces 验证配置中的 HerdrBin 绝对路径被真实用于 SSH terminal 命令执行，
// 且路径带空格或非标准路径时能安全调用
func TestHerdrBinCustomPathExec_WithSpaces(t *testing.T) {
	tempDir := testpaths.ShortTempDir(t)
	spacedDir := filepath.Join(tempDir, "custom herdr bin with space")
	if err := os.MkdirAll(spacedDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	customHerdr := filepath.Join(spacedDir, "fake_herdr.sh")
	script := `#!/bin/sh
echo "custom-herdr-called: $@"
exit 0
`
	if err := os.WriteFile(customHerdr, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	configPath, server, clientConfig, _, _ := setupTestSSH(t, true, false)

	// 将 customHerdr 路径写入配置
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.HerdrBin = customHerdr
	if err := Save(configPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	client, err := dialLocalTCP(t, server, clientConfig)
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer sess.Close()

	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr

	cmd := "herdr terminal session observe s1 --cols 80 --rows 24"
	if err := sess.Run(cmd); err != nil {
		t.Fatalf("run herdr terminal failed: %v (stderr: %s)", err, stderr.String())
	}

	outStr := stdout.String()
	if !strings.Contains(outStr, "custom-herdr-called: terminal session observe s1 --cols 80 --rows 24") {
		t.Fatalf("expected custom herdr to be called with arguments, got: %s", outStr)
	}
}
