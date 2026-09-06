package tunnel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/agent"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

func testLogger(t *testing.T, name string) logger.Logf {
	t.Helper()
	return func(format string, args ...any) {
		t.Logf("["+name+"] "+format, args...)
	}
}

func TestValidateDialAddr_AllNodesAndIPv6Pin(t *testing.T) {
	v := NewSSRFValidator()
	good := &tailcat.ConnInfo{
		ServerPublic: tailcat.NodePublic{NodePublic: key.NewNode().Public()},
		PresharedKey: tailcat.NewPresharedKey(),
		Region: []*tailcfg.DERPRegion{{
			RegionID: 7,
			Nodes: []*tailcfg.DERPNode{
				{HostName: "8.8.8.8", IPv4: "8.8.8.8", IPv6: "", DERPPort: 443},
				{HostName: "1.1.1.1", IPv4: "1.1.1.1", IPv6: "none", DERPPort: 443},
			},
		}},
	}
	dial, err := v.ValidateDialAddr(string(good.Addr()))
	if err != nil {
		t.Fatalf("expected public DERP nodes to pass: %v", err)
	}
	parsed, err := tailcat.ParseAddr(tailcat.Addr(dial))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Region[0].Nodes[0].IPv6 != "none" {
		t.Fatalf("empty IPv6 was not pinned to none: %q", parsed.Region[0].Nodes[0].IPv6)
	}

	evil := &tailcat.ConnInfo{
		ServerPublic: tailcat.NodePublic{NodePublic: key.NewNode().Public()},
		PresharedKey: tailcat.NewPresharedKey(),
		Region: []*tailcfg.DERPRegion{{
			RegionID: 8,
			Nodes: []*tailcfg.DERPNode{
				{HostName: "8.8.8.8", IPv4: "8.8.8.8", IPv6: "none", DERPPort: 443},
				{HostName: "169.254.169.254", DERPPort: 80},
			},
		}},
	}
	if _, err := v.ValidateDialAddr(string(evil.Addr())); err == nil {
		t.Fatal("backup metadata node must fail SSRF")
	}

	short := &tailcat.ConnInfo{
		ServerPublic: tailcat.NodePublic{NodePublic: key.NewNode().Public()},
		PresharedKey: tailcat.NewPresharedKey(),
		RegionID:     304,
	}
	if _, err := v.ValidateDialAddr(string(short.Addr())); err == nil {
		t.Fatal("RegionID-only address must not be expanded by guessing derpN.tailscale.com")
	}
}

func TestSSRFValidator(t *testing.T) {
	v := NewSSRFValidator()

	// 1. 回环地址拦截
	if err := v.ValidateTarget("127.0.0.1", 3478); err == nil {
		t.Fatalf("expected loopback 127.0.0.1 to be rejected")
	}

	// 2. 云元数据地址拦截
	if err := v.ValidateTarget("169.254.169.254", 80); err == nil {
		t.Fatalf("expected metadata endpoint to be rejected")
	}

	// 3. 私网未授权拦截
	if err := v.ValidateTarget("192.168.1.100", 3478); err == nil {
		t.Fatalf("expected unapproved private IP to be rejected")
	}

	// 4. 白名单放行测试
	ap := netip.MustParseAddrPort("127.0.0.1:4444")
	v.AllowPrivateEndpoint(ap)
	if err := v.ValidateTarget("127.0.0.1", 4444); err != nil {
		t.Fatalf("expected whitelisted endpoint to be allowed, got: %v", err)
	}
}

func TestConnectionString_BuildAndParse(t *testing.T) {
	clientNodeKey := key.NewNode()
	clientNodeKeyTxt, _ := clientNodeKey.MarshalText()
	_, serverAuthSSH, err := secure.GenerateSSHKey("test-host")
	if err != nil {
		t.Fatalf("gen ssh: %v", err)
	}

	validCI := &tailcat.ConnInfo{
		ServerPublic: tailcat.NodePublic{NodePublic: key.NewNode().Public()},
		PresharedKey: tailcat.NewPresharedKey(),
		RegionID:     304,
	}
	validAddr := string(validCI.Addr())

	payload := ConnectionPayload{
		V:            1,
		AgentID:      "agent-node-01",
		TailcatAddr:  validAddr,
		ClientPriv:   string(clientNodeKeyTxt),
		SSHHostKey:   string(serverAuthSSH),
		EnrollmentID: "enroll-test-12345",
		PairSecret:   "secret-long-enough-12345",
		Host:         "my-host",
		OS:           "linux",
		Arch:         "amd64",
		AgentVer:     "v0.1.0",
		Exp:          time.Now().Add(10 * time.Minute).Unix(),
	}

	// 1. 序列化
	connStr, err := BuildConnectionString(payload)
	if err != nil {
		t.Fatalf("build conn str: %v", err)
	}
	if !strings.HasPrefix(connStr, "herdrx://v1/") {
		t.Fatalf("prefix mismatch: %s", connStr)
	}

	// 2. 正常反序列化
	parsed, err := ParseConnectionString(connStr)
	if err != nil {
		t.Fatalf("parse conn str: %v", err)
	}
	if parsed.Payload.AgentID != "agent-node-01" || parsed.Payload.EnrollmentID != "enroll-test-12345" {
		t.Fatalf("parsed fields mismatch: %+v", parsed.Payload)
	}

	// 3. 负例：无 PSK 降级拦截 (ci.PresharedKey is zero)
	noPSKCI := &tailcat.ConnInfo{
		ServerPublic: tailcat.NodePublic{NodePublic: key.NewNode().Public()},
		RegionID:     304,
	}
	badPayload := payload
	badPayload.TailcatAddr = string(noPSKCI.Addr())
	badConnStr, _ := BuildConnectionString(badPayload)
	if _, err := ParseConnectionString(badConnStr); err == nil {
		t.Fatalf("expected connection string without PSK to be rejected")
	}

	// 4. 负例：超限截断拦截
	hugePayload := payload
	hugePayload.Host = strings.Repeat("A", 17000)
	if _, err := BuildConnectionString(hugePayload); err == nil {
		t.Fatalf("expected oversize connection string to be rejected")
	}
}

// TestTwoPhaseProtocol_LiveDERPEndToEnd 测试真实的临时端点建连、两阶段 prepare -> Ed25519 commit -> 激活
func TestTwoPhaseProtocol_LiveDERPEndToEnd(t *testing.T) {
	dm := RunTestDERPAndSTUN(t, testLogger(t, "derp"), "127.0.0.1")
	reg := dm.Regions[1]
	if reg == nil {
		t.Fatal("no region 1")
	}

	agentPrivKey := tailcat.NewPrivateKey()
	agentNodeKey := agentPrivKey.Private
	agentPSK := tailcat.NewPresharedKey()
	agentHostPriv, _, err := secure.GenerateSSHKey("agent-host")
	if err != nil {
		t.Fatalf("gen agent ssh: %v", err)
	}
	agentHostSigner, err := ssh.ParsePrivateKey(agentHostPriv)
	if err != nil {
		t.Fatalf("parse host signer: %v", err)
	}

	mockStore := &mockConfigStore{
		cfg: agent.Config{
			Version:        1,
			Node:           *agentPrivKey,
			SSHHostPrivate: string(agentHostPriv),
			Enrollment: agent.EnrollmentConfig{
				EnrollmentID: "enroll-live-001",
				ExpiresAt:    time.Now().Add(10 * time.Minute),
			},
		},
	}
	state := NewTwoPhaseState(mockStore)

	// 1. 启动临时端点 Server
	tempClientKey := key.NewNode()
	enrollCtx := &EnrollmentContext{
		EnrollmentID: "enroll-live-001",
		EphemeralKey: key.NewNode(),
		EphemeralPSK: tailcat.NewPresharedKey(),
		ClientPriv:   tempClientKey,
		PairSecret:   "secret-pair-live-12345",
		ExpiresAt:    time.Now().Add(10 * time.Minute),
	}

	ephemServer, err := StartEphemeralPairingServer(enrollCtx, state, "agent-01", agentHostSigner, reg)
	if err != nil {
		t.Fatalf("start ephem server: %v", err)
	}
	defer ephemServer.Close()

	// 生产真实装配：收到 prepare 请求时由受控端自动启动正式 PermanentServer
	var formalServer *PermanentTailcatServer
	ephemServer.StartFormalServer = func(formalNode key.NodePublic) (string, error) {
		var sErr error
		formalServer, sErr = StartPermanentServer(agentNodeKey, agentPSK, state, "agent-01", agentHostSigner, "", formalNode, reg)
		if sErr != nil {
			return "", sErr
		}
		return formalServer.TailcatAddr(), nil
	}
	defer func() {
		if formalServer != nil {
			_ = formalServer.Close()
		}
	}()

	// 2. 构造 Controller 真实身份
	formalClientNode := key.NewNode()
	ctrlPub, ctrlPriv, _ := ed25519.GenerateKey(rand.Reader)
	ctrlSSHPub, _ := ssh.NewPublicKey(ctrlPub)

	// 3. Web 客户端通过临时通道发起 prepare
	tempClient := &tailcat.Client{Server: tailcat.Addr(enrollCtx.TailcatAddr), Key: tempClientKey, Logf: testLogger(t, "temp-cl")}
	defer tempClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	netConn, err := tempClient.DialTCPPort(ctx, 22)
	if err != nil {
		t.Fatalf("dial ephemeral server: %v", err)
	}
	defer netConn.Close()

	sshClientCfg := &ssh.ClientConfig{
		User:            "pairing",
		Auth:            []ssh.AuthMethod{ssh.Password(enrollCtx.PairSecret)},
		HostKeyCallback: ssh.FixedHostKey(agentHostSigner.PublicKey()),
		Timeout:         3 * time.Second,
	}
	sshClientConn, chans, reqs, err := ssh.NewClientConn(netConn, "", sshClientCfg)
	if err != nil {
		t.Fatalf("ssh handshake on temp endpoint: %v", err)
	}
	sshClient := ssh.NewClient(sshClientConn, chans, reqs)
	defer sshClient.Close()

	sess, err := sshClient.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	hexSSHPub := hex.EncodeToString(ssh.MarshalAuthorizedKey(ctrlSSHPub))
	prepareCmd := fmt.Sprintf("pairing-prepare req-uuid-001 ctrl-001 %s %s", formalClientNode.Public().String(), hexSSHPub)
	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	if err := sess.Run(prepareCmd); err != nil {
		t.Fatalf("pairing-prepare failed: %v, stderr: %s", err, stderr.String())
	}
	sess.Close()

	outStr := stdout.String()
	if !strings.Contains(outStr, "status=prepared") {
		t.Fatalf("prepare failed, output: %s", outStr)
	}

	// 提取 binding_id 和 challenge
	var bindingID, challenge, formalAddr string
	for _, field := range strings.Fields(outStr) {
		if strings.HasPrefix(field, "binding_id=") {
			bindingID = strings.TrimPrefix(field, "binding_id=")
		} else if strings.HasPrefix(field, "challenge=") {
			challenge = strings.TrimPrefix(field, "challenge=")
		} else if strings.HasPrefix(field, "formal_addr=") {
			formalAddr = strings.TrimPrefix(field, "formal_addr=")
		}
	}
	if bindingID == "" || challenge == "" {
		t.Fatalf("missing binding_id or challenge in output: %s", outStr)
	}

	// 4. 正式客户端拨号从 prepare 真实返回的正式端点 formalAddr，完成 commit
	formalClient := &tailcat.Client{Server: tailcat.Addr(formalAddr), Key: formalClientNode, Logf: testLogger(t, "formal-cl")}
	defer formalClient.Close()

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	netConn2, err := formalClient.DialTCPPort(ctx2, 22)
	if err != nil {
		t.Fatalf("dial formal server: %v", err)
	}
	defer netConn2.Close()

	formalSSHClientCfg := &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(mustSigner(ctrlPriv))},
		HostKeyCallback: ssh.FixedHostKey(agentHostSigner.PublicKey()),
		Timeout:         3 * time.Second,
	}
	fConn, fChans, fReqs, err := ssh.NewClientConn(netConn2, "", formalSSHClientCfg)
	if err != nil {
		t.Fatalf("ssh handshake on formal endpoint: %v", err)
	}
	formalSSHClient := ssh.NewClient(fConn, fChans, fReqs)
	defer formalSSHClient.Close()

	// 5.1 在 prepared 状态下尝试执行系统命令：断言必须被协议层拒绝 (exit 126)
	{
		testSess, err := formalSSHClient.NewSession()
		if err != nil {
			t.Fatalf("new session: %v", err)
		}
		if err := testSess.Run("uname -sm"); err == nil {
			testSess.Close()
			t.Fatalf("expected uname to be prohibited before commit on formal endpoint")
		}
		testSess.Close()
	}

	// 5.2 构造真实 Ed25519 证明并执行 pairing-commit
	sshFP := ssh.FingerprintSHA256(ctrlSSHPub)
	msg := CommitContextMessage(challenge, bindingID, "ctrl-001", "agent-01", enrollCtx.EnrollmentID, formalClientNode.Public().String(), sshFP, formalAddr)
	digest := sha256.Sum256(msg)
	sigProof := ed25519.Sign(ctrlPriv, digest[:])

	commitCmd := fmt.Sprintf("pairing-commit %s %s", bindingID, hex.EncodeToString(sigProof))
	commitSess, err := formalSSHClient.NewSession()
	if err != nil {
		t.Fatalf("new commit session: %v", err)
	}
	var commitOut, commitErr bytes.Buffer
	commitSess.Stdout = &commitOut
	commitSess.Stderr = &commitErr
	if err := commitSess.Run(commitCmd); err != nil {
		t.Fatalf("pairing-commit failed: %v, stderr: %s", err, commitErr.String())
	}
	commitSess.Close()

	if !strings.Contains(commitOut.String(), "status=active") {
		t.Fatalf("expected commit to activate binding, got: %s", commitOut.String())
	}

	// 5.3 核心断言：在同一 SSH 连接中，即使 commit 成功，角色固定于认证时刻，执行终端命令依然被拒绝
	{
		denySess, err := formalSSHClient.NewSession()
		if err != nil {
			t.Fatalf("new session on pairing connection: %v", err)
		}
		if err := denySess.Run("uname -sm"); err == nil {
			denySess.Close()
			t.Fatalf("expected in-place escalation to be denied on commit connection")
		}
		denySess.Close()
	}

	// 6. 新建正式 SSH 连接（认证时刻角色为 active）：正常放行 uname
	{
		netConn3, err := formalClient.DialTCPPort(ctx2, 22)
		if err != nil {
			t.Fatalf("dial formal endpoint after commit: %v", err)
		}
		defer netConn3.Close()

		fSSHConn3, fChans3, fReqs3, err := ssh.NewClientConn(netConn3, "", formalSSHClientCfg)
		if err != nil {
			t.Fatalf("ssh handshake after commit: %v", err)
		}
		formalSSHClient3 := ssh.NewClient(fSSHConn3, fChans3, fReqs3)
		defer formalSSHClient3.Close()

		actSess, err := formalSSHClient3.NewSession()
		if err != nil {
			t.Fatalf("new active session: %v", err)
		}
		var actOut bytes.Buffer
		actSess.Stdout = &actOut
		if err := actSess.Run("uname -sm"); err != nil {
			t.Fatalf("uname failed on active channel: %v", err)
		}
		actSess.Close()
		if !strings.Contains(actOut.String(), "linux amd64") {
			t.Fatalf("unexpected uname output: %s", actOut.String())
		}
	}
}

func mustSigner(priv ed25519.PrivateKey) ssh.Signer {
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		panic(err)
	}
	return signer
}

type mockConfigStore struct {
	mu             sync.RWMutex
	cfg            agent.Config
	failNextUpdate error
}

func (m *mockConfigStore) Snapshot() (agent.Config, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg, nil
}

func (m *mockConfigStore) Update(mutator func(*agent.Config) error) (agent.Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failNextUpdate != nil {
		err := m.failNextUpdate
		m.failNextUpdate = nil
		return agent.Config{}, err
	}
	c := m.cfg
	if err := mutator(&c); err != nil {
		return agent.Config{}, err
	}
	m.cfg = c
	return c, nil
}
