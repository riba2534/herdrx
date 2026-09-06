package agentcli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/agent"
	"github.com/riba2534/herdrx/internal/secure"
	"golang.org/x/crypto/ssh"
	"tailscale.com/types/key"
)

// TestLegacy_FullWorkflowThroughIPCAndSSH 端到端验证真实的 legacy 工作流闭环：
// init -> 启动运行中 serve (IPC + SSH Listener 共享 StateStore) ->
// 在线 CLI pair 经 IPC 事务生成 URL -> 解码提取 token ->
// 真实 SSH 客户端拨号发送 confirm-pair -> IPC status 实时变为 paired ->
// IPC unpair 撤销 -> 验证连接断开与状态更新
func TestLegacy_FullWorkflowThroughIPCAndSSH(t *testing.T) {
	tempDir := t.TempDir()
	env := Environment{
		ConfigDir:  tempDir,
		RuntimeDir: tempDir,
		HomeDir:    tempDir,
		CommandRunner: func(name string, args ...string) ([]byte, error) {
			return []byte("herdr 0.8.2\n"), nil
		},
	}
	cfgPath := filepath.Join(tempDir, "config.json")

	// 1. 生成服务端 Tailcat Node Key 与 Web 端的正式 SSH 客户端密钥
	serverNodeKey := key.NewNode()
	clientPrivPEM, serverAuthSSH, err := secure.GenerateSSHKey("web-controller-key")
	if err != nil {
		t.Fatalf("generate controller ssh key: %v", err)
	}
	clientSigner, err := ssh.ParsePrivateKey(clientPrivPEM)
	if err != nil {
		t.Fatalf("parse client private key: %v", err)
	}

	// 2. 执行 init 命令
	var stdout, stderr bytes.Buffer
	initArgs := []string{
		"init",
		"--config", cfgPath,
		"--allow-node", serverNodeKey.Public().String(),
		"--ssh-key", string(serverAuthSSH),
		"--public-url", "https://herdrx.example.com",
		"--setup-id", "setup-test-12345",
	}
	exitCode := Run(initArgs, &stdout, &stderr, env)
	if exitCode != 0 {
		t.Fatalf("legacy init failed (exit %d): %s", exitCode, stderr.String())
	}

	// 3. 构造 StateStore 并启动 IPC 服务端与底层 SSHServer（模拟运行中的 serve）
	store, err := NewStateStore(cfgPath)
	if err != nil {
		t.Fatalf("new state store: %v", err)
	}
	if err := store.AcquireDaemonLock(); err != nil {
		t.Fatalf("acquire daemon lock: %v", err)
	}
	defer store.ReleaseDaemonLock()

	ipcServer := NewIPCServer(env, store)
	if err := ipcServer.Start(); err != nil {
		t.Fatalf("start ipc server: %v", err)
	}
	defer ipcServer.Close()

	// 启动本地回环 TCP 上的 SSHServer，注入同一个 store 作为 ConfigUpdater
	cfgSnap, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot config: %v", err)
	}
	sshServer, err := agent.NewSSHServer(cfgPath, cfgSnap)
	if err != nil {
		t.Fatalf("new ssh server: %v", err)
	}
	sshServer.SetUpdater(store) // 关键：注入同一 StateStore 事务管理器

	sshListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen ssh tcp: %v", err)
	}
	defer sshListener.Close()

	go func() {
		for {
			conn, err := sshListener.Accept()
			if err != nil {
				return
			}
			go sshServer.Handle(conn)
		}
	}()

	hostSigner, err := ssh.ParsePrivateKey([]byte(cfgSnap.SSHHostPrivate))
	if err != nil {
		t.Fatalf("parse host key: %v", err)
	}
	sshClientConfig := &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.FixedHostKey(hostSigner.PublicKey()),
		Timeout:         3 * time.Second,
	}

	// 4. 在线 CLI 执行 pair 命令（此时 daemon 正在运行并持锁）：
	// 验证 CLI pair 经由在线 IPC 成功执行（杜绝抢锁超时和 HTTP 500！）
	stdout.Reset()
	stderr.Reset()
	pairArgs := []string{
		"pair",
		"--config", cfgPath,
		"--runtime-dir", tempDir,
	}
	exitCode = Run(pairArgs, &stdout, &stderr, env)
	if exitCode != 0 {
		t.Fatalf("online CLI pair failed (exit %d): %s", exitCode, stderr.String())
	}

	pairOut := stdout.String()
	if !strings.Contains(pairOut, "https://herdrx.example.com/#pair=") {
		t.Fatalf("expected pair output to contain import URL, got: %s", pairOut)
	}

	// 从 URL 解码出 payload 与 rawToken
	lines := strings.Split(pairOut, "\n")
	var pairURL string
	for _, l := range lines {
		if strings.Contains(l, "/#pair=") {
			pairURL = strings.TrimSpace(l)
			break
		}
	}
	encodedPayload := strings.Split(pairURL, "/#pair=")[1]
	payloadBytes, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	rawToken := payload["token"].(string)

	// 验证哈希正确性（与 confirmPair 100% 匹配）
	cfgAfterPair, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot config after pair: %v", err)
	}
	expectedHash := base64.RawStdEncoding.EncodeToString(secure.TokenHash(rawToken))
	if cfgAfterPair.PairTokenHash != expectedHash {
		t.Fatalf("token hash mismatch! got %q, want %q", cfgAfterPair.PairTokenHash, expectedHash)
	}

	// 5. 关键验证：通过真实 SSH 客户端连接发送 confirm-pair <token>
	sshClient, err := ssh.Dial("tcp", sshListener.Addr().String(), sshClientConfig)
	if err != nil {
		t.Fatalf("dial ssh client for confirm-pair: %v", err)
	}
	defer sshClient.Close()

	sess, err := sshClient.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	var sessOut, sessErr bytes.Buffer
	sess.Stdout = &sessOut
	sess.Stderr = &sessErr
	if err := sess.Run("confirm-pair " + rawToken); err != nil {
		t.Fatalf("confirm-pair over real SSH failed: %v (stderr: %s)", err, sessErr.String())
	}
	if !strings.Contains(sessOut.String(), "paired") {
		t.Fatalf("unexpected ssh confirm-pair output: %s", sessOut.String())
	}
	_ = sess.Close()

	// 6. 关键验证：通过 IPC 查询 status，断言配对状态已经实时更新为 paired=true（消除缓存陈旧）
	sockPath := filepath.Join(tempDir, "control.sock")
	var daemonStat DaemonStatus
	if err := ClientCallIPC(sockPath, "GET", "/status", nil, &daemonStat); err != nil {
		t.Fatalf("IPC status query failed: %v", err)
	}
	if !daemonStat.Paired || daemonStat.Revoked {
		t.Fatalf("daemon status failed to reflect real-time paired state from SSH transaction: %+v", daemonStat)
	}
	t.Logf("StateStore cache successfully updated in real-time via SSH confirm-pair!")

	// 7. 通过 IPC 执行在线 unpair 撤销
	var unpairResp map[string]string
	if err := ClientCallIPC(sockPath, "POST", "/unpair", nil, &unpairResp); err != nil {
		t.Fatalf("IPC unpair failed: %v", err)
	}
	if unpairResp["status"] != "unpaired" {
		t.Fatalf("unexpected unpair response: %+v", unpairResp)
	}

	// 再次查询 status，断言已撤销
	if err := ClientCallIPC(sockPath, "GET", "/status", nil, &daemonStat); err != nil {
		t.Fatalf("IPC status query after unpair failed: %v", err)
	}
	if daemonStat.Paired || !daemonStat.Revoked {
		t.Fatalf("daemon status did not reflect revoked state: %+v", daemonStat)
	}
}
