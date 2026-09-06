package agentcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/riba2534/herdrx/internal/agent"
)

// ErrPersistUncertain means the config path was already replaced, but the
// parent directory could not be synced. Callers must treat disk as the source
// of truth and must not roll authorization epochs backward.
var ErrPersistUncertain = errors.New("config file was replaced but directory durability is uncertain")

var errNoLegacyConfiguration = errors.New("no legacy configuration found")

var persistFault struct {
	sync.Mutex
	beforeRename error
	afterRename  error
}

func setPersistFault(beforeRename, afterRename error) {
	persistFault.Lock()
	persistFault.beforeRename = beforeRename
	persistFault.afterRename = afterRename
	persistFault.Unlock()
}

func cloneConfig(cfg Config) (Config, error) {
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return Config{}, fmt.Errorf("clone config: %w", err)
	}
	out, err := agent.Decode(encoded)
	if err != nil {
		// Incomplete documents (test fixtures mid-write) still need isolation.
		var parsed Config
		if jsonErr := json.Unmarshal(encoded, &parsed); jsonErr != nil {
			return Config{}, fmt.Errorf("clone config: %w", err)
		}
		parsed.SyncPSK()
		return parsed, nil
	}
	return out, nil
}

// Config 直接复用 agent.Config 模型
type Config = agent.Config

// SafeLoadConfig opens the config with O_NOFOLLOW, then authenticates the
// descriptor (regular file, owner, mode) before reading. This avoids TOCTOU
// between Lstat and ReadFile.
func SafeLoadConfig(path string) (Config, error) {
	cleanPath := filepath.Clean(path)
	f, err := os.OpenFile(cleanPath, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return Config{}, fmt.Errorf("refusing to read config through symlink: %s", cleanPath)
		}
		return Config{}, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return Config{}, fmt.Errorf("stat config: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return Config{}, fmt.Errorf("config %s is not a regular file", cleanPath)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return Config{}, fmt.Errorf("config %s: unsupported stat type", cleanPath)
	}
	if int(st.Uid) != os.Getuid() {
		return Config{}, fmt.Errorf("config %s not owned by current user: uid %d != %d", cleanPath, st.Uid, os.Getuid())
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return Config{}, fmt.Errorf("config %s permissions %#o are too open", cleanPath, fi.Mode().Perm())
	}

	encoded, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if len(encoded) > 4<<20 {
		return Config{}, errors.New("identity config exceeds 4 MiB")
	}
	return agent.Decode(encoded)
}

// SafeSaveConfig 原子落地候选文件，严格检查属主/权限，并确保父目录 fsync 成功
func SafeSaveConfig(path string, cfg Config) error {
	cfg.SyncPSK()
	cleanPath := filepath.Clean(path)
	dirPath := filepath.Dir(cleanPath)

	if err := os.MkdirAll(dirPath, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	dirInfo, err := os.Stat(dirPath)
	if err != nil {
		return fmt.Errorf("stat config dir: %w", err)
	}
	// 校验目录属主
	dirStat := dirInfo.Sys().(*syscall.Stat_t)
	if int(dirStat.Uid) != os.Getuid() {
		return fmt.Errorf("config dir not owned by current user: uid %d != %d", dirStat.Uid, os.Getuid())
	}

	// 检查目标路径是否为软链接
	fi, err := os.Lstat(cleanPath)
	if err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to overwrite symlink target: %s", cleanPath)
	}

	encoded, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	tmp, err := os.CreateTemp(dirPath, ".config-*")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}

	persistFault.Lock()
	beforeRename := persistFault.beforeRename
	afterRename := persistFault.afterRename
	persistFault.Unlock()
	if beforeRename != nil {
		return beforeRename
	}

	if err := os.Rename(tmpName, cleanPath); err != nil {
		return fmt.Errorf("atomic rename config: %w", err)
	}

	if afterRename != nil {
		persistFault.Lock()
		persistFault.afterRename = nil
		persistFault.Unlock()
		return fmt.Errorf("%w: %v", ErrPersistUncertain, afterRename)
	}

	// 必须打开父目录并执行 fsync，且不得吞掉错误
	dir, err := os.Open(dirPath)
	if err != nil {
		return fmt.Errorf("%w: open config parent dir for sync: %v", ErrPersistUncertain, err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("%w: %v", ErrPersistUncertain, err)
	}

	return nil
}

// MigrateFromLegacy 检查并平滑迁移旧配置到指定的 targetConfigPath
// 保证：目标文件已有时直接使用；旧服务正在运行时拒绝迁移以防双开同一 NodeKey；
// 迁移时严格保留密钥，绝不将 Paired=false 变为 true，绝不复活已撤销状态。
func MigrateFromLegacy(env Environment, targetConfigPath string) (Config, bool, error) {
	cleanTarget, err := filepath.Abs(filepath.Clean(targetConfigPath))
	if err != nil {
		return Config{}, false, err
	}

	if exists, err := agent.Exists(cleanTarget); err != nil {
		return Config{}, false, err
	} else if exists {
		cfg, err := SafeLoadConfig(cleanTarget)
		return cfg, false, err
	}
	targetLock, err := TryAcquireConfigLock(cleanTarget)
	if err != nil {
		return Config{}, false, fmt.Errorf("target identity is in use: %w", err)
	}
	defer targetLock.Release()
	if exists, err := agent.Exists(cleanTarget); err != nil {
		return Config{}, false, err
	} else if exists {
		cfg, err := SafeLoadConfig(cleanTarget)
		return cfg, false, err
	}

	configRoot := filepath.Join(env.HomeDir, ".config")
	if env.ConfigDir != "" {
		configRoot = filepath.Dir(env.ConfigDir)
	}
	var legacyConfigPath string
	seen := make(map[string]bool)
	for _, root := range []string{configRoot, filepath.Join(env.HomeDir, ".config")} {
		path := filepath.Join(root, "herdrx-agent", "config.json")
		if seen[path] {
			continue
		}
		seen[path] = true
		if exists, err := agent.Exists(path); err != nil {
			return Config{}, false, fmt.Errorf("inspect legacy config: %w", err)
		} else if exists {
			if legacyConfigPath != "" {
				return Config{}, false, errors.New("multiple legacy identities found; stop the legacy service and choose its existing config explicitly with setup --config")
			}
			legacyConfigPath = path
		}
	}
	if legacyConfigPath == "" {
		return Config{}, false, errNoLegacyConfiguration
	}
	if err := disableInactiveLegacyService(env, legacyConfigPath); err != nil {
		return Config{}, false, err
	}

	// 检查旧服务是否仍然存活：若旧服务 socket 仍在响应，拒绝直接迁移，提示先停止旧服务
	legacySockets := []string{filepath.Join(env.HomeDir, ".local", "state", "herdrx-agent", "control.sock")}
	if env.RuntimeDir != "" {
		legacySockets = append(legacySockets, filepath.Join(filepath.Dir(env.RuntimeDir), "herdrx-agent", "control.sock"))
	}
	if env.StateDir != "" {
		legacySockets = append(legacySockets, filepath.Join(filepath.Dir(env.StateDir), "herdrx-agent", "control.sock"))
	}
	for _, legacySock := range legacySockets {
		fi, err := os.Lstat(legacySock)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Config{}, false, err
		}
		if fi.Mode()&os.ModeSocket == 0 {
			return Config{}, false, fmt.Errorf("legacy control path is not a socket: %s", legacySock)
		}
		if conn, err := net.DialTimeout("unix", legacySock, 300*time.Millisecond); err == nil {
			_ = conn.Close()
			return Config{}, false, fmt.Errorf("legacy service is still running; stop herdrx-agent.service before migrating to prevent duplicate NodeKey instances")
		} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ECONNREFUSED) {
			return Config{}, false, fmt.Errorf("cannot verify legacy service is stopped: %w", err)
		}
	}
	lock, err := TryAcquireConfigLock(legacyConfigPath)
	if err != nil {
		return Config{}, false, fmt.Errorf("legacy identity is in use; stop the old service before migrating: %w", err)
	}
	defer lock.Release()

	legacyCfg, err := SafeLoadConfig(legacyConfigPath)
	if err != nil {
		return Config{}, false, fmt.Errorf("load legacy config: %w", err)
	}

	// 写入新指定的目标路径
	if err := SafeSaveConfig(cleanTarget, legacyCfg); err != nil {
		return Config{}, false, fmt.Errorf("save migrated config to target %s: %w", cleanTarget, err)
	}

	return legacyCfg, true, nil
}

func disableInactiveLegacyService(env Environment, configPath string) error {
	unitPath := filepath.Join(filepath.Dir(filepath.Dir(configPath)), "systemd", "user", "herdrx-agent.service")
	raw, err := os.ReadFile(unitPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.Contains(string(raw), "Description=herdrx tailcat agent") && !strings.Contains(string(raw), "Description=herdrx remote access agent") {
		return errors.New("legacy service is customized; disable it explicitly before migrating")
	}
	runner := env.ServiceRunner
	if runner == nil {
		runner = NewRealServiceRunner()
	}
	inspector, ok := runner.(interface{ IsActive(string) (bool, error) })
	if !ok {
		return errors.New("service manager cannot verify the legacy service state")
	}
	active, err := inspector.IsActive("herdrx-agent.service")
	if err != nil {
		return err
	}
	if active {
		return errors.New("legacy service is still running; run systemctl --user stop herdrx-agent.service before migrating")
	}
	if err := runner.DisableAndStop("herdrx-agent.service"); err != nil {
		return fmt.Errorf("disable legacy autostart before migration: %w", err)
	}
	return nil
}
