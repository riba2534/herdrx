package agentlog

import (
	"strconv"
	"strings"
)

// decodeClaudeLine 把一行 Claude Code JSONL 解码成一条记录。
//
// 返回值第二个分量是「这条消息形状的行没能产出任何内容」，也就是契约里的 skipped：
// 上游改字段会**静默**丢消息，skipped 计数就是给这种静默准备的对策。反过来，
// provider 的内部元数据（summary / system / queue-operation / file-history-snapshot 等）
// 与设计上省略的 thinking 都不计入 skipped —— 它们不是「本该有却没有」的消息。
func decodeClaudeLine(context LineContext, line string) (*Record, bool) {
	record := parseJSONObject(line)
	if record == nil {
		// 完整的一行却不是合法 JSON，只可能是格式漂移。
		return nil, true
	}
	role := extractString(record["type"])
	if role != RoleUser && role != RoleAssistant {
		return nil, false
	}
	id := extractString(record["uuid"])
	if id == "" {
		id = "claude:" + strconv.FormatInt(context.Offset, 10)
	}
	at := parseTimestamp(record["timestamp"])

	if extractString(record["interruptedMessageId"]) != "" {
		// Claude 把「用户打断」写成一条注入的 user 记录。保留成 system 状态行，
		// 但不能让它冒充用户真实提问。
		return &Record{ID: id, Role: RoleSystem, At: at, Blocks: []Block{{Type: BlockText, Text: InterruptedNotice}}}, false
	}

	message := asRecord(record["message"])
	blocks, unrecognized := claudeContentBlocks(message["content"])
	// Claude 结构上标记了注入轮次：isMeta / isSynthetic / isCompactSummary 的 user 记录
	// 是系统注入而不是用户输入。但 tool-result 是真实输出，即使落在注入轮里也必须保留。
	if role == RoleUser && (isTrue(record["isMeta"]) || isTrue(record["isSynthetic"]) || isTrue(record["isCompactSummary"])) {
		injected := make([]Block, 0, len(blocks))
		for _, block := range blocks {
			if block.Type == BlockToolResult {
				injected = append(injected, block)
			}
		}
		blocks = injected
	}
	if len(blocks) == 0 {
		// 只有 thinking 的记录是设计上省略（reasoning 不入输出），不是格式畸形。
		return nil, unrecognized
	}
	return &Record{ID: id, Role: claudeRole(role, blocks), At: at, Blocks: blocks}, false
}

// claudeRole 派生记录角色。`tool` 是派生的：user 记录里全是 tool-result 才是工具结果，
// 只要还夹着任何一个非 tool-result 块，它就仍然是用户问题。
func claudeRole(role string, blocks []Block) string {
	if role != RoleUser {
		return role
	}
	for _, block := range blocks {
		if block.Type != BlockToolResult {
			return RoleUser
		}
	}
	return RoleTool
}

// claudeContentBlocks 把 Claude 的 message.content（字符串或块数组）转成块。
//
// 第二个返回值表示「出现了无法归类的形状」：只有这种情况才该计入 skipped。
// thinking 属于已知但按设计省略的类型，不算未识别。
func claudeContentBlocks(content any) ([]Block, bool) {
	switch typed := content.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, false
		}
		return []Block{{Type: BlockText, Text: typed}}, false
	case []any:
		blocks := make([]Block, 0, len(typed))
		unrecognized := false
		for _, item := range typed {
			if text, ok := item.(string); ok {
				if strings.TrimSpace(text) != "" {
					blocks = append(blocks, Block{Type: BlockText, Text: text})
				}
				continue
			}
			record := asRecord(item)
			if record == nil {
				unrecognized = true
				continue
			}
			switch extractString(record["type"]) {
			case "text":
				if text := rawString(record["text"]); strings.TrimSpace(text) != "" {
					blocks = append(blocks, Block{Type: BlockText, Text: text})
				}
			case "thinking":
				// 思想链在服务端直接省略：不输出，也不计入 skipped。
			case "tool_use":
				blocks = append(blocks, Block{
					Type:   BlockToolCall,
					CallID: extractString(record["id"]),
					Name:   toolName(record["name"]),
					Input:  rawJSON(record["input"]),
				})
			case "tool_result":
				blocks = append(blocks, Block{
					Type:    BlockToolResult,
					CallID:  extractString(record["tool_use_id"]),
					Output:  toolResultOutput(record["content"]),
					IsError: isTrue(record["is_error"]),
				})
			case "image":
				// 契约明确不输出图片引用。
			default:
				unrecognized = true
			}
		}
		return blocks, unrecognized
	default:
		// content 缺失或形状不认识：这是真正需要曝光的静默丢失。
		return nil, true
	}
}

// toolName 取工具名，provider 缺字段时用占位名，绝不让工具卡片显示空白。
func toolName(value any) string {
	if name := extractString(value); name != "" {
		return name
	}
	return "tool"
}
