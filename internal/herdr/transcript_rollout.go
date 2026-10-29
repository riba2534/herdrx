package herdr

import (
	"strings"

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
	if _, rollout := rolloutSiblingRoot(agent); !rollout {
		return transcriptRootFor(agent), rel
	}
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

// rolloutSiblingRoot 是另一个 rollout 根。Codex 的 hook 把 traex 也报成 codex，
// 所以 codex pane 的证据可能指向 traex 根，反之亦然。
func rolloutSiblingRoot(agent string) (transcriptRootKind, bool) {
	switch agent {
	case agentlog.AgentCodex:
		return transcriptRootTraex, true
	case agentlog.AgentTraex:
		return transcriptRootCodex, true
	}
	return "", false
}
