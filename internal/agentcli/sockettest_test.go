package agentcli

import (
	"net"
	"testing"

	"github.com/riba2534/herdrx/internal/testpaths"
)

// 这些是 internal/testpaths 的包内别名。Unix socket 路径长度的处理规则集中在
// 那个包里（macOS 的 TMPDIR 很长，t.TempDir() 拼上测试名容易越过 103 字节上限，
// bind 会以「invalid argument」失败），这里只做转发，避免各包各写一份。

const maxUnixSocketPath = testpaths.MaxUnixSocketPath

func shortTempDir(t *testing.T) string { t.Helper(); return testpaths.ShortTempDir(t) }

func requireSocketPath(t *testing.T, path string) string {
	t.Helper()
	return testpaths.RequireSocketPath(t, path)
}

func listenShortUnix(t *testing.T, dir, name string) (net.Listener, string) {
	t.Helper()
	return testpaths.ListenShortUnix(t, dir, name)
}

// TestShortTempDirFitsUnixSocket 自检：夹具给出的目录必须真的能放下 socket，
// 否则后面所有依赖它的测试都会以难以理解的方式失败。
func TestShortTempDirFitsUnixSocket(t *testing.T) {
	dir := shortTempDir(t)
	_, path := listenShortUnix(t, dir, "control.sock")
	if len(path) > maxUnixSocketPath {
		t.Fatalf("short temp dir still produced an over-long path: %s", path)
	}
}
