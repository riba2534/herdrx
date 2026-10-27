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

// collectCursorCandidates 只在 workspacePath 与 pane cwd 相同的项目里列 transcript。
func collectCursorCandidates(ctx context.Context, files transcriptFS, codec transcriptCodec, cwd string) ([]agentlog.Candidate, bool, error) {
	projects, err := files.List(ctx, transcriptRootCursor, "", 1)
	if err != nil {
		return nil, false, err
	}
	capped := len(projects) >= transcriptMaxListEntries
	entries := make([]transcriptEntry, 0)
	for _, project := range projects {
		if !project.Dir || project.Link || strings.Contains(project.Rel, "/") {
			continue
		}
		workspace, err := cursorProjectWorkspace(ctx, files, project.Rel)
		if err != nil {
			return nil, false, err
		}
		if workspace != cwd {
			continue
		}
		listed, err := files.List(ctx, transcriptRootCursor, project.Rel+"/agent-transcripts", 2)
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
		workspace, err := cursorWorkspace(ctx, files, entry.Rel)
		if err != nil {
			return nil, false, err
		}
		if workspace != cwd {
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

func cursorProjectWorkspace(ctx context.Context, files transcriptFS, project string) (string, error) {
	if project == "" || strings.Contains(project, "/") {
		return "", nil
	}
	result, err := transcriptReadOne(ctx, files, transcriptRootCursor, project+"/.workspace-trusted", 0, 4096)
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
