package herdr

import (
	"context"
	"errors"
	"strings"

	"github.com/riba2534/herdrx/internal/agentlog"
)

// Gemini CLI 的项目临时目录有两代（见契约 §8.4）：
//   - 旧版 `~/.gemini/tmp/<sha256(项目根)>/`；
//   - 新版 `~/.gemini/tmp/<slug>/`，slug 由 `~/.gemini/projects.json` 注册表分配，目录里的
//     `.project_root` 记着项目根路径（config/projectRegistry.ts）。
//
// 注册表可能过期，不作信任依据：slug 目录只有在 `.project_root` 与 pane cwd 精确相等时才算
// 这个 pane 的。两代目录都还要求会话头的 projectHash 等于 sha256(pane cwd)。

const geminiProjectMarker = ".project_root"

// geminiProjectDirs 返回属于 pane cwd 的项目目录名：旧版 hash 目录直接按名字认，新版 slug
// 目录按 `.project_root` 认。标记文件按 64 个一批读。
func geminiProjectDirs(ctx context.Context, files transcriptFS, cwd string) ([]string, error) {
	top, err := files.List(ctx, transcriptRootGemini, "", 1)
	if err != nil {
		return nil, err
	}
	hashes := map[string]bool{}
	for _, spelling := range cwdSpellings(files, cwd) {
		hashes[agentlog.GeminiProjectHash(spelling)] = true
	}
	var dirs, slugs []string
	for _, entry := range top {
		if !entry.Dir || entry.Link || strings.Contains(entry.Rel, "/") {
			continue
		}
		if hashes[entry.Rel] {
			dirs = append(dirs, entry.Rel)
			continue
		}
		if agentlog.GeminiProjectSlug(entry.Rel) {
			slugs = append(slugs, entry.Rel)
		}
	}
	marked, err := geminiMarkedDirs(ctx, files, slugs, cwd)
	if err != nil {
		return nil, err
	}
	return append(dirs, marked...), nil
}

// geminiMarkedDirs 读各 slug 目录的 `.project_root`，留下内容与 pane cwd 相等的那些。
func geminiMarkedDirs(ctx context.Context, files transcriptFS, slugs []string, cwd string) ([]string, error) {
	spellings := cwdSpellings(files, cwd)
	var owned []string
	for offset := 0; offset < len(slugs); offset += transcriptReadBatch {
		batch := slugs[offset:min(offset+transcriptReadBatch, len(slugs))]
		specs := make([]transcriptReadSpec, len(batch))
		for index, slug := range batch {
			specs[index] = transcriptReadSpec{Rel: slug + "/" + geminiProjectMarker, Length: 4096}
		}
		results, err := files.Read(ctx, transcriptRootGemini, specs)
		if err != nil {
			return nil, err
		}
		for index, result := range results {
			if index < len(batch) && result.Err == nil && result.Exists && geminiMarkerNames(result.Data, spellings) {
				owned = append(owned, batch[index])
			}
		}
	}
	return owned, nil
}

func geminiMarkerNames(data []byte, spellings []string) bool {
	root := strings.TrimRight(string(data), "\r\n")
	if !strings.HasPrefix(root, "/") {
		return false
	}
	for _, spelling := range spellings {
		if root == spelling {
			return true
		}
	}
	return false
}

// listGeminiProjects 列出属于 pane cwd 的项目目录里的会话（含 `chats/<parentId>/` 子目录）。
func listGeminiProjects(ctx context.Context, files transcriptFS, cwd string) ([]transcriptEntry, error) {
	dirs, err := geminiProjectDirs(ctx, files, cwd)
	if err != nil {
		return nil, err
	}
	var entries []transcriptEntry
	for _, dir := range dirs {
		listed, err := files.List(ctx, transcriptRootGemini, dir+"/chats", 2)
		if errors.Is(err, errTranscriptNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		entries = append(entries, listed...)
	}
	return entries, nil
}

// geminiHeaderOwned 报告会话头的 projectHash 是否是 sha256(pane cwd)（家目录两种写法各算一次）。
func geminiHeaderOwned(files transcriptFS, projectHash, cwd string) bool {
	if projectHash == "" {
		return false
	}
	for _, spelling := range cwdSpellings(files, cwd) {
		if agentlog.GeminiProjectHash(spelling) == projectHash {
			return true
		}
	}
	return false
}

// geminiOwned 是读取已绑定会话时的完整核对：会话头 projectHash 对上，且所在目录是 pane cwd
// 的项目目录（旧版 hash 目录，或 `.project_root` 指向 pane cwd 的 slug 目录）。
func geminiOwned(ctx context.Context, files transcriptFS, projectHash, rel, cwd string) bool {
	if !geminiHeaderOwned(files, projectHash, cwd) {
		return false
	}
	dir, _, ok := strings.Cut(rel, "/")
	if !ok {
		return false
	}
	if dir == projectHash {
		return true
	}
	marked, err := geminiMarkedDirs(ctx, files, []string{dir}, cwd)
	return err == nil && len(marked) == 1
}
