//go:build unix

package agentlog

import (
	"errors"
	"os"
	"strconv"
	"syscall"
)

// agyFDDirs 是列 fd 号的目录，取第一个能读的。测试可以只留 /dev/fd 走 macOS 的路径。
var agyFDDirs = []string{"/proc/self/fd", "/dev/fd"}

// agyOpenFDs 返回本进程当前的 fd 表：fd 号到它指向的文件。
//
// 目录只用来列出 fd 号（Linux 的 /proc/self/fd、macOS 的 /dev/fd），文件身份一律用
// fstat(fd) 取：macOS 的 /dev/fd 是 fdesc 文件系统，对 `/dev/fd/N` 做 stat 不保证得到目标
// 文件的 dev/ino；fstat 自己进程的 fd 号在两个平台上语义相同。列目录时打开的那个目录 fd
// 也在表里，它指向 fd 目录本身，不会与会话库同 inode。
func agyOpenFDs() (map[string]agyFD, error) {
	for _, base := range agyFDDirs {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		fds := make(map[string]agyFD, len(entries))
		for _, entry := range entries {
			fd, err := strconv.Atoi(entry.Name())
			if err != nil {
				continue
			}
			var stat syscall.Stat_t
			if syscall.Fstat(fd, &stat) == nil {
				fds[entry.Name()] = agyFD{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}
			}
		}
		return fds, nil
	}
	return nil, errors.New("fd table unavailable")
}

func agyFileID(info os.FileInfo) agyFD {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return agyFD{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}
	}
	return agyFD{}
}
