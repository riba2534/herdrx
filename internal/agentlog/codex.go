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
)

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
		if role != RoleUser && role != RoleAssistant {
			return nil, true
		}
		blocks := codexMessageBlocks(item["content"])
		if role == RoleUser {
			blocks = dropSkillContext(blocks)
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

// dropSkillContext 过滤显式的技能展开：那是模型上下文，不是用户真实输入的 prompt。
func dropSkillContext(blocks []Block) []Block {
	kept := blocks[:0]
	for _, block := range blocks {
		if block.Type == BlockText && strings.HasPrefix(strings.ToLower(strings.TrimSpace(block.Text)), "<skill>") {
			continue
		}
		kept = append(kept, block)
	}
	return kept
}
