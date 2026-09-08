package agentcli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/agent"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/updater"
	"github.com/tailscale/tailcat"
)

type processUpdateRunner struct {
	FakeServiceRunner
	stable, config, run, crashVersion string
	process                           *exec.Cmd
	exited                            chan error
	afterStop                         func()
	restarts                          int
}

func (r *processUpdateRunner) stop() {
	if r.process == nil {
		return
	}
	_ = r.process.Process.Signal(syscall.SIGTERM)
	select {
	case <-r.exited:
	case <-time.After(5 * time.Second):
		_ = r.process.Process.Kill()
		<-r.exited
	}
	r.process = nil
}

// expectedServiceLabel 是本平台服务的标识，由 layoutFor 给出同一个来源，
// 避免夹具和实现各写一份而漂移。
var expectedServiceLabel = layoutFor(runtime.GOOS, Environment{}).Label

func (r *processUpdateRunner) Restart(name string) error {
	// 期望的标识随平台变化（systemd 单元名 / launchd 标签），但仍必须精确匹配，
	// 以免 update 误重启别的服务。
	if name != expectedServiceLabel {
		return fmt.Errorf("unexpected service %s", name)
	}
	r.restarts++
	r.stop()
	if r.afterStop != nil {
		r.afterStop()
		r.afterStop = nil
	}
	cmd := exec.Command(r.stable, "serve", "--config", r.config, "--runtime-dir", r.run)
	cmd.Env = isolatedEnv(filepath.Dir(filepath.Dir(r.config)), r.run, filepath.Join(filepath.Dir(r.run), "state"))
	if err := cmd.Start(); err != nil {
		return err
	}
	r.process = cmd
	r.exited = make(chan error, 1)
	go func() { r.exited <- cmd.Wait() }()
	if r.crashVersion != "" {
		raw, err := exec.Command(r.stable, "version").Output()
		if err == nil && strings.Contains(string(raw), r.crashVersion) {
			_ = cmd.Process.Kill()
		}
	}
	return nil
}
func newUpdateConfig(t *testing.T, path string) Config {
	t.Helper()
	node := tailcat.NewPrivateKey()
	node.Public.PresharedKey = tailcat.NewPresharedKey()
	hostKey, _, err := secure.GenerateSSHKey("test-update")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Version: 1, Node: *node, PresharedKey: node.Public.PresharedKey, SSHHostPrivate: string(hostKey), SetupID: GenerateSetupID(*node), Revoked: true, Binding: agent.BindingConfig{Status: "revoked", Epoch: 7}}
	if err := SafeSaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}
func buildVersionCLI(t *testing.T, path, version string) []byte {
	t.Helper()
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-ldflags", "-X main.version="+version, "-o", path, "../../cmd/herdrx")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build candidate: %v: %s", err, out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestCLIUpdate_RealProcessesRollbackAndRevocation(t *testing.T) {
	dir, err := os.MkdirTemp("", "hx-update-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	env := Environment{HomeDir: dir, ConfigDir: filepath.Join(dir, "config", "herdrx"), RuntimeDir: filepath.Join(dir, "run"), UpdateHealthTimeout: 1200 * time.Millisecond}
	stable := filepath.Join(dir, ".local", "bin", "herdrx")
	if err := os.MkdirAll(filepath.Dir(stable), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(env.ConfigDir, "config.json")
	cfg := newUpdateConfig(t, configPath)
	mockHerdr, _ := createMockHerdrFixture(t, dir, filepath.Dir(env.ConfigDir))
	env.HerdrBin = mockHerdr
	cfg.HerdrBin = mockHerdr
	if err := SafeSaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	oldBytes := buildVersionCLI(t, stable, "v0.1.0")
	nextPath := filepath.Join(dir, "next")
	nextBytes := buildVersionCLI(t, nextPath, "v0.2.0")
	// 按本平台的服务布局写单元：在 macOS 上放 systemd 单元会被 update 正确拒绝。
	layout := layoutFor(runtime.GOOS, env)
	unit := layout.UnitPath
	os.MkdirAll(filepath.Dir(unit), 0o700)
	os.WriteFile(unit, []byte(layout.Content(stable, configPath, env.RuntimeDir)), 0o600)
	runner := &processUpdateRunner{stable: stable, config: configPath, run: env.RuntimeDir}
	env.ServiceRunner = runner
	defer runner.stop()
	if err := runner.Restart(layout.Label); err != nil {
		t.Fatal(err)
	}
	current, err := probeExecutable(env, stable, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForReadiness(env, current, configPath, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	var setupOut, setupErr bytes.Buffer
	beforeSetup, _ := os.ReadFile(configPath)
	if code := runSetup([]string{"--skip-service"}, &setupOut, &setupErr, env, configPath); code != 0 {
		t.Fatalf("repeated setup with live daemon failed: %s", setupErr.String())
	}
	afterSetup, _ := os.ReadFile(configPath)
	if !bytes.Equal(beforeSetup, afterSetup) {
		t.Fatal("repeated setup rewrote the live identity")
	}
	// Independent task is never in a daemon process group or update transaction.
	task := exec.Command("sleep", "120")
	if err := task.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = task.Process.Kill(); _ = task.Wait() }()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(nextBytes)
	now := time.Now().UTC()
	manifest := updater.Manifest{Version: "v0.2.0", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, SHA256: hex.EncodeToString(sum[:]), MinProto: 1, MaxProto: 1, MinState: 1, MaxState: 1, CreatedAt: now.Add(-time.Minute).Format(time.RFC3339), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}
	manifest.Signature = hex.EncodeToString(ed25519.Sign(priv, updater.CanonicalPayload(manifest)))
	raw, _ := json.Marshal(manifest)
	manPath := filepath.Join(dir, "manifest.json")
	os.WriteFile(manPath, raw, 0o600)
	args := []string{"--manifest", manPath, "--binary", nextPath, "--pubkey", hex.EncodeToString(pub)}
	var stdout, stderr bytes.Buffer
	// A revocation advanced after the old process stopped must remain advanced.
	runner.afterStop = func() {
		cfg, err := SafeLoadConfig(configPath)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Binding.Epoch = 42
		if err := SafeSaveConfig(configPath, cfg); err != nil {
			t.Fatal(err)
		}
	}
	if code := runUpdate(args, &stdout, &stderr, env); code != 0 {
		t.Fatalf("update=%d: %s", code, stderr.String())
	}
	nextReady, err := probeExecutable(env, stable, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if nextReady.Version != "v0.2.0" || nextReady.Epoch != 42 || !nextReady.Revoked {
		t.Fatalf("wrong updated identity: %+v", nextReady)
	}
	// Reinstall exactly the same version without destroying the prior rollback point.
	if code := runUpdate(args, &stdout, &stderr, env); code != 0 {
		t.Fatalf("repeat update=%d: %s", code, stderr.String())
	}
	if code := runRollback(nil, &stdout, &stderr, env); code != 0 {
		t.Fatalf("rollback=%d: %s", code, stderr.String())
	}
	restored, _ := os.ReadFile(stable)
	if !bytes.Equal(restored, oldBytes) {
		t.Fatal("rollback did not restore original regular-file installer binary")
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	// Force the candidate daemon to crash while its self-test still succeeds.
	runner.crashVersion = "v0.2.0"
	stdout.Reset()
	stderr.Reset()
	if code := runUpdate(args, &stdout, &stderr, env); code == 0 || !strings.Contains(stderr.String(), "restored v0.1.0") {
		t.Fatalf("crash did not trigger verified recovery: %d, %s", code, stderr.String())
	}
	restored, _ = os.ReadFile(stable)
	if !bytes.Equal(restored, oldBytes) {
		t.Fatal("failed candidate was left active")
	}
	after, _ := os.ReadFile(configPath)
	if !bytes.Equal(before, after) {
		t.Fatal("update/recovery rewrote authorization state")
	}
	if err := task.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("independent task was stopped")
	}
	// A bad signed package must fail before any service mutation.
	restarts := runner.restarts
	nextBytes[0] ^= 0xff
	os.WriteFile(nextPath, nextBytes, 0o755)
	if code := runUpdate(args, &stdout, &stderr, env); code == 0 || runner.restarts != restarts {
		t.Fatal("tampered package touched service")
	}
}

func TestSelfTestAndReadinessNeedNoHerdrOrInternet(t *testing.T) {
	dir, err := os.MkdirTemp("", "hx-ready-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "config.json")
	newUpdateConfig(t, path)
	before, _ := os.ReadFile(path)
	var stdout, stderr bytes.Buffer
	if code := runSelfTest([]string{"--json", "--config", path}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var result Readiness
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	store, err := NewStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	server := NewIPCServer(Environment{RuntimeDir: filepath.Join(dir, "run"), CommandRunner: func(string, ...string) ([]byte, error) {
		t.Error("readiness invoked an external command")
		return nil, fmt.Errorf("offline")
	}}, store)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var ready Readiness
	if err := clientCallIPCContext(ctx, server.socketPath, "GET", "/ready", path, nil, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.Identity != result.Identity || ready.Epoch != 7 || !ready.Revoked {
		t.Fatalf("unexpected readiness: %+v", ready)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("self-test/readiness mutated config")
	}
}
