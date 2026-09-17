package terminalgeometry

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
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
	const helperEnv = "HERDRX_TEST_CONTROLLING_TERMINAL"
	if os.Getenv(helperEnv) != "1" {
		// 即使测试运行器没有终端，也让真正执行断言的父进程拥有控制终端。
		// 这样删掉下面的 Setsid 时，headless CI 同样会发现继承终端的回归。
		command := exec.Command(os.Args[0], "-test.run=^TestReadPIDWithoutControllingTerminal$", "-test.count=1", "-test.timeout=15s")
		command.Env = append(os.Environ(), helperEnv+"=1")
		terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 47, Cols: 65})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait(); _ = terminal.Close() })
		// PTY 在子进程退出后可能用 EIO 表示流结束；退出状态决定测试是否通过。
		output, readErr := io.ReadAll(terminal)
		if err := command.Wait(); err != nil {
			t.Fatalf("test with controlling terminal: %v\n%s", err, output)
		}
		if readErr != nil && !errors.Is(readErr, syscall.EIO) {
			t.Fatalf("read test output: %v", readErr)
		}
		return
	}
	if geometry, err := ReadPID(os.Getpid()); err != nil || geometry != (Geometry{Cols: 65, Rows: 47}) {
		t.Fatalf("parent controlling terminal: geometry=%+v err=%v", geometry, err)
	}
	command := exec.Command("sleep", "30")
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
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

func TestTerminalPathUsesPTMXNaming(t *testing.T) {
	for minor, want := range map[uint32]string{
		0: "/dev/ttys000", 9: "/dev/ttys009", 48: "/dev/ttys048", 999: "/dev/ttys999",
	} {
		if path := terminalPath(unix.Mkdev(16, minor)); path != want {
			t.Errorf("minor %d: path=%q want=%q", minor, path, want)
		}
	}
}

// 校验的是打开后的 fd，而不是打开前的路径：设备号对不上就不能返回尺寸。
func TestOpenControllingTerminalRejectsMismatchedDevice(t *testing.T) {
	terminal, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close(); _ = terminal.Close() })
	var stat unix.Stat_t
	if err := unix.Fstat(int(slave.Fd()), &stat); err != nil {
		t.Fatal(err)
	}
	device := uint64(uint32(stat.Rdev))
	// 先确认候选可以打开，再只改 major：路径不变，负例必须经过 fd 校验。
	fd, err := openControllingTerminal(device)
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)
	wrongDevice := unix.Mkdev(unix.Major(device)^1, unix.Minor(device))
	if fd, err := openControllingTerminal(wrongDevice); err == nil {
		_ = unix.Close(fd)
		t.Fatal("accepted a device that is not the controlling terminal")
	} else if !strings.Contains(err.Error(), "not the controlling terminal") {
		t.Fatalf("did not reach device validation: %v", err)
	}
}
