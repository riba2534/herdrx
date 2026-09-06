package httpapi

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/agent"
	"github.com/riba2534/herdrx/internal/config"
	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/push"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/riba2534/herdrx/internal/tunnel"
	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

func TestTailcatEnrollment_SSRFAndSizeValidation(t *testing.T) {
	dataDir := t.TempDir()
	dataStore, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()

	vault, err := secure.OpenVault(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		Addr: "127.0.0.1:0", DataDir: dataDir, PublicURL: "http://example.test", BootstrapToken: "bootstrap-token",
		SessionTTL: time.Hour, HerdrBinary: "herdr", AllowPrivateHosts: true, DERPRegionID: 304,
	}

	api, err := New(cfg, dataStore, vault, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	server := httptest.NewServer(api.Handler())
	defer server.Close()

	client := newTestClient(t)
	bootstrap := postJSON(t, client, server.URL+"/api/bootstrap", "", map[string]any{
		"email": "admin@example.test", "display_name": "Admin", "password": "correct-horse-battery-staple", "token": "bootstrap-token",
	}, http.StatusOK)
	csrf := bootstrap["csrf_token"].(string)

	// 1. 超限输入（>16 KiB）：必须返回 400
	hugeStr := strings.Repeat("a", 18*1024)
	postJSON(t, client, server.URL+"/api/tailcat/enrollments", csrf, map[string]any{
		"connection_string": hugeStr,
	}, http.StatusBadRequest)

	// 2. SSRF 拦截测试：连接串指向 169.254.169.254 (元数据地址)
	clientKey := key.NewNode()
	clientKeyTxt, _ := clientKey.MarshalText()
	_, hostAuthKey, _ := secure.GenerateSSHKey("host")

	metaCI := &tailcat.ConnInfo{
		ServerPublic: tailcat.NodePublic{NodePublic: key.NewNode().Public()},
		PresharedKey: tailcat.NewPresharedKey(),
		Region: []*tailcfg.DERPRegion{{
			RegionID: 99,
			Nodes:    []*tailcfg.DERPNode{{HostName: "169.254.169.254", DERPPort: 80}},
		}},
	}

	metadataPayload := tunnel.ConnectionPayload{
		V:            1,
		AgentID:      "agent-meta",
		TailcatAddr:  string(metaCI.Addr()),
		ClientPriv:   string(clientKeyTxt),
		SSHHostKey:   string(hostAuthKey),
		EnrollmentID: "enroll-meta-001",
		PairSecret:   "secret-long-enough-12345",
	}
	metaConnStr, _ := tunnel.BuildConnectionString(metadataPayload)
	res := postJSON(t, client, server.URL+"/api/tailcat/enrollments", csrf, map[string]any{
		"connection_string": metaConnStr,
	}, http.StatusBadRequest)
	if !strings.Contains(fmt.Sprintf("%v", res), "ssrf_violation") {
		t.Fatalf("expected SSRF violation for metadata address: %v", res)
	}

	// 3. SSRF 拦截测试：连接串指向 127.0.0.1
	loopCI := &tailcat.ConnInfo{
		ServerPublic: tailcat.NodePublic{NodePublic: key.NewNode().Public()},
		PresharedKey: tailcat.NewPresharedKey(),
		Region: []*tailcfg.DERPRegion{{
			RegionID: 98,
			Nodes:    []*tailcfg.DERPNode{{HostName: "127.0.0.1", DERPPort: 9999}},
		}},
	}
	loopbackPayload := metadataPayload
	loopbackPayload.TailcatAddr = string(loopCI.Addr())
	loopConnStr, _ := tunnel.BuildConnectionString(loopbackPayload)
	resLoop := postJSON(t, client, server.URL+"/api/tailcat/enrollments", csrf, map[string]any{
		"connection_string": loopConnStr,
	}, http.StatusBadRequest)
	if !strings.Contains(fmt.Sprintf("%v", resLoop), "ssrf_violation") {
		t.Fatalf("expected SSRF violation for loopback: %v", resLoop)
	}
}

func TestTailcatEnrollment_EndToEndWithLocalDERP(t *testing.T) {
	// A Unix socket in an isolated remote configuration supplies a stable pane.
	// The formal Tailcat server forwards the real SSH stream to this socket.
	remoteDir, err := os.MkdirTemp("", "herdrx-restore-peer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(remoteDir)
	t.Setenv("XDG_CONFIG_HOME", remoteDir)
	if err := os.Mkdir(filepath.Join(remoteDir, "herdr"), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(remoteDir, "herdr", "herdr.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var request struct{ ID, Method string }
				if json.NewDecoder(conn).Decode(&request) != nil || request.Method != "session.snapshot" {
					return
				}
				_ = json.NewEncoder(conn).Encode(map[string]any{"id": request.ID, "result": map[string]any{"snapshot": map[string]any{"protocol": 22, "panes": []map[string]string{{"pane_id": "original-pane", "agent_status": "working"}}}}})
			}()
		}
	}()
	dm := tunnel.RunTestDERPAndSTUN(t, func(string, ...any) {}, "127.0.0.1")
	reg := dm.Regions[1]
	if reg == nil {
		t.Fatal("no region 1")
	}

	// 白名单放行自建本地测试 DERP 端口
	derpPort := reg.Nodes[0].DERPPort
	ap := netip.MustParseAddrPort(fmt.Sprintf("127.0.0.1:%d", derpPort))
	tunnel.DefaultSSRFValidator.AllowPrivateEndpoint(ap)
	stunPort := reg.Nodes[0].STUNPort
	if stunPort <= 0 {
		stunPort = 3478
	}
	tunnel.DefaultSSRFValidator.AllowPrivateEndpoint(netip.MustParseAddrPort(fmt.Sprintf("127.0.0.1:%d", stunPort)))
	defer tunnel.DefaultSSRFValidator.ClearAllowed()

	dataDir := t.TempDir()
	dataStore, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()

	vault, err := secure.OpenVault(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Addr: "127.0.0.1:0", DataDir: dataDir, PublicURL: "http://example.test", BootstrapToken: "bootstrap-token-2",
		SessionTTL: time.Hour, HerdrBinary: "herdr", AllowPrivateHosts: true, DERPRegionID: 304,
	}

	api, err := New(cfg, dataStore, vault, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	server := httptest.NewServer(api.Handler())
	defer server.Close()

	client := newTestClient(t)
	bootstrap := postJSON(t, client, server.URL+"/api/bootstrap", "", map[string]any{
		"email": "e2e@example.test", "display_name": "E2E", "password": "correct-horse-battery-staple", "token": "bootstrap-token-2",
	}, http.StatusOK)
	adminUserID := bootstrap["user"].(map[string]any)["id"].(string)
	csrf := bootstrap["csrf_token"].(string)

	// 1. 受控端启动两阶段状态机与临时端点
	agentPriv := tailcat.NewPrivateKey()
	agentNodeKey := agentPriv.Private
	agentPSK := tailcat.NewPresharedKey()
	agentHostPriv, _, err := secure.GenerateSSHKey("agent-e2e")
	if err != nil {
		t.Fatal(err)
	}
	agentHostSigner, err := ssh.ParsePrivateKey(agentHostPriv)
	if err != nil {
		t.Fatal(err)
	}

	mockStore := &mockConfigStore{
		cfg: agent.Config{
			Version:        1,
			Node:           *agentPriv,
			SSHHostPrivate: string(agentHostPriv),
			Enrollment: agent.EnrollmentConfig{
				EnrollmentID: "enroll-e2e-8888",
				ExpiresAt:    time.Now().Add(10 * time.Minute),
			},
		},
	}
	state := tunnel.NewTwoPhaseState(mockStore)
	tempClientKey := key.NewNode()
	enrollCtx := &tunnel.EnrollmentContext{
		EnrollmentID: "enroll-e2e-8888",
		EphemeralKey: key.NewNode(),
		EphemeralPSK: tailcat.NewPresharedKey(),
		ClientPriv:   tempClientKey,
		PairSecret:   "secret-long-enough-e2e-12345",
		ExpiresAt:    time.Now().Add(10 * time.Minute),
	}

	var formalServer *tunnel.PermanentTailcatServer
	starter := func(formalNode key.NodePublic) (string, error) {
		var sErr error
		formalServer, sErr = tunnel.StartPermanentServer(agentNodeKey, agentPSK, state, "agent-e2e-node", agentHostSigner, "", formalNode, reg)
		if sErr != nil {
			return "", sErr
		}
		return formalServer.TailcatAddr(), nil
	}
	ephemServer, err := tunnel.StartEphemeralPairingServer(enrollCtx, state, "agent-e2e-node", agentHostSigner, reg, starter)
	if err != nil {
		t.Fatalf("start ephem server: %v", err)
	}
	defer ephemServer.Close()
	defer func() {
		if formalServer != nil {
			_ = formalServer.Close()
		}
	}()

	// 构造连接串
	tempClientPrivTxt, _ := tempClientKey.MarshalText()
	connStr, err := tunnel.BuildConnectionString(tunnel.ConnectionPayload{
		V:            1,
		AgentID:      "agent-e2e-node",
		TailcatAddr:  enrollCtx.TailcatAddr,
		ClientPriv:   string(tempClientPrivTxt),
		SSHHostKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(agentHostSigner.PublicKey()))),
		EnrollmentID: enrollCtx.EnrollmentID,
		PairSecret:   enrollCtx.PairSecret,
		Host:         "my-e2e-box",
		OS:           "linux",
		Arch:         "amd64",
		AgentVer:     "v0.1.0",
	})
	if err != nil {
		t.Fatalf("build conn str: %v", err)
	}

	// 2. Web 发送导入请求 POST /api/tailcat/enrollments
	enrollResp := postJSON(t, client, server.URL+"/api/tailcat/enrollments", csrf, map[string]any{
		"connection_string": connStr,
		"name":              "E2E Tailcat Box",
	}, http.StatusAccepted)

	taskID := enrollResp["task_id"].(string)
	if taskID == "" {
		t.Fatalf("missing task_id in response: %v", enrollResp)
	}

	// 3. 轮询 GET /api/tailcat/enrollments/{id} 直至状态迁移为 active
	var finalHostID string
	success := false
	for attempt := 0; attempt < 40; attempt++ {
		time.Sleep(150 * time.Millisecond)
		taskStatus := requestJSON(t, client, http.MethodGet, server.URL+"/api/tailcat/enrollments/"+taskID, "", nil, http.StatusOK)
		st := taskStatus["status"].(string)
		t.Logf("attempt %d status: %s error: %v", attempt, st, taskStatus["error"])
		if st == string(EnrollmentStatusActive) {
			success = true
			if hid, ok := taskStatus["host_id"].(string); ok {
				finalHostID = hid
			}
			break
		}
		if st == string(EnrollmentStatusFailed) {
			t.Fatalf("enrollment task failed: %v", taskStatus["error"])
		}
	}

	if !success {
		t.Fatalf("enrollment task did not reach active in time")
	}
	if finalHostID == "" {
		t.Fatalf("missing host_id after active enrollment")
	}

	// 4. 验证通过 GET /api/hosts/{id} 导出的 Host 记录及秘密隔离
	hostResp := requestJSON(t, client, http.MethodGet, server.URL+"/api/hosts/"+finalHostID, "", nil, http.StatusOK)
	hostMap := hostResp["host"].(map[string]any)
	if hostMap["name"] != "E2E Tailcat Box" || hostMap["transport"] != "tailcat" {
		t.Fatalf("host fields mismatch: %+v", hostMap)
	}

	// 核心断言：公开的 Host JSON 序列化输出绝对不包含明文私钥或未脱敏的 PSK 地址！
	marshaledHost, _ := json.Marshal(hostResp)
	if strings.Contains(string(marshaledHost), "?psk=") {
		t.Fatalf("Host JSON must NOT leak sensitive PSK address: %s", string(marshaledHost))
	}
	if strings.Contains(string(marshaledHost), "PRIVATE KEY") {
		t.Fatalf("Host JSON must NOT leak private key: %s", string(marshaledHost))
	}

	// 5. 验证终端连接打通：通过 factory.Open 真实打开 Tailcat 终端会话
	storedHost, err := dataStore.HostByID(context.Background(), adminUserID, finalHostID)
	if err != nil {
		t.Fatalf("load host from db: %v", err)
	}
	endpoint, err := api.hosts.Open(context.Background(), storedHost)
	if err != nil {
		t.Fatalf("open tailcat host endpoint failed: %v", err)
	}
	if endpoint == nil {
		t.Fatalf("expected non-nil endpoint for active tailcat host")
	}
	checkSnapshot := func(endpoint interface {
		Snapshot(context.Context) (herdr.Snapshot, error)
	}) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		snapshot, err := endpoint.Snapshot(ctx)
		if err != nil || len(snapshot.Panes) != 1 || snapshot.Panes[0].ID != "original-pane" {
			t.Fatal("lost original pane after reconnect", err)
		}
	}
	checkSnapshot(endpoint)
	_ = endpoint.Close()
	// Restore a consistent backup in a new data directory with no old transports.
	// Keep the independent formal peer running throughout website replacement.
	ctx := context.Background()
	originalCredential, err := dataStore.CredentialByID(ctx, adminUserID, storedHost.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	originalSecret, err := hostruntime.OpenCredential(vault, adminUserID, originalCredential.ID, originalCredential.Kind, originalCredential.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	var delivered atomic.Bool
	vapidPublic := api.push.PublicKey()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) < 60 || !strings.Contains(r.Header.Get("Authorization"), "k="+vapidPublic) {
			t.Error("restored VAPID identity did not authenticate encrypted push")
		}
		delivered.Store(true)
		w.WriteHeader(201)
	}))
	defer provider.Close()
	pushKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pushAuth := make([]byte, 16)
	if _, err := rand.Read(pushAuth); err != nil {
		t.Fatal(err)
	}
	subscription := store.PushSubscription{ID: "restore-push", UserID: adminUserID, Endpoint: "https://push.example.test/restore", P256DH: base64.RawURLEncoding.EncodeToString(pushKey.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(pushAuth)}
	api.stopBackground()
	if err := dataStore.UpsertPushSubscription(ctx, subscription); err != nil {
		t.Fatal(err)
	}
	server.Close()
	api.Close()
	restoredDir := t.TempDir()
	if err := dataStore.Backup(ctx, filepath.Join(restoredDir, "herdrx.db")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"master.key", "vapid.json"} {
		content, err := os.ReadFile(filepath.Join(dataDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(restoredDir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"herdrx.db", "master.key", "vapid.json"} {
		info, err := os.Stat(filepath.Join(restoredDir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("backup permissions are not owner-only", err)
		}
	}
	restoredStore, err := store.Open(restoredDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredStore.Close()
	restoredVault, err := secure.OpenVault(restoredDir, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = restoredDir
	restoredAPI, err := New(cfg, restoredStore, restoredVault, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredAPI.Close()
	restoredAPI.stopBackground()
	setFixturePushClient(t, restoredAPI, provider)
	restoredServer := httptest.NewServer(restoredAPI.Handler())
	defer restoredServer.Close()
	// Both a saved web login and a fresh password login survive the complete copy.
	requestJSON(t, client, "GET", restoredServer.URL+"/api/me", "", nil, 200)
	postJSON(t, newTestClient(t), restoredServer.URL+"/api/login", "", map[string]any{"email": "e2e@example.test", "password": "correct-horse-battery-staple"}, 200)
	restoredHost, err := restoredStore.HostByID(ctx, adminUserID, finalHostID)
	if err != nil {
		t.Fatal(err)
	}
	restoredCredential, err := restoredStore.CredentialByID(ctx, adminUserID, restoredHost.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	restoredSecret, err := hostruntime.OpenCredential(restoredVault, adminUserID, restoredCredential.ID, restoredCredential.Kind, restoredCredential.Ciphertext)
	if err != nil || string(restoredSecret) != string(originalSecret) {
		t.Fatal("restored binding or credential changed", err)
	}
	restoredEndpoint, err := restoredAPI.hosts.Open(ctx, restoredHost)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredEndpoint.Close()
	checkSnapshot(restoredEndpoint)
	status := requestJSON(t, client, "GET", restoredServer.URL+"/api/tailcat/enrollments/"+taskID, "", nil, 200)
	if status["status"] != string(EnrollmentStatusActive) || status["host_id"] != finalHostID {
		t.Fatal("restored binding is not active")
	}
	subscriptions, err := restoredStore.PushSubscriptions(ctx)
	if err != nil || len(subscriptions) != 1 || subscriptions[0] != subscription {
		t.Fatal("push subscription lost", err)
	}
	if restoredAPI.push.PublicKey() != vapidPublic {
		t.Fatal("VAPID identity rotated during restore")
	}
	if expired, err := restoredAPI.push.Notify(ctx, subscriptions[0], push.Notification{Title: "Restore test", Body: "Original binding resumed"}); expired || err != nil || !delivered.Load() {
		t.Fatal("restored notification failed", err)
	}
}

type mockConfigStore struct {
	mu  sync.RWMutex
	cfg agent.Config
}

func (m *mockConfigStore) Snapshot() (agent.Config, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg, nil
}

func (m *mockConfigStore) Update(mutator func(*agent.Config) error) (agent.Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.cfg
	if err := mutator(&c); err != nil {
		return agent.Config{}, err
	}
	m.cfg = c
	return c, nil
}
