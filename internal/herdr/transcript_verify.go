package herdr

import (
	"context"
	"strings"

	"github.com/riba2534/herdrx/internal/agentlog"
)

// transcriptFromEvidence 在没有 session 查询时，用 snapshot 的 agent_session 读唯一文件。
// 只有支持且 reason 为空、并且记录 cwd 已经对上时才返回 verified。其余情况交给候选列表。
func transcriptFromEvidence(ctx context.Context, files transcriptFS, codec transcriptCodec, scope TranscriptScope, request TranscriptRequest) (TranscriptPage, bool) {
	evidence := scope.Evidence
	if evidence == nil || evidence.Agent != scope.Agent || !agentlog.Supported(scope.Agent) {
		return TranscriptPage{}, false
	}
	switch evidence.Kind {
	case "id":
		if !validEvidenceID(evidence.Value) {
			return TranscriptPage{}, false
		}
		switch scope.Agent {
		case agentlog.AgentClaude:
			return verifyClaudeID(ctx, files, codec, scope, request, evidence.Value)
		case agentlog.AgentDSH:
			return verifyDSHID(ctx, files, codec, scope, request, evidence.Value)
		case agentlog.AgentAgy:
			return verifyAgyID(ctx, files, codec, scope, request, evidence.Value)
		default:
			// Cursor / Gemini 的 id 没有唯一相对路径。不扫目录树去找它。
			return TranscriptPage{}, false
		}
	case "path":
		// agy 只接受 kind=id 的 uuid。不读客户端传来的路径。
		if scope.Agent == agentlog.AgentAgy {
			return TranscriptPage{}, false
		}
		return verifyEvidencePath(ctx, files, codec, scope, request, evidence.Value)
	default:
		return TranscriptPage{}, false
	}
}

func validEvidenceID(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	return !strings.Contains(value, "/") && !strings.Contains(value, `\`) && !strings.ContainsRune(value, 0)
}

func validEvidencePath(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, `\`) || strings.ContainsRune(value, 0) {
		return false
	}
	parts, err := transcriptRelParts(value)
	return err == nil && len(parts) > 0
}

func verifyClaudeID(ctx context.Context, files transcriptFS, codec transcriptCodec, scope TranscriptScope, request TranscriptRequest, sessionID string) (TranscriptPage, bool) {
	name := sessionID + ".jsonl"
	if !agentlog.ClaudeSessionFile(name) {
		return TranscriptPage{}, false
	}
	rel := agentlog.ClaudeProjectDir(scope.CWD) + "/" + name
	shape, ok := evidenceAdmitted(ctx, files, scope.Agent, rel)
	if !ok || shape.CWD != scope.CWD {
		return TranscriptPage{}, false
	}
	return finishVerified(ctx, files, codec, scope, request, sessionID, rel)
}

func verifyAgyID(ctx context.Context, files transcriptFS, codec transcriptCodec, scope TranscriptScope, request TranscriptRequest, sessionID string) (TranscriptPage, bool) {
	if !agentlog.AgyUUID(sessionID) {
		return TranscriptPage{}, false
	}
	return finishVerified(ctx, files, codec, scope, request, sessionID, sessionID+".db")
}

func verifyDSHID(ctx context.Context, files transcriptFS, codec transcriptCodec, scope TranscriptScope, request TranscriptRequest, sessionID string) (TranscriptPage, bool) {
	for _, name := range []string{"session.v3.jsonl", "session.v3.jsonl.zstd"} {
		rel, ok := dshRelForSession(scope.CWD, sessionID, name)
		if !ok || !transcriptRelAllowed(scope.Agent, scope.CWD, sessionID, rel) {
			continue
		}
		shape, admitted := evidenceAdmitted(ctx, files, scope.Agent, rel)
		if !admitted || shape.DSHVersion != agentlog.DSHFormatVersion || shape.SessionID != sessionID || shape.CWD != scope.CWD {
			continue
		}
		if page, ok := finishVerified(ctx, files, codec, scope, request, sessionID, rel); ok {
			return page, true
		}
	}
	return TranscriptPage{}, false
}

func verifyEvidencePath(ctx context.Context, files transcriptFS, codec transcriptCodec, scope TranscriptScope, request TranscriptRequest, rel string) (TranscriptPage, bool) {
	if !validEvidencePath(rel) {
		return TranscriptPage{}, false
	}
	sessionID := evidencePathSessionID(scope.Agent, rel)
	if scope.Agent == agentlog.AgentDSH {
		shape, ok := evidenceAdmitted(ctx, files, scope.Agent, rel)
		if !ok || shape.DSHVersion != agentlog.DSHFormatVersion || shape.SessionID == "" || shape.CWD != scope.CWD {
			return TranscriptPage{}, false
		}
		sessionID = shape.SessionID
	}
	if !transcriptRelAllowed(scope.Agent, scope.CWD, sessionID, rel) {
		return TranscriptPage{}, false
	}
	if scope.Agent == agentlog.AgentClaude || scope.Agent == agentlog.AgentCodex {
		shape, ok := evidenceAdmitted(ctx, files, scope.Agent, rel)
		if !ok || shape.CWD != scope.CWD {
			return TranscriptPage{}, false
		}
		if sessionID == "" {
			sessionID = shape.SessionID
		}
	}
	return finishVerified(ctx, files, codec, scope, request, sessionID, rel)
}

func evidencePathSessionID(agent, rel string) string {
	switch agent {
	case agentlog.AgentCursor:
		return agentlog.CursorSessionID(rel)
	case agentlog.AgentClaude, agentlog.AgentCodex:
		return agentlog.SessionStem(baseName(rel))
	default:
		return ""
	}
}

// evidenceAdmitted 只读这一个相对路径的身份。不存在或读失败都不当作 verified。
func evidenceAdmitted(ctx context.Context, files transcriptFS, agent, rel string) (agentlog.FileShape, bool) {
	shapes, reads, err := transcriptShapes(ctx, files, transcriptRootFor(agent), agent, []string{rel})
	if err != nil || len(reads) == 0 {
		return agentlog.FileShape{}, false
	}
	read := reads[0]
	if read.Err != nil || !read.Exists || len(read.Data) == 0 {
		return agentlog.FileShape{}, false
	}
	shape := shapes[0]
	if agent == agentlog.AgentDSH && agentlog.DSHCompressedSessionFile(baseName(rel)) {
		return shape, shape.DSHVersion == agentlog.DSHFormatVersion && shape.CWD != "" && shape.SessionID != ""
	}
	if !hasCompleteLine(read.Data) {
		return agentlog.FileShape{}, false
	}
	return shape, true
}

func finishVerified(ctx context.Context, files transcriptFS, codec transcriptCodec, scope TranscriptScope, request TranscriptRequest, sessionID, rel string) (TranscriptPage, bool) {
	if rel == "" || !transcriptRelAllowed(scope.Agent, scope.CWD, sessionID, rel) {
		return TranscriptPage{}, false
	}
	page := transcriptMessages(ctx, files, codec, scope.Agent, scope.CWD, sessionID, rel, request)
	if !page.Supported || page.Reason != "" || page.SessionID == "" {
		return TranscriptPage{}, false
	}
	if scope.Agent != agentlog.AgentDSH && len(page.Messages) == 0 {
		return TranscriptPage{}, false
	}
	page.Binding = ChatBindingVerified
	if page.Messages == nil {
		page.Messages = []agentlog.Record{}
	}
	return page, true
}
