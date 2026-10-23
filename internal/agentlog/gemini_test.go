package agentlog

import "testing"

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
	if document.SessionID != "test-session-1" || document.CWD != "/data/projects/my_project" || len(document.Records) != 2 {
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
	if replayed.SessionID != "s1" || len(replayed.Records) != 2 || replayed.Records[0].Blocks[0].Text != "hello" || replayed.Records[1].Role != RoleAssistant || replayed.Records[1].Blocks[0].Text != "current answer" {
		t.Fatalf("replay = %+v", replayed)
	}
	workdir := DecodeGemini("hash/chats/session-workdir.json", []byte(`{"sessionId":"workdir-test","messages":[{"type":"user","content":"Working directory: /home/user/myproject\nPlease help me."}]}`))
	if workdir.CWD != "/home/user/myproject" {
		t.Fatalf("workdir = %q", workdir.CWD)
	}
	if got := DecodeGemini("hash/chats/session-plain.json", []byte(`{"sessionId":"plain","messages":[{"type":"user","content":"Hello, how are you?"}]}`)); got.CWD != "" {
		t.Fatalf("hash directory was used as cwd: %+v", got)
	}
	if GeminiSessionRel("hash/not-chats/session-1.json") || GeminiSessionRel("hash/chats/notes.json") {
		t.Fatal("non-session gemini path accepted")
	}
}
