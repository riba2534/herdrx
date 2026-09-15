package agentlog

import (
	"encoding/json"
	"strings"
	"testing"
)

// 这些 fixture 是**合成**的：形状抄自本机安装的 `@deepseek-ai/dsh@0.1.5-rc.2` 源码
// （dsh-session-persistence-jsonl 的物理行格式与文件命名、dsh-session-format-v1-to-v2
// 的 v2/v3 共用编解码、dsh-session-format-v2-to-v3 的 v3 准入、dsh-session 的事件词汇表），
// 但不含任何真实用户会话内容。仓库里没有、也不应该有真实 transcript 样本。

const (
	dshFixtureSessionID = "01J8ZQ4T7K3M9P2R5V6W7X8Y9Z"
	dshFixtureCWD       = "/srv/proj"
)

func dshHeaderLine() string {
	return `{"type":"session","version":3,"id":"` + dshFixtureSessionID + `","createdAt":1757742303000,"isSeeded":false,"delegationDepth":0,"cwd":"` + dshFixtureCWD + `"}`
}

func dshContext(offset int64) LineContext {
	return LineContext{Agent: AgentDSH, SessionID: dshFixtureSessionID, Offset: offset}
}

func dshDecode(t *testing.T, line string) *Record {
	t.Helper()
	record, skipped, failure := DecodeLineChecked(dshContext(0), line)
	if failure != nil {
		t.Fatalf("unexpected format failure: %v", failure)
	}
	if skipped {
		t.Fatalf("unexpected skipped: %s", line)
	}
	if record == nil {
		t.Fatalf("no record for: %s", line)
	}
	return record
}

// ── 合成 fixture：契约里的最小规范形状 ──

const (
	dshLineUser = `{"type":"user/message","seq":1,"time":1757742304000,"data":{"id":"msg-u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"跑一下测试"}]},"surfaceOp":"append"}`

	dshLineAssistant = `{"type":"assistant/message","seq":2,"time":1757742305000,"data":{"turn":1,"step":1,"message":{"id":"msg-a1","role":"assistant","source":{"kind":"model","provider":"deepseek","model":"deepseek-chat"},"content":[{"type":"reasoning","text":"先看目录"},{"type":"text","text":"好的，先跑测试。"},{"type":"tool-call","id":"call-1","name":"bash","arguments":"{\"command\":\"pnpm test\"}"}]}},"surfaceOp":"append"}`

	dshLineToolCall = `{"type":"tool/call","seq":3,"time":1757742306000,"data":{"turn":1,"step":1,"callId":"call-1","name":"bash","arguments":"{\"command\":\"pnpm test\"}"}}`

	dshLineToolResult = `{"type":"tool/result","seq":4,"time":1757742307000,"data":{"turn":1,"step":1,"message":{"id":"msg-t1","role":"user","source":{"kind":"tool","callId":"call-1"},"content":[{"type":"tool-result","toolCallId":"call-1","content":[{"type":"text","text":"42 passed"}],"isError":false}]}},"surfaceOp":"append","sourceEventSeqs":[3]}`

	dshLineSystem = `{"type":"system/message","seq":5,"time":1757742308000,"data":{"turn":1,"step":1,"message":{"id":"msg-s1","role":"system","source":{"kind":"plugin","plugin":"runtime-context"},"content":[{"type":"text","text":"运行环境：Linux aarch64"}]}},"surfaceOp":"append"}`
)

// header 是物理行，必须带 `type:"session"` 与精确键集合，归属只认 header 里真实 cwd。
func TestDSHFileShapeReadsHeaderCWDAndID(t *testing.T) {
	shape := ReadFileShape(AgentDSH, []byte(dshHeaderLine()+"\n"+dshLineUser+"\n"))
	if shape.CWD != dshFixtureCWD || shape.SessionID != dshFixtureSessionID {
		t.Fatalf("shape = %+v", shape)
	}
	if shape.DSHVersion != DSHFormatVersion {
		t.Fatalf("DSHVersion = %d", shape.DSHVersion)
	}

	// 缺 type 的 header 不是 DSH 物理 header：什么都不主张。
	noType := `{"version":3,"id":"s","createdAt":1,"isSeeded":false,"delegationDepth":0,"cwd":"/srv/proj"}`
	if got := ReadFileShape(AgentDSH, []byte(noType+"\n")); got.DSHVersion != 0 || got.CWD != "" || got.SessionID != "" {
		t.Fatalf("header without type accepted: %+v", got)
	}
	// 多出来的未审计字段同样是硬错误（上游 exactKeys）。
	extra := `{"type":"session","version":3,"id":"s","createdAt":1,"isSeeded":false,"delegationDepth":0,"cwd":"/srv/proj","sandboxMode":"x"}`
	if got := ReadFileShape(AgentDSH, []byte(extra+"\n")); got.CWD != "" || got.SessionID != "" {
		t.Fatalf("header with unexpected field accepted: %+v", got)
	}
	// 相对 cwd 不是绝对路径：拒绝，绝不猜归属。
	relative := `{"type":"session","version":3,"id":"s","createdAt":1,"isSeeded":false,"delegationDepth":0,"cwd":"srv/proj"}`
	if got := ReadFileShape(AgentDSH, []byte(relative+"\n")); got.CWD != "" {
		t.Fatalf("relative cwd accepted: %+v", got)
	}

	// 首行不是 header 时绝不往后面找替补：header 是行 0 是格式本身的一部分。
	window := []byte("这不是 JSON\n" + dshHeaderLine() + "\n")
	if got := ReadFileShape(AgentDSH, window); got.DSHVersion != 0 || got.CWD != "" || got.SessionID != "" {
		t.Fatalf("header was taken from a later line: %+v", got)
	}
	// 首帧明文之后还跟着事件行时，只读首行。
	frame := []byte(dshHeaderLine() + "\n" + dshLineUser + "\n" + dshLineAssistant + "\n")
	shape = ReadFileShape(AgentDSH, frame)
	if shape.CWD != dshFixtureCWD || shape.SessionID != dshFixtureSessionID || shape.DSHVersion != DSHFormatVersion {
		t.Fatalf("frame window shape = %+v", shape)
	}
	// 尾部半行不影响首行结论。
	if got := ReadFileShape(AgentDSH, []byte(dshHeaderLine()+"\n{\"type\":\"user/messa")); got.SessionID != dshFixtureSessionID {
		t.Fatalf("torn trailing line broke the shape: %+v", got)
	}
}

// 陌生代际只能报版本号，不能声明归属，更不能按 v3 硬解。
func TestDSHRejectsUnsupportedGeneration(t *testing.T) {
	v2 := `{"type":"session","version":2,"id":"sess-v2","createdAt":1,"isSeeded":false,"delegationDepth":0,"cwd":"/srv/proj"}`
	shape := ReadFileShape(AgentDSH, []byte(v2+"\n"))
	if shape.DSHVersion != 2 {
		t.Fatalf("DSHVersion = %d, want 2", shape.DSHVersion)
	}
	if shape.CWD != "" || shape.SessionID != "" {
		t.Fatalf("unsupported generation claimed ownership: %+v", shape)
	}

	_, _, failure := DecodeLineChecked(dshContext(0), v2)
	if failure == nil || failure.Reason != FormatUnsupportedVersion {
		t.Fatalf("failure = %+v", failure)
	}
}

// user/message 只有 source.kind=="user" 才是用户说的；注入上下文一律标成 system。
func TestDSHUserMessageNeverLetsInjectedContextImpersonateTheUser(t *testing.T) {
	user := dshDecode(t, dshLineUser)
	if user.Role != RoleUser || user.ID != dshFixtureSessionID+":1" {
		t.Fatalf("user record = %+v", user)
	}
	if len(user.Blocks) != 1 || user.Blocks[0].Text != "跑一下测试" {
		t.Fatalf("user blocks = %+v", user.Blocks)
	}

	// plugin / agent-instructions 等来源是系统注入，不能冒充用户提问。
	for _, kind := range []string{"plugin", "agent-instructions", "session-reference", "skill-catalog", "goal", "webhook", "subagent-report"} {
		line := `{"type":"user/message","seq":9,"time":1757742304000,"data":{"id":"m","role":"user","source":{"kind":"` + kind + `"},"content":[{"type":"text","text":"注进上下文的内容"}]},"surfaceOp":"append"}`
		record := dshDecode(t, line)
		if record.Role != RoleSystem {
			t.Fatalf("source kind %q produced role %q, want system", kind, record.Role)
		}
	}
}

// assistant 只发布规范可见文本：reasoning 省略，tool-call 让位给独立的 tool/call 事件。
func TestDSHAssistantMessagePublishesCanonicalVisibleTextOnly(t *testing.T) {
	record := dshDecode(t, dshLineAssistant)
	if record.Role != RoleAssistant || record.ID != dshFixtureSessionID+":2" {
		t.Fatalf("record = %+v", record)
	}
	if len(record.Blocks) != 1 || record.Blocks[0].Type != BlockText || record.Blocks[0].Text != "好的，先跑测试。" {
		t.Fatalf("blocks = %+v", record.Blocks)
	}

	// 同一条 assistant 消息里的 tool-call 不会再产出第二份工具卡片。
	data := []byte(dshLineAssistant + "\n" + dshLineToolCall + "\n")
	result := DecodeRange(dshContext(0), data, 0)
	records := UpsertEntries(result.Entries)
	if len(records) != 2 {
		t.Fatalf("records = %d, want assistant + tool/call", len(records))
	}
	toolCalls := 0
	for _, candidate := range records {
		for _, block := range candidate.Blocks {
			if block.Type == BlockToolCall {
				toolCalls++
			}
		}
	}
	if toolCalls != 1 {
		t.Fatalf("tool-call blocks = %d, want exactly 1", toolCalls)
	}
}

// 工具调用与结果靠 callId 关联，不需要读 sourceEventSeqs 也不需要跨行状态。
func TestDSHToolCallAndResultCorrelateByCallID(t *testing.T) {
	call := dshDecode(t, dshLineToolCall)
	if call.Role != RoleAssistant || call.Blocks[0].Type != BlockToolCall {
		t.Fatalf("call record = %+v", call)
	}
	block := call.Blocks[0]
	if block.CallID != "call-1" || block.Name != "bash" {
		t.Fatalf("call block = %+v", block)
	}
	// arguments 是字符串形式的 JSON：按结构发布，工具卡片才能展开。
	if string(block.Input) != `{"command":"pnpm test"}` {
		t.Fatalf("call input = %s", block.Input)
	}

	result := dshDecode(t, dshLineToolResult)
	if result.Role != RoleTool || result.Blocks[0].Type != BlockToolResult {
		t.Fatalf("result record = %+v", result)
	}
	if result.Blocks[0].CallID != block.CallID {
		t.Fatalf("call/result callId mismatch: %q vs %q", block.CallID, result.Blocks[0].CallID)
	}
	if result.Blocks[0].Output != "42 passed" || result.Blocks[0].IsError {
		t.Fatalf("result block = %+v", result.Blocks[0])
	}
}

// 内部记录（reasoning / attempt / request / 生命周期）不产出记录，也不计入 skipped。
func TestDSHInternalRecordsAreHiddenNotSkipped(t *testing.T) {
	hidden := []string{
		`{"type":"assistant/attempt","seq":9,"time":1,"data":{"turn":1,"step":1,"stream":[]}}`,
		`{"type":"request/header","seq":9,"time":1,"data":{"header":{"tools":[{"name":"bash"}]},"reason":"initial"}}`,
		`{"type":"step/start","seq":9,"time":1,"data":{"turn":1,"step":1}}`,
		`{"type":"compaction/summary","seq":9,"time":1,"data":{"compactionId":"c1","summary":[],"shadowedRange":{"start":0,"end":1},"shadowedSeqs":[0],"shadowedTokenCount":1,"provider":"deepseek","model":"m"}}`,
		`{"type":"feedback/message-put","seq":9,"time":1,"data":{"sessionId":"s","item":{"messageId":"m","rating":"positive","version":"1","createdAt":1,"updatedAt":1}}}`,
		`{"type":"feedback/message-delete","seq":9,"time":1,"data":{"sessionId":"s","messageId":"m"}}`,
		`{"type":"todo/write","seq":9,"time":1,"data":{"todos":[{"content":"x","status":"pending"}]}}`,
	}
	for _, line := range hidden {
		record, skipped, failure := DecodeLineChecked(dshContext(0), line)
		if record != nil || skipped || failure != nil {
			t.Fatalf("hidden event produced output: record=%v skipped=%v failure=%v (%s)", record, skipped, failure, line)
		}
	}
}

// 未知事件只有在明确标了 ignorable 时才允许跳过；否则必须报格式失败。
func TestDSHUnknownEventsFailFormatUnlessIgnorable(t *testing.T) {
	ignorable := `{"type":"future/thing","seq":9,"time":1,"ignorable":true,"data":{"x":1}}`
	if _, skipped, failure := DecodeLineChecked(dshContext(0), ignorable); skipped || failure != nil {
		t.Fatalf("ignorable unknown event: skipped=%v failure=%v", skipped, failure)
	}

	required := `{"type":"future/thing","seq":9,"time":1,"data":{"x":1}}`
	_, _, failure := DecodeLineChecked(dshContext(0), required)
	if failure == nil || failure.Reason != FormatUnsupportedEvent {
		t.Fatalf("failure = %+v", failure)
	}

	// v3 里 PTC 派发类型只允许以「可忽略」的形式出现。
	dispatch := `{"type":"tool/code-dispatch","seq":9,"time":1,"data":{"rootCallId":"r","parentCallId":"p","subCallId":"s","name":"n","isError":false,"content":[]}}`
	if _, _, failure := DecodeLineChecked(dshContext(0), dispatch); failure == nil || failure.Reason != FormatUnsupportedEvent {
		t.Fatalf("non-ignorable code-dispatch failure = %+v", failure)
	}
	dispatchIgnorable := strings.Replace(dispatch, `"data"`, `"ignorable":true,"data"`, 1)
	if _, _, failure := DecodeLineChecked(dshContext(0), dispatchIgnorable); failure != nil {
		t.Fatalf("ignorable code-dispatch failure = %+v", failure)
	}
}

// 格式失败必须停在那一行上：不越过它、不把它算成 skipped、之前解出的记录仍然有效。
func TestDSHFormatFailureStopsAtTheOffendingRow(t *testing.T) {
	bad := `{"type":"future/thing","seq":2,"time":1,"data":{}}`
	data := []byte(dshLineUser + "\n" + bad + "\n" + dshLineAssistant + "\n")
	result := DecodeRange(dshContext(0), data, 0)
	if result.Failure == nil || result.Failure.Reason != FormatUnsupportedEvent {
		t.Fatalf("failure = %+v", result.Failure)
	}
	if result.Failure.Offset != int64(len(dshLineUser)+1) {
		t.Fatalf("failure offset = %d", result.Failure.Offset)
	}
	if result.Consumed != result.Failure.Offset {
		t.Fatalf("consumed = %d, want the failing row start %d", result.Consumed, result.Failure.Offset)
	}
	if result.Skipped != 0 {
		t.Fatalf("skipped = %d, want 0", result.Skipped)
	}
	// 失败之前解出的记录仍然有效；失败之后的记录不得被发布。
	if len(result.Entries) != 1 || result.Entries[0].Record == nil || result.Entries[0].Record.Role != RoleUser {
		t.Fatalf("entries = %+v", result.Entries)
	}
	// 从同一个偏移重读只会得到同一个失败：调用方不能把它当空日志重试。
	again := DecodeRange(dshContext(0), data[result.Failure.Offset:], result.Failure.Offset)
	if again.Failure == nil {
		t.Fatal("re-reading from the failure offset must fail the same way")
	}
}

// 模型侧改写（surfaceOp replace）不落地，但也不是静默丢弃：条数报在 Replacements 上，
// 被遮蔽的原始记录仍然且只发布一次。
func TestDSHSurfaceReplaceIsCountedAndNeverDuplicated(t *testing.T) {
	// 压缩写下的检查点：replace 形态的 user/message。
	checkpoint := `{"type":"user/message","seq":5,"time":1757742309000,"data":{"id":"msg-cp","role":"user","source":{"kind":"plugin","plugin":"compact"},"content":[{"type":"text","text":"继续"}]},"surfaceOp":{"op":"replace","startSeq":1,"endSeq":4},"sourceEventSeqs":[1,4]}`
	// 裁剪写下的 replace 形态 tool/result：同一 callId 的第二份副本绝不能出现。
	pruned := `{"type":"tool/result","seq":6,"time":1757742310000,"data":{"turn":1,"step":1,"message":{"id":"msg-t1-pruned","role":"user","source":{"kind":"tool","callId":"call-1"},"content":[{"type":"tool-result","toolCallId":"call-1","content":[{"type":"text","text":"42…"}],"isError":false}]}},"surfaceOp":{"op":"replace","startSeq":4,"endSeq":4},"sourceEventSeqs":[4]}`

	data := []byte(strings.Join([]string{
		dshHeaderLine(), dshLineUser, dshLineAssistant, dshLineToolCall, dshLineToolResult, checkpoint, pruned,
	}, "\n") + "\n")
	result := DecodeRange(dshContext(0), data, 0)
	if result.Failure != nil {
		t.Fatalf("failure = %+v", result.Failure)
	}
	if result.Replacements != 2 {
		t.Fatalf("replacements = %d, want 2", result.Replacements)
	}
	records := UpsertEntries(result.Entries)
	if len(records) != 4 {
		t.Fatalf("records = %d, want user+assistant+call+result", len(records))
	}
	toolResults := 0
	for _, record := range records {
		for _, block := range candidateBlocks(record, BlockToolResult) {
			toolResults++
			if block.Output != "42 passed" {
				t.Fatalf("superseded replacement leaked into the transcript: %+v", block)
			}
		}
	}
	if toolResults != 1 {
		t.Fatalf("tool-result blocks = %d, want exactly 1", toolResults)
	}
	// replace 记录自己一条都不产出，也不计入 skipped。
	for _, entry := range result.Entries {
		if entry.Record != nil && entry.Record.ID == dshFixtureSessionID+":5" {
			t.Fatal("superseded checkpoint was published as a record")
		}
	}
	if result.Skipped != 0 {
		t.Fatalf("skipped = %d, want 0", result.Skipped)
	}
}

func candidateBlocks(record Record, blockType string) []Block {
	out := make([]Block, 0, len(record.Blocks))
	for _, block := range record.Blocks {
		if block.Type == blockType {
			out = append(out, block)
		}
	}
	return out
}

// 记录 id 由 seq 导出：跨帧分次读取稳定，且同一帧内不会因共用偏移而相撞。
func TestDSHRecordIDsAreSeqDerivedAndFrameSafe(t *testing.T) {
	data := []byte(dshLineUser + "\n" + dshLineAssistant + "\n")

	// 传输层逐 zstd 帧调用，只拿得到帧起始偏移：同一帧内所有行共用同一个 Offset。
	whole := DecodeRange(dshContext(0), data, 0)
	firstFrame := DecodeRange(dshContext(0), []byte(dshLineUser+"\n"), 0)
	secondFrame := DecodeRange(dshContext(0), []byte(dshLineAssistant+"\n"), 0)

	wholeIDs := map[string]bool{}
	for _, entry := range whole.Entries {
		wholeIDs[entry.Record.ID] = true
	}
	if len(wholeIDs) != 2 {
		t.Fatalf("ids collided inside one frame: %v", wholeIDs)
	}
	if firstFrame.Entries[0].Record.ID != dshFixtureSessionID+":1" {
		t.Fatalf("first id = %q", firstFrame.Entries[0].Record.ID)
	}
	if secondFrame.Entries[0].Record.ID != dshFixtureSessionID+":2" {
		t.Fatalf("second id = %q", secondFrame.Entries[0].Record.ID)
	}
	if !wholeIDs[firstFrame.Entries[0].Record.ID] || !wholeIDs[secondFrame.Entries[0].Record.ID] {
		t.Fatal("frame-by-frame ids differ from the whole-range ids")
	}
	// 同一个 Offset 下，两行的 id 必须不同 —— 用偏移当身份就会在这里合流。
	sameOffset := DecodeRange(dshContext(0), data, 4096)
	if sameOffset.Entries[0].Record.ID == sameOffset.Entries[1].Record.ID {
		t.Fatal("ids depend on LineContext.Offset")
	}
	// 会话 id 缺失时仍然逐行唯一。
	orphan := DecodeRange(LineContext{Agent: AgentDSH}, data, 4096)
	if orphan.Entries[0].Record.ID == orphan.Entries[1].Record.ID {
		t.Fatal("orphan ids collided")
	}
}

// system/message 与被打断的轮次都落到 system 角色，永远不会被渲染成用户提问。
func TestDSHSystemRecordsAndInterrupts(t *testing.T) {
	system := dshDecode(t, dshLineSystem)
	if system.Role != RoleSystem || system.Blocks[0].Text != "运行环境：Linux aarch64" {
		t.Fatalf("system record = %+v", system)
	}

	aborted := `{"type":"turn/end","seq":9,"time":1,"data":{"turn":1,"reason":{"kind":"aborted","reason":{"kind":"user"}}}}`
	record := dshDecode(t, aborted)
	if record.Role != RoleSystem || record.Blocks[0].Text != InterruptedNotice {
		t.Fatalf("aborted record = %+v", record)
	}
	interrupted := `{"type":"turn/end","seq":9,"time":1,"data":{"turn":1,"reason":{"kind":"interrupted"}}}`
	if record := dshDecode(t, interrupted); record.Role != RoleSystem || record.Blocks[0].Text != InterruptedNotice {
		t.Fatalf("interrupted record = %+v", record)
	}
	// 正常生命周期不产出记录。
	for _, reason := range []string{`{"kind":"completed"}`, `{"kind":"max-tokens"}`, `{"kind":"blocked"}`} {
		line := `{"type":"turn/end","seq":9,"time":1,"data":{"turn":1,"reason":` + reason + `}}`
		if record, skipped, failure := DecodeLineChecked(dshContext(0), line); record != nil || skipped || failure != nil {
			t.Fatalf("turn/end %s produced output", reason)
		}
	}
}

// content 里契约没有的块类型计入 skipped（暴露这次丢失），而不是伪装成解出来了。
func TestDSHUnknownContentBlockIsSkippedNotSilent(t *testing.T) {
	line := `{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","source":{"kind":"user"},"content":[{"type":"hologram","data":1}]},"surfaceOp":"append"}`
	record, skipped, failure := DecodeLineChecked(dshContext(0), line)
	if record != nil || !skipped || failure != nil {
		t.Fatalf("record=%v skipped=%v failure=%v", record, skipped, failure)
	}

	// content 根本不是数组：同样是消息形状却解不出来。
	malformed := `{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","source":{"kind":"user"},"content":"裸字符串"},"surfaceOp":"append"}`
	if _, skipped, _ := DecodeLineChecked(dshContext(0), malformed); !skipped {
		t.Fatal("non-array content must count as skipped")
	}
}

// 信封违规（缺 seq、非法 surfaceOp、surface 事件缺标记、未审计字段）是格式失败。
func TestDSHRejectsEnvelopeViolations(t *testing.T) {
	cases := map[string]string{
		"missing seq":       `{"type":"user/message","time":1,"data":{},"surfaceOp":"append"}`,
		"negative seq":      `{"type":"user/message","seq":-1,"time":1,"data":{},"surfaceOp":"append"}`,
		"float seq":         `{"type":"user/message","seq":1.5,"time":1,"data":{},"surfaceOp":"append"}`,
		"missing data":      `{"type":"user/message","seq":1,"time":1,"surfaceOp":"append"}`,
		"data not object":   `{"type":"user/message","seq":1,"time":1,"data":[],"surfaceOp":"append"}`,
		"ignorable false":   `{"type":"user/message","seq":1,"time":1,"data":{},"ignorable":false,"surfaceOp":"append"}`,
		"surface no marker": `{"type":"user/message","seq":1,"time":1,"data":{}}`,
		"bad surfaceOp":     `{"type":"user/message","seq":1,"time":1,"data":{},"surfaceOp":"prepend"}`,
		"replace forward":   `{"type":"user/message","seq":1,"time":1,"data":{},"surfaceOp":{"op":"replace","startSeq":2,"endSeq":2}}`,
		"unaudited field":   `{"type":"user/message","seq":1,"time":1,"data":{},"surfaceOp":"append","extra":1}`,
		"non-surface op":    `{"type":"turn/start","seq":1,"time":1,"data":{},"surfaceOp":"append"}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, failure := DecodeLineChecked(dshContext(0), line)
			if failure == nil || failure.Reason != FormatInvalidRow {
				t.Fatalf("failure = %+v", failure)
			}
		})
	}
}

// 尾部未闭合的半行不解析、不产出、游标也不推进（压缩帧之外的明文代际也走同一条路径）。
func TestDSHLeavesPartialTailLineUnconsumed(t *testing.T) {
	complete := dshLineUser
	partial := `{"type":"assistant/message","seq":2,"time":1,"data":{"turn":1,"step":1,"mess`
	data := []byte(complete + "\n" + partial)
	result := DecodeRange(dshContext(0), data, 0)
	if len(result.Entries) != 1 || result.Consumed != int64(len(complete)+1) {
		t.Fatalf("entries=%d consumed=%d", len(result.Entries), result.Consumed)
	}
	if result.Failure != nil {
		t.Fatalf("partial tail must not fail: %+v", result.Failure)
	}
}

func TestDSHSessionFileNames(t *testing.T) {
	if !DSHSessionFile("session.v3.jsonl") {
		t.Fatal("plaintext v3 name rejected")
	}
	if !DSHCompressedSessionFile("session.v3.jsonl.zstd") {
		t.Fatal("zstd v3 name rejected")
	}
	if DSHSessionFile("session.v3.jsonl.zstd") || DSHCompressedSessionFile("session.v3.jsonl") {
		t.Fatal("physical encoding suffixes are not interchangeable")
	}
	for _, name := range []string{
		"session.jsonl", "session.v0.jsonl", "session.v2.jsonl", "session.v4.jsonl",
		"session.V3.jsonl", "session.03.jsonl", "session.v3", "SESSION.v3.jsonl",
		"..jsonl", "session.v3.jsonl.tmp", "sub/session.v3.jsonl", "",
	} {
		if DSHSessionFile(name) || DSHCompressedSessionFile(name) {
			t.Fatalf("%q accepted as a supported DSH log", name)
		}
	}
	// 陌生代际仍然要能被认出来，调用方才能如实说「这个版本暂不支持」。
	if version, ok := dshLogVersion("session.v4.jsonl.zstd"); !ok || version != 4 {
		t.Fatalf("v4 generation not recognized: %d %v", version, ok)
	}
}

// seeded 会话带一段从父会话继承来的前缀，它不是本会话里用户当前说的话。
// 切分边界要读完整个文件才知道，分页读取拿不到，所以直接拒绝而不是解一半。
func TestDSHSeededSessionsAreRejectedNotHalfDecoded(t *testing.T) {
	seeded := `{"type":"session","version":3,"id":"` + dshFixtureSessionID + `","createdAt":1757742303000,"isSeeded":true,"delegationDepth":1,"cwd":"` + dshFixtureCWD + `"}`
	_, _, failure := DecodeLineChecked(dshContext(0), seeded)
	if failure == nil || failure.Reason != FormatUnsupportedSeeded {
		t.Fatalf("failure = %+v", failure)
	}

	// 整段读取必须在 header 这一行就停下：继承来的上下文一条都不能被当成用户提问发布。
	data := []byte(seeded + "\n" + dshLineUser + "\n" + dshLineAssistant + "\n")
	result := DecodeRange(dshContext(0), data, 0)
	if result.Failure == nil || result.Failure.Reason != FormatUnsupportedSeeded {
		t.Fatalf("range failure = %+v", result.Failure)
	}
	if result.Failure.Offset != 0 || result.Consumed != 0 {
		t.Fatalf("consumed=%d offset=%d, want both 0", result.Consumed, result.Failure.Offset)
	}
	if len(result.Entries) != 0 {
		t.Fatalf("seeded session published %d entries", len(result.Entries))
	}

	// 未 seeded 的同一份日志照常解出来：拒绝的是 seeded，不是这个形状。
	unseeded := strings.Replace(seeded, `"isSeeded":true`, `"isSeeded":false`, 1)
	ok := DecodeRange(dshContext(0), []byte(unseeded+"\n"+dshLineUser+"\n"), 0)
	if ok.Failure != nil || len(ok.Entries) != 2 {
		t.Fatalf("unseeded control failed: failure=%+v entries=%d", ok.Failure, len(ok.Entries))
	}
	// isSeeded 缺失同样是 header 准入错误（上游要求它必须是布尔）。
	missing := strings.Replace(seeded, `"isSeeded":true,`, "", 1)
	if _, _, failure := DecodeLineChecked(dshContext(0), missing); failure == nil || failure.Reason != FormatInvalidRow {
		t.Fatalf("missing isSeeded failure = %+v", failure)
	}
	// ReadFileShape 仍然如实报告归属：拒绝发生在解码，不在「这是谁的会话」这一步。
	shape := ReadFileShape(AgentDSH, []byte(seeded+"\n"))
	if shape.SessionID != dshFixtureSessionID || shape.CWD != dshFixtureCWD || shape.DSHVersion != DSHFormatVersion {
		t.Fatalf("seeded shape = %+v", shape)
	}
}

// sourceEventSeqs 是溯源坐标，必须被校验，不能当成可以不看的可选字段。
func TestDSHSourceEventSeqsIsValidated(t *testing.T) {
	build := func(sources string) string {
		return `{"type":"tool/result","seq":4,"time":1,"data":{"turn":1,"step":1,"message":{"id":"m","role":"user","source":{"kind":"tool","callId":"c"},"content":[{"type":"tool-result","toolCallId":"c","content":[{"type":"text","text":"x"}],"isError":false}]}},"surfaceOp":"append","sourceEventSeqs":` + sources + `}`
	}
	accepted := []string{
		`[3]`,
		`[0,1,2,3]`,
		`[[0,3]]`,
		`[0,[2,3]]`,
	}
	for _, sources := range accepted {
		record, skipped, failure := DecodeLineChecked(dshContext(0), build(sources))
		if failure != nil || record == nil || skipped {
			t.Fatalf("sourceEventSeqs %s rejected: record=%v skipped=%v failure=%v", sources, record, skipped, failure)
		}
	}
	rejected := map[string]string{
		"not an array":        `3`,
		"empty":               `[]`,
		"string member":       `["3"]`,
		"negative member":     `[-1]`,
		"self reference":      `[4]`,
		"forward reference":   `[5]`,
		"duplicate":           `[3,3]`,
		"duplicate via range": `[3,[3,3]]`,
		"range not a pair":    `[[3]]`,
		"range triple":        `[[0,1,2]]`,
		"range reversed":      `[[3,1]]`,
		"range past self":     `[[0,4]]`,
		"range overflows":     `[[0,3],[0,3]]`,
		"range unordered":     `[[2,3],[0,1]]`,
		"float member":        `[1.5]`,
	}
	for name, sources := range rejected {
		t.Run(name, func(t *testing.T) {
			_, _, failure := DecodeLineChecked(dshContext(0), build(sources))
			if failure == nil || failure.Reason != FormatInvalidRow {
				t.Fatalf("failure = %+v", failure)
			}
		})
	}
	// v3 明文规定 assistant/message 不允许带溯源坐标（它自己内嵌了 stream）。
	assistant := strings.Replace(dshLineAssistant, `"surfaceOp":"append"`, `"surfaceOp":"append","sourceEventSeqs":[1]`, 1)
	if _, _, failure := DecodeLineChecked(dshContext(0), assistant); failure == nil || failure.Reason != FormatInvalidRow {
		t.Fatalf("assistant/message sourceEventSeqs failure = %+v", failure)
	}
	// 非 surface 事件带溯源坐标同样已经在键规则里被拒。
	if _, _, failure := DecodeLineChecked(dshContext(0), `{"type":"turn/start","seq":4,"time":1,"data":{"turn":1},"sourceEventSeqs":[1]}`); failure == nil || failure.Reason != FormatInvalidRow {
		t.Fatalf("non-surface sourceEventSeqs failure = %+v", failure)
	}
}

// 契约形状不能漂移：块序列化出来的键必须与冻结契约一致。
func TestDSHBlocksMarshalContractShape(t *testing.T) {
	records := UpsertEntries(DecodeRange(dshContext(0), []byte(strings.Join([]string{
		dshLineUser, dshLineAssistant, dshLineToolCall, dshLineToolResult,
	}, "\n")+"\n"), 0).Entries)
	if len(records) != 4 {
		t.Fatalf("records = %d", len(records))
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, expected := range []string{
		`"role":"user"`, `"role":"assistant"`, `"role":"tool"`,
		`"type":"tool-call"`, `"call_id":"call-1"`, `"name":"bash"`,
		`"type":"tool-result"`, `"output":"42 passed"`, `"is_error":false`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("contract shape missing %s in %s", expected, text)
		}
	}
}
