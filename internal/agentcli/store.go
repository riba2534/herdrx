package agentcli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ConfigLock 封装基于规范化 config 路径的持久化排他文件锁（固定 inode，不删除）
type ConfigLock struct {
	fd   int
	path string
}

// acquireConfigLockInternal 打开锁文件并校验属主与常规文件属性（真实 O_NOFOLLOW 与 fstat）
func acquireConfigLockInternal(configPath string, nonBlocking bool) (*ConfigLock, error) {
	cleanPath, err := filepath.Abs(filepath.Clean(configPath))
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	lockPath := cleanPath + ".lock"
	dirPath := filepath.Dir(lockPath)

	if err := os.MkdirAll(dirPath, 0o700); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	// 1. 打开锁文件：必须显式带 syscall.O_NOFOLLOW
	// 若 lockPath 是符号链接，syscall.Open 会直接返回 ELOOP 错误，杜绝 symlink 劫持
	fd, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("refusing to acquire lock through symlink: %s", lockPath)
		}
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	// 2. 在获取的文件描述符上执行 fstat，校验属主与普通文件类型
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("fstat lock file: %w", err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("lock file %s is not a regular file", lockPath)
	}
	if int(stat.Uid) != os.Getuid() {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("lock file %s not owned by current user: uid %d != %d", lockPath, stat.Uid, os.Getuid())
	}

	// 3. 获取排他锁
	flags := syscall.LOCK_EX
	if nonBlocking {
		flags |= syscall.LOCK_NB
		if err := syscall.Flock(fd, flags); err != nil {
			_ = syscall.Close(fd)
			return nil, fmt.Errorf("config lock already held: %w", err)
		}
	} else {
		// 阻塞模式：带 5 秒超时保护重试
		deadline := time.Now().Add(5 * time.Second)
		for {
			err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				_ = syscall.Close(fd)
				return nil, fmt.Errorf("timeout waiting for config lock: %w", err)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	return &ConfigLock{fd: fd, path: lockPath}, nil
}

// AcquireConfigLock 阻塞获取锁（用于事务读改写，保证并发排队不丢失）
func AcquireConfigLock(configPath string) (*ConfigLock, error) {
	return acquireConfigLockInternal(configPath, false)
}

// TryAcquireConfigLock 非阻塞获取锁（用于 daemon 启动检查，已有实例立即退出）
func TryAcquireConfigLock(configPath string) (*ConfigLock, error) {
	return acquireConfigLockInternal(configPath, true)
}

// Release 释放锁并关闭文件描述符，严格禁止删除锁文件以保持稳定 inode
func (l *ConfigLock) Release() error {
	if l == nil || l.fd < 0 {
		return nil
	}
	_ = syscall.Flock(l.fd, syscall.LOCK_UN)
	err := syscall.Close(l.fd)
	l.fd = -1
	return err
}

// StateStore 提供严格的原子读改写事务（RMW Transaction），支持进程级生命周期锁持有以杜绝重入死锁
type StateStore struct {
	configPath string
	heldLock   *ConfigLock // daemon 长期持有的排他锁，Update 时感知到已持锁直接复用，杜绝重入
	mu         sync.RWMutex
	cached     *Config
}

func NewStateStore(configPath string) (*StateStore, error) {
	absPath, err := filepath.Abs(filepath.Clean(configPath))
	if err != nil {
		return nil, err
	}
	return &StateStore{configPath: absPath}, nil
}

func (s *StateStore) ConfigPath() string {
	return s.configPath
}

// AcquireDaemonLock 由常驻 daemon 进程在启动时单次获取文件排他锁并长期持有，杜绝多实例双开
func (s *StateStore) AcquireDaemonLock() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.heldLock != nil {
		return nil
	}
	lock, err := TryAcquireConfigLock(s.configPath)
	if err != nil {
		return err
	}
	s.heldLock = lock
	return nil
}

// ReleaseDaemonLock 由 daemon 在优雅退出时释放锁
func (s *StateStore) ReleaseDaemonLock() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.heldLock == nil {
		return nil
	}
	err := s.heldLock.Release()
	s.heldLock = nil
	return err
}

// Snapshot 获取当前配置的安全只读快照。返回值是深拷贝，调用方不能改到缓存里的嵌套 Region。
func (s *StateStore) Snapshot() (Config, error) {
	s.mu.RLock()
	if s.cached != nil {
		cached := *s.cached
		s.mu.RUnlock()
		return cloneConfig(cached)
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil {
		return cloneConfig(*s.cached)
	}
	cfg, err := SafeLoadConfig(s.configPath)
	if err != nil {
		return Config{}, err
	}
	cloned, err := cloneConfig(cfg)
	if err != nil {
		return Config{}, err
	}
	s.cached = &cloned
	return cloneConfig(cloned)
}

// Update 在事务保护下执行原子读改写；若当前 store 已持有 daemon 排他锁，直接复用并在内存锁保护下串行执行，绝不重入抢锁
func (s *StateStore) Update(mutator func(*Config) error) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var tempLock *ConfigLock
	if s.heldLock == nil {
		// 离线/CLI 模式：当前进程尚未持有长期锁，临时获取跨进程排他锁
		var err error
		tempLock, err = AcquireConfigLock(s.configPath)
		if err != nil {
			return Config{}, fmt.Errorf("acquire config lock: %w", err)
		}
		defer tempLock.Release()
	}

	// 重新从磁盘加载最新候选配置
	var candidate Config
	loaded, err := SafeLoadConfig(s.configPath)
	if err == nil {
		candidate = loaded
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("read latest config before update: %w", err)
	}

	// 应用变更器
	if err := mutator(&candidate); err != nil {
		return Config{}, err
	}

	// 原子安全落地候选文件并执行父目录同步
	if err := SafeSaveConfig(s.configPath, candidate); err != nil {
		if errors.Is(err, ErrPersistUncertain) {
			// Rename already published the candidate. Reload so memory matches
			// disk; never reconstruct a previous authorization epoch.
			if loaded, loadErr := SafeLoadConfig(s.configPath); loadErr == nil {
				if cloned, cloneErr := cloneConfig(loaded); cloneErr == nil {
					s.cached = &cloned
					out, _ := cloneConfig(cloned)
					return out, fmt.Errorf("save updated config: %w", err)
				}
			}
		}
		return Config{}, fmt.Errorf("save updated config: %w", err)
	}

	cloned, err := cloneConfig(candidate)
	if err != nil {
		return Config{}, err
	}
	s.cached = &cloned
	return cloneConfig(cloned)
}
