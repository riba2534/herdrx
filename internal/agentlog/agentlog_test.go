package agentlog

import (
	"encoding/json"
	"strings"
	"testing"
)

// 这些 fixture 是**合成**的：形状抄自公开参考实现
// `stablyai/orca@e86cba888b2eb88241a9c12dc019962f9c74307e` 的
// `transcript-line-decoders-{claude,codex}.ts` 与真实 CLI 写盘的字段名，
// 但不含任何真实用户会话内容。仓库里没有、也不应该有真实 transcript 样本。

func claudeContext(offset int64) LineContext {
	return LineContext{Agent: AgentClaude, Offset: offset}
}

func codexContext(offset int64, responseItemPath bool) LineContext {
	return LineContext{Agent: AgentCodex, SessionID: "sess-1", Offset: offset, ResponseItemPath: responseItemPath}
}

func decodeOne(t *testing.T, context LineContext, line string) (*Record, bool) {
	t.Helper()
	return DecodeLine(context, line)
}

func roles(blocks []Block) []string {
	out := make([]string, 0, len(blocks))
	for _, block := range blocks {
		out = append(out, block.Type)
	}
	return out
}

func TestClaudeDecodesUserAndAssistantTurns(t *testing.T) {
	user := `{"type":"user","uuid":"u-1","timestamp":"2026-09-13T04:25:03.000Z","cwd":"/srv/proj","message":{"role":"user","content":"跑一下测试"}}`
	record, skipped := decodeOne(t, claudeContext(0), user)
	if skipped || record == nil {
		t.Fatalf("user record not decoded: skipped=%v record=%v", skipped, record)
	}
	if record.Role != RoleUser || record.ID != "u-1" || record.At != "2026-09-13T04:25:03Z" {
		t.Fatalf("unexpected user record: %+v", record)
	}
	if len(record.Blocks) != 1 || record.Blocks[0].Text != "跑一下测试" {
		t.Fatalf("unexpected user blocks: %+v", record.Blocks)
	}

	assistant := `{"type":"assistant","uuid":"a-1","timestamp":"2026-09-13T04:25:04.000Z","cwd":"/srv/proj","message":{"role":"assistant","content":[{"type":"thinking","thinking":"先看目录"},{"type":"text","text":"好的。"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"pnpm test"}}]}}`
	record, skipped = decodeOne(t, claudeContext(10), assistant)
	if skipped || record == nil {
		t.Fatalf("assistant record not decoded: skipped=%v", skipped)
	}
	if record.Role != RoleAssistant {
		t.Fatalf("assistant role = %q", record.Role)
	}
	// thinking 是设计上省略，不是格式畸形，所以这里只剩 text + tool-call。
	if got := roles(record.Blocks); len(got) != 2 || got[0] != BlockText || got[1] != BlockToolCall {
		t.Fatalf("assistant block types = %v", got)
	}
	call := record.Blocks[1]
	if call.CallID != "toolu_1" || call.Name != "Bash" || string(call.Input) != `{"command":"pnpm test"}` {
		t.Fatalf("unexpected tool call: %+v", call)
	}
}

// 契约 §5：`tool` 是派生角色。一条 user 记录只要 blocks 全是 tool-result 就是工具结果，
// 只要还夹着任何非 tool-result 块，它就仍然是用户问题。
func TestClaudeDerivesToolRoleOnlyForPureToolResults(t *testing.T) {
	pure := `{"type":"user","uuid":"u-2","cwd":"/srv/proj","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok","is_error":false}]}}`
	record, skipped := decodeOne(t, claudeContext(0), pure)
	if skipped || record == nil {
		t.Fatal("tool result record not decoded")
	}
	if record.Role != RoleTool {
		t.Fatalf("pure tool-result user record role = %q, want tool", record.Role)
	}
	if record.Blocks[0].CallID != "toolu_1" || record.Blocks[0].Output != "ok" || record.Blocks[0].IsError {
		t.Fatalf("unexpected tool-result block: %+v", record.Blocks[0])
	}

	mixed := `{"type":"user","uuid":"u-3","cwd":"/srv/proj","message":{"role":"user","content":[{"type":"text","text":"顺便看看这个"},{"type":"tool_result","tool_use_id":"toolu_2","content":"boom","is_error":true}]}}`
	record, _ = decodeOne(t, claudeContext(0), mixed)
	if record == nil || record.Role != RoleUser {
		t.Fatalf("mixed user record role = %v, want user", record)
	}
	if !record.Blocks[1].IsError {
		t.Fatal("tool error flag lost")
	}
}

// 注入轮次过滤是**设计行为**，不能计入 skipped；反之真正解不出来的形状必须计进去。
func TestClaudeSeparatesDesignElisionFromFormatDrift(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantNil  bool
		wantSkip bool
	}{
		{"meta injection", `{"type":"user","uuid":"u-4","isMeta":true,"cwd":"/srv/proj","message":{"role":"user","content":"<system-reminder>灌进来的</system-reminder>"}}`, true, false},
		{"compact summary", `{"type":"user","uuid":"u-5","isCompactSummary":true,"cwd":"/srv/proj","message":{"role":"user","content":"摘要注入"}}`, true, false},
		{"thinking only", `{"type":"assistant","uuid":"a-2","cwd":"/srv/proj","message":{"role":"assistant","content":[{"type":"thinking","thinking":"只有思想链"}]}}`, true, false},
		{"metadata record", `{"type":"summary","summary":"会话摘要","leafUuid":"u-1"}`, true, false},
		{"unknown block", `{"type":"assistant","uuid":"a-3","cwd":"/srv/proj","message":{"role":"assistant","content":[{"type":"server_tool_use","id":"x"}]}}`, true, true},
		{"missing content", `{"type":"assistant","uuid":"a-4","cwd":"/srv/proj","message":{"role":"assistant"}}`, true, true},
		{"malformed json", `{"type":"user","uuid":"u-9",`, true, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			record, skipped := decodeOne(t, claudeContext(0), testCase.line)
			if testCase.wantNil && record != nil {
				t.Fatalf("expected no record, got %+v", record)
			}
			if skipped != testCase.wantSkip {
				t.Fatalf("skipped = %v, want %v", skipped, testCase.wantSkip)
			}
		})
	}
}

// 注入轮次里的 tool-result 是真实输出，不能被过滤掉。
func TestClaudeKeepsToolResultsInsideInjectedTurn(t *testing.T) {
	line := `{"type":"user","uuid":"u-6","isMeta":true,"cwd":"/srv/proj","message":{"role":"user","content":[{"type":"text","text":"注入文本"},{"type":"tool_result","tool_use_id":"toolu_3","content":"真实输出"}]}}`
	record, _ := decodeOne(t, claudeContext(0), line)
	if record == nil || record.Role != RoleTool {
		t.Fatalf("injected tool-result record = %+v", record)
	}
	if len(record.Blocks) != 1 || record.Blocks[0].Output != "真实输出" {
		t.Fatalf("injected turn tool-result blocks = %+v", record.Blocks)
	}
}

func TestClaudeInterruptBecomesSystemRecord(t *testing.T) {
	line := `{"type":"user","uuid":"u-7","cwd":"/srv/proj","message":{"role":"user","content":"[Request interrupted by user]"}}`
	line = strings.Replace(line, `"uuid":"u-7"`, `"uuid":"u-7","interruptedMessageId":"u-7"`, 1)
	record, skipped := decodeOne(t, claudeContext(0), line)
	if skipped || record == nil || record.Role != RoleSystem {
		t.Fatalf("interrupt record = %+v skipped=%v", record, skipped)
	}
	if len(record.Blocks) != 1 || record.Blocks[0].Text != InterruptedNotice {
		t.Fatalf("interrupt blocks = %+v", record.Blocks)
	}
}

// 同一个 uuid 重复出现 = 同一条记录被重发（内容可能已更新），必须原地更新而不是追加。
// 但**内容相同的两次真实提问**必须留下两条 —— 这正是禁止按文本 hash 去重的原因。
func TestUpsertUpdatesSameIDAndKeepsRepeatedPrompts(t *testing.T) {
	first := `{"type":"assistant","uuid":"a-1","cwd":"/srv/proj","message":{"role":"assistant","content":[{"type":"text","text":"先说一半"}]}}`
	second := `{"type":"assistant","uuid":"a-1","cwd":"/srv/proj","message":{"role":"assistant","content":[{"type":"text","text":"补齐后的完整回答"}]}}`
	data := []byte(first + "\n" + second + "\n")
	result := DecodeRange(claudeContext(0), data, 0)
	records := UpsertEntries(result.Entries)
	if len(records) != 1 {
		t.Fatalf("re-delivered uuid produced %d records, want 1", len(records))
	}
	if records[0].Blocks[0].Text != "补齐后的完整回答" {
		t.Fatalf("upsert did not take the latest version: %+v", records[0])
	}

	repeat := `{"type":"user","uuid":"u-a","cwd":"/srv/proj","message":{"role":"user","content":"再跑一次"}}`
	repeat2 := `{"type":"user","uuid":"u-b","cwd":"/srv/proj","message":{"role":"user","content":"再跑一次"}}`
	result = DecodeRange(claudeContext(0), []byte(repeat+"\n"+repeat2+"\n"), 0)
	records = UpsertEntries(result.Entries)
	if len(records) != 2 {
		t.Fatalf("identical repeated prompts collapsed into %d records, want 2", len(records))
	}
}

// 尾部未闭合的半行不解析、不产出、游标也不推进。
func TestDecodeRangeLeavesPartialTailLineUnconsumed(t *testing.T) {
	complete := `{"type":"user","uuid":"u-1","cwd":"/srv/proj","message":{"role":"user","content":"完整"}}`
	partial := `{"type":"assistant","uuid":"a-1","cwd":"/srv/proj","message":{"role":"ass`
	data := []byte(complete + "\n" + partial)
	result := DecodeRange(claudeContext(0), data, 0)
	if len(result.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(result.Entries))
	}
	if result.Consumed != int64(len(complete)+1) {
		t.Fatalf("consumed = %d, want %d", result.Consumed, len(complete)+1)
	}
}

func TestClaudeProjectDirIsLossyAndOnlyNarrows(t *testing.T) {
	cases := map[string]string{
		"/srv/proj":       "-srv-proj",
		"/home/x/.claude": "-home-x--claude",
		"/srv/proj/":      "-srv-proj",
		"/":               "-",
		"/a-b":            "-a-b",
		"/a/b":            "-a-b",
		"/srv/项目":         "-srv---",
	}
	for cwd, want := range cases {
		if got := ClaudeProjectDir(cwd); got != want {
			t.Fatalf("ClaudeProjectDir(%q) = %q, want %q", cwd, got, want)
		}
	}
	// 编码碰撞是事实：`/a-b` 与 `/a/b` 同码，所以目录名永远不能证明 cwd 归属。
	if ClaudeProjectDir("/a-b") != ClaudeProjectDir("/a/b") {
		t.Fatal("expected the documented encoding collision")
	}
}

func TestClaudeSessionFileRejectsUnsafeNames(t *testing.T) {
	allowed := []string{"0f3a.jsonl", "session-1.jsonl", "ABC.def.jsonl"}
	rejected := []string{".hidden.jsonl", "..jsonl", "sub/dir.jsonl", "a.txt", "", "x.jsonl ", "../x.jsonl", "-x.jsonl"}
	for _, name := range allowed {
		if !ClaudeSessionFile(name) {
			t.Fatalf("ClaudeSessionFile(%q) = false, want true", name)
		}
	}
	for _, name := range rejected {
		if ClaudeSessionFile(name) {
			t.Fatalf("ClaudeSessionFile(%q) = true, want false", name)
		}
	}
	if !CodexSessionFile("rollout-2026-09-13T04-25-03-sess.jsonl") {
		t.Fatal("codex rollout name rejected")
	}
	if CodexSessionFile("session.jsonl") {
		t.Fatal("non-rollout name accepted for codex")
	}
	if got := SessionStem("rollout-1.jsonl"); got != "rollout-1" {
		t.Fatalf("SessionStem = %q", got)
	}
}

func TestClaudeFileShapeReadsRealCWD(t *testing.T) {
	data := []byte(`{"type":"summary","summary":"x"}` + "\n" +
		`{"type":"user","uuid":"u-1","cwd":"/srv/proj","message":{"role":"user","content":"hi"}}` + "\n")
	shape := ReadFileShape(AgentClaude, data)
	if shape.CWD != "/srv/proj" {
		t.Fatalf("shape.CWD = %q", shape.CWD)
	}
	if shape.SessionID != "" || shape.ResponseItemPath {
		t.Fatalf("unexpected claude shape: %+v", shape)
	}
	if got := ReadFileShape(AgentClaude, []byte(`{"type":"user","uuid":"u-1","cwd":"relative/path"}`+"\n")); got.CWD != "" {
		t.Fatalf("relative cwd accepted: %+v", got)
	}
}

func TestCodexResponseItemPathAndUnwrappedForm(t *testing.T) {
	head := `{"type":"session_meta","timestamp":"2026-09-13T04:25:00.000Z","payload":{"id":"sess-1","cwd":"/srv/proj","cli_version":"0.1.0"}}`
	shape := ReadFileShape(AgentCodex, []byte(head+"\n"))
	if shape.CWD != "/srv/proj" || shape.SessionID != "sess-1" || shape.ResponseItemPath {
		t.Fatalf("session_meta shape = %+v", shape)
	}
	withResponse := ReadFileShape(AgentCodex, []byte(head+"\n"+`{"type":"response_item","payload":{"type":"message","role":"user","content":[]}}`+"\n"))
	if !withResponse.ResponseItemPath {
		t.Fatal("response_item frame not detected")
	}
	unwrapped := ReadFileShape(AgentCodex, []byte(head+"\n"+`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"裸"}]}`+"\n"))
	if !unwrapped.ResponseItemPath {
		t.Fatal("unwrapped response_item frame not detected")
	}

	context := codexContext(0, false)
	wrapped, skipped := decodeOne(t, context, `{"type":"response_item","timestamp":"2026-09-13T04:25:03Z","payload":{"type":"message","id":"m1","role":"user","content":[{"type":"input_text","text":"跑一下测试"}]}}`)
	if skipped || wrapped == nil || wrapped.Role != RoleUser || wrapped.ID != "sess-1:0" {
		t.Fatalf("wrapped response_item message = %+v skipped=%v", wrapped, skipped)
	}
	bare, skipped := decodeOne(t, context, `{"type":"message","timestamp":"2026-09-13T04:25:04Z","role":"assistant","content":[{"type":"output_text","text":"裸形态也能解"}]}`)
	if skipped || bare == nil || bare.Role != RoleAssistant || bare.Blocks[0].Text != "裸形态也能解" {
		t.Fatalf("unwrapped response_item = %+v skipped=%v", bare, skipped)
	}
}

// 双记形态：文件里出现过 response_item 时，event_msg 的消息型 payload 全部忽略，
// 但生命周期信号 turn_aborted 必须保留。
func TestCodexCanonicalPathIsPerFile(t *testing.T) {
	userMessage := `{"type":"event_msg","payload":{"type":"user_message","message":"重复的用户消息"}}`
	aborted := `{"type":"event_msg","payload":{"type":"turn_aborted"}}`

	if record, _ := decodeOne(t, codexContext(0, true), userMessage); record != nil {
		t.Fatalf("event_msg message kept on the response_item path: %+v", record)
	}
	if record, _ := decodeOne(t, codexContext(0, false), userMessage); record == nil || record.Role != RoleUser {
		t.Fatalf("event_msg message dropped without response_item path: %+v", record)
	}
	for _, path := range []bool{true, false} {
		record, skipped := decodeOne(t, codexContext(0, path), aborted)
		if skipped || record == nil || record.Role != RoleSystem {
			t.Fatalf("turn_aborted lost (responseItemPath=%v): %+v", path, record)
		}
	}
}

func TestCodexToolCallsAndResults(t *testing.T) {
	call := `{"type":"response_item","timestamp":"2026-09-13T04:25:05Z","payload":{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":[\"ls\"]}"}}`
	record, skipped := decodeOne(t, codexContext(0, false), call)
	if skipped || record == nil || record.Role != RoleAssistant {
		t.Fatalf("function_call = %+v", record)
	}
	block := record.Blocks[0]
	if block.Type != BlockToolCall || block.CallID != "call_1" || block.Name != "shell" {
		t.Fatalf("function_call block = %+v", block)
	}
	if string(block.Input) != `"{\"command\":[\"ls\"]}"` {
		t.Fatalf("arguments passed through verbatim? got %s", block.Input)
	}

	output := `{"type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":{"success":false,"content":"炸了"}}}`
	record, _ = decodeOne(t, codexContext(0, false), output)
	if record == nil || record.Role != RoleTool {
		t.Fatalf("function_call_output = %+v", record)
	}
	if record.Blocks[0].Output != "炸了" || !record.Blocks[0].IsError {
		t.Fatalf("function_call_output block = %+v", record.Blocks[0])
	}
}

// reasoning 按契约省略，不计入 skipped；用户轮里的技能展开是模型上下文，不是用户问题。
func TestCodexDropsReasoningAndSkillContext(t *testing.T) {
	reasoning := `{"type":"response_item","payload":{"type":"reasoning","text":"内部推理","summary":[]}}`
	if record, skipped := decodeOne(t, codexContext(0, false), reasoning); record != nil || skipped {
		t.Fatalf("reasoning = %+v skipped=%v, want (nil,false)", record, skipped)
	}
	skill := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<skill>展开的技能</skill>"},{"type":"input_text","text":"真正的问题"}]}}`
	record, _ := decodeOne(t, codexContext(0, false), skill)
	if record == nil || len(record.Blocks) != 1 || record.Blocks[0].Text != "真正的问题" {
		t.Fatalf("skill context not filtered: %+v", record)
	}
}

func TestCodexMetadataIsNotCountedAsSkipped(t *testing.T) {
	for _, line := range []string{
		`{"type":"session_meta","payload":{"id":"sess-1","cwd":"/srv/proj"}}`,
		`{"type":"turn_context","payload":{"cwd":"/srv/proj","model":"gpt"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{}}}`,
		`{"type":"event_msg","payload":{"type":"task_started"}}`,
	} {
		if record, skipped := decodeOne(t, codexContext(0, false), line); record != nil || skipped {
			t.Fatalf("metadata %s produced record=%+v skipped=%v", line, record, skipped)
		}
	}
	if _, skipped := decodeOne(t, codexContext(0, false), `{"type":"event_msg","payload":`); !skipped {
		t.Fatal("malformed codex line not counted as skipped")
	}
}

// 记录 id 里的字节 offset 让重复内容天然得到两条不同记录。
func TestCodexRecordIDUsesByteOffset(t *testing.T) {
	line := `{"type":"event_msg","payload":{"type":"user_message","message":"再跑一次"}}`
	first, _ := decodeOne(t, codexContext(0, false), line)
	second, _ := decodeOne(t, codexContext(120, false), line)
	if first == nil || second == nil {
		t.Fatal("codex message not decoded")
	}
	if first.ID == second.ID {
		t.Fatalf("identical prompts share id %q; repeats must survive", first.ID)
	}
}

func TestElideOversizedBlockKeepsStructure(t *testing.T) {
	long := strings.Repeat("字", 100)
	record := Record{ID: "a", Role: RoleAssistant, Blocks: []Block{{Type: BlockText, Text: long}}}
	elided := ElideOversized(record, 30)
	if len(elided.Blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(elided.Blocks))
	}
	if len(elided.Blocks[0].Text) > 30 || !strings.HasPrefix(long, elided.Blocks[0].Text) {
		t.Fatalf("first block not truncated to a prefix: %q", elided.Blocks[0].Text)
	}
	if !strings.HasPrefix(elided.Blocks[1].Text, ElisionPrefix) {
		t.Fatalf("notice block = %q", elided.Blocks[1].Text)
	}
	// 截断必须落在 UTF-8 边界上：每个「字」是 3 字节，30 不是 3 的倍数。
	for _, block := range elided.Blocks {
		if !utf8Valid(block.Text) {
			t.Fatalf("truncation split a rune: %q", block.Text)
		}
	}
	if unchanged := ElideOversized(record, 4096); len(unchanged.Blocks) != 1 {
		t.Fatalf("small block was elided: %+v", unchanged.Blocks)
	}
}

func TestBlockMarshalsContractShape(t *testing.T) {
	cases := []struct {
		block Block
		want  string
	}{
		{Block{Type: BlockText, Text: "hi"}, `{"type":"text","text":"hi"}`},
		{Block{Type: BlockToolCall, Name: "Bash"}, `{"type":"tool-call","call_id":"","name":"Bash","input":null}`},
		{Block{Type: BlockToolResult, CallID: "c1", Output: "ok"}, `{"type":"tool-result","call_id":"c1","output":"ok","is_error":false}`},
	}
	for _, testCase := range cases {
		encoded, err := json.Marshal(testCase.block)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != testCase.want {
			t.Fatalf("marshal = %s, want %s", encoded, testCase.want)
		}
	}
	if _, err := json.Marshal(Block{Type: "nope"}); err == nil {
		t.Fatal("unknown block type marshalled instead of failing")
	}
}

func TestSupportedAgentsIsClosedSet(t *testing.T) {
	for _, agent := range []string{AgentClaude, AgentCodex, AgentDSH} {
		if !Supported(agent) {
			t.Fatalf("%s should be supported", agent)
		}
	}
	// 与 claude/codex 同格式族的这些 agent 在各自的 decoder 与日志根被真实验证前一律不支持。
	for _, agent := range []string{"openclaude", "grok", "omp", "gemini", "cursor", "", "CLAUDE", "DSH"} {
		if Supported(agent) {
			t.Fatalf("%q must not be claimed as supported", agent)
		}
	}
}

func utf8Valid(value string) bool {
	for _, runeValue := range value {
		if runeValue == '�' {
			return false
		}
	}
	return true
}
