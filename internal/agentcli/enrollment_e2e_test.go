package agentcli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/tunnel"
	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

// isolatedEnv 构造严格隔离的环境变量切片，显式排除宿主继承的 HERDR_*、XDG_* 与外部 HOME
func isolatedEnv(configDir, runtimeDir, stateDir string) []string {
	cacheDir := filepath.Join(stateDir, "cache")
	_ = os.MkdirAll(cacheDir, 0o700)
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"XDG_CONFIG_HOME=" + configDir,
		"XDG_RUNTIME_DIR=" + runtimeDir,
		"XDG_STATE_HOME=" + stateDir,
		"XDG_CACHE_HOME=" + cacheDir,
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HERDR_") || strings.HasPrefix(kv, "XDG_") || strings.HasPrefix(kv, "HOME=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// buildCLI 动态从当前源码构建 race 插桩的可执行二进制到测试临时目录，绝不依赖本机固定安装或 Skip。
func buildCLI(t *testing.T, targetBin string) {
	t.Helper()
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		var lookErr error
		goBin, lookErr = exec.LookPath("go")
		if lookErr != nil {
			t.Fatalf("cannot locate go binary: %v", lookErr)
		}
	}
	buildCmd := exec.Command(goBin, "build", "-race", "-o", targetBin, "../../cmd/herdrx")
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	var buildErr bytes.Buffer
	buildCmd.Stderr = &buildErr
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("build herdrx binary from source failed: %v, stderr: %s", err, buildErr.String())
	}
}

// createMockHerdrFixture 创建隔离运行的 Mock Herdr 真实 Unix Domain Socket 与可信 fake CLI 脚本
// 严格放置在 ${XDG_CONFIG_HOME}/herdr/herdr.sock，以满足 allowedSocket 白名单校验并控制路径长度
func createMockHerdrFixture(t *testing.T, baseDir, configDir string) (string, string) {
	t.Helper()
	herdrDir := filepath.Join(configDir, "herdr")
	if err := os.MkdirAll(herdrDir, 0o700); err != nil {
		t.Fatalf("mkdir herdr config dir: %v", err)
	}
	mockSocketPath := filepath.Join(herdrDir, "herdr.sock")
	listener, err := net.Listen("unix", mockSocketPath)
	if err != nil {
		t.Fatalf("[MockHerdr Fixture] listen mock herdr socket at %s failed: %v", mockSocketPath, err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 256)
				n, err := conn.Read(buf)
				if err != nil && err != io.EOF {
					return
				}
				inStr := string(buf[:n])
				if strings.Contains(inStr, `"method":"ping"`) || strings.Contains(inStr, "ping") {
					_, _ = conn.Write([]byte(`{"id":"1","result":{"status":"pong"}}` + "\n"))
				} else {
					_, _ = conn.Write([]byte(`{"id":"1","result":{"status":"ok"}}` + "\n"))
				}
			}(c)
		}
	}()

	fakeHerdrPath := filepath.Join(baseDir, "fake_herdr.sh")
	fakeScript := fmt.Sprintf(`#!/bin/sh
# [MockHerdr Fixture] 模拟 Herdr CLI 预检与终端会话
if [ "$1" = "--version" ]; then
    echo "herdr 0.8.2"
    exit 0
fi
if [ "$1" = "api" ] && [ "$2" = "schema" ]; then
    echo '{"schemas":{"request":{"oneOf":[{"properties":{"method":{"const":"session.snapshot"}}},{"properties":{"method":{"const":"workspace.list"}}}]}}}'
    exit 0
fi
if [ "$1" = "status" ] && [ "$2" = "server" ]; then
    echo "status: running"
    echo "version: 0.8.2"
    echo "socket: %s"
    exit 0
fi
if [ "$1" = "terminal" ] && [ "$2" = "session" ]; then
    echo "herdr-real-terminal-launched: $@"
    # 支持真实 stdin 到 stdout 交互流动
    if [ ! -t 0 ]; then
        read line
        echo "terminal-input-received: $line"
    fi
    exit 0
fi
exit 0
`, mockSocketPath)

	if err := os.WriteFile(fakeHerdrPath, []byte(fakeScript), 0o755); err != nil {
		t.Fatalf("[MockHerdr Fixture] write fake herdr script failed: %v", err)
	}

	return fakeHerdrPath, mockSocketPath
}

// waitDaemonReady 轮询等待 daemon 的 control.sock 就绪且 PID 与 ConfigPath 严格与新进程匹配
func waitDaemonReady(t *testing.T, runtimeDir, configPath string, expectedPID int, timeout time.Duration) {
	t.Helper()
	if timeout < 20*time.Second {
		timeout = 20 * time.Second
	}
	sockPath := filepath.Join(runtimeDir, "control.sock")
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if expectedPID > 0 {
			if err := syscall.Kill(expectedPID, 0); err != nil {
				t.Fatalf("daemon PID %d exited before becoming ready (config: %s): %v", expectedPID, configPath, err)
			}
		}
		var stat DaemonStatus
		err := ClientCallIPCWithConfig(sockPath, "GET", "/status", configPath, nil, &stat)
		if err == nil && stat.PID == expectedPID && stat.ConfigPath == configPath {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("daemon PID %d (config: %s) failed to become ready on %s within %v", expectedPID, configPath, sockPath, timeout)
}

// TestSetup_AgentIDUniquenessAcrossInstances 真实 CLI 子进程身份唯一性红例：
// 对 3 个独立配置路径执行真实的 CLI 子进程 setup 命令，严格断言生成的 SetupID 是否存在两两碰撞。
// 当前生产实现截取 node.Public.Addr()[:8]（由于 Tailcat v0.6 紧凑地址前缀为固定 CBOR 头 "tcpGFwWC"），
// 必定生成完全相同的 SetupID 导致碰撞失败 (exit code 1)！
func TestSetup_AgentIDUniquenessAcrossInstances(t *testing.T) {
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}
	binPath := filepath.Join(binDir, "herdrx")
	buildCLI(t, binPath)

	configDir := filepath.Join(tempDir, "c")
	runtimeDir := filepath.Join(tempDir, "r")
	stateDir := filepath.Join(tempDir, "s")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	fakeHerdrPath, _ := createMockHerdrFixture(t, tempDir, configDir)
	baseEnv := isolatedEnv(configDir, runtimeDir, stateDir)

	cfgPath1 := filepath.Join(configDir, "cfg1.json")
	cfgPath2 := filepath.Join(configDir, "cfg2.json")
	cfgPath3 := filepath.Join(configDir, "cfg3.json")

	// 真实执行 CLI 子进程 setup
	runSetupCmd := func(cfg string) {
		cmd := exec.Command(binPath, "setup",
			"--herdr-bin", fakeHerdrPath,
			"--config", cfg,
			"--runtime-dir", runtimeDir,
			"--skip-service")
		cmd.Env = baseEnv
		var errOut bytes.Buffer
		cmd.Stderr = &errOut
		if err := cmd.Run(); err != nil {
			t.Fatalf("setup %s failed: %v (stderr: %s)", cfg, err, errOut.String())
		}
	}

	runSetupCmd(cfgPath1)
	runSetupCmd(cfgPath2)
	runSetupCmd(cfgPath3)

	cfg1, err := SafeLoadConfig(cfgPath1)
	if err != nil {
		t.Fatalf("load cfg1: %v", err)
	}
	cfg2, err := SafeLoadConfig(cfgPath2)
	if err != nil {
		t.Fatalf("load cfg2: %v", err)
	}
	cfg3, err := SafeLoadConfig(cfgPath3)
	if err != nil {
		t.Fatalf("load cfg3: %v", err)
	}

	t.Logf("Real CLI Generated SetupIDs: cfg1=%s, cfg2=%s, cfg3=%s", cfg1.SetupID, cfg2.SetupID, cfg3.SetupID)

	// 幂等性复测：同一配置再次执行 setup，身份绝不漂移
	runSetupCmd(cfgPath1)
	cfg1Repeat, err := SafeLoadConfig(cfgPath1)
	if err != nil {
		t.Fatalf("load cfg1 repeat: %v", err)
	}
	if cfg1Repeat.SetupID != cfg1.SetupID {
		t.Fatalf("setup must be idempotent on same config, got %s != %s", cfg1Repeat.SetupID, cfg1.SetupID)
	}

	// 核心断言：3 个独立受控端实例的 SetupID 必须两两不同，绝不能发生碰撞冲突！
	if cfg1.SetupID == cfg2.SetupID || cfg2.SetupID == cfg3.SetupID || cfg1.SetupID == cfg3.SetupID {
		t.Fatalf("CRITICAL SECURITY FLAW: SetupID collision detected across instances! (cfg1=%s, cfg2=%s, cfg3=%s). Current implementation takes fixed CBOR prefix 'tcpGFwWC'!",
			cfg1.SetupID, cfg2.SetupID, cfg3.SetupID)
	}
}

// TestCLI_ConnectPrepareCommitAndHerdrIO_FullPipeline 真实 CLI 纵向全链路闭环实测：
// setup -> serve 真实子进程 -> connect 真实 CLI 获得连接串 ->
// 外部 Controller 经由真实临时端点 prepare -> 生产代码自动拉起正式端点 ->
// 真实 Ed25519 签名 commit -> 正式通道执行 Herdr 命令 (stdin->stdout) 与真实 streamlocal 双向数据交互。
func TestCLI_ConnectPrepareCommitAndHerdrIO_FullPipeline(t *testing.T) {
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	binPath := filepath.Join(binDir, "herdrx")
	buildCLI(t, binPath)

	dm := tunnel.RunTestDERPAndSTUN(t, func(string, ...any) {}, "127.0.0.1")
	reg := dm.Regions[1]
	derpPort := reg.Nodes[0].DERPPort
	ap := netip.MustParseAddrPort(fmt.Sprintf("127.0.0.1:%d", derpPort))
	tunnel.DefaultSSRFValidator.AllowPrivateEndpoint(ap)
	defer tunnel.DefaultSSRFValidator.ClearAllowed()

	configDir := filepath.Join(tempDir, "c")
	runtimeDir := filepath.Join(tempDir, "r")
	stateDir := filepath.Join(tempDir, "s")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	fakeHerdrPath, mockHerdrSockPath := createMockHerdrFixture(t, tempDir, configDir)
	subEnv := isolatedEnv(configDir, runtimeDir, stateDir)

	configPath := filepath.Join(configDir, "cfg.json")

	// 步骤 1: 真实执行 herdrx setup
	{
		setupCmd := exec.Command(binPath, "setup",
			"--herdr-bin", fakeHerdrPath,
			"--config", configPath,
			"--runtime-dir", runtimeDir,
			"--skip-service")
		setupCmd.Env = subEnv
		var errOut bytes.Buffer
		setupCmd.Stderr = &errOut
		if err := setupCmd.Run(); err != nil {
			t.Fatalf("setup failed: %v (stderr: %s)", err, errOut.String())
		}
	}

	// 步骤 2: 注入本地自建测试 DERP Region（仅配置测试网络，绝不预填 binding 或公钥）
	store, err := NewStateStore(configPath)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	_, err = store.Update(func(c *Config) error {
		c.Node.Public.Region = []*tailcfg.DERPRegion{reg}
		return nil
	})
	if err != nil {
		t.Fatalf("update region fixture failed: %v", err)
	}

	// 步骤 3: 真实启动后台子进程 herdrx serve
	serveCmd := exec.Command(binPath, "serve", "--config", configPath, "--runtime-dir", runtimeDir)
	serveCmd.Env = subEnv
	var serveErr bytes.Buffer
	serveCmd.Stderr = &serveErr
	if err := serveCmd.Start(); err != nil {
		t.Fatalf("start serve failed: %v", err)
	}
	pid1 := serveCmd.Process.Pid
	t.Logf("Started herdrx serve instance (PID: %d)", pid1)
	defer func() {
		if serveCmd.Process != nil {
			_ = serveCmd.Process.Kill()
			_ = serveCmd.Wait()
		}
	}()

	waitDaemonReady(t, runtimeDir, configPath, pid1, 3*time.Second)

	// 步骤 4: 真实执行外部 CLI herdrx connect --plain 获取连接串
	var connStr string
	{
		connectCmd := exec.Command(binPath, "connect", "--plain", "--config", configPath, "--runtime-dir", runtimeDir)
		connectCmd.Env = subEnv
		var cOut, cErr bytes.Buffer
		connectCmd.Stdout = &cOut
		connectCmd.Stderr = &cErr
		if err := connectCmd.Run(); err != nil {
			t.Fatalf("connect command failed: %v, stderr: %s", err, cErr.String())
		}
		connStr = strings.TrimSpace(cOut.String())
		if !strings.HasPrefix(connStr, "herdrx://v1/") {
			t.Fatalf("invalid connection string prefix from CLI connect")
		}
		t.Logf("Acquired valid connection string from CLI connect (prefix: herdrx://v1/, len: %d)", len(connStr))
	}

	// 步骤 5: 外部 Controller 解析连接串并通过真实临时端点发送 pairing-prepare
	parsed, err := tunnel.ParseConnectionString(connStr)
	if err != nil {
		t.Fatalf("parse connection string: %v", err)
	}

	ctrlClientNode := key.NewNode()
	ctrlPrivPEM, ctrlAuthSSH, err := secure.GenerateSSHKey("ctrl-e2e")
	if err != nil {
		t.Fatalf("gen ctrl ssh key: %v", err)
	}
	ctrlSigner, err := ssh.ParsePrivateKey(ctrlPrivPEM)
	if err != nil {
		t.Fatalf("parse ctrl private key: %v", err)
	}
	ctrlSSHPub, _, _, _, err := ssh.ParseAuthorizedKey(ctrlAuthSSH)
	if err != nil {
		t.Fatalf("parse ctrl auth ssh: %v", err)
	}

	tempClient := tailcat.NewClient(tailcat.Addr(parsed.Payload.TailcatAddr))
	tempClient.Key = parsed.ClientNode
	tempClient.Logf = func(string, ...any) {} // 显式静默
	defer tempClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	netConn, err := tempClient.DialTCPPort(ctx, 22)
	if err != nil {
		t.Fatalf("dial temporary endpoint failed: %v", err)
	}
	defer netConn.Close()

	tempSSHCfg := &ssh.ClientConfig{
		User:            "pairing",
		Auth:            []ssh.AuthMethod{ssh.Password(parsed.Payload.PairSecret)},
		HostKeyCallback: ssh.FixedHostKey(parsed.SSHHostKey),
		Timeout:         3 * time.Second,
	}
	tempConn, chans, reqs, err := ssh.NewClientConn(netConn, "", tempSSHCfg)
	if err != nil {
		t.Fatalf("ssh handshake on temp endpoint: %v", err)
	}
	tempSSHClient := ssh.NewClient(tempConn, chans, reqs)
	defer tempSSHClient.Close()

	sess, err := tempSSHClient.NewSession()
	if err != nil {
		t.Fatalf("new session on temp: %v", err)
	}

	hexSSHPub := hex.EncodeToString(ctrlAuthSSH)
	prepareCmd := fmt.Sprintf("pairing-prepare req-pipeline-01 ctrl-inst-01 %s %s", ctrlClientNode.Public().String(), hexSSHPub)
	var prepOut, prepErr bytes.Buffer
	sess.Stdout = &prepOut
	sess.Stderr = &prepErr
	if err := sess.Run(prepareCmd); err != nil {
		t.Fatalf("pairing-prepare failed: %v (stderr: %s)", err, prepErr.String())
	}
	sess.Close()

	var bindingID, challenge, formalAddr string
	for _, f := range strings.Fields(prepOut.String()) {
		if strings.HasPrefix(f, "binding_id=") {
			bindingID = strings.TrimPrefix(f, "binding_id=")
		} else if strings.HasPrefix(f, "challenge=") {
			challenge = strings.TrimPrefix(f, "challenge=")
		} else if strings.HasPrefix(f, "formal_addr=") {
			formalAddr = strings.TrimPrefix(f, "formal_addr=")
		}
	}
	if bindingID == "" || challenge == "" || formalAddr == "" {
		t.Fatalf("prepare response missing binding_id, challenge or formal_addr")
	}
	t.Logf("Production daemon auto-launched formal endpoint (addr masked: %s)", MaskAddress(formalAddr))

	// 步骤 6: 外部 Controller 连接正式端点并执行 commit
	formalClient := tailcat.NewClient(tailcat.Addr(formalAddr))
	formalClient.Key = ctrlClientNode
	formalClient.Logf = func(string, ...any) {} // 显式静默
	defer formalClient.Close()

	ctxFormal, cancelFormal := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFormal()
	fConn, err := formalClient.DialTCPPort(ctxFormal, 22)
	if err != nil {
		t.Fatalf("dial formal endpoint failed: %v", err)
	}
	defer fConn.Close()

	formalSSHCfg := &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(ctrlSigner)},
		HostKeyCallback: ssh.FixedHostKey(parsed.SSHHostKey),
		Timeout:         3 * time.Second,
	}
	fSSHConn, fChans, fReqs, err := ssh.NewClientConn(fConn, "", formalSSHCfg)
	if err != nil {
		t.Fatalf("ssh handshake on formal endpoint: %v", err)
	}
	formalSSHClient := ssh.NewClient(fSSHConn, fChans, fReqs)
	defer formalSSHClient.Close()

	// prepared 状态下终端命令严格被拒 (exit 126)
	{
		prepDenySess, err := formalSSHClient.NewSession()
		if err != nil {
			t.Fatalf("new session: %v", err)
		}
		if err := prepDenySess.Run("uname -sm"); err == nil {
			prepDenySess.Close()
			t.Fatalf("expected terminal command to be denied in prepared state on formal endpoint")
		}
		prepDenySess.Close()
	}

	// 发送真实 Ed25519 签名 commit
	sshFP := ssh.FingerprintSHA256(ctrlSSHPub)
	msg := tunnel.CommitContextMessage(challenge, bindingID, "ctrl-inst-01", parsed.Payload.AgentID, parsed.Payload.EnrollmentID, ctrlClientNode.Public().String(), sshFP, formalAddr)
	digest := sha256.Sum256(msg)

	block, _ := pem.Decode(ctrlPrivPEM)
	if block == nil {
		t.Fatalf("pem decode ctrl priv key failed")
	}
	rawPriv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse pkcs8 ctrl priv key: %v", err)
	}
	edPriv, ok := rawPriv.(ed25519.PrivateKey)
	if !ok {
		t.Fatalf("ctrl priv key is not ed25519")
	}
	sigProof := ed25519.Sign(edPriv, digest[:])

	commitCmd := fmt.Sprintf("pairing-commit %s %s", bindingID, hex.EncodeToString(sigProof))
	commitSess, err := formalSSHClient.NewSession()
	if err != nil {
		t.Fatalf("new session for commit: %v", err)
	}
	var cOut, cErr bytes.Buffer
	commitSess.Stdout = &cOut
	commitSess.Stderr = &cErr
	if err := commitSess.Run(commitCmd); err != nil {
		t.Fatalf("pairing-commit failed: %v, stderr: %s", err, cErr.String())
	}
	commitSess.Close()

	if !strings.Contains(cOut.String(), "status=active") {
		t.Fatalf("expected commit to return status=active")
	}
	t.Logf("Commit verified and active status achieved on production daemon!")

	// 步骤 7: 新建正式 SSH 连接，验证真实 Herdr 终端 stdin/stdout 交互与合法 direct-streamlocal
	fConn2, err := formalClient.DialTCPPort(ctxFormal, 22)
	if err != nil {
		t.Fatalf("dial formal endpoint for active session: %v", err)
	}
	defer fConn2.Close()

	fSSHConn2, fChans2, fReqs2, err := ssh.NewClientConn(fConn2, "", formalSSHCfg)
	if err != nil {
		t.Fatalf("ssh handshake after commit: %v", err)
	}
	formalSSHClientActive := ssh.NewClient(fSSHConn2, fChans2, fReqs2)
	defer formalSSHClientActive.Close()

	// 7.1 验证真实 HerdrBin 执行与 stdin/stdout 交互
	{
		actSess, err := formalSSHClientActive.NewSession()
		if err != nil {
			t.Fatalf("new active session: %v", err)
		}
		stdinPipe, err := actSess.StdinPipe()
		if err != nil {
			t.Fatalf("stdin pipe: %v", err)
		}
		var hOut bytes.Buffer
		actSess.Stdout = &hOut
		cmd := "herdr terminal session observe s1 --cols 80 --rows 24"
		if err := actSess.Start(cmd); err != nil {
			t.Fatalf("start herdr terminal failed: %v", err)
		}
		if _, err := io.WriteString(stdinPipe, "client-data-line\n"); err != nil {
			t.Fatalf("write stdin to terminal: %v", err)
		}
		if err := stdinPipe.Close(); err != nil {
			t.Fatalf("close stdin pipe: %v", err)
		}
		if err := actSess.Wait(); err != nil {
			t.Fatalf("wait active session: %v", err)
		}

		outStr := hOut.String()
		if !strings.Contains(outStr, "herdr-real-terminal-launched") || !strings.Contains(outStr, "terminal-input-received: client-data-line") {
			t.Fatalf("expected real HerdrBin execution with stdin/stdout flow, got: %s", outStr)
		}
		t.Logf("Real HerdrBin terminal execution with stdin/stdout verified on active formal channel!")
	}

	// 7.2 验证通过 ${XDG_CONFIG_HOME}/herdr/herdr.sock 的真实 direct-streamlocal 协议转发
	{
		type streamLocalPayload struct {
			SocketPath string
			Reserved0  string
			Reserved1  uint32
		}
		streamPayload := ssh.Marshal(streamLocalPayload{SocketPath: mockHerdrSockPath})
		streamCh, streamReqs, err := formalSSHClientActive.OpenChannel("direct-streamlocal@openssh.com", streamPayload)
		if err != nil {
			t.Fatalf("open direct-streamlocal channel to %s failed: %v", mockHerdrSockPath, err)
		}
		defer streamCh.Close()
		go ssh.DiscardRequests(streamReqs)

		// 发送真实结构化 JSON 协议请求
		reqJSON := []byte(`{"id":"101","method":"ping"}` + "\n")
		if _, err := streamCh.Write(reqJSON); err != nil {
			t.Fatalf("write to streamlocal channel: %v", err)
		}

		replyBuf := make([]byte, 128)
		n, err := streamCh.Read(replyBuf)
		if err != nil {
			t.Fatalf("read from streamlocal channel: %v", err)
		}
		replyStr := string(replyBuf[:n])
		if !strings.Contains(replyStr, `"status":"pong"`) {
			t.Fatalf("expected structured pong response from mock herdr socket, got: %s", replyStr)
		}
		t.Logf("Real streamlocal forwarding to Herdr socket verified with structured response: %s", strings.TrimSpace(replyStr))
	}
}

// TestCLI_CrashRecovery_PreparedRestart prepared 崩溃恢复红例：
// 在真实 CLI 链推进至 prepared 状态时杀死守护进程 serve1，并启动全新 PID 的 serve2，
// 严格检验守护进程能否从持久化材料恢复正式端点与客户端通信。
// 在当前生产实现下，runServe 重启时未恢复两阶段 formal Server，此测试将必然在重启拨号时触发超时失败，直接揭穿缺陷！
func TestCLI_CrashRecovery_PreparedRestart(t *testing.T) {
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	binPath := filepath.Join(binDir, "herdrx")
	buildCLI(t, binPath)

	dm := tunnel.RunTestDERPAndSTUN(t, func(string, ...any) {}, "127.0.0.1")
	reg := dm.Regions[1]
	derpPort := reg.Nodes[0].DERPPort
	ap := netip.MustParseAddrPort(fmt.Sprintf("127.0.0.1:%d", derpPort))
	tunnel.DefaultSSRFValidator.AllowPrivateEndpoint(ap)
	defer tunnel.DefaultSSRFValidator.ClearAllowed()

	configDir := filepath.Join(tempDir, "c")
	runtimeDir := filepath.Join(tempDir, "r")
	stateDir := filepath.Join(tempDir, "s")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatalf("mkdir runtime: %v", err)
	}

	fakeHerdrPath, _ := createMockHerdrFixture(t, tempDir, configDir)
	subEnv := isolatedEnv(configDir, runtimeDir, stateDir)

	configPath := filepath.Join(configDir, "cfg.json")

	// 步骤 1: 真实执行 setup
	{
		cmd := exec.Command(binPath, "setup",
			"--herdr-bin", fakeHerdrPath,
			"--config", configPath,
			"--runtime-dir", runtimeDir,
			"--skip-service")
		cmd.Env = subEnv
		var errOut bytes.Buffer
		cmd.Stderr = &errOut
		if err := cmd.Run(); err != nil {
			t.Fatalf("setup failed: %v (stderr: %s)", err, errOut.String())
		}
	}

	store, err := NewStateStore(configPath)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	_, err = store.Update(func(c *Config) error {
		c.Node.Public.Region = []*tailcfg.DERPRegion{reg}
		return nil
	})
	if err != nil {
		t.Fatalf("update region fixture failed: %v", err)
	}

	// 步骤 2: 启动 serve 实例 1
	serve1 := exec.Command(binPath, "serve", "--config", configPath, "--runtime-dir", runtimeDir)
	serve1.Env = subEnv
	var sErr1 bytes.Buffer
	serve1.Stderr = &sErr1
	if err := serve1.Start(); err != nil {
		t.Fatalf("start serve instance 1 failed: %v (stderr: %s)", err, sErr1.String())
	}
	pid1 := serve1.Process.Pid
	t.Logf("Started serve instance 1 (PID: %d)", pid1)
	defer func() {
		if serve1.Process != nil {
			_ = serve1.Process.Kill()
			_ = serve1.Wait()
		}
	}()

	waitDaemonReady(t, runtimeDir, configPath, pid1, 3*time.Second)

	// 步骤 3: 外部 connect 拿串
	cCmd := exec.Command(binPath, "connect", "--plain", "--config", configPath, "--runtime-dir", runtimeDir)
	cCmd.Env = subEnv
	var cOut, cErr bytes.Buffer
	cCmd.Stdout = &cOut
	cCmd.Stderr = &cErr
	if err := cCmd.Run(); err != nil {
		t.Fatalf("connect failed: %v (stderr: %s)", err, cErr.String())
	}
	connStr := strings.TrimSpace(cOut.String())
	parsed, err := tunnel.ParseConnectionString(connStr)
	if err != nil {
		t.Fatalf("parse connection string: %v", err)
	}

	ctrlClientNode := key.NewNode()
	ctrlPrivPEM, ctrlAuthSSH, err := secure.GenerateSSHKey("ctrl-recovery-prep")
	if err != nil {
		t.Fatalf("gen ctrl ssh: %v", err)
	}
	ctrlSigner, err := ssh.ParsePrivateKey(ctrlPrivPEM)
	if err != nil {
		t.Fatalf("parse ctrl priv: %v", err)
	}
	ctrlSSHPub, _, _, _, err := ssh.ParseAuthorizedKey(ctrlAuthSSH)
	if err != nil {
		t.Fatalf("parse ctrl pub: %v", err)
	}

	// 步骤 4: 发送 prepare 达成 prepared 状态
	tempClient := tailcat.NewClient(tailcat.Addr(parsed.Payload.TailcatAddr))
	tempClient.Key = parsed.ClientNode
	tempClient.Logf = func(string, ...any) {}
	defer tempClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	netConn, err := tempClient.DialTCPPort(ctx, 22)
	if err != nil {
		t.Fatalf("dial temp endpoint: %v", err)
	}

	tempConn, chans, reqs, err := ssh.NewClientConn(netConn, "", &ssh.ClientConfig{
		User:            "pairing",
		Auth:            []ssh.AuthMethod{ssh.Password(parsed.Payload.PairSecret)},
		HostKeyCallback: ssh.FixedHostKey(parsed.SSHHostKey),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatalf("temp ssh handshake: %v", err)
	}
	tempSSHClient := ssh.NewClient(tempConn, chans, reqs)
	sess, err := tempSSHClient.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	hexSSHPub := hex.EncodeToString(ctrlAuthSSH)
	var prepOut, prepErr bytes.Buffer
	sess.Stdout = &prepOut
	sess.Stderr = &prepErr
	if err := sess.Run(fmt.Sprintf("pairing-prepare req-rec-01 ctrl-rec-01 %s %s", ctrlClientNode.Public().String(), hexSSHPub)); err != nil {
		t.Fatalf("prepare failed: %v (stderr: %s)", err, prepErr.String())
	}
	sess.Close()
	tempSSHClient.Close()
	netConn.Close()
	tempClient.Close()

	var bindingID, challenge, formalAddr string
	for _, f := range strings.Fields(prepOut.String()) {
		if strings.HasPrefix(f, "binding_id=") {
			bindingID = strings.TrimPrefix(f, "binding_id=")
		} else if strings.HasPrefix(f, "challenge=") {
			challenge = strings.TrimPrefix(f, "challenge=")
		} else if strings.HasPrefix(f, "formal_addr=") {
			formalAddr = strings.TrimPrefix(f, "formal_addr=")
		}
	}
	if bindingID == "" || challenge == "" || formalAddr == "" {
		t.Fatalf("failed to reach prepared state: binding_id/challenge/formal_addr empty")
	}

	// 核心断点: 在 prepared 状态下杀死 serve1 子进程！
	if err := serve1.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill serve1: %v", err)
	}
	_ = serve1.Wait()
	t.Logf("Killed serve instance 1 (PID: %d) at prepared breakpoint", pid1)

	// 启动全新 PID 的 serve 实例 2
	serve2 := exec.Command(binPath, "serve", "--config", configPath, "--runtime-dir", runtimeDir)
	serve2.Env = subEnv
	var sErr2 bytes.Buffer
	serve2.Stderr = &sErr2
	if err := serve2.Start(); err != nil {
		t.Fatalf("start serve instance 2 failed: %v (stderr: %s)", err, sErr2.String())
	}
	pid2 := serve2.Process.Pid
	t.Logf("Started serve instance 2 (PID: %d)", pid2)
	defer func() {
		if serve2.Process != nil {
			_ = serve2.Process.Kill()
			_ = serve2.Wait()
		}
	}()

	waitDaemonReady(t, runtimeDir, configPath, pid2, 3*time.Second)

	// 核心验证：serve2 重启成功恢复 prepared 状态下的 formal Server！
	formalClient := tailcat.NewClient(tailcat.Addr(formalAddr))
	formalClient.Key = ctrlClientNode
	formalClient.Logf = func(string, ...any) {}
	defer formalClient.Close()

	ctxFormal, cancelFormal := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelFormal()
	fConn, dialFormalErr := formalClient.DialTCPPort(ctxFormal, 22)
	if dialFormalErr != nil {
		t.Fatalf("RECOVERY FAILED: formal server was NOT restored across daemon restart in prepared state: %v", dialFormalErr)
	}
	defer fConn.Close()

	// 验证恢复后的 formal Server：
	// 5.1 进行 SSH 握手并验证处于 prepared 状态
	formalSSHCfg := &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(ctrlSigner)},
		HostKeyCallback: ssh.FixedHostKey(parsed.SSHHostKey),
		Timeout:         3 * time.Second,
	}
	fSSHConn, fChans, fReqs, err := ssh.NewClientConn(fConn, "", formalSSHCfg)
	if err != nil {
		t.Fatalf("ssh handshake on restored prepared formal server failed: %v", err)
	}
	formalSSHClient := ssh.NewClient(fSSHConn, fChans, fReqs)
	defer formalSSHClient.Close()

	// 5.2 prepared 状态下验证 pairing-status 与终端命令严格被拒 (exit 126)
	{
		statusSess, err := formalSSHClient.NewSession()
		if err != nil {
			t.Fatalf("new session for status: %v", err)
		}
		var sOut bytes.Buffer
		statusSess.Stdout = &sOut
		if err := statusSess.Run("pairing-status"); err != nil {
			t.Fatalf("run pairing-status on restored prepared server failed: %v", err)
		}
		statusSess.Close()
		if !strings.Contains(sOut.String(), "status=prepared") {
			t.Fatalf("expected status=prepared on restored server, got: %s", sOut.String())
		}

		denySess, err := formalSSHClient.NewSession()
		if err != nil {
			t.Fatalf("new session for deny test: %v", err)
		}
		if err := denySess.Run("uname -sm"); err == nil {
			denySess.Close()
			t.Fatalf("expected terminal command to be denied in prepared state on restored server")
		}
		denySess.Close()
	}

	// 5.3 在恢复的 formal server 上提交 commit
	sshFP := ssh.FingerprintSHA256(ctrlSSHPub)
	msg := tunnel.CommitContextMessage(challenge, bindingID, "ctrl-rec-01", parsed.Payload.AgentID, parsed.Payload.EnrollmentID, ctrlClientNode.Public().String(), sshFP, formalAddr)
	digest := sha256.Sum256(msg)

	block, _ := pem.Decode(ctrlPrivPEM)
	if block == nil {
		t.Fatal("generated controller key is not PEM")
	}
	rawPriv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse pkcs8: %v", err)
	}
	edPriv := rawPriv.(ed25519.PrivateKey)
	sigProof := ed25519.Sign(edPriv, digest[:])

	commitSess, err := formalSSHClient.NewSession()
	if err != nil {
		t.Fatalf("new session for commit: %v", err)
	}
	var commitOut, commitErr bytes.Buffer
	commitSess.Stdout = &commitOut
	commitSess.Stderr = &commitErr
	if err := commitSess.Run(fmt.Sprintf("pairing-commit %s %s", bindingID, hex.EncodeToString(sigProof))); err != nil {
		t.Fatalf("commit on restored server failed: %v, stderr: %s", err, commitErr.String())
	}
	commitSess.Close()

	if !strings.Contains(commitOut.String(), "status=active") {
		t.Fatalf("expected status=active from commit on restored server, got: %s", commitOut.String())
	}
	t.Logf("Commit verified on restored server instance 2 (PID: %d)", pid2)

	// 5.4 关键安全断言：同连接绝不原地升权！
	// 此前用于 commit 的 SSH 连接在认证时刻 role 被固定为 prepared，
	// 即使 commit 成功，该同一连接依然严禁执行终端命令！
	{
		elevationSess, err := formalSSHClient.NewSession()
		if err != nil {
			t.Fatalf("new session for non-elevation test: %v", err)
		}
		if err := elevationSess.Run("uname -sm"); err == nil {
			elevationSess.Close()
			t.Fatalf("CRITICAL SECURITY FLAW: existing connection was elevated in-place after commit!")
		}
		elevationSess.Close()
		t.Logf("Non-elevation on existing connection verified: command denied as required")
	}

	// 5.5 新建正式 SSH 连接，此时认证角色为 active，放行 Herdr 终端
	ctxActive, cancelActive := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelActive()
	fConnAct, err := formalClient.DialTCPPort(ctxActive, 22)
	if err != nil {
		t.Fatalf("dial formal server after commit: %v", err)
	}
	defer fConnAct.Close()

	fSSHConnAct, fChansAct, fReqsAct, err := ssh.NewClientConn(fConnAct, "", formalSSHCfg)
	if err != nil {
		t.Fatalf("ssh handshake for new active session: %v", err)
	}
	formalSSHClientAct := ssh.NewClient(fSSHConnAct, fChansAct, fReqsAct)
	defer formalSSHClientAct.Close()

	// 验证终端 stdin/stdout 交互
	{
		actSess, err := formalSSHClientAct.NewSession()
		if err != nil {
			t.Fatalf("new session on active client: %v", err)
		}
		stdinPipe, err := actSess.StdinPipe()
		if err != nil {
			t.Fatalf("stdin pipe: %v", err)
		}
		var hOut bytes.Buffer
		actSess.Stdout = &hOut
		if err := actSess.Start("herdr terminal session observe s1 --cols 80 --rows 24"); err != nil {
			t.Fatalf("start herdr terminal on restored active server: %v", err)
		}
		if _, err := io.WriteString(stdinPipe, "prepared-restored-data\n"); err != nil {
			t.Fatalf("write stdin: %v", err)
		}
		_ = stdinPipe.Close()
		if err := actSess.Wait(); err != nil {
			t.Fatalf("wait session: %v", err)
		}
		if !strings.Contains(hOut.String(), "terminal-input-received: prepared-restored-data") {
			t.Fatalf("expected real HerdrBin execution on restored server, got: %s", hOut.String())
		}
		t.Logf("Real Herdr terminal execution verified on restored server after commit!")
	}
}

// TestCLI_CrashRecovery_ActiveRestart active 崩溃恢复红例：
// 在全链路达成 active 状态后杀死守护进程 serve1，并启动全新 PID 的 serve2，
// 严格检验守护进程能否从磁盘恢复正式端点与有效 SSH 凭据，并执行真实 Herdr IO。
// 在当前生产实现下，runServe 重启时未恢复两阶段 active formal Server，必然触发恢复失败！
func TestCLI_CrashRecovery_ActiveRestart(t *testing.T) {
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	binPath := filepath.Join(binDir, "herdrx")
	buildCLI(t, binPath)

	dm := tunnel.RunTestDERPAndSTUN(t, func(string, ...any) {}, "127.0.0.1")
	reg := dm.Regions[1]
	derpPort := reg.Nodes[0].DERPPort
	ap := netip.MustParseAddrPort(fmt.Sprintf("127.0.0.1:%d", derpPort))
	tunnel.DefaultSSRFValidator.AllowPrivateEndpoint(ap)
	defer tunnel.DefaultSSRFValidator.ClearAllowed()

	configDir := filepath.Join(tempDir, "c")
	runtimeDir := filepath.Join(tempDir, "r")
	stateDir := filepath.Join(tempDir, "s")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatalf("mkdir runtime: %v", err)
	}

	fakeHerdrPath, mockHerdrSockPath := createMockHerdrFixture(t, tempDir, configDir)
	subEnv := isolatedEnv(configDir, runtimeDir, stateDir)

	configPath := filepath.Join(configDir, "cfg.json")

	// 步骤 1: 真实执行 setup
	{
		cmd := exec.Command(binPath, "setup",
			"--herdr-bin", fakeHerdrPath,
			"--config", configPath,
			"--runtime-dir", runtimeDir,
			"--skip-service")
		cmd.Env = subEnv
		var errOut bytes.Buffer
		cmd.Stderr = &errOut
		if err := cmd.Run(); err != nil {
			t.Fatalf("setup failed: %v (stderr: %s)", err, errOut.String())
		}
	}

	store, err := NewStateStore(configPath)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	_, err = store.Update(func(c *Config) error {
		c.Node.Public.Region = []*tailcfg.DERPRegion{reg}
		return nil
	})
	if err != nil {
		t.Fatalf("update region fixture failed: %v", err)
	}

	// 步骤 2: 启动 serve 实例 1
	serve1 := exec.Command(binPath, "serve", "--config", configPath, "--runtime-dir", runtimeDir)
	serve1.Env = subEnv
	var sErr1 bytes.Buffer
	serve1.Stderr = &sErr1
	if err := serve1.Start(); err != nil {
		t.Fatalf("start serve instance 1 failed: %v (stderr: %s)", err, sErr1.String())
	}
	pid1 := serve1.Process.Pid
	t.Logf("Started serve instance 1 (PID: %d)", pid1)
	defer func() {
		if serve1.Process != nil {
			_ = serve1.Process.Kill()
			_ = serve1.Wait()
		}
	}()

	waitDaemonReady(t, runtimeDir, configPath, pid1, 3*time.Second)

	// 步骤 3: 外部 connect 拿串
	cCmd := exec.Command(binPath, "connect", "--plain", "--config", configPath, "--runtime-dir", runtimeDir)
	cCmd.Env = subEnv
	var cOut, cErr bytes.Buffer
	cCmd.Stdout = &cOut
	cCmd.Stderr = &cErr
	if err := cCmd.Run(); err != nil {
		t.Fatalf("connect failed: %v (stderr: %s)", err, cErr.String())
	}
	connStr := strings.TrimSpace(cOut.String())
	parsed, err := tunnel.ParseConnectionString(connStr)
	if err != nil {
		t.Fatalf("parse connection string: %v", err)
	}

	ctrlClientNode := key.NewNode()
	ctrlPrivPEM, ctrlAuthSSH, err := secure.GenerateSSHKey("ctrl-recovery-act")
	if err != nil {
		t.Fatalf("gen ctrl ssh: %v", err)
	}
	ctrlSigner, err := ssh.ParsePrivateKey(ctrlPrivPEM)
	if err != nil {
		t.Fatalf("parse ctrl priv: %v", err)
	}
	ctrlSSHPub, _, _, _, err := ssh.ParseAuthorizedKey(ctrlAuthSSH)
	if err != nil {
		t.Fatalf("parse ctrl pub: %v", err)
	}

	// 步骤 4: 发送 prepare 达成 prepared 状态
	tempClient := tailcat.NewClient(tailcat.Addr(parsed.Payload.TailcatAddr))
	tempClient.Key = parsed.ClientNode
	tempClient.Logf = func(string, ...any) {}
	defer tempClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	netConn, err := tempClient.DialTCPPort(ctx, 22)
	if err != nil {
		t.Fatalf("dial temp endpoint: %v", err)
	}

	tempConn, chans, reqs, err := ssh.NewClientConn(netConn, "", &ssh.ClientConfig{
		User:            "pairing",
		Auth:            []ssh.AuthMethod{ssh.Password(parsed.Payload.PairSecret)},
		HostKeyCallback: ssh.FixedHostKey(parsed.SSHHostKey),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatalf("temp ssh handshake: %v", err)
	}
	tempSSHClient := ssh.NewClient(tempConn, chans, reqs)
	sess, err := tempSSHClient.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	hexSSHPub := hex.EncodeToString(ctrlAuthSSH)
	var prepOut, prepErr bytes.Buffer
	sess.Stdout = &prepOut
	sess.Stderr = &prepErr
	if err := sess.Run(fmt.Sprintf("pairing-prepare req-rec-act ctrl-rec-act %s %s", ctrlClientNode.Public().String(), hexSSHPub)); err != nil {
		t.Fatalf("prepare failed: %v (stderr: %s)", err, prepErr.String())
	}
	sess.Close()
	tempSSHClient.Close()
	netConn.Close()
	tempClient.Close()

	var bindingID, challenge, formalAddr string
	for _, f := range strings.Fields(prepOut.String()) {
		if strings.HasPrefix(f, "binding_id=") {
			bindingID = strings.TrimPrefix(f, "binding_id=")
		} else if strings.HasPrefix(f, "challenge=") {
			challenge = strings.TrimPrefix(f, "challenge=")
		} else if strings.HasPrefix(f, "formal_addr=") {
			formalAddr = strings.TrimPrefix(f, "formal_addr=")
		}
	}
	if bindingID == "" || challenge == "" || formalAddr == "" {
		t.Fatalf("failed to reach prepared state: binding_id/challenge/formal_addr empty")
	}

	// 步骤 5: 提交 commit 达成 active 状态
	formalClient := tailcat.NewClient(tailcat.Addr(formalAddr))
	formalClient.Key = ctrlClientNode
	formalClient.Logf = func(string, ...any) {}
	defer formalClient.Close()

	ctxFormal, cancelFormal := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFormal()
	fConn, err := formalClient.DialTCPPort(ctxFormal, 22)
	if err != nil {
		t.Fatalf("dial formal endpoint failed: %v", err)
	}
	defer fConn.Close()

	formalSSHCfg := &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(ctrlSigner)},
		HostKeyCallback: ssh.FixedHostKey(parsed.SSHHostKey),
		Timeout:         3 * time.Second,
	}
	fSSHConn, fChans, fReqs, err := ssh.NewClientConn(fConn, "", formalSSHCfg)
	if err != nil {
		t.Fatalf("ssh handshake on formal endpoint: %v", err)
	}
	formalSSHClient := ssh.NewClient(fSSHConn, fChans, fReqs)

	sshFP := ssh.FingerprintSHA256(ctrlSSHPub)
	msg := tunnel.CommitContextMessage(challenge, bindingID, "ctrl-rec-act", parsed.Payload.AgentID, parsed.Payload.EnrollmentID, ctrlClientNode.Public().String(), sshFP, formalAddr)
	digest := sha256.Sum256(msg)

	block, _ := pem.Decode(ctrlPrivPEM)
	if block == nil {
		t.Fatalf("pem decode ctrl priv key failed")
	}
	rawPriv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse pkcs8 ctrl priv key: %v", err)
	}
	edPriv := rawPriv.(ed25519.PrivateKey)
	sigProof := ed25519.Sign(edPriv, digest[:])

	commitSess, err := formalSSHClient.NewSession()
	if err != nil {
		t.Fatalf("new session for commit: %v", err)
	}
	var commitOut, commitErr bytes.Buffer
	commitSess.Stdout = &commitOut
	commitSess.Stderr = &commitErr
	if err := commitSess.Run(fmt.Sprintf("pairing-commit %s %s", bindingID, hex.EncodeToString(sigProof))); err != nil {
		t.Fatalf("commit failed: %v (stderr: %s)", err, commitErr.String())
	}
	commitSess.Close()
	formalSSHClient.Close()
	fConn.Close()

	if !strings.Contains(commitOut.String(), "status=active") {
		t.Fatalf("expected commit to return status=active")
	}
	t.Logf("Active binding achieved on instance 1 (PID: %d)", pid1)

	// 核心断点: 在 active 状态下杀死 serve1 子进程！
	if err := serve1.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill serve1: %v", err)
	}
	_ = serve1.Wait()
	t.Logf("Killed serve instance 1 (PID: %d) at active breakpoint", pid1)

	// 启动全新 PID 的 serve 实例 2
	serve2 := exec.Command(binPath, "serve", "--config", configPath, "--runtime-dir", runtimeDir)
	serve2.Env = subEnv
	var sErr2 bytes.Buffer
	serve2.Stderr = &sErr2
	if err := serve2.Start(); err != nil {
		t.Fatalf("start serve instance 2 failed: %v (stderr: %s)", err, sErr2.String())
	}
	pid2 := serve2.Process.Pid
	t.Logf("Started serve instance 2 (PID: %d)", pid2)
	defer func() {
		if serve2.Process != nil {
			_ = serve2.Process.Kill()
			_ = serve2.Wait()
		}
	}()

	waitDaemonReady(t, runtimeDir, configPath, pid2, 3*time.Second)

	// 核心红例断言：在当前生产实现下，serve2 启动时未恢复 active formal Server！
	// 导致外部 Controller 拨号 formalAddr 失败 (直接揭穿当前缺陷！)
	formalClient2 := tailcat.NewClient(tailcat.Addr(formalAddr))
	formalClient2.Key = ctrlClientNode
	formalClient2.Logf = func(string, ...any) {}
	defer formalClient2.Close()

	ctxActive, cancelActive := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelActive()
	fConn2, dialActiveErr := formalClient2.DialTCPPort(ctxActive, 22)
	if dialActiveErr != nil {
		t.Fatalf("ACTIVE RECOVERY FAILED: formal server was NOT restored across daemon restart in active state: %v", dialActiveErr)
	}
	defer fConn2.Close()

	// 若连接成功，进一步验证恢复后的 SSH 认证与真实 Herdr 交互
	fSSHConn2, fChans2, fReqs2, err := ssh.NewClientConn(fConn2, "", formalSSHCfg)
	if err != nil {
		t.Fatalf("ACTIVE RECOVERY FAILED: ssh handshake on restored active formal server failed: %v", err)
	}
	formalSSHClient2 := ssh.NewClient(fSSHConn2, fChans2, fReqs2)
	defer formalSSHClient2.Close()

	// 验证终端交互
	{
		actSess, err := formalSSHClient2.NewSession()
		if err != nil {
			t.Fatalf("new session on restored active server: %v", err)
		}
		stdinPipe, err := actSess.StdinPipe()
		if err != nil {
			t.Fatalf("stdin pipe: %v", err)
		}
		var hOut bytes.Buffer
		actSess.Stdout = &hOut
		if err := actSess.Start("herdr terminal session observe s1 --cols 80 --rows 24"); err != nil {
			t.Fatalf("start herdr terminal on restored active server: %v", err)
		}
		if _, err := io.WriteString(stdinPipe, "active-restart-data\n"); err != nil {
			t.Fatalf("write stdin: %v", err)
		}
		if err := stdinPipe.Close(); err != nil {
			t.Fatalf("close stdin: %v", err)
		}
		if err := actSess.Wait(); err != nil {
			t.Fatalf("wait session: %v", err)
		}
		if !strings.Contains(hOut.String(), "terminal-input-received: active-restart-data") {
			t.Fatalf("expected real HerdrBin execution with stdin/stdout flow on restored active server, got: %s", hOut.String())
		}
	}

	// 验证 streamlocal
	{
		type streamLocalPayload struct {
			SocketPath string
			Reserved0  string
			Reserved1  uint32
		}
		streamPayload := ssh.Marshal(streamLocalPayload{SocketPath: mockHerdrSockPath})
		streamCh, streamReqs, err := formalSSHClient2.OpenChannel("direct-streamlocal@openssh.com", streamPayload)
		if err != nil {
			t.Fatalf("open streamlocal on restored active server: %v", err)
		}
		defer streamCh.Close()
		go ssh.DiscardRequests(streamReqs)

		reqJSON := []byte(`{"id":"202","method":"ping"}` + "\n")
		if _, err := streamCh.Write(reqJSON); err != nil {
			t.Fatalf("write to streamlocal: %v", err)
		}
		replyBuf := make([]byte, 128)
		n, err := streamCh.Read(replyBuf)
		if err != nil {
			t.Fatalf("read streamlocal: %v", err)
		}
		if !strings.Contains(string(replyBuf[:n]), `"status":"pong"`) {
			t.Fatalf("expected pong on restored active server streamlocal: %s", string(replyBuf[:n]))
		}
	}
	formalSSHClient2.Close()
	fConn2.Close()

	// 核心断点 3: 再次杀死 serve2 子进程，启动 serve3 验证二次重启连续恢复！
	if err := serve2.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill serve2: %v", err)
	}
	_ = serve2.Wait()
	t.Logf("Killed serve instance 2 (PID: %d) for double-restart verification", pid2)

	serve3 := exec.Command(binPath, "serve", "--config", configPath, "--runtime-dir", runtimeDir)
	serve3.Env = subEnv
	var sErr3 bytes.Buffer
	serve3.Stderr = &sErr3
	if err := serve3.Start(); err != nil {
		t.Fatalf("start serve instance 3 failed: %v (stderr: %s)", err, sErr3.String())
	}
	pid3 := serve3.Process.Pid
	t.Logf("Started serve instance 3 (PID: %d)", pid3)
	defer func() {
		if serve3.Process != nil {
			_ = serve3.Process.Kill()
			_ = serve3.Wait()
		}
	}()

	waitDaemonReady(t, runtimeDir, configPath, pid3, 3*time.Second)

	// 验证 serve3 再次恢复
	formalClient3 := tailcat.NewClient(tailcat.Addr(formalAddr))
	formalClient3.Key = ctrlClientNode
	formalClient3.Logf = func(string, ...any) {}
	defer formalClient3.Close()

	ctx3, cancel3 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel3()
	fConn3, err := formalClient3.DialTCPPort(ctx3, 22)
	if err != nil {
		t.Fatalf("dial formal server on instance 3 failed: %v", err)
	}
	defer fConn3.Close()

	fSSHConn3, fChans3, fReqs3, err := ssh.NewClientConn(fConn3, "", formalSSHCfg)
	if err != nil {
		t.Fatalf("ssh handshake on instance 3 failed: %v", err)
	}
	formalSSHClient3 := ssh.NewClient(fSSHConn3, fChans3, fReqs3)
	defer formalSSHClient3.Close()

	{
		actSess3, err := formalSSHClient3.NewSession()
		if err != nil {
			t.Fatalf("new session on instance 3: %v", err)
		}
		stdinPipe, err := actSess3.StdinPipe()
		if err != nil {
			t.Fatalf("stdin on restarted daemon: %v", err)
		}
		var hOut3 bytes.Buffer
		actSess3.Stdout = &hOut3
		if err := actSess3.Start("herdr terminal session observe s1 --cols 80 --rows 24"); err != nil {
			t.Fatalf("start herdr on instance 3: %v", err)
		}
		if _, err := io.WriteString(stdinPipe, "double-restart-data\n"); err != nil {
			t.Fatalf("write to restarted daemon: %v", err)
		}
		if err := stdinPipe.Close(); err != nil {
			t.Fatalf("close restarted daemon stdin: %v", err)
		}
		if err := actSess3.Wait(); err != nil {
			t.Fatalf("restarted daemon command: %v", err)
		}
		if !strings.Contains(hOut3.String(), "terminal-input-received: double-restart-data") {
			t.Fatalf("expected real Herdr execution on instance 3, got: %s", hOut3.String())
		}
		t.Logf("Double daemon restart recovery verified on instance 3 (PID: %d)!", pid3)
	}
}

// TestCLI_Security_IdempotencyAndConflictAndReject 测试准备幂等、冲突拒绝、未授权公钥拦截与已激活 commit 重试
func TestCLI_Security_IdempotencyAndConflictAndReject(t *testing.T) {
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("os.MkdirAll: %v", err)
	}
	binPath := filepath.Join(binDir, "herdrx")
	buildCLI(t, binPath)

	dm := tunnel.RunTestDERPAndSTUN(t, func(string, ...any) {}, "127.0.0.1")
	reg := dm.Regions[1]
	derpPort := reg.Nodes[0].DERPPort
	ap := netip.MustParseAddrPort(fmt.Sprintf("127.0.0.1:%d", derpPort))
	tunnel.DefaultSSRFValidator.AllowPrivateEndpoint(ap)
	defer tunnel.DefaultSSRFValidator.ClearAllowed()

	configDir := filepath.Join(tempDir, "c")
	runtimeDir := filepath.Join(tempDir, "r")
	stateDir := filepath.Join(tempDir, "s")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("os.MkdirAll: %v", err)
	}
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatalf("os.MkdirAll: %v", err)
	}

	fakeHerdrPath, _ := createMockHerdrFixture(t, tempDir, configDir)
	subEnv := isolatedEnv(configDir, runtimeDir, stateDir)

	configPath := filepath.Join(configDir, "cfg.json")

	// 1. setup
	setupCmd := exec.Command(binPath, "setup", "--herdr-bin", fakeHerdrPath, "--config", configPath, "--runtime-dir", runtimeDir, "--skip-service")
	setupCmd.Env = subEnv
	if err := setupCmd.Run(); err != nil {
		t.Fatalf("setup: %v", err)
	}

	store, err := NewStateStore(configPath)
	if err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}
	if _, err := store.Update(func(c *Config) error {
		c.Node.Public.Region = []*tailcfg.DERPRegion{reg}
		return nil
	}); err != nil {
		t.Fatalf("inject DERP region: %v", err)
	}

	// 2. serve
	serveCmd := exec.Command(binPath, "serve", "--config", configPath, "--runtime-dir", runtimeDir)
	serveCmd.Env = subEnv
	if err := serveCmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	defer func() {
		if serveCmd.Process != nil {
			_ = serveCmd.Process.Kill()
			_ = serveCmd.Wait()
		}
	}()
	waitDaemonReady(t, runtimeDir, configPath, serveCmd.Process.Pid, 3*time.Second)

	// 3. connect
	cCmd := exec.Command(binPath, "connect", "--plain", "--config", configPath, "--runtime-dir", runtimeDir)
	cCmd.Env = subEnv
	cOut, err := cCmd.Output()
	if err != nil {
		t.Fatalf("cCmd.Output: %v", err)
	}
	connStr := strings.TrimSpace(string(cOut))
	parsed, err := tunnel.ParseConnectionString(connStr)
	if err != nil {
		t.Fatalf("parse conn str: %v", err)
	}

	ctrlClientNode := key.NewNode()
	ctrlPrivPEM, ctrlAuthSSH, err := secure.GenerateSSHKey("ctrl-sec-test")
	if err != nil {
		t.Fatalf("secure.GenerateSSHKey: %v", err)
	}
	ctrlSigner, err := ssh.ParsePrivateKey(ctrlPrivPEM)
	if err != nil {
		t.Fatalf("ssh.ParsePrivateKey: %v", err)
	}
	ctrlSSHPub, _, _, _, err := ssh.ParseAuthorizedKey(ctrlAuthSSH)
	if err != nil {
		t.Fatalf("ssh.ParseAuthorizedKey: %v", err)
	}

	tempClient := tailcat.NewClient(tailcat.Addr(parsed.Payload.TailcatAddr))
	tempClient.Key = parsed.ClientNode
	tempClient.Logf = func(string, ...any) {}
	defer tempClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	netConn, err := tempClient.DialTCPPort(ctx, 22)
	if err != nil {
		t.Fatalf("dial temp: %v", err)
	}
	defer netConn.Close()

	tempConn, chans, reqs, err := ssh.NewClientConn(netConn, "", &ssh.ClientConfig{
		User:            "pairing",
		Auth:            []ssh.AuthMethod{ssh.Password(parsed.Payload.PairSecret)},
		HostKeyCallback: ssh.FixedHostKey(parsed.SSHHostKey),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatalf("ssh.NewClientConn: %v", err)
	}
	tempSSHClient := ssh.NewClient(tempConn, chans, reqs)
	defer tempSSHClient.Close()

	hexSSHPub := hex.EncodeToString(ctrlAuthSSH)

	// 4. 初次 prepare
	var bindingID1, challenge1, formalAddr1 string
	{
		sess, err := tempSSHClient.NewSession()
		if err != nil {
			t.Fatalf("tempSSHClient.NewSession: %v", err)
		}
		var pOut bytes.Buffer
		sess.Stdout = &pOut
		if err := sess.Run(fmt.Sprintf("pairing-prepare req-sec-01 ctrl-sec-01 %s %s", ctrlClientNode.Public().String(), hexSSHPub)); err != nil {
			t.Fatalf("prepare 1 failed: %v", err)
		}
		sess.Close()
		for _, f := range strings.Fields(pOut.String()) {
			if strings.HasPrefix(f, "binding_id=") {
				bindingID1 = strings.TrimPrefix(f, "binding_id=")
			} else if strings.HasPrefix(f, "challenge=") {
				challenge1 = strings.TrimPrefix(f, "challenge=")
			} else if strings.HasPrefix(f, "formal_addr=") {
				formalAddr1 = strings.TrimPrefix(f, "formal_addr=")
			}
		}
	}

	// 5. 幂等 prepare：全量相同参数再次发送，必须复用相同 binding_id 与 formal_addr
	{
		sess, err := tempSSHClient.NewSession()
		if err != nil {
			t.Fatalf("tempSSHClient.NewSession: %v", err)
		}
		var pOut bytes.Buffer
		sess.Stdout = &pOut
		if err := sess.Run(fmt.Sprintf("pairing-prepare req-sec-01 ctrl-sec-01 %s %s", ctrlClientNode.Public().String(), hexSSHPub)); err != nil {
			t.Fatalf("idempotent prepare failed: %v", err)
		}
		sess.Close()
		var bindingID2, formalAddr2 string
		for _, f := range strings.Fields(pOut.String()) {
			if strings.HasPrefix(f, "binding_id=") {
				bindingID2 = strings.TrimPrefix(f, "binding_id=")
			} else if strings.HasPrefix(f, "formal_addr=") {
				formalAddr2 = strings.TrimPrefix(f, "formal_addr=")
			}
		}
		if bindingID2 != bindingID1 || formalAddr2 != formalAddr1 {
			t.Fatalf("idempotent prepare mismatch: (%s, %s) != (%s, %s)", bindingID2, formalAddr2, bindingID1, formalAddr1)
		}
		t.Logf("Prepare idempotency verified: exact same binding_id and formal_addr reused")
	}

	// 6. 冲突 prepare：尝试用另一个不同的 controller_id 覆盖已有预留，必须被严格拒绝且不破坏已有端点
	{
		sess, err := tempSSHClient.NewSession()
		if err != nil {
			t.Fatalf("tempSSHClient.NewSession: %v", err)
		}
		var pOut, pErr bytes.Buffer
		sess.Stdout = &pOut
		sess.Stderr = &pErr
		err = sess.Run(fmt.Sprintf("pairing-prepare req-sec-02 ctrl-DIFFERENT %s %s", ctrlClientNode.Public().String(), hexSSHPub))
		sess.Close()
		if err == nil {
			t.Fatalf("expected conflicting prepare to be rejected, but it succeeded: %s", pOut.String())
		}
		t.Logf("Conflicting prepare rejection verified: %s", strings.TrimSpace(pErr.String()))
	}

	// 7. 正式端点公钥拦截：使用未授权的随机 SSH 私钥拨号正式端点，必须在握手阶段被拒！
	formalClient := tailcat.NewClient(tailcat.Addr(formalAddr1))
	formalClient.Key = ctrlClientNode
	formalClient.Logf = func(string, ...any) {}
	defer formalClient.Close()

	dialFormal := func() (net.Conn, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return formalClient.DialTCPPort(ctx, 22)
	}

	{
		badPrivPEM, _, err := secure.GenerateSSHKey("ctrl-attacker")
		if err != nil {
			t.Fatalf("secure.GenerateSSHKey: %v", err)
		}
		badSigner, err := ssh.ParsePrivateKey(badPrivPEM)
		if err != nil {
			t.Fatalf("ssh.ParsePrivateKey: %v", err)
		}
		fConnBad, err := dialFormal()
		if err != nil {
			t.Fatalf("dial for bad key test: %v", err)
		}
		defer fConnBad.Close()

		_, _, _, authErr := ssh.NewClientConn(fConnBad, "", &ssh.ClientConfig{
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(badSigner)},
			HostKeyCallback: ssh.FixedHostKey(parsed.SSHHostKey),
			Timeout:         2 * time.Second,
		})
		if authErr == nil {
			t.Fatalf("CRITICAL SECURITY FLAW: formal endpoint accepted unauthorized SSH public key!")
		}
		t.Logf("Unauthorized SSH key strictly rejected during handshake: %v", authErr)
	}

	// 8. 使用合法密钥连接并 commit 激活
	fConn, err := dialFormal()
	if err != nil {
		t.Fatalf("formalClient.DialTCPPort: %v", err)
	}
	defer fConn.Close()
	fSSHConn, fChans, fReqs, err := ssh.NewClientConn(fConn, "", &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(ctrlSigner)},
		HostKeyCallback: ssh.FixedHostKey(parsed.SSHHostKey),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatalf("legit ssh handshake: %v", err)
	}
	formalSSHClient := ssh.NewClient(fSSHConn, fChans, fReqs)
	defer formalSSHClient.Close()

	sshFP := ssh.FingerprintSHA256(ctrlSSHPub)
	msg := tunnel.CommitContextMessage(challenge1, bindingID1, "ctrl-sec-01", parsed.Payload.AgentID, parsed.Payload.EnrollmentID, ctrlClientNode.Public().String(), sshFP, formalAddr1)
	digest := sha256.Sum256(msg)
	block, _ := pem.Decode(ctrlPrivPEM)
	if block == nil {
		t.Fatal("generated controller key is not PEM")
	}
	rawPriv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("x509.ParsePKCS8PrivateKey: %v", err)
	}
	edPriv := rawPriv.(ed25519.PrivateKey)
	sigProof := ed25519.Sign(edPriv, digest[:])

	{
		commitSess, err := formalSSHClient.NewSession()
		if err != nil {
			t.Fatalf("formalSSHClient.NewSession: %v", err)
		}
		var cOut, cErr bytes.Buffer
		commitSess.Stdout = &cOut
		commitSess.Stderr = &cErr
		if err := commitSess.Run(fmt.Sprintf("pairing-commit %s %s", bindingID1, hex.EncodeToString(sigProof))); err != nil {
			t.Fatalf("commit failed: %v, stderr: %s", err, cErr.String())
		}
		commitSess.Close()
		if !strings.Contains(cOut.String(), "status=active") {
			t.Fatalf("expected status=active: %s", cOut.String())
		}
	}

	// 9. Commit 丢应答后在新的 active 连接上重试 commit：验证幂等通过且严格验签
	{
		fConnAct, err := dialFormal()
		if err != nil {
			t.Fatalf("formalClient.DialTCPPort: %v", err)
		}
		defer fConnAct.Close()
		fSSHConnAct, fChansAct, fReqsAct, err := ssh.NewClientConn(fConnAct, "", &ssh.ClientConfig{
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(ctrlSigner)},
			HostKeyCallback: ssh.FixedHostKey(parsed.SSHHostKey),
			Timeout:         3 * time.Second,
		})
		if err != nil {
			t.Fatalf("ssh.NewClientConn: %v", err)
		}
		actClient := ssh.NewClient(fSSHConnAct, fChansAct, fReqsAct)
		defer actClient.Close()

		// 9.1 重试正确的 commit -> 幂等返回 status=active
		commitRetrySess, err := actClient.NewSession()
		if err != nil {
			t.Fatalf("actClient.NewSession: %v", err)
		}
		var retryOut bytes.Buffer
		commitRetrySess.Stdout = &retryOut
		if err := commitRetrySess.Run(fmt.Sprintf("pairing-commit %s %s", bindingID1, hex.EncodeToString(sigProof))); err != nil {
			t.Fatalf("idempotent commit retry failed: %v", err)
		}
		commitRetrySess.Close()
		if !strings.Contains(retryOut.String(), "status=active") {
			t.Fatalf("expected status=active on commit retry, got: %s", retryOut.String())
		}
		t.Logf("Commit retry on active connection succeeded idempotently!")

		// 9.2 重试伪造签名的 commit -> 严格拒绝
		badSig := make([]byte, len(sigProof))
		copy(badSig, sigProof)
		badSig[0] ^= 0xff
		badRetrySess, err := actClient.NewSession()
		if err != nil {
			t.Fatalf("actClient.NewSession: %v", err)
		}
		var badOut, badErr bytes.Buffer
		badRetrySess.Stdout = &badOut
		badRetrySess.Stderr = &badErr
		if err := badRetrySess.Run(fmt.Sprintf("pairing-commit %s %s", bindingID1, hex.EncodeToString(badSig))); err == nil {
			t.Fatalf("expected forged signature commit retry to fail, but succeeded!")
		}
		badRetrySess.Close()
		t.Logf("Forged signature on commit retry strictly rejected: %s", strings.TrimSpace(badErr.String()))
	}
}

// TestCLI_RevokeAndReEnroll 测试 unpair 真实连接强断与撤销后新 connect 全新周期绑定
func TestCLI_RevokeAndReEnroll(t *testing.T) {
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("os.MkdirAll: %v", err)
	}
	binPath := filepath.Join(binDir, "herdrx")
	buildCLI(t, binPath)

	dm := tunnel.RunTestDERPAndSTUN(t, func(string, ...any) {}, "127.0.0.1")
	reg := dm.Regions[1]
	derpPort := reg.Nodes[0].DERPPort
	ap := netip.MustParseAddrPort(fmt.Sprintf("127.0.0.1:%d", derpPort))
	tunnel.DefaultSSRFValidator.AllowPrivateEndpoint(ap)
	defer tunnel.DefaultSSRFValidator.ClearAllowed()

	configDir := filepath.Join(tempDir, "c")
	runtimeDir := filepath.Join(tempDir, "r")
	stateDir := filepath.Join(tempDir, "s")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("os.MkdirAll: %v", err)
	}
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatalf("os.MkdirAll: %v", err)
	}

	fakeHerdrPath, _ := createMockHerdrFixture(t, tempDir, configDir)
	subEnv := isolatedEnv(configDir, runtimeDir, stateDir)

	configPath := filepath.Join(configDir, "cfg.json")

	// 1. setup & inject region
	setupCmd := exec.Command(binPath, "setup", "--herdr-bin", fakeHerdrPath, "--config", configPath, "--runtime-dir", runtimeDir, "--skip-service")
	setupCmd.Env = subEnv
	if err := setupCmd.Run(); err != nil {
		t.Fatalf("setupCmd.Run: %v", err)
	}

	store, err := NewStateStore(configPath)
	if err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}
	if _, err := store.Update(func(c *Config) error {
		c.Node.Public.Region = []*tailcfg.DERPRegion{reg}
		return nil
	}); err != nil {
		t.Fatalf("inject DERP region: %v", err)
	}

	// 2. serve
	serveCmd := exec.Command(binPath, "serve", "--config", configPath, "--runtime-dir", runtimeDir)
	serveCmd.Env = subEnv
	if err := serveCmd.Start(); err != nil {
		t.Fatalf("serveCmd.Start: %v", err)
	}
	defer func() {
		if serveCmd.Process != nil {
			_ = serveCmd.Process.Kill()
			_ = serveCmd.Wait()
		}
	}()
	waitDaemonReady(t, runtimeDir, configPath, serveCmd.Process.Pid, 3*time.Second)

	// 3. connect & pair controller 1
	cCmd1 := exec.Command(binPath, "connect", "--plain", "--config", configPath, "--runtime-dir", runtimeDir)
	cCmd1.Env = subEnv
	out1, err := cCmd1.Output()
	if err != nil {
		t.Fatalf("cCmd1.Output: %v", err)
	}
	parsed1, err := tunnel.ParseConnectionString(strings.TrimSpace(string(out1)))
	if err != nil {
		t.Fatalf("tunnel.ParseConnectionString: %v", err)
	}

	ctrlNode1 := key.NewNode()
	ctrlPrivPEM1, ctrlAuthSSH1, err := secure.GenerateSSHKey("ctrl-1")
	if err != nil {
		t.Fatalf("secure.GenerateSSHKey: %v", err)
	}
	ctrlSigner1, err := ssh.ParsePrivateKey(ctrlPrivPEM1)
	if err != nil {
		t.Fatalf("ssh.ParsePrivateKey: %v", err)
	}
	ctrlSSHPub1, _, _, _, err := ssh.ParseAuthorizedKey(ctrlAuthSSH1)
	if err != nil {
		t.Fatalf("ssh.ParseAuthorizedKey: %v", err)
	}

	tcClient1 := tailcat.NewClient(tailcat.Addr(parsed1.Payload.TailcatAddr))
	tcClient1.Key = parsed1.ClientNode
	tcClient1.Logf = func(string, ...any) {}
	defer tcClient1.Close()

	// Each dial gets its own deadline; CLI subprocess shutdown and revocation
	// checks must not consume the next controller's connection budget.
	dialTCP := func(client *tailcat.Client) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return client.DialTCPPort(ctx, 22)
	}
	netConn1, err := dialTCP(tcClient1)
	if err != nil {
		t.Fatalf("dialTCP: %v", err)
	}
	tempConn1, chans1, reqs1, err := ssh.NewClientConn(netConn1, "", &ssh.ClientConfig{
		User:            "pairing",
		Auth:            []ssh.AuthMethod{ssh.Password(parsed1.Payload.PairSecret)},
		HostKeyCallback: ssh.FixedHostKey(parsed1.SSHHostKey),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatalf("ssh.NewClientConn: %v", err)
	}
	tempClientSSH1 := ssh.NewClient(tempConn1, chans1, reqs1)

	sess1, err := tempClientSSH1.NewSession()
	if err != nil {
		t.Fatalf("tempClientSSH1.NewSession: %v", err)
	}
	var pOut1 bytes.Buffer
	sess1.Stdout = &pOut1
	if err := sess1.Run(fmt.Sprintf("pairing-prepare req-1 ctrl-1 %s %s", ctrlNode1.Public().String(), hex.EncodeToString(ctrlAuthSSH1))); err != nil {
		t.Fatalf("sess1.Run: %v", err)
	}
	sess1.Close()
	tempClientSSH1.Close()
	netConn1.Close()

	var bindingID1, challenge1, formalAddr1 string
	for _, f := range strings.Fields(pOut1.String()) {
		if strings.HasPrefix(f, "binding_id=") {
			bindingID1 = strings.TrimPrefix(f, "binding_id=")
		} else if strings.HasPrefix(f, "challenge=") {
			challenge1 = strings.TrimPrefix(f, "challenge=")
		} else if strings.HasPrefix(f, "formal_addr=") {
			formalAddr1 = strings.TrimPrefix(f, "formal_addr=")
		}
	}

	formalClient1 := tailcat.NewClient(tailcat.Addr(formalAddr1))
	formalClient1.Key = ctrlNode1
	formalClient1.Logf = func(string, ...any) {}
	defer formalClient1.Close()

	fConn1, err := dialTCP(formalClient1)
	if err != nil {
		t.Fatalf("dialTCP: %v", err)
	}
	fSSHConn1, fChans1, fReqs1, err := ssh.NewClientConn(fConn1, "", &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(ctrlSigner1)},
		HostKeyCallback: ssh.FixedHostKey(parsed1.SSHHostKey),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatalf("ssh.NewClientConn: %v", err)
	}
	formalSSH1 := ssh.NewClient(fSSHConn1, fChans1, fReqs1)
	defer formalSSH1.Close()

	if bindingID1 == "" || challenge1 == "" || formalAddr1 == "" {
		t.Fatal("prepare response omitted binding, challenge or formal endpoint")
	}
	msg1 := tunnel.CommitContextMessage(challenge1, bindingID1, "ctrl-1", parsed1.Payload.AgentID, parsed1.Payload.EnrollmentID, ctrlNode1.Public().String(), ssh.FingerprintSHA256(ctrlSSHPub1), formalAddr1)
	digest1 := sha256.Sum256(msg1)
	block1, _ := pem.Decode(ctrlPrivPEM1)
	if block1 == nil {
		t.Fatal("generated controller key is not PEM")
	}
	raw1, err := x509.ParsePKCS8PrivateKey(block1.Bytes)
	if err != nil {
		t.Fatalf("x509.ParsePKCS8PrivateKey: %v", err)
	}
	sig1 := ed25519.Sign(raw1.(ed25519.PrivateKey), digest1[:])

	commitSess1, err := formalSSH1.NewSession()
	if err != nil {
		t.Fatalf("formalSSH1.NewSession: %v", err)
	}
	commitOut1, err := commitSess1.CombinedOutput(fmt.Sprintf("pairing-commit %s %s", bindingID1, hex.EncodeToString(sig1)))
	if err != nil || !bytes.Contains(commitOut1, []byte("status=active")) {
		t.Fatalf("controller 1 commit: %v, output: %s", err, commitOut1)
	}
	commitSess1.Close()

	// 保持一条活跃长连接
	activeConn1, err := dialTCP(formalClient1)
	if err != nil {
		t.Fatalf("dialTCP: %v", err)
	}
	defer activeConn1.Close()
	actSSHConn1, actChans1, actReqs1, err := ssh.NewClientConn(activeConn1, "", &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(ctrlSigner1)},
		HostKeyCallback: ssh.FixedHostKey(parsed1.SSHHostKey),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatalf("ssh.NewClientConn: %v", err)
	}
	actSSH1 := ssh.NewClient(actSSHConn1, actChans1, actReqs1)
	defer actSSH1.Close()

	// Prove this is an authorized active connection before testing revocation.
	probe, err := actSSH1.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	probe.Stdin = strings.NewReader("before-revoke\n")
	probeOut, err := probe.CombinedOutput("herdr terminal session observe s1 --cols 80 --rows 24")
	probe.Close()
	if err != nil || !bytes.Contains(probeOut, []byte("terminal-input-received: before-revoke")) {
		t.Fatalf("active connection before revoke: %v, output: %s", err, probeOut)
	}
	doneSevered := make(chan error, 1)
	go func() { doneSevered <- actSSH1.Wait() }()
	select {
	case err := <-doneSevered:
		t.Fatalf("active transport closed before revocation: %v", err)
	default:
	}

	// 4. 执行 CLI unpair 撤销
	unpairCmd := exec.Command(binPath, "unpair", "--config", configPath, "--runtime-dir", runtimeDir)
	unpairCmd.Env = subEnv
	var uOut bytes.Buffer
	unpairCmd.Stdout = &uOut
	if err := unpairCmd.Run(); err != nil {
		t.Fatalf("unpair failed: %v", err)
	}
	t.Logf("CLI unpair succeeded: %s", strings.TrimSpace(uOut.String()))

	// 5. Observe closure of the SSH transport itself, not an unsupported command.
	select {
	case <-doneSevered:
		t.Log("Existing active SSH transport closed after unpair")
	case <-time.After(2 * time.Second):
		_ = actSSH1.Close()
		t.Fatal("existing active SSH transport did not close within 2 seconds after unpair")
	}

	// 6. 验证新拨号正式端点被拒绝
	ctxShort, cancelShort := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancelShort()
	if _, dialErr := formalClient1.DialTCPPort(ctxShort, 22); dialErr == nil {
		t.Fatalf("expected dial to revoked formal server to fail, but succeeded")
	}
	t.Logf("Formal endpoint is closed and inaccessible after unpair as required")

	// 7. 撤销后执行新 connect：验证能够开启全新的 enrollment 周期（递增新 epoch）
	cCmd2 := exec.Command(binPath, "connect", "--plain", "--config", configPath, "--runtime-dir", runtimeDir)
	cCmd2.Env = subEnv
	out2, err := cCmd2.Output()
	if err != nil {
		t.Fatalf("new connect after unpair failed: %v", err)
	}
	connStr2 := strings.TrimSpace(string(out2))
	parsed2, err := tunnel.ParseConnectionString(connStr2)
	if err != nil {
		t.Fatalf("parse new conn str: %v", err)
	}
	if parsed2.Payload.EnrollmentID == parsed1.Payload.EnrollmentID {
		t.Fatalf("new connect must generate distinct enrollment ID after revoke")
	}
	t.Logf("New connect generated distinct fresh enrollment after unpair: %s", parsed2.Payload.EnrollmentID)

	// 8. 新 Controller 2 可以正常完成两阶段握手并建立新绑定
	ctrlNode2 := key.NewNode()
	ctrlPrivPEM2, ctrlAuthSSH2, err := secure.GenerateSSHKey("ctrl-2")
	if err != nil {
		t.Fatalf("secure.GenerateSSHKey: %v", err)
	}
	ctrlSigner2, err := ssh.ParsePrivateKey(ctrlPrivPEM2)
	if err != nil {
		t.Fatalf("ssh.ParsePrivateKey: %v", err)
	}
	ctrlSSHPub2, _, _, _, err := ssh.ParseAuthorizedKey(ctrlAuthSSH2)
	if err != nil {
		t.Fatalf("ssh.ParseAuthorizedKey: %v", err)
	}

	tcClient2 := tailcat.NewClient(tailcat.Addr(parsed2.Payload.TailcatAddr))
	tcClient2.Key = parsed2.ClientNode
	tcClient2.Logf = func(string, ...any) {}
	defer tcClient2.Close()

	netConn2, err := dialTCP(tcClient2)
	if err != nil {
		t.Fatalf("dial new temp endpoint: %v", err)
	}
	defer netConn2.Close()

	tempConn2, chans2, reqs2, err := ssh.NewClientConn(netConn2, "", &ssh.ClientConfig{
		User:            "pairing",
		Auth:            []ssh.AuthMethod{ssh.Password(parsed2.Payload.PairSecret)},
		HostKeyCallback: ssh.FixedHostKey(parsed2.SSHHostKey),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatalf("ssh.NewClientConn: %v", err)
	}
	tempClientSSH2 := ssh.NewClient(tempConn2, chans2, reqs2)
	defer tempClientSSH2.Close()

	sess2, err := tempClientSSH2.NewSession()
	if err != nil {
		t.Fatalf("tempClientSSH2.NewSession: %v", err)
	}
	var pOut2 bytes.Buffer
	sess2.Stdout = &pOut2
	if err := sess2.Run(fmt.Sprintf("pairing-prepare req-2 ctrl-2 %s %s", ctrlNode2.Public().String(), hex.EncodeToString(ctrlAuthSSH2))); err != nil {
		t.Fatalf("prepare for controller 2 failed: %v", err)
	}
	sess2.Close()

	var bindingID2, challenge2, formalAddr2 string
	for _, f := range strings.Fields(pOut2.String()) {
		if strings.HasPrefix(f, "binding_id=") {
			bindingID2 = strings.TrimPrefix(f, "binding_id=")
		} else if strings.HasPrefix(f, "challenge=") {
			challenge2 = strings.TrimPrefix(f, "challenge=")
		} else if strings.HasPrefix(f, "formal_addr=") {
			formalAddr2 = strings.TrimPrefix(f, "formal_addr=")
		}
	}

	formalClient2 := tailcat.NewClient(tailcat.Addr(formalAddr2))
	formalClient2.Key = ctrlNode2
	formalClient2.Logf = func(string, ...any) {}
	defer formalClient2.Close()

	fConn2, err := dialTCP(formalClient2)
	if err != nil {
		t.Fatalf("dial formal server for controller 2: %v", err)
	}
	defer fConn2.Close()

	fSSHConn2, fChans2, fReqs2, err := ssh.NewClientConn(fConn2, "", &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(ctrlSigner2)},
		HostKeyCallback: ssh.FixedHostKey(parsed2.SSHHostKey),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatalf("ssh.NewClientConn: %v", err)
	}
	formalSSH2 := ssh.NewClient(fSSHConn2, fChans2, fReqs2)
	defer formalSSH2.Close()

	msg2 := tunnel.CommitContextMessage(challenge2, bindingID2, "ctrl-2", parsed2.Payload.AgentID, parsed2.Payload.EnrollmentID, ctrlNode2.Public().String(), ssh.FingerprintSHA256(ctrlSSHPub2), formalAddr2)
	digest2 := sha256.Sum256(msg2)
	block2, _ := pem.Decode(ctrlPrivPEM2)
	if block2 == nil {
		t.Fatal("generated controller key is not PEM")
	}
	raw2, err := x509.ParsePKCS8PrivateKey(block2.Bytes)
	if err != nil {
		t.Fatalf("x509.ParsePKCS8PrivateKey: %v", err)
	}
	sig2 := ed25519.Sign(raw2.(ed25519.PrivateKey), digest2[:])

	commitSess2, err := formalSSH2.NewSession()
	if err != nil {
		t.Fatalf("formalSSH2.NewSession: %v", err)
	}
	var cOut2, cErr2 bytes.Buffer
	commitSess2.Stdout = &cOut2
	commitSess2.Stderr = &cErr2
	if err := commitSess2.Run(fmt.Sprintf("pairing-commit %s %s", bindingID2, hex.EncodeToString(sig2))); err != nil {
		t.Fatalf("commit for controller 2 failed: %v, stderr: %s", err, cErr2.String())
	}
	commitSess2.Close()

	if !strings.Contains(cOut2.String(), "status=active") {
		t.Fatalf("expected controller 2 commit status=active, got: %s", cOut2.String())
	}
	t.Logf("Controller 2 successfully paired and bound after previous unpair cycle!")
}

// TestCLI_CrashRecoveryAtPreparedAndActive 组合别名用例：兼容旧调用
func TestCLI_CrashRecoveryAtPreparedAndActive(t *testing.T) {
	t.Run("PreparedRestart", TestCLI_CrashRecovery_PreparedRestart)
	t.Run("ActiveRestart", TestCLI_CrashRecovery_ActiveRestart)
}
