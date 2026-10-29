package herdr

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/riba2534/herdrx/internal/agentlog"
)

// cursorWorkspace 读取 transcript 旁边的 `.workspace-trusted`。
// 找不到或路径不是绝对路径时返回空串：调用方不得改用项目目录名。
func cursorWorkspace(ctx context.Context, files transcriptFS, rel string) (string, error) {
	sidecar := agentlog.CursorWorkspaceRel(rel)
	if sidecar == "" {
		return "", nil
	}
	result, err := transcriptReadOne(ctx, files, transcriptRootCursor, sidecar, 0, 4096)
	if err != nil {
		return "", err
	}
	if result.Err != nil {
		if errors.Is(result.Err, errTranscriptNotFound) || errors.Is(result.Err, errTranscriptRoot) {
			return "", nil
		}
		return "", result.Err
	}
	if !result.Exists {
		return "", nil
	}
	return agentlog.CursorWorkspacePath(result.Data), nil
}

// transcriptReadBatch 是一次 Read 请求最多带的区间数，与远端读取器的上限一致。
const transcriptReadBatch = 64

// collectCursorCandidates 只在 workspacePath 与 pane cwd 相同的项目里列 transcript。
// 各项目的 `.workspace-trusted` 按 64 个一批读取；同一项目下的 transcript 复用这次的结果，
// 不再逐个文件重读。
func collectCursorCandidates(ctx context.Context, files transcriptFS, codec transcriptCodec, cwd string) ([]agentlog.Candidate, bool, error) {
	projects, err := files.List(ctx, transcriptRootCursor, "", 1)
	if err != nil {
		return nil, false, err
	}
	capped := len(projects) >= transcriptMaxListEntries
	names := make([]string, 0, len(projects))
	for _, project := range projects {
		if project.Dir && !project.Link && !strings.Contains(project.Rel, "/") {
			names = append(names, project.Rel)
		}
	}
	workspaces, err := cursorProjectWorkspaces(ctx, files, names)
	if err != nil {
		return nil, false, err
	}
	entries := make([]transcriptEntry, 0)
	for _, project := range names {
		if !cwdMatch(files, workspaces[project], cwd) {
			continue
		}
		listed, err := files.List(ctx, transcriptRootCursor, project+"/agent-transcripts", 2)
		if errors.Is(err, errTranscriptNotFound) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if len(listed) >= transcriptMaxListEntries {
			capped = true
		}
		entries = append(entries, listed...)
	}
	candidates := make([]agentlog.Candidate, 0, transcriptMaxCandidates)
	for _, entry := range entries {
		if entry.Dir || entry.Link || !agentlog.CursorTranscriptRel(entry.Rel) {
			continue
		}
		sessionID := agentlog.CursorSessionID(entry.Rel)
		id, err := codec.seal(agentlog.AgentCursor, sessionID, entry.Rel)
		if err != nil {
			return nil, false, err
		}
		candidates = append(candidates, agentlog.Candidate{
			ID: id, Agent: agentlog.AgentCursor, SessionID: sessionID,
			UpdatedAt: entry.Modified.UTC().Format(time.RFC3339),
		})
		if len(candidates) == transcriptMaxCandidates {
			return candidates, false, nil
		}
	}
	if len(candidates) == 0 && capped {
		return nil, false, errTranscriptLimit
	}
	return candidates, false, nil
}

// cursorProjectWorkspaces 批量读取各项目的 workspacePath。缺文件的项目没有条目。
func cursorProjectWorkspaces(ctx context.Context, files transcriptFS, projects []string) (map[string]string, error) {
	workspaces := make(map[string]string, len(projects))
	for offset := 0; offset < len(projects); offset += transcriptReadBatch {
		batch := projects[offset:min(offset+transcriptReadBatch, len(projects))]
		specs := make([]transcriptReadSpec, len(batch))
		for index, project := range batch {
			specs[index] = transcriptReadSpec{Rel: project + "/.workspace-trusted", Length: 4096}
		}
		results, err := files.Read(ctx, transcriptRootCursor, specs)
		if err != nil {
			return nil, err
		}
		for index, result := range results {
			if index >= len(batch) {
				break
			}
			if result.Err != nil {
				if errors.Is(result.Err, errTranscriptNotFound) || errors.Is(result.Err, errTranscriptRoot) {
					continue
				}
				return nil, result.Err
			}
			if result.Exists {
				workspaces[batch[index]] = agentlog.CursorWorkspacePath(result.Data)
			}
		}
	}
	return workspaces, nil
}
