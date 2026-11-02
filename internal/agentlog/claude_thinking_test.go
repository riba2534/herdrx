package agentlog

import (
	"os"
	"strings"
	"testing"
)

// testdata/claude_thinking.jsonl 是 Claude Code 2.1.316 真实会话里的三行（已按契约脱敏）：
// 带正文与 thinkingDurationMs 的 thinking 记录、紧随其后的 AskUserQuestion 调用、
// 只有 signature 的加密 thinking 记录。Claude 把同一条消息的每个块各写成一条记录。
func TestClaudeThinkingFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/claude_thinking.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	result := DecodeRange(claudeContext(0), data, 0)
	if result.Failure != nil || result.Skipped != 0 {
		t.Fatalf("failure = %v skipped = %d", result.Failure, result.Skipped)
	}
	records := UpsertEntries(result.Entries)
	if len(records) != 2 {
		t.Fatalf("records = %+v, want the thinking record and the tool call", records)
	}
	thinking := records[0]
	if thinking.ID != "a9fc4ba6-adbf-4bc9-9e2b-fc6145f60938" || thinking.Role != RoleAssistant || len(thinking.Blocks) != 1 {
		t.Fatalf("thinking record = %+v", thinking)
	}
	block := thinking.Blocks[0]
	if block.Type != BlockThinking || !strings.HasPrefix(block.Text, "The user is asking me to call the AskUserQuestion tool") || block.DurationMS == nil || *block.DurationMS != 2922 {
		t.Fatalf("thinking block = %+v", block)
	}
	if call := records[1]; call.Blocks[0].Type != BlockToolCall || call.Blocks[0].Name != "AskUserQuestion" {
		t.Fatalf("tool call record = %+v", call)
	}
}

// 去掉真实行里的 thinkingDurationMs：块照常输出，只是没有耗时。
func TestClaudeThinkingWithoutDuration(t *testing.T) {
	data, err := os.ReadFile("testdata/claude_thinking.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	line, _, _ := strings.Cut(string(data), "\n")
	stripped := strings.Replace(line, `"thinkingDurationMs":2922,`, "", 1)
	record, skipped := decodeOne(t, claudeContext(0), stripped)
	if stripped == line || skipped || record == nil || len(record.Blocks) != 1 || record.Blocks[0].Type != BlockThinking || record.Blocks[0].DurationMS != nil {
		t.Fatalf("record = %+v skipped = %v", record, skipped)
	}
}

// 耗时只挂在第一个 thinking 块上；不是非负整数的耗时当作缺失；只有 signature 的块不输出。
func TestClaudeThinkingDurationRules(t *testing.T) {
	line := func(duration string, content string) string {
		return `{"type":"assistant","uuid":"a-t","thinkingDurationMs":` + duration + `,"message":{"role":"assistant","content":[` + content + `]}}`
	}
	both := `{"type":"thinking","thinking":"","signature":"s0"},{"type":"thinking","thinking":"第一段","signature":"s1"},{"type":"text","text":"好的"},{"type":"thinking","thinking":"第二段","signature":"s2"}`
	record, _ := decodeOne(t, claudeContext(0), line("1500", both))
	if record == nil || len(record.Blocks) != 3 {
		t.Fatalf("record = %+v", record)
	}
	if first := record.Blocks[0]; first.Type != BlockThinking || first.Text != "第一段" || first.DurationMS == nil || *first.DurationMS != 1500 {
		t.Fatalf("first thinking = %+v", first)
	}
	if second := record.Blocks[2]; second.Type != BlockThinking || second.Text != "第二段" || second.DurationMS != nil {
		t.Fatalf("second thinking = %+v", second)
	}
	if zero, _ := decodeOne(t, claudeContext(0), line("0", both)); zero.Blocks[0].DurationMS == nil || *zero.Blocks[0].DurationMS != 0 {
		t.Fatalf("zero duration = %+v", zero.Blocks[0])
	}
	for _, invalid := range []string{"-1", "1.5", `"1500"`, "null", "1e300"} {
		record, _ := decodeOne(t, claudeContext(0), line(invalid, both))
		if record == nil || record.Blocks[0].DurationMS != nil {
			t.Fatalf("duration %s = %+v", invalid, record)
		}
	}
	signatureOnly := `{"type":"thinking","thinking":"   ","signature":"s"}`
	if record, skipped := decodeOne(t, claudeContext(0), line("900", signatureOnly)); record != nil || skipped {
		t.Fatalf("signature-only thinking = %+v skipped = %v", record, skipped)
	}
}

// 思考正文与 text 块共用单块上限和截断提示。
func TestClaudeThinkingIsElidedLikeText(t *testing.T) {
	long := strings.Repeat("想", MaxBlockBytes)
	line := `{"type":"assistant","uuid":"a-long","thinkingDurationMs":10,"message":{"role":"assistant","content":[{"type":"thinking","thinking":"` + long + `"}]}}`
	result := DecodeRange(claudeContext(0), []byte(line+"\n"), 0)
	records := UpsertEntries(result.Entries)
	if len(records) != 1 || len(records[0].Blocks) != 2 {
		t.Fatalf("records = %d blocks", len(records[0].Blocks))
	}
	thinking, note := records[0].Blocks[0], records[0].Blocks[1]
	if thinking.Type != BlockThinking || len(thinking.Text) > MaxBlockBytes || thinking.DurationMS == nil || *thinking.DurationMS != 10 {
		t.Fatalf("thinking = %d bytes, duration %v", len(thinking.Text), thinking.DurationMS)
	}
	if note.Type != BlockText || !strings.HasPrefix(note.Text, ElisionPrefix) {
		t.Fatalf("note = %+v", note)
	}
}
