package herdr

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/riba2534/herdrx/internal/agentlog"
)

// transcriptHome 能报告账号家目录的两种写法。logical 是 $HOME 原文，
// resolved 是只对这一层做 realpath 的结果。两者相同就没有可替换的前缀。
type transcriptHome interface {
	HomePrefixes() (logical, resolved string)
}

func homePrefixes(files transcriptFS) (string, string) {
	homes, ok := files.(transcriptHome)
	if !ok {
		return "", ""
	}
	logical, resolved := homes.HomePrefixes()
	logical = filepath.Clean(logical)
	resolved = filepath.Clean(resolved)
	if logical == "." {
		logical = ""
	}
	if resolved == "." {
		resolved = ""
	}
	return logical, resolved
}

func (f *localTranscriptFS) HomePrefixes() (string, string) {
	logical := filepath.Clean(f.home)
	resolved, err := filepath.EvalSymlinks(logical)
	if err != nil || !strings.HasPrefix(resolved, "/") {
		return logical, logical
	}
	return logical, filepath.Clean(resolved)
}

// resolvedHome 是打开日志根时使用的目录。只解析家目录本身；其下每一段仍拒绝符号链接。
func (f *localTranscriptFS) resolvedHome() string {
	_, resolved := f.HomePrefixes()
	if resolved == "" {
		return filepath.Clean(f.home)
	}
	return resolved
}

func (f *sshTranscriptFS) HomePrefixes() (string, string) {
	f.endpoint.transcriptMu.Lock()
	defer f.endpoint.transcriptMu.Unlock()
	return f.endpoint.transcriptHome, f.endpoint.transcriptHomeReal
}

// cwdSpellings 是 pane cwd 的原文，以及把家目录前缀换成另一种写法之后的路径。
// 只替换完整的家目录前缀，不改写其它路径。
func cwdSpellings(files transcriptFS, cwd string) []string {
	spellings := []string{cwd}
	alt := otherHomeCWD(files, cwd)
	if alt != "" && alt != cwd {
		spellings = append(spellings, alt)
	}
	return spellings
}

func otherHomeCWD(files transcriptFS, cwd string) string {
	logical, resolved := homePrefixes(files)
	if logical == "" || resolved == "" || logical == resolved {
		return cwd
	}
	switch {
	case cwd == logical || strings.HasPrefix(cwd, logical+"/"):
		return resolved + strings.TrimPrefix(cwd, logical)
	case cwd == resolved || strings.HasPrefix(cwd, resolved+"/"):
		return logical + strings.TrimPrefix(cwd, resolved)
	default:
		return cwd
	}
}

// cwdMatch 在精确相等之外，接受家目录符号链接的两种写法。
func cwdMatch(files transcriptFS, record, pane string) bool {
	if record == pane {
		return true
	}
	return otherHomeCWD(files, record) == pane || otherHomeCWD(files, pane) == record
}

// listCWDProjects 按 pane cwd 的每一种家目录写法列出编码目录并合并：同一个 cwd 用两种写法
// 启动过的 Agent 会各写一个目录。都不存在时返回 errTranscriptNotFound；权限或符号链接错误
// 立即返回，不会改到另一个目录上。
func listCWDProjects(ctx context.Context, files transcriptFS, root transcriptRootKind, cwd string, project func(string) (string, bool), depth int) ([]transcriptEntry, error) {
	var entries []transcriptEntry
	found := false
	listed := map[string]bool{}
	for _, spelling := range cwdSpellings(files, cwd) {
		rel, ok := project(spelling)
		if !ok || rel == "" || listed[rel] {
			continue
		}
		listed[rel] = true
		batch, err := files.List(ctx, root, rel, depth)
		if errors.Is(err, errTranscriptNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		found = true
		entries = append(entries, batch...)
	}
	if !found {
		return nil, errTranscriptNotFound
	}
	return entries, nil
}

// transcriptRelAllowedAny 接受按 pane cwd 编码的相对路径，也接受家目录另一种写法编码出来的那一个。
func transcriptRelAllowedAny(files transcriptFS, agent, cwd, sessionID, rel string) bool {
	for _, spelling := range cwdSpellings(files, cwd) {
		if transcriptRelAllowed(agent, spelling, sessionID, rel) {
			return true
		}
	}
	return false
}

func claudeProject(cwd string) (string, bool) {
	if cwd == "" {
		return "", false
	}
	return agentlog.ClaudeProjectDir(cwd), true
}

func piProject(cwd string) (string, bool) {
	if cwd == "" {
		return "", false
	}
	return agentlog.PiProjectDir(cwd), true
}

func grokProject(cwd string) (string, bool) {
	if !strings.HasPrefix(cwd, "/") {
		return "", false
	}
	return agentlog.GrokProjectDir(cwd), true
}
