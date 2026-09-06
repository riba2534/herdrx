package terminalgeometry

import (
	"fmt"
	"golang.org/x/sys/unix"
)

// ReadPID only reads the kernel size of the process's existing stdin terminal.
// O_NOCTTY prevents this helper from acquiring a controlling terminal.
func ReadPID(pid int) (Geometry, error) {
	if pid <= 1 || pid > 1<<30 {
		return Geometry{}, fmt.Errorf("invalid terminal process id")
	}
	fd, err := unix.Open(fmt.Sprintf("/proc/%d/fd/0", pid), unix.O_RDONLY|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return Geometry{}, fmt.Errorf("open terminal size source: %w", err)
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return Geometry{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFCHR {
		return Geometry{}, fmt.Errorf("process stdin is not a terminal")
	}
	size, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return Geometry{}, fmt.Errorf("read terminal size: %w", err)
	}
	return FromWinsize(size.Row, size.Col, size.Xpixel, size.Ypixel)
}
