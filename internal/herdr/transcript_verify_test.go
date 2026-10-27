package herdr

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/agentlog"
)

func TestPaneOmitsAgentSessionUntilReported(t *testing.T) {
	var missing Pane
	if err := json.Unmarshal([]byte(`{"pane_id":"p","agent":"claude"}`), &missing); err != nil {
		t.Fatal(err)
	}
	if missing.AgentSession != nil {
		t.Fatalf("omitted evidence was invented: %+v", missing.AgentSession)
	}
	var reported Pane
	raw := `{"pane_id":"p","agent_session":{"source":"hook","agent":"claude","kind":"id","value":"sess-1"}}`
	if err := json.Unmarshal([]byte(raw), &reported); err != nil {
		t.Fatal(err)
	}
	if reported.AgentSession == nil || reported.AgentSession.Kind != "id" || reported.AgentSession.Value != "sess-1" || reported.AgentSession.Agent != "claude" {
		t.Fatalf("evidence = %+v", reported.AgentSession)
	}
}

func TestVerifiedBindingRequiresHookEvidence(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/proj"
	writeLines(t, claudeSessionPath(home, cwd, "sess-ok"), []string{claudeUser("u-1", cwd, "我的问题")})
	writeLines(t, claudeSessionPath(home, cwd, "sess-bad"), []string{claudeUser("u-2", "/srv/other", "别人的问题")})
	if err := os.WriteFile(claudeSessionPath(home, cwd, "sess-empty"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	scope := claudeScope(cwd)
	plain := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{})
	if plain.Binding != "" || len(plain.Messages) != 0 || len(plain.Candidates) != 1 {
		t.Fatalf("one candidate was bound without evidence: %+v", plain)
	}

	matched := scope
	matched.Evidence = &AgentSession{Source: "hook", Agent: agentlog.AgentClaude, Kind: "id", Value: "sess-ok"}
	page := readTranscript(context.Background(), files, codec, matched, TranscriptRequest{})
	if !page.Supported || page.Reason != "" || page.Binding != ChatBindingVerified || page.SessionID != "sess-ok" {
		t.Fatalf("verified page = %+v", page)
	}
	if len(page.Messages) != 1 || page.Messages[0].Role != agentlog.RoleUser || page.Messages[0].Blocks[0].Text != "我的问题" {
		t.Fatalf("verified messages = %+v", page.Messages)
	}
	if strings.Contains(page.SessionID, "/") || page.Candidates != nil && len(page.Candidates) != 0 {
		t.Fatalf("verified page leaked candidates: %+v", page)
	}

	mismatched := scope
	mismatched.Evidence = &AgentSession{Source: "hook", Agent: agentlog.AgentClaude, Kind: "id", Value: "sess-bad"}
	rejected := readTranscript(context.Background(), files, codec, mismatched, TranscriptRequest{})
	if rejected.Binding == ChatBindingVerified {
		t.Fatalf("mismatched cwd was verified: %+v", rejected)
	}
	encoded, err := json.Marshal(rejected)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "别人的问题") {
		t.Fatalf("mismatched message was returned: %s", encoded)
	}

	empty := scope
	empty.Evidence = &AgentSession{Source: "hook", Agent: agentlog.AgentClaude, Kind: "id", Value: "sess-empty"}
	blank := readTranscript(context.Background(), files, codec, empty, TranscriptRequest{})
	if blank.Binding != "" || len(blank.Messages) != 0 {
		t.Fatalf("empty file was verified: %+v", blank)
	}

	for _, value := range []string{"../sess-ok", "/tmp/sess-ok", "sess/ok", `sess\ok`, ".", ".."} {
		bad := scope
		bad.Evidence = &AgentSession{Source: "hook", Agent: agentlog.AgentClaude, Kind: "id", Value: value}
		page := readTranscript(context.Background(), files, codec, bad, TranscriptRequest{})
		if page.Binding == ChatBindingVerified {
			t.Fatalf("id %q was verified", value)
		}
	}
}

func TestVerifiedDSHIDUsesHeaderCWD(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := dshLocalFS(home)
	writeLines(t, dshTestSessionPath(home, dshTestCWD, dshTestSessionID, "session.v3.jsonl"), []string{
		dshTestHeader(dshTestSessionID, dshTestCWD), dshTestUser(1, "DSH 问题"),
	})
	scope := dshScope(dshTestCWD)
	scope.Evidence = &AgentSession{Source: "hook", Agent: agentlog.AgentDSH, Kind: "id", Value: dshTestSessionID}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{})
	if page.Binding != ChatBindingVerified || page.SessionID != dshTestSessionID {
		t.Fatalf("dsh verified = %+v", page)
	}
	found := false
	for _, message := range page.Messages {
		for _, block := range message.Blocks {
			if block.Text == "DSH 问题" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("dsh message missing: %+v", page.Messages)
	}
	wrong := dshScope("/srv/other")
	wrong.Evidence = scope.Evidence
	rejected := readTranscript(context.Background(), files, codec, wrong, TranscriptRequest{})
	if rejected.Binding == ChatBindingVerified {
		t.Fatalf("dsh cwd mismatch verified: %+v", rejected)
	}
}

func TestCursorSessionIDVerifiesTheWorkspaceFile(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/proj"
	writeCursorTranscript(t, home, "proj", "sess-1", cwd, `{"role":"user","message":{"content":[{"type":"text","text":"cursor text"}]}}`)
	writeCursorTranscript(t, home, "other", "sess-1", "/srv/other", `{"role":"user","message":{"content":[{"type":"text","text":"别人的 cursor"}]}}`)
	scope := TranscriptScope{
		PaneID: "p", Agent: agentlog.AgentCursor, CWD: cwd,
		Evidence: &AgentSession{Source: "hook", Agent: agentlog.AgentCursor, Kind: "id", Value: "sess-1"},
	}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{})
	if page.Binding != ChatBindingVerified || len(page.Messages) != 1 || page.Messages[0].Blocks[0].Text != "cursor text" {
		t.Fatalf("cursor id = %+v", page)
	}
	wrong := scope
	wrong.CWD = "/srv/missing"
	wrong.Evidence = scope.Evidence
	rejected := readTranscript(context.Background(), files, codec, wrong, TranscriptRequest{})
	if rejected.Binding == ChatBindingVerified {
		t.Fatalf("cursor cwd mismatch verified: %+v", rejected)
	}
}

func TestVerifiedPathRejectsAbsoluteAndParentSegments(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/proj"
	writeLines(t, claudeSessionPath(home, cwd, "sess-ok"), []string{claudeUser("u-1", cwd, "我的问题")})
	rel := agentlog.ClaudeProjectDir(cwd) + "/sess-ok.jsonl"
	scope := claudeScope(cwd)
	scope.Evidence = &AgentSession{Source: "hook", Agent: agentlog.AgentClaude, Kind: "path", Value: rel}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{})
	if page.Binding != ChatBindingVerified || page.Messages[0].Blocks[0].Text != "我的问题" {
		t.Fatalf("relative path = %+v", page)
	}
	for _, value := range []string{"/etc/passwd", rel + "/../../etc/passwd", `proj\sess-ok.jsonl`} {
		bad := claudeScope(cwd)
		bad.Evidence = &AgentSession{Source: "hook", Agent: agentlog.AgentClaude, Kind: "path", Value: value}
		rejected := readTranscript(context.Background(), files, codec, bad, TranscriptRequest{})
		if rejected.Binding == ChatBindingVerified {
			t.Fatalf("path %q was verified", value)
		}
	}
}

func TestIdentifiedGeminiOverBudgetReadsTheTail(t *testing.T) {
	home := t.TempDir()
	codec := newTranscriptCodecForTest(t)
	files := &localTranscriptFS{home: home}
	cwd := "/srv/app"
	var body strings.Builder
	body.WriteString("{\"kind\":\"main\",\"sessionId\":\"s-big\",\"projectHash\":\"h\",\"messages\":[{\"id\":\"m0\",\"type\":\"user\",\"content\":\"Working directory: /srv/app\\nseed\"}]}\n")
	pad := "{\"type\":\"user\",\"content\":\"pad\"}\n"
	for body.Len() <= transcriptGeminiMaxBytes {
		body.WriteString(pad)
	}
	body.WriteString("{\"id\":\"tail\",\"type\":\"gemini\",\"content\":\"tail-answer\"}\n")
	writeGeminiSession(t, home, "h", "session-big.jsonl", body.String())
	rel := "h/chats/session-big.jsonl"
	scope := TranscriptScope{
		PaneID: "p", Agent: agentlog.AgentGemini, CWD: cwd,
		Evidence: &AgentSession{Source: "hook", Agent: agentlog.AgentGemini, Kind: "path", Value: rel},
	}
	page := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{})
	if page.Reason == ChatReasonReadLimitExceeded || page.Binding != ChatBindingVerified || page.PrevCursor == "" {
		t.Fatalf("tail page = reason %q binding %q previous %q messages %d", page.Reason, page.Binding, page.PrevCursor, len(page.Messages))
	}
	found := false
	for _, message := range page.Messages {
		for _, block := range message.Blocks {
			if block.Text == "tail-answer" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("tail message missing: %+v", page.Messages)
	}
}
