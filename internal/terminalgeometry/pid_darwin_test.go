package terminalgeometry

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func TestReadPIDPreservesExistingTerminal(t *testing.T) {
	command := exec.Command("sleep", "30")
	size := &pty.Winsize{Rows: 47, Cols: 65, X: 520, Y: 752}
	terminal, err := pty.StartWithSize(command, size)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait(); _ = terminal.Close() })
	// 重复读：确认这条路径只观察、不改变尺寸，也不依赖首次调用的副作用。
	for i := 0; i < 3; i++ {
		geometry, err := ReadPID(command.Process.Pid)
		if err != nil || geometry != (Geometry{Cols: 65, Rows: 47, CellWidthPx: 8, CellHeightPx: 16}) {
			t.Fatalf("geometry=%+v err=%v", geometry, err)
		}
	}
	actual, err := unix.IoctlGetWinsize(int(terminal.Fd()), unix.TIOCGWINSZ)
	if err != nil || actual.Row != 47 || actual.Col != 65 || actual.Xpixel != 520 || actual.Ypixel != 752 {
		t.Fatalf("size changed: %+v %v", actual, err)
	}
	if _, err := ReadPID(0); err == nil {
		t.Fatal("invalid pid accepted")
	}
	if _, err := ReadPID(1073741824); err == nil {
		t.Fatal("missing pid accepted")
	}
}

// 没有控制终端的进程必须明确报错，而不是回退到调用者自己的终端尺寸。
// NODEV 也会因为候选路径不存在而失败，所以这里断言的是具体原因：错误必须
// 指出「没有控制终端」，不能让用户去排查一个不存在的 /dev 项。
func TestReadPIDWithoutControllingTerminal(t *testing.T) {
	command := exec.Command("sleep", "30")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	geometry, err := ReadPID(command.Process.Pid)
	if err == nil {
		t.Fatalf("accepted a process with no controlling terminal: %+v", geometry)
	}
	if !strings.Contains(err.Error(), "no controlling terminal") {
		t.Fatalf("error does not name the real cause: %v", err)
	}
}

// 设备号到 /dev 路径不是双射（/dev/ttys0 与 /dev/ttys048 的 minor 同为 48）。
// 候选列表必须覆盖两种命名，实际选择由 fstat 核对 st_rdev 决定。
func TestTerminalPathsCoverBothDeviceNamings(t *testing.T) {
	paths := terminalPaths(unix.Mkdev(16, 48))
	want := map[string]bool{"/dev/ttys048": false, "/dev/ttys48": false}
	for _, path := range paths {
		if _, ok := want[path]; !ok {
			t.Fatalf("unexpected candidate %q", path)
		}
		want[path] = true
	}
	for path, found := range want {
		if !found {
			t.Fatalf("missing candidate %q", path)
		}
	}
}

// 校验的是打开后的 fd，而不是打开前的路径：设备号对不上就不能返回尺寸。
func TestOpenControllingTerminalRejectsMismatchedDevice(t *testing.T) {
	command := exec.Command("sleep", "30")
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 47, Cols: 65})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait(); _ = terminal.Close() })
	var stat unix.Stat_t
	if err := unix.Fstat(int(terminal.Fd()), &stat); err != nil {
		t.Fatal(err)
	}
	// 用一个确实存在、但不是任何 tty 的字符设备号（/dev/null）来探测：
	// 它的候选路径 /dev/ttysNNN 要么不存在，要么 rdev 不符，都必须失败。
	var null unix.Stat_t
	if err := unix.Stat("/dev/null", &null); err != nil {
		t.Fatal(err)
	}
	if fd, err := openControllingTerminal(uint64(null.Rdev)); err == nil {
		_ = unix.Close(fd)
		t.Fatal("accepted a device that is not the controlling terminal")
	}
}
