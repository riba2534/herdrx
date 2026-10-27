package herdr

import (
	"context"
	"strings"
	"unicode"

	"github.com/riba2534/herdrx/internal/agentlog"
)

// 这两个前缀只出现在服务端封进令牌的相对路径里，用来记住 rollout 文件在哪一个根。
// 读取前会剥掉，不会当成磁盘上的目录。
const (
	rolloutCodexMarker = "codex-root"
	rolloutTraexMarker = "trae-root"
)

func rolloutRelAllowed(parts []string) bool {
	if len(parts) == 0 {
		return false
	}
	if parts[0] == rolloutCodexMarker || parts[0] == rolloutTraexMarker {
		parts = parts[1:]
	}
	if len(parts) == 0 || len(parts) > transcriptMaxCodexDepth+1 {
		return false
	}
	return agentlog.CodexSessionFile(parts[len(parts)-1])
}

// rolloutStorage 把令牌里的相对路径还原成日志根和磁盘上的相对路径。
func rolloutStorage(agent, rel string) (transcriptRootKind, string) {
	switch {
	case strings.HasPrefix(rel, rolloutTraexMarker+"/"):
		return transcriptRootTraex, strings.TrimPrefix(rel, rolloutTraexMarker+"/")
	case strings.HasPrefix(rel, rolloutCodexMarker+"/"):
		return transcriptRootCodex, strings.TrimPrefix(rel, rolloutCodexMarker+"/")
	default:
		return transcriptRootFor(agent), rel
	}
}

func rolloutTokenRel(agent string, root transcriptRootKind, rel string) string {
	if root == transcriptRootFor(agent) {
		return rel
	}
	switch root {
	case transcriptRootTraex:
		return rolloutTraexMarker + "/" + rel
	case transcriptRootCodex:
		return rolloutCodexMarker + "/" + rel
	default:
		return rel
	}
}

// verifyRolloutID 用会话 id 找唯一的 rollout。Codex 的 hook 把 traex 也报成 codex，
// 所以先找 ~/.codex/sessions，再找 ~/.trae/cli/sessions。pane 本身叫 traex 时顺序相反。
// 只认文件名里的会话 id，再用 session_meta 对上 id 和 cwd。多于一个匹配就不猜。
func verifyRolloutID(ctx context.Context, files transcriptFS, codec transcriptCodec, scope TranscriptScope, request TranscriptRequest, sessionID string) (TranscriptPage, bool) {
	roots := []transcriptRootKind{transcriptRootCodex, transcriptRootTraex}
	if scope.Agent == agentlog.AgentTraex {
		roots = []transcriptRootKind{transcriptRootTraex, transcriptRootCodex}
	}
	for _, root := range roots {
		rel, ok := findConfirmedRollout(ctx, files, root, scope.CWD, sessionID)
		if !ok {
			continue
		}
		return finishVerified(ctx, files, codec, scope, request, sessionID, rolloutTokenRel(scope.Agent, root, rel))
	}
	return TranscriptPage{}, false
}

func findConfirmedRollout(ctx context.Context, files transcriptFS, root transcriptRootKind, cwd, sessionID string) (string, bool) {
	var matches []string
	truncated := false
	walkRollouts(ctx, files, root, "", 0, cwd, sessionID, &matches, &truncated)
	if truncated || len(matches) != 1 {
		return "", false
	}
	return matches[0], true
}

func walkRollouts(ctx context.Context, files transcriptFS, root transcriptRootKind, rel string, depth int, cwd, sessionID string, matches *[]string, truncated *bool) {
	if ctx.Err() != nil || len(*matches) > 1 {
		return
	}
	entries, err := files.List(ctx, root, rel, 1)
	if err != nil {
		return
	}
	if len(entries) >= transcriptMaxListEntries {
		*truncated = true
	}
	for _, entry := range entries {
		if entry.Link {
			continue
		}
		if entry.Dir {
			if depth >= 3 || !rolloutDateDir(baseName(entry.Rel), depth) {
				continue
			}
			walkRollouts(ctx, files, root, entry.Rel, depth+1, cwd, sessionID, matches, truncated)
			continue
		}
		if !agentlog.RolloutMatchesSession(baseName(entry.Rel), sessionID) {
			continue
		}
		shapes, reads, err := transcriptShapes(ctx, files, root, agentlog.AgentCodex, []string{entry.Rel})
		if err != nil || len(reads) == 0 {
			continue
		}
		read := reads[0]
		if read.Err != nil || !read.Exists || !hasCompleteLine(read.Data) {
			continue
		}
		shape := shapes[0]
		if shape.SessionID != sessionID || shape.CWD != cwd {
			continue
		}
		*matches = append(*matches, entry.Rel)
	}
}

func rolloutDateDir(name string, depth int) bool {
	want := 2
	if depth == 0 {
		want = 4
	}
	if len(name) != want {
		return false
	}
	for _, character := range name {
		if !unicode.IsDigit(character) {
			return false
		}
	}
	return true
}
