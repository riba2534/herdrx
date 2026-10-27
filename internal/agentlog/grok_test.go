package agentlog

import (
	"strings"
	"testing"
)

func TestGrokPathRoundTrip(t *testing.T) {
	cwd := "/srv/proj"
	rel := GrokProjectDir(cwd) + "/11111111-2222-4333-8444-555555555555/chat_history.jsonl"
	if GrokCWD(rel) != cwd || GrokSessionID(rel) != "11111111-2222-4333-8444-555555555555" || !GrokHistoryRel(rel) {
		t.Fatalf("rel = %s cwd=%q id=%q", rel, GrokCWD(rel), GrokSessionID(rel))
	}
	if GrokHistoryRel(GrokProjectDir(cwd) + "/11111111-2222-4333-8444-555555555555/events.jsonl") {
		t.Fatal("events.jsonl was treated as the transcript")
	}
}

func TestDecodeGrokHistory(t *testing.T) {
	const thought = "GROK_REASONING_SECRET"
	lines := []string{
		`{"type":"system","content":"你是编码助手"}`,
		`{"type":"user","content":[{"type":"text","text":"项目说明"}],"synthetic_reason":"project_instructions"}`,
		`{"type":"user","content":[{"type":"text","text":"真正的问题"}],"prompt_index":1}`,
		`{"type":"reasoning","id":"r1","status":"done","summary":[{"type":"summary_text","text":"` + thought + `"}],"encrypted_content":"cipher"}`,
		`{"type":"assistant","content":"看一下","tool_calls":[{"id":"call_1","name":"read_file","arguments":"{\"path\":\"a.go\"}"}],"model_id":"grok","model_fingerprint":"fp","reasoning_effort":"low"}`,
		`{"type":"tool_result","tool_call_id":"call_1","content":"package main"}`,
	}
	decoded := DecodeRange(LineContext{Agent: AgentGrok, SessionID: "sess"}, []byte(strings.Join(lines, "\n")+"\n"), 0)
	records := UpsertEntries(decoded.Entries)
	if len(records) != 5 {
		t.Fatalf("records = %+v", records)
	}
	if records[0].Role != RoleSystem || records[0].Blocks[0].Text != "你是编码助手" {
		t.Fatalf("system = %+v", records[0])
	}
	if records[1].Role != RoleSystem || records[1].Blocks[0].Text != "项目说明" {
		t.Fatalf("synthetic = %+v", records[1])
	}
	if records[2].Role != RoleUser || records[2].Blocks[0].Text != "真正的问题" {
		t.Fatalf("user = %+v", records[2])
	}
	if records[3].Role != RoleAssistant || records[3].Blocks[0].Text != "看一下" || records[3].Blocks[1].Name != "read_file" || string(records[3].Blocks[1].Input) != `{"path":"a.go"}` {
		t.Fatalf("assistant = %+v", records[3])
	}
	if records[4].Role != RoleTool || records[4].Blocks[0].CallID != "call_1" || records[4].Blocks[0].Output != "package main" {
		t.Fatalf("tool = %+v", records[4])
	}
	blob := string(mustJSON(t, &records[3]))
	if strings.Contains(blob, thought) || strings.Contains(blob, "cipher") {
		t.Fatal("reasoning leaked")
	}
}
