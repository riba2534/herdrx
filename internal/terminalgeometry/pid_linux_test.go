package terminalgeometry

import (
	"os/exec"
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
