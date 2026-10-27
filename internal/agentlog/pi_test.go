package agentlog

import (
	"strings"
	"testing"
)

func TestPiProjectDirMatchesTheWrappedEncoding(t *testing.T) {
	if got := PiProjectDir("/Users/shengtao/openlark"); got != "--Users-shengtao-openlark--" {
		t.Fatalf("dir = %q", got)
	}
	name := "2024-12-03T14-00-00-000Z_11111111-2222-4333-8444-555555555555.jsonl"
	if PiSessionID(name) != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("id = %q", PiSessionID(name))
	}
}

func TestDecodePiMessageAndToolResult(t *testing.T) {
	const thought = "PI_THOUGHT_SECRET"
	lines := []string{
		`{"type":"session","version":3,"id":"11111111-2222-4333-8444-555555555555","timestamp":"2024-12-03T14:00:00.000Z","cwd":"/srv/proj"}`,
		`{"type":"model_change","id":"m1","parentId":null,"timestamp":"2024-12-03T14:00:00.000Z","provider":"anthropic","modelId":"claude"}`,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2024-12-03T14:00:01.000Z","message":{"role":"user","content":"用户的问题"}}`,
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2024-12-03T14:00:02.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"` + thought + `"},{"type":"text","text":"先看文件"},{"type":"toolCall","id":"call_1","name":"bash","arguments":{"command":"ls"}}]}}`,
		`{"type":"message","id":"t1","parentId":"a1","timestamp":"2024-12-03T14:00:03.000Z","message":{"role":"toolResult","toolCallId":"call_1","toolName":"bash","content":[{"type":"text","text":"main.go"}],"isError":false}}`,
		`{"type":"message","id":"s1","parentId":"t1","timestamp":"2024-12-03T14:00:04.000Z","message":{"role":"system","content":"","sections":{"cwd":"/srv/proj"}}}`,
	}
	decoded := DecodeRange(LineContext{Agent: AgentPi, SessionID: "11111111-2222-4333-8444-555555555555"}, []byte(strings.Join(lines, "\n")+"\n"), 0)
	if decoded.Failure != nil || len(decoded.Entries) != len(lines) {
		t.Fatalf("range = %+v", decoded)
	}
	records := UpsertEntries(decoded.Entries)
	if len(records) != 3 {
		t.Fatalf("records = %+v", records)
	}
	if records[0].Role != RoleUser || records[0].ID != "u1" || records[0].Blocks[0].Text != "用户的问题" {
		t.Fatalf("user = %+v", records[0])
	}
	if records[1].Role != RoleAssistant || records[1].Blocks[0].Text != "先看文件" || records[1].Blocks[1].Name != "bash" || string(records[1].Blocks[1].Input) != `{"command":"ls"}` {
		t.Fatalf("assistant = %+v", records[1])
	}
	if records[2].Role != RoleTool || records[2].Blocks[0].CallID != "call_1" || records[2].Blocks[0].Output != "main.go" {
		t.Fatalf("tool = %+v", records[2])
	}
	encoded := string(mustJSON(t, &records[1]))
	if strings.Contains(encoded, thought) {
		t.Fatal("thinking leaked")
	}
	shape := ReadFileShape(AgentPi, []byte(lines[0]+"\n"))
	if shape.CWD != "/srv/proj" || shape.SessionID != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("shape = %+v", shape)
	}
}
