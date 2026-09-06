package agentcli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/agent"
	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
)

func TestSafeSave_SymlinkRefused(t *testing.T) {
	tempDir := t.TempDir()
	targetFile := filepath.Join(tempDir, "real_target.txt")
	_ = os.WriteFile(targetFile, []byte("target data"), 0o600)

	symlinkPath := filepath.Join(tempDir, "config_symlink.json")
	if err := os.Symlink(targetFile, symlinkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	cfg := Config{Version: 1}
	err := SafeSaveConfig(symlinkPath, cfg)
	if err == nil {
		t.Fatalf("expected SafeSaveConfig to refuse overwriting symlink, but succeeded")
	}
}

func TestConfigLock_SymlinkRefused(t *testing.T) {
	tempDir := t.TempDir()
	targetFile := filepath.Join(tempDir, "real_lock_target.txt")
	_ = os.WriteFile(targetFile, []byte("some target"), 0o600)

	cfgPath := filepath.Join(tempDir, "agent_config.json")
	lockPath := cfgPath + ".lock"
	if err := os.Symlink(targetFile, lockPath); err != nil {
		t.Fatalf("create lock symlink: %v", err)
	}

	// 获取锁：必须拒绝通过 symlink 获取
	_, err := AcquireConfigLock(cfgPath)
	if err == nil {
		t.Fatalf("expected AcquireConfigLock to fail on symlink lock, but succeeded")
	}
}

func TestConfigLock_SameConfigMutualExclusion(t *testing.T) {
	tempDir := t.TempDir()
	cfgPath := filepath.Join(tempDir, "agent_config.json")

	l1, err := TryAcquireConfigLock(cfgPath)
	if err != nil {
		t.Fatalf("acquire l1: %v", err)
	}

	// 在同一个 config 路径下尝试非阻塞二次获取：必须互斥被拒绝
	l2, err := TryAcquireConfigLock(cfgPath)
	if err == nil {
		_ = l2.Release()
		t.Fatalf("expected second acquire to fail, but succeeded")
	}

	// 释放 l1 后，l3 应该能够顺利获取
	if err := l1.Release(); err != nil {
		t.Fatalf("release l1: %v", err)
	}

	// 释放锁时绝不应该将锁文件从磁盘删除（保持稳定 inode）
	if _, err := os.Stat(cfgPath + ".lock"); err != nil {
		t.Fatalf("lock file must persist across releases to maintain fixed inode, got: %v", err)
	}

	l3, err := AcquireConfigLock(cfgPath)
	if err != nil {
		t.Fatalf("acquire l3 after release failed: %v", err)
	}
	_ = l3.Release()
}

// TestStateStore_ConcurrentIncrementalUpdateNoLost 测试真正的并发 RMW 事务，断言多个 worker 的累加更新无丢失
func TestStateStore_ConcurrentIncrementalUpdateNoLost(t *testing.T) {
	tempDir := t.TempDir()
	cfgPath := filepath.Join(tempDir, "trans_config.json")
	store, err := NewStateStore(cfgPath)
	if err != nil {
		t.Fatalf("new state store: %v", err)
	}

	node := tailcat.NewPrivateKey()
	initialCfg := Config{
		Version:          1,
		Node:             *node,
		AllowedNodeKey:   string(node.Public.Addr()),
		AuthorizedSSHKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey",
		SSHHostPrivate:   "test-priv",
		SetupID:          "count:0",
		Paired:           false,
		Revoked:          false,
	}
	if err := SafeSaveConfig(cfgPath, initialCfg); err != nil {
		t.Fatalf("save initial config: %v", err)
	}

	workers := 10
	iterations := 5
	var wg sync.WaitGroup
	errCh := make(chan error, workers*iterations)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for it := 0; it < iterations; it++ {
				// 执行真实的原子读改写事务
				_, err := store.Update(func(c *Config) error {
					var currentCount int
					_, _ = fmt.Sscanf(c.SetupID, "count:%d", &currentCount)
					c.SetupID = fmt.Sprintf("count:%d", currentCount+1)
					return nil
				})
				if err != nil {
					errCh <- err
				}
				time.Sleep(1 * time.Millisecond)
			}
		}(w)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("transaction update error: %v", err)
	}

	// 核心断言：最终计数必须精确等于 workers * iterations（无任何 lost update 竞争丢失！）
	finalCfg, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot final config: %v", err)
	}

	var finalCount int
	_, _ = fmt.Sscanf(finalCfg.SetupID, "count:%d", &finalCount)
	expectedCount := workers * iterations
	if finalCount != expectedCount {
		t.Fatalf("lost updates detected in RMW transaction: got count %d, want %d", finalCount, expectedCount)
	}
}

// TestStateStore_UpdateFailureLeavesStateUnchanged 验证更新失败不会污染内存或保存中间状态
func TestStateStore_UpdateFailureLeavesStateUnchanged(t *testing.T) {
	tempDir := t.TempDir()
	cfgPath := filepath.Join(tempDir, "fail_config.json")
	store, _ := NewStateStore(cfgPath)

	node := tailcat.NewPrivateKey()
	initialCfg := Config{
		Version:          1,
		Node:             *node,
		AllowedNodeKey:   string(node.Public.Addr()),
		AuthorizedSSHKey: "ssh-ed25519 test",
		SSHHostPrivate:   "test-priv",
		SetupID:          "original-id",
	}
	_ = SafeSaveConfig(cfgPath, initialCfg)

	// 变更器返回错误
	_, err := store.Update(func(c *Config) error {
		c.SetupID = "modified-but-aborted"
		return errors.New("business rule validation failed")
	})
	if err == nil {
		t.Fatalf("expected error from update")
	}

	// 断言快照依然为原有值
	snap, err := store.Snapshot()
	if err != nil || snap.SetupID != "original-id" {
		t.Fatalf("snapshot should remain original after aborted update, got: %s", snap.SetupID)
	}
}

func TestMigrateFromLegacy_PreservesState(t *testing.T) {
	tempDir := t.TempDir()
	env := Environment{
		HomeDir: tempDir,
	}

	legacyDir := filepath.Join(tempDir, ".config", "herdrx-agent")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	legacyPath := filepath.Join(legacyDir, "config.json")

	node := tailcat.NewPrivateKey()
	legacyCfg := Config{
		Version:          1,
		Node:             *node,
		AllowedNodeKey:   string(node.Public.Addr()),
		AuthorizedSSHKey: "ssh-ed25519 test",
		SSHHostPrivate:   "legacy-priv-key",
		PublicURL:        "https://example.com",
		SetupID:          "legacy-setup",
		Paired:           false, // 关键：未配对
		Revoked:          true,  // 关键：已撤销
	}

	if err := SafeSaveConfig(legacyPath, legacyCfg); err != nil {
		t.Fatalf("save legacy config: %v", err)
	}

	targetNewPath := filepath.Join(tempDir, "custom", "herdrx", "custom_config.json")

	// 执行平滑迁移到指定目标路径
	migrated, wasMigrated, err := MigrateFromLegacy(env, targetNewPath)
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	if !wasMigrated {
		t.Fatalf("expected wasMigrated=true")
	}

	// 核心断言：未配对状态未被篡改，撤销状态未复活
	if migrated.Paired {
		t.Fatalf("Paired must not be escalated to true during migration")
	}
	if !migrated.Revoked {
		t.Fatalf("Revoked state must be strictly preserved")
	}
	if migrated.SSHHostPrivate != "legacy-priv-key" {
		t.Fatalf("private key lost during migration")
	}

	// 目标文件已存在时，再次调用不重复迁移
	second, wasMigrated2, err := MigrateFromLegacy(env, targetNewPath)
	if err != nil || wasMigrated2 || second.SetupID != "legacy-setup" {
		t.Fatalf("second migration should use existing new config")
	}
}

func TestSnapshot_DeepCopyIsolatesNestedRegion(t *testing.T) {
	tempDir := t.TempDir()
	cfgPath := filepath.Join(tempDir, "region.json")
	store, err := NewStateStore(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	node := tailcat.NewPrivateKey()
	initial := Config{
		Version:        1,
		Node:           *node,
		SSHHostPrivate: "test-priv",
		SetupID:        "region-copy",
	}
	initial.Node.Public.Region = []*tailcfg.DERPRegion{{
		RegionID:   1,
		RegionCode: "orig",
		Nodes:      []*tailcfg.DERPNode{{Name: "n1", HostName: "127.0.0.1", IPv4: "127.0.0.1", IPv6: "none"}},
	}}
	if err := SafeSaveConfig(cfgPath, initial); err != nil {
		t.Fatal(err)
	}
	snap, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	snap.Node.Public.Region[0].RegionCode = "mutated"
	snap.Node.Public.Region[0].Nodes[0].HostName = "evil.example"
	again, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if again.Node.Public.Region[0].RegionCode != "orig" || again.Node.Public.Region[0].Nodes[0].HostName != "127.0.0.1" {
		t.Fatalf("snapshot leaked nested mutation: %+v", again.Node.Public.Region[0])
	}
}

func TestStateStore_PersistFailureBeforeRenameLeavesCache(t *testing.T) {
	tempDir := t.TempDir()
	cfgPath := filepath.Join(tempDir, "before.json")
	store, _ := NewStateStore(cfgPath)
	node := tailcat.NewPrivateKey()
	initial := Config{Version: 1, Node: *node, SSHHostPrivate: "test-priv", SetupID: "original"}
	if err := SafeSaveConfig(cfgPath, initial); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	setPersistFault(errors.New("injected rename failure"), nil)
	t.Cleanup(func() { setPersistFault(nil, nil) })
	_, err := store.Update(func(c *Config) error {
		c.SetupID = "should-not-commit"
		c.Binding.Epoch = 99
		return nil
	})
	if err == nil {
		t.Fatal("expected rename failure")
	}
	snap, err := store.Snapshot()
	if err != nil || snap.SetupID != "original" || snap.Binding.Epoch == 99 {
		t.Fatalf("pre-rename failure changed cached/disk state: %+v err=%v", snap, err)
	}
}

func TestStateStore_RenameThenParentFsyncFailureAlignsCache(t *testing.T) {
	tempDir := t.TempDir()
	cfgPath := filepath.Join(tempDir, "after.json")
	store, _ := NewStateStore(cfgPath)
	node := tailcat.NewPrivateKey()
	initial := Config{Version: 1, Node: *node, SSHHostPrivate: "test-priv", SetupID: "original", Binding: agent.BindingConfig{Epoch: 7}}
	if err := SafeSaveConfig(cfgPath, initial); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	setPersistFault(nil, errors.New("injected parent fsync failure"))
	t.Cleanup(func() { setPersistFault(nil, nil) })
	_, err := store.Update(func(c *Config) error {
		c.SetupID = "published"
		c.Binding.Epoch = 11
		return nil
	})
	if !errors.Is(err, ErrPersistUncertain) {
		t.Fatalf("expected uncertain persist, got %v", err)
	}
	snap, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.SetupID != "published" {
		t.Fatalf("cache was not aligned to replaced file, got %s", snap.SetupID)
	}
	if snap.Binding.Epoch != 11 {
		t.Fatalf("authorization epoch rolled back after uncertain persist: %d", snap.Binding.Epoch)
	}
}

func TestXDGLegacyMigrationDisablesAutostartAndPreservesRevocation(t *testing.T) {
	dir := t.TempDir()
	runner := &FakeServiceRunner{Active: true}
	env := Environment{HomeDir: dir, ConfigDir: filepath.Join(dir, "xdg-config", "herdrx"), StateDir: filepath.Join(dir, "xdg-state", "herdrx"), ServiceRunner: runner}
	legacy := filepath.Join(dir, "xdg-config", "herdrx-agent", "config.json")
	cfg := newUpdateConfig(t, legacy)
	unit := filepath.Join(dir, "xdg-config", "systemd", "user", "herdrx-agent.service")
	if err := os.MkdirAll(filepath.Dir(unit), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unit, []byte("[Unit]\nDescription=herdrx tailcat agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(env.ConfigDir, "config.json")
	if _, _, err := MigrateFromLegacy(env, target); err == nil {
		t.Fatal("migrated active legacy service")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("created replacement identity for a running legacy service")
	}
	runner.Active = false
	got, migrated, err := MigrateFromLegacy(env, target)
	if err != nil || !migrated {
		t.Fatalf("migration: %v", err)
	}
	if !got.Node.Private.Equal(cfg.Node.Private) || got.SSHHostPrivate != cfg.SSHHostPrivate || got.Binding.Epoch != 7 || !got.Revoked {
		t.Fatal("migration changed identity/revocation")
	}
	found := false
	for _, call := range runner.Calls {
		if call == "DisableAndStop:herdrx-agent.service" {
			found = true
		}
	}
	if !found {
		t.Fatal("legacy autostart still enabled")
	}
}

func TestDefaultEnvHonorsXDGStateDataAndRuntime(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_RUNTIME_DIR", "")
	env := DefaultEnv()
	if env.ConfigDir != filepath.Join(dir, "config", "herdrx") || env.StateDir != filepath.Join(dir, "state", "herdrx") || env.RuntimeDir != filepath.Join(dir, "state", "herdrx", "run") || defaultReleasesDir(env) != filepath.Join(dir, "data", "herdrx", "releases") {
		t.Fatal("XDG override lost", env.ConfigDir, env.StateDir, env.RuntimeDir, env.DataDir)
	}
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(dir, "runtime"))
	if DefaultEnv().RuntimeDir != filepath.Join(dir, "runtime", "herdrx") {
		t.Fatal("runtime override ignored")
	}
}
