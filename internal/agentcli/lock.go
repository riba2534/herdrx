package agentcli

import (
	"path/filepath"
)

// ProcessLock 封装跨进程排他锁，语义与 ConfigLock 完全一致（固定 inode，严格不 unlink）
type ProcessLock struct {
	lock *ConfigLock
}

// AcquireProcessLock 尝试获取指定文件路径的排他进程锁（非阻塞模式，杜绝 symlink，保持固定 inode）
func AcquireProcessLock(path string) (*ProcessLock, error) {
	cleanPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	lock, err := acquireConfigLockInternal(cleanPath, true)
	if err != nil {
		return nil, err
	}
	return &ProcessLock{lock: lock}, nil
}

func (l *ProcessLock) Release() error {
	if l == nil || l.lock == nil {
		return nil
	}
	err := l.lock.Release()
	l.lock = nil
	return err
}
