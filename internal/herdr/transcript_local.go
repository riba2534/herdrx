package herdr

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// 单次读取的字节上限。分页窗口是 256 KiB 加对齐回看，这里留一倍余量，
// 任何调用方都不可能一次要走更多。
const transcriptMaxReadBytes = 1 << 20

// Transcript 实现 `TranscriptEndpoint`：本机 Herdr 与 workbench 同机，直接按边界读文件。
//
// 本机是管理员专属接入（工厂层已强制 admin），共享主机故障边界，所以这里不做额外的
// owner 判定 —— owner 校验在 HTTP 层。
func (e *LocalEndpoint) Transcript(ctx context.Context, scope TranscriptScope, request TranscriptRequest) (TranscriptPage, error) {
	codec, err := transcriptTokens()
	if err != nil {
		return transcriptDegrade(ChatReasonInternalError, ""), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || !strings.HasPrefix(home, "/") {
		// 取不到 $HOME 就是 log_root_unavailable，不去猜任何相对路径。
		return transcriptDegrade(ChatReasonLogRootUnavailable, ""), nil
	}
	return readTranscript(ctx, &localTranscriptFS{home: filepath.Clean(home)}, codec, scope, request), nil
}

// localTranscriptFS 把两个固定日志根映射成受限文件访问。
//
// 日志根只有两个：`<HOME>/.claude/projects` 与 `<HOME>/.codex/sessions`。不使用通配，
// 也不接受调用方传入任何绝对路径。路径从 HOME 开始逐段解析：`$HOME` 是服务端自己解析的
// 部署锚点，它以下（`.claude`、`projects`、编码目录……）的每一段都拒绝符号链接。
type localTranscriptFS struct {
	home string
}

func (f *localTranscriptFS) rootParts(root transcriptRootKind) ([]string, error) {
	switch root {
	case transcriptRootClaude:
		return []string{".claude", "projects"}, nil
	case transcriptRootCodex:
		return []string{".codex", "sessions"}, nil
	case transcriptRootDSH:
		// 固定根，来自上游 `dshHomePath('sessions')`；自定义 DSH_HOME 不在本期支持范围。
		return []string{".dsh", "sessions"}, nil
	default:
		return nil, errTranscriptRoot
	}
}

// anchored 把「日志根 + 相对路径」拼成从 HOME 起算的绝对路径与路径分量。
func (f *localTranscriptFS) anchored(root transcriptRootKind, rel string) (string, []string, error) {
	rootParts, err := f.rootParts(root)
	if err != nil {
		return "", nil, err
	}
	relParts, err := transcriptRelParts(rel)
	if err != nil {
		return "", nil, err
	}
	parts := make([]string, 0, len(rootParts)+len(relParts))
	parts = append(parts, rootParts...)
	parts = append(parts, relParts...)
	return filepath.Join(f.home, filepath.FromSlash(strings.Join(parts, "/"))), parts, nil
}

func (f *localTranscriptFS) List(ctx context.Context, root transcriptRootKind, rel string, depth int) ([]transcriptEntry, error) {
	rootParts, err := f.rootParts(root)
	if err != nil {
		return nil, err
	}
	directory, parts, err := f.anchored(root, rel)
	if err != nil {
		return nil, err
	}
	// 先验日志根本身：`.claude/projects` / `.codex/sessions` 不存在或不是目录时，
	// 这是 log_root_unavailable（能力缺失），而不是「这个 cwd 没有候选」。
	if err := transcriptVerifyNoSymlink(f.home, rootParts); err != nil {
		if errors.Is(err, errTranscriptNotFound) {
			return nil, errTranscriptRoot
		}
		return nil, err
	}
	// 再验根以下的部分：日志根存在、但当前 cwd 的目录还没有，才是「候选为空」。
	if err := transcriptVerifyNoSymlink(filepath.Join(f.home, filepath.FromSlash(strings.Join(rootParts, "/"))), parts[len(rootParts):]); err != nil {
		return nil, err
	}
	entries := make([]transcriptEntry, 0, 16)
	if err := f.walk(ctx, directory, rel, depth, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (f *localTranscriptFS) walk(ctx context.Context, directory, rel string, depth int, entries *[]transcriptEntry) error {
	if depth < 1 {
		return nil
	}
	items, err := os.ReadDir(directory)
	if err != nil {
		return errTranscriptDenied
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(*entries) >= transcriptMaxListEntries {
			return nil
		}
		name := item.Name()
		child := name
		if rel != "" {
			child = rel + "/" + name
		}
		info, err := os.Lstat(filepath.Join(directory, name))
		if err != nil {
			continue
		}
		// 目录项本身是符号链接的一律标记出来，由上层拒绝：契约要求符号链接（含父目录）
		// 全部拒绝，不做「跟随但限制在根内」的宽容处理。
		entry := transcriptEntry{Rel: child, Modified: info.ModTime()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			entry.Link = true
		case info.IsDir():
			entry.Dir = true
		case info.Mode().IsRegular():
			entry.Size = info.Size()
		default:
			continue
		}
		*entries = append(*entries, entry)
		if entry.Dir && depth > 1 {
			if err := f.walk(ctx, filepath.Join(directory, name), child, depth-1, entries); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *localTranscriptFS) Read(ctx context.Context, root transcriptRootKind, specs []transcriptReadSpec) ([]transcriptReadResult, error) {
	rootParts, err := f.rootParts(root)
	if err != nil {
		return nil, err
	}
	results := make([]transcriptReadResult, 0, len(specs))
	for _, spec := range specs {
		results = append(results, f.readOne(ctx, rootParts, spec))
	}
	return results, nil
}

func (f *localTranscriptFS) readOne(ctx context.Context, rootParts []string, spec transcriptReadSpec) transcriptReadResult {
	relParts, err := transcriptRelParts(spec.Rel)
	if err != nil || len(relParts) == 0 {
		return transcriptReadResult{Err: errTranscriptDenied}
	}
	if err := ctx.Err(); err != nil {
		return transcriptReadResult{Err: errTranscriptDenied}
	}
	parts := make([]string, 0, len(rootParts)+len(relParts))
	parts = append(parts, rootParts...)
	parts = append(parts, relParts...)
	// 逐段 openat + O_NOFOLLOW：既有符号链接拒绝，也没有 test-then-open 的竞态窗口。
	file, err := openRegularNoFollow(f.home, parts)
	if err != nil {
		return transcriptReadResult{Err: err}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return transcriptReadResult{Err: errTranscriptDenied}
	}
	size := info.Size()
	result := transcriptReadResult{Exists: true, Size: size, Identity: fileIdentity(info)}
	if spec.Offset < 0 || spec.Length <= 0 || spec.Offset >= size {
		return result
	}
	length := spec.Length
	if remaining := size - spec.Offset; length > remaining {
		length = remaining
	}
	if length > transcriptMaxReadBytes {
		length = transcriptMaxReadBytes
	}
	buffer := make([]byte, length)
	read, err := file.ReadAt(buffer, spec.Offset)
	if err != nil && !errors.Is(err, io.EOF) && read == 0 {
		return transcriptReadResult{Exists: true, Size: size, Err: errTranscriptDenied}
	}
	result.Data = buffer[:read]
	return result
}

// transcriptVerifyNoSymlink 从 root 开始逐段 Lstat：parts 里任何一段不存在或本身是
// 符号链接都失败。root 自身只要求是目录 —— 它是服务端解析出来的部署锚点（$HOME），
// 不是用户可控的中间目录。
//
// 这是列表路径的守卫；正式读取另外走 openat + O_NOFOLLOW 的竞态安全路径。
func transcriptVerifyNoSymlink(root string, parts []string) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		// 日志根不存在：这是 log_root_unavailable，不是「候选为空」。
		return errTranscriptRoot
	}
	current := root
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return errTranscriptNotFound
			}
			return errTranscriptDenied
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errTranscriptDenied
		}
	}
	return nil
}
