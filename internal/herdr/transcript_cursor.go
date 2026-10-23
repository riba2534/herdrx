package herdr

import (
	"context"
	"errors"

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
