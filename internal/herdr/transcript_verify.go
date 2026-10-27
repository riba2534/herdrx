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
	// 只认 Herdr 自己的集成上报。agy 的 source 是 `herdr:antigravity_cli`，不等于 `herdr:agy`。
	if evidence == nil || !strings.HasPrefix(evidence.Source, "herdr:") || !agentlog.SameTranscriptAgent(evidence.Agent, scope.Agent) || !agentlog.Supported(scope.Agent) {
		return TranscriptPage{}, false
	}
	switch evidence.Kind {
	case "id":
		if !validEvidenceID(evidence.Value) {
			return TranscriptPage{}, false
		}
		switch scope.Agent {
		case agentlog.AgentCodex, agentlog.AgentTraex:
			return verifyRolloutID(ctx, files, codec, scope, request, evidence.Value)
		case agentlog.AgentClaude:
			return verifyClaudeID(ctx, files, codec, scope, request, evidence.Value)
		case agentlog.AgentDSH:
			return verifyDSHID(ctx, files, codec, scope, request, evidence.Value)
		case agentlog.AgentAgy:
			return verifyAgyID(ctx, files, codec, scope, request, evidence.Value)
		case agentlog.AgentCursor:
			return verifyCursorID(ctx, files, codec, scope, request, evidence.Value)
		case agentlog.AgentPi:
			return verifyPiID(ctx, files, codec, scope, request, evidence.Value)
		case agentlog.AgentGrok:
			return verifyGrokID(ctx, files, codec, scope, request, evidence.Value)
		default:
			// Cursor / Gemini 的 id 没有唯一相对路径。不扫目录树去找它。
			return TranscriptPage{}, false
		}
	case "path":
		// agy 与 grok 只接受 kind=id。不读客户端传来的路径。
		if scope.Agent == agentlog.AgentAgy || scope.Agent == agentlog.AgentGrok {
			return TranscriptPage{}, false
		}
		// Pi 的 hook 优先上报绝对文件路径。不打开那个路径，只取出文件名里的会话 id，
		// 再在 pane cwd 对应的目录里找同 id 的文件。
		if scope.Agent == agentlog.AgentPi {
			return verifyPiReportedPath(ctx, files, codec, scope, request, evidence.Value)
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
	for _, spelling := range cwdSpellings(files, scope.CWD) {
		rel := agentlog.ClaudeProjectDir(spelling) + "/" + name
		shape, ok := evidenceAdmitted(ctx, files, scope.Agent, rel)
		if !ok || !cwdMatch(files, shape.CWD, scope.CWD) {
			continue
		}
		return finishVerified(ctx, files, codec, scope, request, sessionID, rel)
	}
	return TranscriptPage{}, false
}

func verifyCursorID(ctx context.Context, files transcriptFS, codec transcriptCodec, scope TranscriptScope, request TranscriptRequest, sessionID string) (TranscriptPage, bool) {
	if !agentlog.CursorSessionFile(sessionID + ".jsonl") {
		return TranscriptPage{}, false
	}
	projects, err := files.List(ctx, transcriptRootCursor, "", 1)
	if err != nil {
		return TranscriptPage{}, false
	}
	rel := ""
	matches := 0
	for _, project := range projects {
		if !project.Dir || project.Link || strings.Contains(project.Rel, "/") {
			continue
		}
		candidate := project.Rel + "/agent-transcripts/" + sessionID + "/" + sessionID + ".jsonl"
		if !agentlog.CursorTranscriptRel(candidate) {
			continue
		}
		workspace, err := cursorWorkspace(ctx, files, candidate)
		if err != nil || !cwdMatch(files, workspace, scope.CWD) {
			continue
		}
		if _, ok := evidenceAdmitted(ctx, files, scope.Agent, candidate); !ok {
			continue
		}
		matches++
		rel = candidate
	}
	if matches != 1 {
		return TranscriptPage{}, false
	}
	return finishVerified(ctx, files, codec, scope, request, sessionID, rel)
}

func verifyPiReportedPath(ctx context.Context, files transcriptFS, codec transcriptCodec, scope TranscriptScope, request TranscriptRequest, value string) (TranscriptPage, bool) {
	if validEvidencePath(value) {
		return verifyEvidencePath(ctx, files, codec, scope, request, value)
	}
	name := value
	if index := strings.LastIndexAny(name, `/\`); index >= 0 {
		name = name[index+1:]
	}
	sessionID := agentlog.PiSessionID(name)
	if sessionID == "" {
		return TranscriptPage{}, false
	}
	return verifyPiID(ctx, files, codec, scope, request, sessionID)
}

func verifyPiID(ctx context.Context, files transcriptFS, codec transcriptCodec, scope TranscriptScope, request TranscriptRequest, sessionID string) (TranscriptPage, bool) {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\`) {
		return TranscriptPage{}, false
	}
	rel := ""
	matches := 0
	for _, spelling := range cwdSpellings(files, scope.CWD) {
		entries, err := files.List(ctx, transcriptRootPi, agentlog.PiProjectDir(spelling), 1)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.Dir || entry.Link || agentlog.PiSessionID(baseName(entry.Rel)) != sessionID {
				continue
			}
			shape, ok := evidenceAdmitted(ctx, files, scope.Agent, entry.Rel)
			if !ok || !cwdMatch(files, shape.CWD, scope.CWD) || shape.SessionID != sessionID {
				continue
			}
			matches++
			rel = entry.Rel
		}
	}
	if matches != 1 {
		return TranscriptPage{}, false
	}
	return finishVerified(ctx, files, codec, scope, request, sessionID, rel)
}

func verifyGrokID(ctx context.Context, files transcriptFS, codec transcriptCodec, scope TranscriptScope, request TranscriptRequest, sessionID string) (TranscriptPage, bool) {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\`) {
		return TranscriptPage{}, false
	}
	for _, spelling := range cwdSpellings(files, scope.CWD) {
		rel := agentlog.GrokProjectDir(spelling) + "/" + sessionID + "/chat_history.jsonl"
		if !agentlog.GrokHistoryRel(rel) || !cwdMatch(files, agentlog.GrokCWD(rel), scope.CWD) {
			continue
		}
		if page, ok := finishVerified(ctx, files, codec, scope, request, sessionID, rel); ok {
			return page, true
		}
	}
	return TranscriptPage{}, false
}

func verifyAgyID(ctx context.Context, files transcriptFS, codec transcriptCodec, scope TranscriptScope, request TranscriptRequest, sessionID string) (TranscriptPage, bool) {
	if !agentlog.AgyUUID(sessionID) {
		return TranscriptPage{}, false
	}
	return finishVerified(ctx, files, codec, scope, request, sessionID, sessionID+".db")
}

func verifyDSHID(ctx context.Context, files transcriptFS, codec transcriptCodec, scope TranscriptScope, request TranscriptRequest, sessionID string) (TranscriptPage, bool) {
	for _, name := range []string{"session.v3.jsonl", "session.v3.jsonl.zstd"} {
		for _, spelling := range cwdSpellings(files, scope.CWD) {
			rel, ok := dshRelForSession(spelling, sessionID, name)
			if !ok || !transcriptRelAllowed(scope.Agent, spelling, sessionID, rel) {
				continue
			}
			shape, admitted := evidenceAdmitted(ctx, files, scope.Agent, rel)
			if !admitted || shape.DSHVersion != agentlog.DSHFormatVersion || shape.SessionID != sessionID || !cwdMatch(files, shape.CWD, scope.CWD) {
				continue
			}
			if page, ok := finishVerified(ctx, files, codec, scope, request, sessionID, rel); ok {
				return page, true
			}
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
		if !ok || shape.DSHVersion != agentlog.DSHFormatVersion || shape.SessionID == "" || !cwdMatch(files, shape.CWD, scope.CWD) {
			return TranscriptPage{}, false
		}
		sessionID = shape.SessionID
	}
	if !transcriptRelAllowedAny(files, scope.Agent, scope.CWD, sessionID, rel) {
		return TranscriptPage{}, false
	}
	if scope.Agent == agentlog.AgentClaude || scope.Agent == agentlog.AgentCodex {
		shape, ok := evidenceAdmitted(ctx, files, scope.Agent, rel)
		if !ok || !cwdMatch(files, shape.CWD, scope.CWD) {
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
	case agentlog.AgentPi:
		return agentlog.PiSessionID(baseName(rel))
	case agentlog.AgentGrok:
		return agentlog.GrokSessionID(rel)
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
	if rel == "" || !transcriptRelAllowedAny(files, scope.Agent, scope.CWD, sessionID, rel) {
		return TranscriptPage{}, false
	}
	page := transcriptMessages(ctx, files, codec, scope.Agent, scope.CWD, sessionID, rel, request)
	if !page.Supported || page.Reason != "" || page.SessionID == "" {
		return TranscriptPage{}, false
	}
	if scope.Agent != agentlog.AgentDSH && len(page.Messages) == 0 {
		return TranscriptPage{}, false
	}
	session, err := codec.seal(scope.Agent, sessionID, rel)
	if err != nil {
		return TranscriptPage{}, false
	}
	page.Binding = ChatBindingVerified
	page.Session = session
	if listed := transcriptCandidates(ctx, files, codec, scope.Agent, scope.CWD); listed.Supported {
		page.Candidates = listed.Candidates
	}
	if page.Messages == nil {
		page.Messages = []agentlog.Record{}
	}
	return page, true
}
