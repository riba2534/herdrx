package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// Gemini 只认 projectHash：只看 sha256(pane cwd) 那一个目录，会话头的 projectHash 也必须相等。
// 正文里写着「Working directory: <cwd>」的别处会话不算。
func TestGeminiTranscriptOwnershipIsTheProjectHash(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/user/myproject"
	hash := agentlog.GeminiProjectHash(cwd)
	body := `{"sessionId":"doc-1","projectHash":"` + hash + `","messages":[{"type":"user","content":"Please help me.","timestamp":"2024-01-01T12:00:00Z"},{"type":"model","content":"Hello from Gemini!","timestamp":"2024-01-01T12:01:00Z"}]}`
	writeGeminiSession(t, home, hash, "session-doc.json", body)
	stream := "{\"kind\":\"main\",\"sessionId\":\"s1\",\"projectHash\":\"" + hash + "\"}\n" +
		"{\"$set\":{\"messages\":[{\"id\":\"m1\",\"type\":\"user\",\"content\":[{\"text\":\"hello\"}]}]}}\n" +
		"{\"id\":\"m2\",\"type\":\"gemini\",\"content\":\"current answer\"}\n"
	writeGeminiSession(t, home, hash, "session-2026-07-09T04-00-s1.jsonl", stream)
	// 正文冒充 cwd，但目录与 projectHash 属于别的项目。
	otherHash := agentlog.GeminiProjectHash("/home/user/other")
	writeGeminiSession(t, home, otherHash, "session-other.json", `{"sessionId":"other","projectHash":"`+otherHash+`","messages":[{"type":"user","content":"Working directory: /home/user/myproject\nOTHER_SECRET"}]}`)
	// 放进本项目目录，但会话头的 projectHash 不符。
	writeGeminiSession(t, home, hash, "session-mismatch.json", `{"sessionId":"mismatch","projectHash":"`+otherHash+`","messages":[{"type":"user","content":"MISMATCH_SECRET"}]}`)
	// 没有 projectHash 的会话不认领；读不了的文件只跳过，不中止扫描。
	writeGeminiSession(t, home, hash, "session-nohash.json", `{"sessionId":"nohash","messages":[{"type":"user","content":"x"}]}`)
	if err := os.Symlink("/etc/hostname", filepath.Join(home, ".gemini", "tmp", hash, "chats", "session-link.json")); err != nil {
		t.Fatal(err)
	}
	writeGeminiSession(t, home, hash, "session-unreadable.json", `{"sessionId":"locked","projectHash":"`+hash+`","messages":[]}`)
	locked := filepath.Join(home, ".gemini", "tmp", hash, "chats", "session-unreadable.json")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o600) })
	// 令牌层同样只认 sha256(cwd) 目录：别的 hash 目录不是这个 pane 的会话路径。
	if transcriptRelAllowed(agentlog.AgentGemini, cwd, "", otherHash+"/chats/session-other.json") || !transcriptRelAllowed(agentlog.AgentGemini, cwd, "", hash+"/chats/session-doc.json") {
		t.Fatal("gemini rel check does not pin the project hash directory")
	}

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
	if !seen["doc-1"] || !seen["s1"] {
		t.Fatalf("candidates = %+v", page.Candidates)
	}
	// 别的项目里的会话即使带着这个目录名的令牌也读不到。
	foreign, err := codec.seal(agentlog.AgentGemini, "other", otherHash+"/chats/session-other.json")
	if err != nil {
		t.Fatal(err)
	}
	lost := readTranscript(context.Background(), files, codec, TranscriptScope{PaneID: "p", Agent: agentlog.AgentGemini, CWD: cwd}, TranscriptRequest{Session: foreign})
	if lost.Reason != ChatReasonSessionUnavailable || len(lost.Messages) != 0 {
		t.Fatalf("foreign token = %+v", lost)
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
	if bytes.Contains(encoded, []byte(`"cwd"`)) || bytes.Contains(encoded, []byte("OTHER_SECRET")) || bytes.Contains(encoded, []byte("MISMATCH_SECRET")) {
		t.Fatalf("response leaked: %s", encoded)
	}
	// 文件增长（Gemini 每次写入都会重写/追加）不改变游标身份：续读只拿到新记录，不 reset。
	path := filepath.Join(home, ".gemini", "tmp", hash, "chats", "session-2026-07-09T04-00-s1.jsonl")
	if err := os.WriteFile(path, []byte(stream+"{\"id\":\"m3\",\"type\":\"user\",\"content\":\"追加的问题\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	grown := readTranscript(context.Background(), files, codec, TranscriptScope{PaneID: "p", Agent: agentlog.AgentGemini, CWD: cwd}, TranscriptRequest{Session: token, Cursor: bound.NextCursor})
	if grown.Reset || len(grown.Messages) != 1 || grown.Messages[0].ID != "m3" {
		t.Fatalf("grown = %+v", grown)
	}
}

// 超过整份回放预算的会话只读尾部窗口；没有 id 的行在两个窗口里 id 相同，翻页不重号。
func TestGeminiTailWindowKeepsStableIDs(t *testing.T) {
	home := t.TempDir()
	cwd := "/srv/proj"
	hash := agentlog.GeminiProjectHash(cwd)
	var body bytes.Buffer
	body.WriteString(`{"kind":"main","sessionId":"big","projectHash":"` + hash + `","startTime":"2026-10-30T00:00:00Z"}` + "\n")
	for body.Len() <= transcriptGeminiMaxBytes {
		body.WriteString(`{"type":"gemini","content":"` + strings.Repeat("y", 4000) + `"}` + "\n")
	}
	writeGeminiSession(t, home, hash, "session-big.jsonl", body.String())
	files := &localTranscriptFS{home: home}
	codec := newTranscriptCodecForTest(t)
	token, err := codec.seal(agentlog.AgentGemini, "big", hash+"/chats/session-big.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	scope := TranscriptScope{PaneID: "p", Agent: agentlog.AgentGemini, CWD: cwd}
	last := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: token})
	if len(last.Messages) == 0 || last.PrevCursor == "" {
		t.Fatalf("tail = reason %q messages %d", last.Reason, len(last.Messages))
	}
	earlier := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: token, Before: last.PrevCursor})
	seen := map[string]bool{}
	for _, record := range append(append([]agentlog.Record{}, earlier.Messages...), last.Messages...) {
		if seen[record.ID] {
			t.Fatalf("duplicate id across pages: %s", record.ID)
		}
		seen[record.ID] = true
	}
	again := readTranscript(context.Background(), files, codec, scope, TranscriptRequest{Session: token})
	if again.Messages[0].ID != last.Messages[0].ID {
		t.Fatalf("ids changed between reads: %s vs %s", again.Messages[0].ID, last.Messages[0].ID)
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
