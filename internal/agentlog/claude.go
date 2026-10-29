package agentlog

import (
	"regexp"
	"strconv"
	"strings"
)

// decodeClaudeLine 把一行 Claude Code JSONL 解码成一条记录。
//
// 返回值第二个分量是「这条消息形状的行没能产出任何内容」，也就是契约里的 skipped：
// 上游改字段会**静默**丢消息，skipped 计数就是给这种静默准备的对策。反过来，
// provider 的内部元数据（summary / queue-operation / file-history-snapshot、无关的 system 行等）
// 与设计上省略的 thinking 都不计入 skipped —— 它们不是「本该有却没有」的消息。
func decodeClaudeLine(context LineContext, line string) (*Record, bool) {
	record := parseJSONObject(line)
	if record == nil {
		// 完整的一行却不是合法 JSON，只可能是格式漂移。
		return nil, true
	}
	role := extractString(record["type"])
	id := extractString(record["uuid"])
	if id == "" {
		id = "claude:" + strconv.FormatInt(context.Offset, 10)
	}
	at := parseTimestamp(record["timestamp"])
	switch role {
	case RoleUser, RoleAssistant:
	case "attachment":
		return claudeQueuedPrompt(asRecord(record["attachment"]), id, at), false
	case "system":
		return claudeSystemLine(record, id, at), false
	default:
		// queue-operation 只记录输入框排队的进出，消息本身稍后以 attachment 或 user 记录落盘，
		// 这里不重复产出（分页读取下也无法回收已发布的「排队中」记录）。
		return nil, false
	}

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
	if role == RoleUser {
		var system bool
		blocks, system = claudeUserBlocks(blocks)
		if system {
			return &Record{ID: id, Role: RoleSystem, At: at, Blocks: blocks}, false
		}
	}
	if len(blocks) == 0 {
		// 只有 thinking 的记录是设计上省略（reasoning 不入输出），不是格式畸形。
		return nil, unrecognized
	}
	return &Record{ID: id, Role: claudeRole(role, blocks), At: at, Blocks: blocks}, false
}

// claudeUserBlocks 清理用户记录里 Claude Code 自己加的包装（长文本粘贴、命令标签、
// 系统提醒）。第二个返回值表示这条记录其实是本地命令输出 / 后台通知这类系统消息。
func claudeUserBlocks(blocks []Block) ([]Block, bool) {
	kept := blocks[:0]
	systemOnly, sawText := true, false
	for _, block := range blocks {
		if block.Type != BlockText {
			systemOnly = false
			kept = append(kept, block)
			continue
		}
		text, system := claudeUserText(block.Text)
		if text == "" {
			continue
		}
		sawText = true
		systemOnly = systemOnly && system
		kept = append(kept, Block{Type: BlockText, Text: text})
	}
	return kept, sawText && systemOnly
}

var (
	// 长文本粘贴（含经 Herdr bracketed paste 进去的输入）被包成
	// `<pasted_content id="…">…</pasted_content id="…">`；嵌套粘贴还会出现转义的斜杠。
	claudePastedTag = regexp.MustCompile(`<[\\/]*pasted_content(?:\s+id="[^"]*")?\s*>`)
	// 给模型看的上下文，不是用户说的话。
	claudeContextTag = regexp.MustCompile(`(?s)<(system-reminder|local-command-caveat)>.*?</(system-reminder|local-command-caveat)>`)
	claudeAnsi       = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
)

// claudeTag 取第一个 `<name>…</name>` 的内容。
func claudeTag(text, name string) string {
	start := strings.Index(text, "<"+name+">")
	if start < 0 {
		return ""
	}
	rest := text[start+len(name)+2:]
	end := strings.Index(rest, "</"+name+">")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// claudeUserText 把一段用户文本还原成用户看到的样子。返回空串表示整段都是注入内容；
// 第二个返回值为 true 表示这是系统消息（本地命令输出、后台任务通知、中断提示）。
func claudeUserText(text string) (string, bool) {
	if strings.Contains(text, "<") {
		// 多数普通提问没有任何标签，不必跑正则。
		text = claudeContextTag.ReplaceAllString(claudePastedTag.ReplaceAllString(text, ""), "")
	}
	text = strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(text, "<command-name>") || strings.HasPrefix(text, "<command-message>"):
		// 斜杠命令：`<command-name>/model</command-name><command-args>x</command-args>` → `/model x`。
		name := claudeTag(text, "command-name")
		if name == "" {
			name = claudeTag(text, "command-message")
		}
		if name == "" {
			return "", false
		}
		if !strings.HasPrefix(name, "/") {
			name = "/" + name
		}
		return strings.TrimSpace(name + " " + claudeTag(text, "command-args")), false
	case strings.HasPrefix(text, "<local-command-stdout>") || strings.HasPrefix(text, "<local-command-stderr>"):
		output := strings.TrimSpace(claudeTag(text, "local-command-stdout") + "\n" + claudeTag(text, "local-command-stderr"))
		return strings.TrimSpace(claudeAnsi.ReplaceAllString(output, "")), true
	case strings.HasPrefix(text, "<task-notification>"):
		if summary := claudeTag(text, "summary"); summary != "" {
			return "后台任务：" + summary, true
		}
		return "后台任务有更新", true
	case text == "[Request interrupted by user]" || text == "[Request interrupted by user for tool use]":
		return InterruptedNotice, true
	}
	return text, false
}

// claudeQueuedPrompt 处理 Agent 工作中用户插入的消息：Claude 把它「中途吸收」进当前轮，
// 落盘成 `attachment{type:queued_command, commandMode:prompt}` 而不是 user 记录。
// 只认人类输入（origin 缺省或 kind=human）；其它会话发来的消息、后台任务通知不是用户说的话。
func claudeQueuedPrompt(attachment map[string]any, id, at string) *Record {
	if extractString(attachment["type"]) != "queued_command" || extractString(attachment["commandMode"]) != "prompt" || isTrue(attachment["isMeta"]) {
		return nil
	}
	if origin := asRecord(attachment["origin"]); origin != nil && extractString(origin["kind"]) != "human" {
		return nil
	}
	blocks, _ := claudeContentBlocks(attachment["prompt"])
	text := make([]Block, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == BlockText {
			text = append(text, block)
		}
	}
	blocks, system := claudeUserBlocks(text)
	if len(blocks) == 0 || system {
		return nil
	}
	return &Record{ID: id, Role: RoleUser, At: at, Blocks: blocks, Queued: true}
}

// claudeSystemLine 把 Claude 的 system 行里对用户有意义的几类变成 system 记录：
// 上下文压缩、API 错误、离开期间的回顾，以及新版写成 system 行的斜杠命令。
// stop_hook_summary / turn_duration / informational 等是运行时元数据，不进对话。
func claudeSystemLine(record map[string]any, id, at string) *Record {
	emit := func(role, text string) *Record {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return &Record{ID: id, Role: role, At: at, Blocks: []Block{{Type: BlockText, Text: text}}}
	}
	switch extractString(record["subtype"]) {
	case "compact_boundary":
		return emit(RoleSystem, CompactedNotice)
	case "api_error":
		// 每次重试都写一行（retryAttempt 1..maxRetries）：只留第一次和最终放弃的那一次。
		attempt, _ := record["retryAttempt"].(float64)
		limit, _ := record["maxRetries"].(float64)
		if attempt > 1 && (limit == 0 || attempt < limit) {
			return nil
		}
		text := "API 错误：" + claudeAPIError(record)
		switch {
		case limit > 0 && attempt >= limit:
			text += "（重试 " + strconv.Itoa(int(attempt)) + " 次后放弃）"
		case attempt == 1:
			text += "（正在重试）"
		}
		return emit(RoleSystem, text)
	case "away_summary":
		recap := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rawString(record["content"])), "(disable recaps in /config)"))
		if recap == "" {
			return nil
		}
		return emit(RoleSystem, "离开期间回顾："+recap)
	case "local_command":
		text, isSystem := claudeUserText(rawString(record["content"]))
		if isSystem {
			return emit(RoleSystem, text)
		}
		return emit(RoleUser, text)
	}
	return nil
}

// claudeAPIError 取 api_error 行的错误说明：error 字符串或 error.formatted / error.message，
// 旧版写在 content 里。
func claudeAPIError(record map[string]any) string {
	if text := extractString(record["error"]); text != "" {
		return text
	}
	if detail := asRecord(record["error"]); detail != nil {
		for _, key := range []string{"formatted", "message"} {
			if text := extractString(detail[key]); text != "" {
				return text
			}
		}
	}
	if text := extractString(record["content"]); text != "" {
		return text
	}
	return "请求失败"
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
