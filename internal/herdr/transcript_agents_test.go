package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/riba2534/herdrx/internal/agentlog"
)

func TestCursorTranscriptMatchesWorkspacePathNotTheSlug(t *testing.T) {
	home := t.TempDir()
	cwd := "/Users/example/workspace/foo"
	other := "/Users/example/workspace/other"
	const userLine = `{"role":"user","message":{"content":[{"type":"text","text":"find the bug"}]}}`
	const assistantLine = `{"role":"assistant","message":{"content":[{"type":"text","text":"on it"},{"type":"tool_use","name":"Grep","input":{"pattern":"bug"}}]}}`
	writeCursorTranscript(t, home, "Users-example-workspace-foo", "sess-1", cwd, userLine, assistantLine)
	writeCursorTranscript(t, home, "Users-example-workspace-foo-decoy", "sess-2", other, userLine)
	// 没有 workspacePath 的项目不能靠目录名认领。
	writeCursorTranscript(t, home, "plain-project", "sess-3", "", userLine)
	sub := filepath.Join(home, ".cursor", "projects", "Users-example-workspace-foo", "agent-transcripts", "sess-1", "subagents", "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "sub.jsonl"), []byte(userLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	page := readTranscript(context.Background(), files, codec, TranscriptScope{PaneID: "p", Agent: agentlog.AgentCursor, CWD: cwd}, TranscriptRequest{})
	page = mustCandidates(t, page, 1)
	if page.Candidates[0].SessionID != "sess-1" || page.Candidates[0].Agent != agentlog.AgentCursor {
		t.Fatalf("candidate = %+v", page.Candidates[0])
	}
	bound := readTranscript(context.Background(), files, codec, TranscriptScope{PaneID: "p", Agent: agentlog.AgentCursor, CWD: cwd}, TranscriptRequest{Session: page.Candidates[0].ID})
	if !bound.Supported || bound.SessionID != "sess-1" || len(bound.Messages) != 2 {
		t.Fatalf("messages = %+v", bound)
	}
	if bound.Messages[0].Blocks[0].Text != "find the bug" || bound.Messages[1].Blocks[1].Name != "Grep" {
		t.Fatalf("records = %+v", bound.Messages)
	}
	encoded, err := json.Marshal(bound.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(cwd)) || bytes.Contains(encoded, []byte("workspacePath")) {
		t.Fatalf("response leaked cwd: %s", encoded)
	}
}

func TestGeminiTranscriptUsesMarkedWorkspaceOnly(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/user/myproject"
	body := `{"sessionId":"workdir-test","projectHash":"hash789","messages":[{"type":"user","content":"Working directory: /home/user/myproject\nPlease help me.","timestamp":"2024-01-01T12:00:00Z"},{"type":"model","content":"Hello from Gemini!","timestamp":"2024-01-01T12:01:00Z"}]}`
	writeGeminiSession(t, home, "hash789", "session-workdir.json", body)
	writeGeminiSession(t, home, "hash-plain", "session-plain.json", `{"sessionId":"plain","messages":[{"type":"user","content":"Hello, how are you?"}]}`)
	stream := "{\"kind\":\"main\",\"sessionId\":\"s1\",\"projectHash\":\"h1\"}\n" +
		"{\"$set\":{\"messages\":[{\"id\":\"m1\",\"type\":\"user\",\"content\":[{\"text\":\"Working directory: /home/user/myproject\\nhello\"}]}]}}\n" +
		"{\"id\":\"m2\",\"type\":\"gemini\",\"content\":\"current answer\"}\n"
	writeGeminiSession(t, home, "h1", "session-2026-07-09T04-00-s1.jsonl", stream)

	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	page := readTranscript(context.Background(), files, codec, TranscriptScope{PaneID: "p", Agent: agentlog.AgentGemini, CWD: cwd}, TranscriptRequest{})
	page = mustCandidates(t, page, 2)
	seen := map[string]bool{}
	for _, candidate := range page.Candidates {
		seen[candidate.SessionID] = true
		if candidate.Agent != agentlog.AgentGemini {
			t.Fatalf("candidate = %+v", candidate)
		}
	}
	if !seen["workdir-test"] || !seen["s1"] || seen["plain"] {
		t.Fatalf("candidates = %+v", page.Candidates)
	}
	var token string
	for _, candidate := range page.Candidates {
		if candidate.SessionID == "s1" {
			token = candidate.ID
		}
	}
	bound := readTranscript(context.Background(), files, codec, TranscriptScope{PaneID: "p", Agent: agentlog.AgentGemini, CWD: cwd}, TranscriptRequest{Session: token})
	if !bound.Supported || len(bound.Messages) != 2 || bound.Messages[0].Blocks[0].Text == "" || bound.Messages[1].Blocks[0].Text != "current answer" {
		t.Fatalf("messages = %+v", bound)
	}
	encoded, err := json.Marshal(bound)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"cwd"`)) {
		t.Fatalf("response leaked a cwd field: %s", encoded)
	}
}

func writeCursorTranscript(t *testing.T, home, project, session, workspace string, lines ...string) {
	t.Helper()
	dir := filepath.Join(home, ".cursor", "projects", project, "agent-transcripts", session)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := ""
	for _, line := range lines {
		body += line + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, session+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if workspace == "" {
		return
	}
	payload, err := json.Marshal(map[string]string{"workspacePath": workspace})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".cursor", "projects", project, ".workspace-trusted"), append(payload, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeGeminiSession(t *testing.T, home, hash, name, body string) {
	t.Helper()
	dir := filepath.Join(home, ".gemini", "tmp", hash, "chats")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
