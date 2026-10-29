package agentlog

import (
	"os"
	"testing"
)

// testdata/claude_events.jsonl 是合成的脱敏样本：字段名取自 Claude Code 写盘的形状，
// 内容全部是 example 占位。它覆盖工作中插入的消息、排队操作、长文本粘贴包装、
// 压缩边界、API 重试、离开回顾、斜杠命令与本地命令输出。
func TestClaudeEventFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/claude_events.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	result := DecodeRange(claudeContext(0), data, 0)
	if result.Failure != nil || result.Skipped != 0 {
		t.Fatalf("failure = %v skipped = %d", result.Failure, result.Skipped)
	}
	type want struct {
		id, role, text string
		queued         bool
	}
	expected := []want{
		{"u-1", RoleUser, "把 example 模块的测试补齐", false},
		{"a-1", RoleAssistant, "先看一下现有测试。", false},
		{"u-2", RoleTool, "", false},
		{"q-1", RoleUser, "顺便看看 README", true},
		{"s-1", RoleSystem, "API 错误：Overloaded（正在重试）", false},
		{"s-3", RoleSystem, "API 错误：Overloaded（重试 3 次后放弃）", false},
		{"s-4", RoleSystem, "上下文已压缩", false},
		{"s-5", RoleSystem, "离开期间回顾：正在补齐 example 模块的测试。", false},
		{"u-4", RoleUser, "/model example-model", false},
		{"u-5", RoleSystem, "Set model to example-model", false},
		{"s-7", RoleUser, "/clear", false},
		{"u-6", RoleUser, "继续", false},
		{"u-7", RoleSystem, InterruptedNotice, false},
		{"a-2", RoleAssistant, "测试已补齐。", false},
	}
	records := UpsertEntries(result.Entries)
	if len(records) != len(expected) {
		ids := make([]string, 0, len(records))
		for _, record := range records {
			ids = append(ids, record.ID)
		}
		t.Fatalf("records = %v, want %d", ids, len(expected))
	}
	for index, item := range expected {
		record := records[index]
		if record.ID != item.id || record.Role != item.role || record.Queued != item.queued {
			t.Fatalf("record %d = %+v, want %+v", index, record, item)
		}
		if item.text != "" && (record.Blocks[0].Type != BlockText || record.Blocks[0].Text != item.text) {
			t.Fatalf("record %s text = %q, want %q", item.id, record.Blocks[0].Text, item.text)
		}
	}
}

// testdata/codex_events.jsonl 同样是合成样本：开发者指令、AGENTS.md / 环境 / 应用页面注入、
// 中断标记都不是用户输入；压缩检查点与 turn_aborted 变成 system 提示。
func TestCodexEventFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/codex_events.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	result := DecodeRange(codexContext(0, true), data, 0)
	if result.Failure != nil || result.Skipped != 0 {
		t.Fatalf("failure = %v skipped = %d", result.Failure, result.Skipped)
	}
	records := UpsertEntries(result.Entries)
	want := []struct{ role, block, text string }{
		{RoleUser, BlockText, "补齐 example 测试"},
		{RoleAssistant, BlockText, "先跑一下测试。"},
		{RoleAssistant, BlockToolCall, ""},
		{RoleTool, BlockToolResult, ""},
		{RoleSystem, BlockText, CompactedNotice},
		{RoleAssistant, BlockText, "测试已通过。"},
		{RoleSystem, BlockText, InterruptedNotice},
	}
	if len(records) != len(want) {
		t.Fatalf("records = %+v", records)
	}
	for index, item := range want {
		record := records[index]
		if record.Role != item.role || len(record.Blocks) != 1 || record.Blocks[0].Type != item.block || record.Blocks[0].Text != item.text {
			t.Fatalf("record %d = %+v, want %+v", index, record, item)
		}
	}
}

func TestClaudeUserTextUnwrapsPastes(t *testing.T) {
	cases := map[string]string{
		`<pasted_content id="x1">第一段</pasted_content id="x1"> 后记`: "第一段 后记",
		`<\pasted_content id="x2">嵌套</\/pasted_content id="x2">`:  "嵌套",
		"<command-message>review</command-message>":               "/review",
		"普通 <b>标签</b> 保留":                                         "普通 <b>标签</b> 保留",
	}
	for input, want := range cases {
		if got, system := claudeUserText(input); got != want || system {
			t.Fatalf("claudeUserText(%q) = %q/%v, want %q", input, got, system, want)
		}
	}
	if got, system := claudeUserText("<task-notification><summary>构建完成</summary></task-notification>"); got != "后台任务：构建完成" || !system {
		t.Fatalf("task notification = %q/%v", got, system)
	}
	// 带图的插入消息只保留文字：契约不输出图片引用。
	record, _ := DecodeLine(claudeContext(0), `{"type":"attachment","uuid":"q","attachment":{"type":"queued_command","commandMode":"prompt","prompt":[{"type":"text","text":"带图"},{"type":"image","source":{}}]}}`)
	if record == nil || !record.Queued || len(record.Blocks) != 1 || record.Blocks[0].Text != "带图" {
		t.Fatalf("queued record = %+v", record)
	}
}
