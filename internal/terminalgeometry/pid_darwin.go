package terminalgeometry

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// 为什么不像 Linux 那样直接读 /proc/<pid>/fd/0：macOS 没有 procfs，也没有按 fd
// 打开另一个进程终端的接口。可用的等价来源是内核进程表里的控制终端设备号，
// 再通过它在 /dev 下定位同一个 tty 字符设备。
//
// Herdr 使用现代 ptmx slave（/dev/ttysNNN）。minor 不能单独标识设备，因此
// 打开后必须 fstat 核对完整 st_rdev 和字符设备类型；不匹配就报错。
func ReadPID(pid int) (Geometry, error) {
	if pid <= 1 || pid > 1<<30 {
		return Geometry{}, fmt.Errorf("invalid terminal process id")
	}
	proc, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return Geometry{}, fmt.Errorf("read terminal process: %w", err)
	}
	// NODEV(-1) 表示进程没有控制终端。
	if proc.Eproc.Tdev == -1 {
		return Geometry{}, fmt.Errorf("process has no controlling terminal")
	}
	device := uint64(uint32(proc.Eproc.Tdev))
	fd, err := openControllingTerminal(device)
	if err != nil {
		return Geometry{}, err
	}
	defer unix.Close(fd)
	size, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return Geometry{}, fmt.Errorf("read terminal size: %w", err)
	}
	return FromWinsize(size.Row, size.Col, size.Xpixel, size.Ypixel)
}

// openControllingTerminal 打开 Herdr 的 ptmx slave，并核对打开到的确实是它。
func openControllingTerminal(device uint64) (int, error) {
	fd, err := unix.Open(terminalPath(device), unix.O_RDONLY|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("open terminal size source: %w", err)
	}
	var stat unix.Stat_t
	// 以打开后的 fd 为准，不能只信任路径推导或打开前的 stat。
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return 0, fmt.Errorf("inspect terminal size source: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFCHR || uint64(stat.Rdev) != device {
		_ = unix.Close(fd)
		return 0, fmt.Errorf("terminal size source is not the controlling terminal")
	}
	return fd, nil
}

// terminalPath 对应 Darwin ptmx 的 ttys%03d 命名，不支持旧式 BSD PTY。
func terminalPath(device uint64) string {
	return fmt.Sprintf("/dev/ttys%03d", unix.Minor(device))
}
