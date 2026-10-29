package agentlog

import (
	"strings"
	"testing"
)

func TestGeminiDecodesProvenSessionShapes(t *testing.T) {
	legacy := []byte(`{
  "sessionId": "test-session-1",
  "projectHash": "hash123",
  "startTime": "2024-01-01T12:00:00Z",
  "lastUpdated": "2024-01-01T12:05:00Z",
  "messages": [
    {"type": "user", "content": "# AGENTS.md instructions for /data/projects/my_project\n\nSome instructions here.", "timestamp": "2024-01-01T12:00:00Z"},
    {"type": "model", "content": "Hello from Gemini!", "timestamp": "2024-01-01T12:01:00Z"}
  ]
}`)
	document := DecodeGemini("hash123/chats/session-test.json", legacy)
	if document.SessionID != "test-session-1" || document.ProjectHash != "hash123" || document.StartTime != "2024-01-01T12:00:00Z" || len(document.Records) != 2 {
		t.Fatalf("legacy = %+v", document)
	}
	if document.Records[0].Role != RoleUser || document.Records[1].Role != RoleAssistant || document.Records[1].Blocks[0].Text != "Hello from Gemini!" {
		t.Fatalf("records = %+v", document.Records)
	}

	// connector_gemini.rs: 当前 JSONL 是事件流，不是一行一条最终消息。
	stream := []byte("" +
		`{"kind":"main","sessionId":"s1","projectHash":"h1","startTime":"2026-07-09T04:00:00Z"}` + "\n" +
		`{"$set":{"lastUpdated":"2026-07-09T04:00:01Z","messages":[{"id":"m1","type":"user","timestamp":"2026-07-09T04:00:00Z","content":[{"text":"hello"}]}]}}` + "\n" +
		`{"id":"m2","type":"gemini","timestamp":"2026-07-09T04:00:01Z","content":"current answer"}` + "\n")
	replayed := DecodeGemini("h1/chats/session-2026-07-09T04-00-s1.jsonl", stream)
	if replayed.SessionID != "s1" || replayed.ProjectHash != "h1" || len(replayed.Records) != 2 || replayed.Records[0].Blocks[0].Text != "hello" || replayed.Records[1].Role != RoleAssistant || replayed.Records[1].Blocks[0].Text != "current answer" {
		t.Fatalf("replay = %+v", replayed)
	}
	if GeminiSessionRel("hash/not-chats/session-1.json") || GeminiSessionRel("hash/chats/notes.json") {
		t.Fatal("non-session gemini path accepted")
	}
}

// projectHash 是项目根绝对路径的 SHA-256 十六进制（Gemini CLI getProjectHash）。
func TestGeminiProjectHashIsSHA256OfTheRoot(t *testing.T) {
	// printf '%s' /srv/proj | sha256sum
	if got := GeminiProjectHash("/srv/proj"); got != "6b9af143446a411aefa3edb76e4693799bd7513e7ed8d919962d6713d3d3b972" {
		t.Fatalf("hash = %q", got)
	}
	if GeminiProjectHash("/srv/proj") == GeminiProjectHash("/srv/proj/") {
		t.Fatal("trailing slash must change the hash; the caller passes the exact cwd")
	}
}

// 没有 id 的事件行按绝对字节位置命名：从不同窗口读到同一行，id 相同；不同行不撞号。
func TestGeminiAnonymousIDsAreStableAcrossWindows(t *testing.T) {
	lines := []string{
		`{"kind":"main","sessionId":"s1","projectHash":"h1"}`,
		`{"type":"user","content":"第一条"}`,
		`{"type":"gemini","content":"第二条"}`,
		`{"type":"user","content":"第三条"}`,
	}
	data := []byte(strings.Join(lines, "\n") + "\n")
	full := DecodeGemini("h1/chats/session-s1.jsonl", data)
	if len(full.Records) != 3 {
		t.Fatalf("full = %+v", full.Records)
	}
	cut := len(lines[0]) + 1 + len(lines[1]) + 1
	tail := DecodeGeminiAt("h1/chats/session-s1.jsonl", data[cut:], int64(cut), "s1")
	if len(tail.Records) != 2 || tail.Records[0].ID != full.Records[1].ID || tail.Records[1].ID != full.Records[2].ID {
		t.Fatalf("tail ids = %v, full ids = %v", recordIDs(tail.Records), recordIDs(full.Records))
	}
	seen := map[string]bool{}
	for _, record := range full.Records {
		if seen[record.ID] {
			t.Fatalf("duplicate id %s", record.ID)
		}
		seen[record.ID] = true
	}
}

// 一条消息里多个工具结果：CallID 为空或重复时 id 也不能撞。
func TestGeminiToolResultIDsDoNotCollide(t *testing.T) {
	data := []byte(`{"sessionId":"s","projectHash":"h","messages":[{"id":"m1","type":"gemini","content":"跑两次","toolCalls":[` +
		`{"name":"shell","args":{},"result":"one"},` +
		`{"name":"shell","args":{},"result":"two"},` +
		`{"id":"dup","name":"shell","args":{},"result":"three"},` +
		`{"id":"dup","name":"shell","args":{},"result":"four"}]}]}`)
	document := DecodeGemini("h/chats/session-s.json", data)
	seen := map[string]bool{}
	tools := 0
	for _, record := range document.Records {
		if seen[record.ID] {
			t.Fatalf("duplicate id %s in %v", record.ID, recordIDs(document.Records))
		}
		seen[record.ID] = true
		if record.Role == RoleTool {
			tools++
		}
	}
	if tools != 4 {
		t.Fatalf("tool records = %d", tools)
	}
}

func TestGeminiDropsThoughtParts(t *testing.T) {
	data := []byte(`{"sessionId":"s","projectHash":"h","messages":[{"id":"m1","type":"gemini","content":[` +
		`{"text":"THOUGHT_SECRET","thought":true},{"text":"可见回答"}]}]}`)
	document := DecodeGemini("h/chats/session-s.json", data)
	if len(document.Records) != 1 || len(document.Records[0].Blocks) != 1 || document.Records[0].Blocks[0].Text != "可见回答" {
		t.Fatalf("records = %+v", document.Records)
	}
}

func recordIDs(records []Record) []string {
	ids := make([]string, len(records))
	for index, record := range records {
		ids[index] = record.ID
	}
	return ids
}
