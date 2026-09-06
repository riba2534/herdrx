package agentcli

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/tunnel"
	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
)

func TestIPC_RoundTripAndStatus(t *testing.T) {
	dm := tunnel.RunTestDERPAndSTUN(t, func(string, ...any) {}, "127.0.0.1")
	reg := dm.Regions[1]

	tempDir := t.TempDir()
	env := Environment{
		ConfigDir:  tempDir,
		RuntimeDir: tempDir,
		HomeDir:    tempDir,
		CommandRunner: func(name string, args ...string) ([]byte, error) {
			return []byte("herdr 0.8.2\n"), nil
		},
	}
	configPath := filepath.Join(tempDir, "config.json")
	store, err := NewStateStore(configPath)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	node := tailcat.NewPrivateKey()
	node.Public.Region = []*tailcfg.DERPRegion{reg}

	hostPrivPEM, _, err := secure.GenerateSSHKey("host-key")
	if err != nil {
		t.Fatalf("gen ssh key: %v", err)
	}
	initialCfg := Config{
		Version:          1,
		Node:             *node,
		AllowedNodeKey:   string(node.Public.Addr()),
		AuthorizedSSHKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey",
		SSHHostPrivate:   string(hostPrivPEM),
		SetupID:          "setup-test-id-12345",
		Paired:           true,
		Revoked:          false,
	}
	if err := SafeSaveConfig(configPath, initialCfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	ipcServer := NewIPCServer(env, store)
	if err := ipcServer.Start(); err != nil {
		t.Fatalf("start ipc server: %v", err)
	}
	defer ipcServer.Close()

	sockPath := filepath.Join(tempDir, "control.sock")

	// 1. 测试 /status 接口
	var stat DaemonStatus
	err = ClientCallIPC(sockPath, "GET", "/status", nil, &stat)
	if err != nil {
		t.Fatalf("client call status: %v", err)
	}
	if stat.PID != os.Getpid() {
		t.Fatalf("pid mismatch: got %d, want %d", stat.PID, os.Getpid())
	}
	if !stat.Paired || stat.Revoked {
		t.Fatalf("unexpected paired status: %+v", stat)
	}
	// 验证地址经过严格脱敏处理（掩码）
	if strings.Contains(stat.TailcatAddrMasked, "nodekey:000000000000") && len(stat.TailcatAddrMasked) > 30 {
		t.Fatalf("address must be masked, got: %s", stat.TailcatAddrMasked)
	}

	// 2. 测试 /doctor 接口返回强类型结构
	var doc DoctorResult
	err = ClientCallIPC(sockPath, "GET", "/doctor", nil, &doc)
	if err != nil {
		t.Fatalf("client call doctor: %v", err)
	}
	if !doc.DaemonRunning || doc.DaemonPID != os.Getpid() || !doc.ConfigValid {
		t.Fatalf("unexpected doctor result: %+v", doc)
	}

	// 3. 测试 /connect 接口成功生成有效连接串
	// 先将配置切换为未配对状态
	_, _ = store.Update(func(c *Config) error {
		c.Paired = false
		return nil
	})
	var connRes ConnectResult
	err = ClientCallIPC(sockPath, "POST", "/connect", nil, &connRes)
	if err != nil {
		t.Fatalf("call connect failed: %v", err)
	}
	if !strings.HasPrefix(connRes.ConnectionString, "herdrx://v1/") || connRes.EnrollmentID == "" {
		t.Fatalf("unexpected connect result: %+v", connRes)
	}

	// 4. 测试 /unpair 撤销事务
	var unpairResp map[string]string
	err = ClientCallIPC(sockPath, "POST", "/unpair", nil, &unpairResp)
	if err != nil {
		t.Fatalf("client call unpair: %v", err)
	}
	if unpairResp["status"] != "unpaired" {
		t.Fatalf("unexpected unpair status: %v", unpairResp)
	}

	// 验证配置已经被标记为 Revoked=true, Paired=false
	updatedCfg, err := store.Snapshot()
	if err != nil || updatedCfg.Paired || !updatedCfg.Revoked {
		t.Fatalf("config not revoked correctly in store: %+v", updatedCfg)
	}
}

func TestIPC_StaleSocketCleanup(t *testing.T) {
	tempDir := t.TempDir()
	env := Environment{
		ConfigDir:  tempDir,
		RuntimeDir: tempDir,
		HomeDir:    tempDir,
	}
	configPath := filepath.Join(tempDir, "config.json")
	sockPath := filepath.Join(tempDir, "control.sock")
	store, _ := NewStateStore(configPath)

	// 使用 syscall.Socket + syscall.Bind 构造一个无进程监听但物理存在的孤儿 Unix Domain Socket
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	sa := &syscall.SockaddrUnix{Name: sockPath}
	if err := syscall.Bind(fd, sa); err != nil {
		_ = syscall.Close(fd)
		t.Fatalf("bind: %v", err)
	}
	_ = syscall.Close(fd)

	fi, err := os.Lstat(sockPath)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("expected stale socket to exist as ModeSocket")
	}

	ipcServer := NewIPCServer(env, store)
	if err := ipcServer.Start(); err != nil {
		t.Fatalf("ipc server should clean stale socket and start, got: %v", err)
	}
	defer ipcServer.Close()

	// 验证 socket 正在正常工作
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial new socket failed: %v", err)
	}
	conn.Close()
}

func TestIPC_NonSocketFileRefused(t *testing.T) {
	tempDir := t.TempDir()
	env := Environment{
		ConfigDir:  tempDir,
		RuntimeDir: tempDir,
		HomeDir:    tempDir,
	}
	configPath := filepath.Join(tempDir, "config.json")
	sockPath := filepath.Join(tempDir, "control.sock")
	store, _ := NewStateStore(configPath)

	// 创建一个普通文件占用 socketPath
	if err := os.WriteFile(sockPath, []byte("important user document"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ipcServer := NewIPCServer(env, store)
	err := ipcServer.Start()
	if err == nil {
		ipcServer.Close()
		t.Fatalf("expected Start to fail when path is an ordinary file, but succeeded")
	}
	if !strings.Contains(err.Error(), "not a socket file") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// 确认普通文件未被误删
	data, err := os.ReadFile(sockPath)
	if err != nil || string(data) != "important user document" {
		t.Fatalf("ordinary file was modified or deleted!")
	}
}

func TestIPC_DoubleStartRejected(t *testing.T) {
	tempDir := t.TempDir()
	env := Environment{
		ConfigDir:  tempDir,
		RuntimeDir: tempDir,
		HomeDir:    tempDir,
	}
	configPath := filepath.Join(tempDir, "config.json")
	store, _ := NewStateStore(configPath)

	s1 := NewIPCServer(env, store)
	if err := s1.Start(); err != nil {
		t.Fatalf("start s1: %v", err)
	}
	defer s1.Close()

	s2 := NewIPCServer(env, store)
	err := s2.Start()
	if err == nil {
		s2.Close()
		t.Fatalf("expected s2 Start to fail when s1 is active, but succeeded")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("unexpected double start error: %v", err)
	}
}

func TestCLI_StatusOfflineExitCode(t *testing.T) {
	tempDir := t.TempDir()
	env := Environment{
		ConfigDir:  tempDir,
		RuntimeDir: tempDir,
		HomeDir:    tempDir,
	}
	cfgPath := filepath.Join(tempDir, "config.json")

	// 1. 文本输出模式下 daemon 离线：退出码必须为 1
	var stdout, stderr bytes.Buffer
	code := runStatus([]string{"--runtime-dir", tempDir}, &stdout, &stderr, env, cfgPath)
	if code != 1 {
		t.Fatalf("expected status command to exit 1 when offline, got %d", code)
	}

	// 2. JSON 输出模式下 daemon 离线：退出码同样必须为 1（与文本模式一致）
	stdout.Reset()
	stderr.Reset()
	codeJSON := runStatus([]string{"--runtime-dir", tempDir, "--json"}, &stdout, &stderr, env, cfgPath)
	if codeJSON != 1 {
		t.Fatalf("expected status --json to exit 1 when offline, got %d", codeJSON)
	}

	var jsonResp map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &jsonResp); err != nil {
		t.Fatalf("unmarshal status json: %v", err)
	}
	if jsonResp["daemon_running"] != false || jsonResp["error"] == nil {
		t.Fatalf("unexpected json offline response: %+v", jsonResp)
	}
}
