package terminalgeometry

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// 为什么不像 Linux 那样直接读 /proc/<pid>/fd/0：macOS 没有 procfs，也没有按 fd
// 打开另一个进程终端的接口。可用的等价来源是内核进程表里的控制终端设备号，
// 再通过它在 /dev 下定位同一个 tty 字符设备。
//
// 设备号到路径不是双射：/dev/ttys0 和 /dev/ttys048 的 minor 都是 48，按名字
// 猜路径可能打开另一个用户的终端，而且猜路径和打开之间存在 TOCTOU。因此打开
// 之后必须 fstat 回读 st_rdev，确认拿到的正是 sysctl 报告的那个设备；不一致
// 就报错，不退而求其次。
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

// openControllingTerminal 打开设备号为 device 的 tty，并核对打开到的确实是它。
// 候选路径覆盖 /dev 下两种 slave 命名（ttysNNN 与 ttysX 短名），逐个尝试。
func openControllingTerminal(device uint64) (int, error) {
	for _, path := range terminalPaths(device) {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		var stat unix.Stat_t
		// 打开的可能不是刚才 stat 的那个文件，也可能根本不是字符设备；
		// 以打开后的 fd 为准做全部校验。
		if unix.Fstat(fd, &stat) == nil &&
			stat.Mode&unix.S_IFMT == unix.S_IFCHR &&
			uint64(stat.Rdev) == device {
			return fd, nil
		}
		_ = unix.Close(fd)
	}
	return 0, fmt.Errorf("open terminal size source: no /dev entry for the controlling terminal")
}

// terminalPaths 给出设备号对应的候选 /dev 路径。macOS 的 pty slave 通常是
// /dev/ttysNNN（三位十进制 minor），历史上也存在 /dev/ttysX 形式的短名，两者
// 可能落到同一个 minor，所以返回全部候选交由 fstat 校验挑选。
func terminalPaths(device uint64) []string {
	minor := unix.Minor(device)
	return []string{
		fmt.Sprintf("/dev/ttys%03d", minor),
		fmt.Sprintf("/dev/ttys%d", minor),
	}
}
