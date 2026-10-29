package agentlog

import (
	"strconv"
	"strings"
)

// Codex 的两种记录形状。同一份会话文件可能同时出现 `response_item` 与 `event_msg`，
// 描述同一个轮次；也可能出现旧式「未包裹的 response_item」（记录本身就是 item，
// 没有 payload 外壳）。
const (
	codexRecordResponseItem = "response_item"
	codexRecordEventMsg     = "event_msg"
	codexEventTurnAborted   = "turn_aborted"
	codexRecordCompacted    = "compacted"
)

// decodeCodexOutcome 解码一行 Codex 或 traex 日志。history_mutation 的 append
// 可以在同一行产出多条记录；replace 是压缩，整行跳过。
func decodeCodexOutcome(context LineContext, line string) lineOutcome {
	record := parseJSONObject(line)
	if record == nil {
		return lineOutcome{skipped: true}
	}
	if extractString(record["type"]) == "history_mutation" {
		return decodeHistoryMutation(context, record)
	}
	decoded, skipped := decodeCodexLine(context, line)
	return lineOutcome{record: decoded, skipped: skipped}
}

func decodeHistoryMutation(context LineContext, record map[string]any) lineOutcome {
	payload := asRecord(record["payload"])
	if payload == nil || extractString(payload["operation"]) != "append" {
		return lineOutcome{}
	}
	items, _ := payload["items"].([]any)
	at := parseTimestamp(record["timestamp"])
	records := make([]*Record, 0, len(items))
	for index, value := range items {
		item := asRecord(value)
		if item == nil {
			continue
		}
		switch extractString(item["type"]) {
		case "reasoning", "trae_extra_info":
			continue
		case "message":
			if extractString(item["role"]) == "developer" {
				continue
			}
		}
		id := extractString(item["id"])
		if id == "" {
			id = context.SessionID + ":" + strconv.FormatInt(context.Offset, 10) + ":" + strconv.Itoa(index)
		}
		decoded, _ := codexItem(item, id, at)
		if decoded != nil {
			records = append(records, decoded)
		}
	}
	if len(records) == 0 {
		return lineOutcome{}
	}
	return lineOutcome{record: records[0], records: records}
}

// decodeCodexLine 把一行 Codex JSONL 解码成一条记录。
//
// 双记形态的规范路径按**文件**判定，由 LineContext.ResponseItemPath 传入：
// 该文件出现过任何 response_item 帧时，消息一律走 response_item，event_msg 里的
// 消息型 payload（user_message / agent_message / item_completed）全部忽略；
// 完全没有 response_item 帧时才走 event_msg。生命周期信号 `turn_aborted` 在任何
// 情况下都保留。这里刻意**不做全局文本去重** —— 同源重复的 prompt 必须留下两条。
func decodeCodexLine(context LineContext, line string) (*Record, bool) {
	record := parseJSONObject(line)
	if record == nil {
		return nil, true
	}
	id := codexRecordID(context)
	at := parseTimestamp(record["timestamp"])

	payload := asRecord(record["payload"])
	if extractString(record["type"]) == codexRecordCompacted {
		// 上下文压缩检查点：payload 是给模型的摘要，只显示一条状态提示。
		return &Record{ID: id, Role: RoleSystem, At: at, Blocks: []Block{{Type: BlockText, Text: CompactedNotice}}}, false
	}
	if payload == nil {
		// 旧式未包裹形态：整条记录本身就是 response_item。
		return codexItem(record, id, at)
	}
	switch extractString(record["type"]) {
	case codexRecordResponseItem:
		return codexItem(payload, id, at)
	case codexRecordEventMsg:
		if extractString(payload["type"]) == codexEventTurnAborted {
			return &Record{ID: id, Role: RoleSystem, At: at, Blocks: []Block{{Type: BlockText, Text: InterruptedNotice}}}, false
		}
		if context.ResponseItemPath {
			// 该文件已有规范路径，event_msg 的消息型 payload 是同一轮的重复记录。
			return nil, false
		}
		return codexEventMessage(payload, id, at)
	}
	// session_meta / turn_context / 其它元数据不是消息，不计入 skipped。
	return nil, false
}

// codexRecordID 用「会话 id + 源文件字节 offset」作记录身份。offset 天然区分重复内容，
// 所以同一个问题被问两次会得到两条不同 id 的记录。
func codexRecordID(context LineContext) string {
	if context.SessionID == "" {
		return "codex:" + strconv.FormatInt(context.Offset, 10)
	}
	return context.SessionID + ":" + strconv.FormatInt(context.Offset, 10)
}

// codexItem 处理一个 response_item 形状的 item。包裹形态与旧式未包裹形态共用它，
// 因为两者的 item 判别式相同，差别只在 content 的别名表上，而别名表已经统一处理。
func codexItem(item map[string]any, id, at string) (*Record, bool) {
	switch extractString(item["type"]) {
	case "message":
		role := extractString(item["role"])
		if role == "developer" || role == RoleSystem {
			// 开发者 / 系统指令是给模型的上下文，不是对话，也不是格式漂移。
			return nil, false
		}
		if role != RoleUser && role != RoleAssistant {
			return nil, true
		}
		blocks := codexMessageBlocks(item["content"])
		if role == RoleUser {
			kept := dropInjectedContext(blocks)
			if len(kept) == 0 && len(blocks) > 0 {
				// 整条都是注入的上下文：按设计省略。
				return nil, false
			}
			blocks = kept
		}
		if len(blocks) == 0 {
			return nil, true
		}
		return &Record{ID: id, Role: role, At: at, Blocks: blocks}, false
	case "reasoning":
		// 契约没有 reasoning 角色：思考过程直接省略，不计入 skipped。
		return nil, false
	case "function_call", "local_shell_call", "custom_tool_call":
		return &Record{ID: id, Role: RoleAssistant, At: at, Blocks: []Block{{
			Type:   BlockToolCall,
			CallID: extractString(item["call_id"]),
			Name:   toolName(item["name"]),
			Input:  codexCallInput(item),
		}}}, false
	case "function_call_output", "custom_tool_call_output":
		return &Record{ID: id, Role: RoleTool, At: at, Blocks: []Block{codexToolResult(extractString(item["call_id"]), item["output"])}}, false
	default:
		// token_count / 其它元数据不是消息。
		return nil, false
	}
}

// codexEventMessage 处理 event_msg 形态的消息型 payload。
func codexEventMessage(payload map[string]any, id, at string) (*Record, bool) {
	switch extractString(payload["type"]) {
	case "user_message":
		if text := rawString(payload["message"]); strings.TrimSpace(text) != "" {
			return &Record{ID: id, Role: RoleUser, At: at, Blocks: []Block{{Type: BlockText, Text: text}}}, false
		}
		return nil, true
	case "agent_message":
		if text := rawString(payload["message"]); strings.TrimSpace(text) != "" {
			return &Record{ID: id, Role: RoleAssistant, At: at, Blocks: []Block{{Type: BlockText, Text: text}}}, false
		}
		return nil, true
	case "item_completed":
		item := asRecord(payload["item"])
		if item == nil {
			return nil, true
		}
		blocks := codexMessageBlocks(item["content"])
		if len(blocks) == 0 {
			return nil, true
		}
		itemID := extractString(item["id"])
		if itemID == "" {
			itemID = id
		}
		switch extractString(item["type"]) {
		case "UserMessage", "user_message":
			return &Record{ID: itemID, Role: RoleUser, At: at, Blocks: blocks}, false
		case "AgentMessage", "agent_message":
			return &Record{ID: itemID, Role: RoleAssistant, At: at, Blocks: blocks}, false
		default:
			return nil, false
		}
	default:
		// task_started / task_complete / token_count 等生命周期与计量事件不是消息。
		return nil, false
	}
}

// codexMessageBlocks 解析 Codex message / item_completed 的 content。
//
// 参考实现按「包裹 / 未包裹」用了两套不同的解码器，但两套的差别只在文本别名上
// （`text` vs `input_text` / `output_text`），而版本之间到底用哪一套并不稳定。
// 这里取**并集**：能解的别名都收，只把真正无法归类的形状计成未识别。并集只会比
// 参考实现多认出消息，不会少认，因此不会把 provider 的真实输出静默丢掉。
//
// 图片引用按契约丢弃（不输出 image-ref 块）。
func codexMessageBlocks(content any) []Block {
	items, ok := content.([]any)
	if !ok {
		// Codex 也写过纯字符串 content；这不是块数组，交给通用解码器。
		blocks, _ := claudeContentBlocks(content)
		return blocks
	}
	blocks := make([]Block, 0, len(items))
	for _, value := range items {
		if text, ok := value.(string); ok {
			if strings.TrimSpace(text) != "" {
				blocks = append(blocks, Block{Type: BlockText, Text: text})
			}
			continue
		}
		item := asRecord(value)
		if item == nil {
			continue
		}
		switch extractString(item["type"]) {
		case "text", "Text", "input_text", "output_text":
			if text := rawString(item["text"]); strings.TrimSpace(text) != "" {
				blocks = append(blocks, Block{Type: BlockText, Text: text})
			}
		case "thinking":
			// 契约没有 reasoning 角色：直接省略。
		case "tool_use":
			blocks = append(blocks, Block{
				Type:   BlockToolCall,
				CallID: extractString(item["id"]),
				Name:   toolName(item["name"]),
				Input:  rawJSON(item["input"]),
			})
		case "tool_result":
			blocks = append(blocks, Block{
				Type:    BlockToolResult,
				CallID:  extractString(item["tool_use_id"]),
				Output:  toolResultOutput(item["content"]),
				IsError: isTrue(item["is_error"]),
			})
		case "image", "Image", "input_image", "local_image", "LocalImage":
			// 契约不输出图片引用。
		}
	}
	return blocks
}

// codexCallInput 原样透传工具入参：三个字段是上游不同调用形态的同义位置。
func codexCallInput(item map[string]any) []byte {
	for _, key := range []string{"arguments", "input", "action"} {
		if value, ok := item[key]; ok {
			return rawJSON(value)
		}
	}
	return rawJSON(nil)
}

// codexToolResult 把工具输出收窄成单个字符串与错误标记。
func codexToolResult(callID string, output any) Block {
	record := asRecord(output)
	isError := false
	var payload any = output
	if record != nil {
		isError = record["success"] == false || isTrue(record["is_error"])
		if content, ok := record["content"]; ok {
			payload = content
		} else if nested, ok := record["output"]; ok {
			payload = nested
		}
	}
	return Block{Type: BlockToolResult, CallID: callID, Output: toolResultOutput(payload), IsError: isError}
}

// codexInjectedPrefixes 是 Codex 以 user 角色写进 response_item 的模型上下文：
// 技能展开、AGENTS.md 指令、环境信息、应用页面状态与中断标记。它们不是用户输入的 prompt。
var codexInjectedPrefixes = []string{
	"<skill>",
	"# agents.md instructions",
	"<instructions>",
	"<user_instructions>",
	"<environment_context>",
	"<external_codex_apps_",
	"<turn_aborted>",
}

// dropInjectedContext 过滤用户记录里注入的模型上下文，只留下用户真实输入。
func dropInjectedContext(blocks []Block) []Block {
	kept := make([]Block, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == BlockText && codexInjected(block.Text) {
			continue
		}
		kept = append(kept, block)
	}
	return kept
}

func codexInjected(text string) bool {
	// 前缀都很短：只把开头一段转小写，不复制整段长文本。
	text = strings.TrimSpace(text)
	text = strings.ToLower(text[:min(len(text), 64)])
	for _, prefix := range codexInjectedPrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}
