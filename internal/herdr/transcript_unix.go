//go:build unix

package herdr

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// openRegularNoFollow 用 openat + O_NOFOLLOW **逐段**打开，最后一段必须是普通文件。
//
// 为什么不先 Lstat 再 os.Open：那是 test-then-open，中间存在竞态窗口，攻击者可以在
// 窗口内把某一段换成指向根外的符号链接。openat 的每一段都带 O_NOFOLLOW，遇到符号链接
// 直接 ELOOP，整条链在同一个目录句柄下解析，窗口不存在。
func openRegularNoFollow(root string, parts []string) (*os.File, error) {
	if len(parts) == 0 {
		return nil, errTranscriptDenied
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, transcriptOpenError(err, true)
	}
	for _, part := range parts[:len(parts)-1] {
		child, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return nil, transcriptOpenError(err, false)
		}
		fd = child
	}
	file, err := unix.Openat(fd, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	unix.Close(fd)
	if err != nil {
		return nil, transcriptOpenError(err, false)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(file, &stat); err != nil {
		unix.Close(file)
		return nil, errTranscriptDenied
	}
	// 目录、设备、FIFO、socket 全部拒绝：只读普通文件。
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(file)
		return nil, errTranscriptDenied
	}
	return os.NewFile(uintptr(file), filepath.Join(root, filepath.Join(parts...))), nil
}

// fileIdentity 返回文件的身份串（设备 + inode）。
//
// 为什么不用内容哈希：会话日志是**追加**写的，小文件一增长，文件头的内容哈希就变了，
// 正常的增量轮询会被误判成「文件被换掉」而触发 reset。设备 + inode 在追加时稳定，
// 在文件被替换（新建、改名覆盖）时改变，正好对应契约里游标要绑定的「文件身份」。
// 取不到时返回空串：不影响会话绑定（游标同时绑定了 agent、会话 id 与相对路径）。
func fileIdentity(info os.FileInfo) string {
	if info == nil {
		return ""
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return strconv.FormatUint(uint64(stat.Dev), 16) + ":" + strconv.FormatUint(uint64(stat.Ino), 16)
}

func transcriptOpenError(err error, root bool) error {
	switch {
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR):
		return errTranscriptDenied
	case errors.Is(err, unix.ENOENT):
		if root {
			return errTranscriptRoot
		}
		return errTranscriptNotFound
	default:
		return errTranscriptDenied
	}
}
