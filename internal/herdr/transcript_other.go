//go:build !unix

package herdr

import "os"

// 受限读取依赖 openat + O_NOFOLLOW 的逐段解析，在非 Unix 平台上没有等价实现。
// 这里明确返回「不支持」，让上层给出 unsupported_transport，而不是退回不安全的读取方式。
func openRegularNoFollow(string, []string) (*os.File, error) {
	return nil, errTranscriptTransport
}

// fileIdentity 在非 Unix 平台上没有设备 / inode 可用；返回空串，游标仍然绑定
// agent、会话 id 与相对路径。
func fileIdentity(os.FileInfo) string { return "" }
