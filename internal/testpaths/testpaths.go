// Package testpaths 为测试提供足够短的临时目录，用于放置 Unix domain socket。
//
// 为什么需要它：sockaddr_un.sun_path 有硬上限（macOS 104 字节含结尾 NUL，
// Linux 108）。而 t.TempDir() 会把测试名拼进路径，macOS 的 TMPDIR 本身又有
// 49 个字符（/var/folders/<2>/<28>/T/），两者相加经常越界，bind 会返回
// 「invalid argument」——看起来像功能坏了，其实只是路径太长。
//
// 这个包只被测试使用，放在 internal 下随源码走，不进入发布产物。
package testpaths

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// MaxUnixSocketPath 取两个平台的最小上限，让同一套断言到处成立。
const MaxUnixSocketPath = 103

// ShortTempDir 返回一个短到能放 Unix socket 的临时目录，测试结束自动清理。
func ShortTempDir(t *testing.T) string {
	t.Helper()
	root := os.TempDir()
	if runtime.GOOS == "darwin" {
		// /tmp（→ /private/tmp）比 per-user 的 TMPDIR 短得多。
		if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
			root = "/tmp"
		}
	}
	dir, err := os.MkdirTemp(root, "hx")
	if err != nil {
		t.Fatalf("create short temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// RequireSocketPath 在路径超限时立即失败并说明原因，而不是把一个语义不明的
// bind 错误留给调用方。
func RequireSocketPath(t *testing.T, path string) string {
	t.Helper()
	if len(path) > MaxUnixSocketPath {
		t.Fatalf("unix socket path is %d bytes, over the %d-byte limit: %s", len(path), MaxUnixSocketPath, path)
	}
	return path
}

// ListenShortUnix 在短路径下监听一个 Unix socket。
func ListenShortUnix(t *testing.T, dir, name string) (net.Listener, string) {
	t.Helper()
	path := RequireSocketPath(t, filepath.Join(dir, name))
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen unix socket at %s: %v", path, err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener, path
}
