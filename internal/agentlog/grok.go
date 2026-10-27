package agentlog

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// Grok CLI 的会话是
// `~/.grok/sessions/<百分号编码的 cwd>/<uuid>/chat_history.jsonl`。
// 同目录里的 events.jsonl、prompt_context.json、rewind_points.jsonl 和 `.lock`
// 都不是对话正文。
//
// 一行一个对象。user 的 content 是字符串或 `{type,text}` 数组；assistant 的
// content 是字符串，tool_calls 是 `{id,name,arguments}`。带 synthetic_reason
// 的 user 行是运行时注入的，记成 system，不能当成用户提问。reasoning 整行丢掉。

// GrokProjectDir 是 pane cwd 的目录名。PathEscape 会把 `/` 编成 `%2F`。
func GrokProjectDir(cwd string) string {
	return url.PathEscape(cwd)
}

// GrokCWD 从相对路径的第一段还原 cwd。解不出来或不是绝对路径时返回空串。
func GrokCWD(rel string) string {
	parts := strings.Split(rel, "/")
	if len(parts) < 1 || parts[0] == "" {
		return ""
	}
	decoded, err := url.PathUnescape(parts[0])
	if err != nil || !strings.HasPrefix(decoded, "/") || strings.ContainsAny(decoded, "\r\n\x00") {
		return ""
	}
	return decoded
}

// GrokSessionID 是会话目录名。路径不是 `<cwd>/<id>/chat_history.jsonl` 时返回空串。
func GrokSessionID(rel string) string {
	if !GrokHistoryRel(rel) {
		return ""
	}
	return strings.Split(rel, "/")[1]
}

// GrokHistoryRel 只接受三层里的 chat_history.jsonl。
func GrokHistoryRel(rel string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) != 3 || parts[2] != "chat_history.jsonl" || !safePathSegment(parts[1]) {
		return false
	}
	return GrokCWD(rel) != ""
}

func decodeGrokLine(context LineContext, line string) (*Record, bool) {
	record := parseJSONObject(line)
	if record == nil {
		return nil, true
	}
	id := extractString(record["id"])
	if id == "" {
		id = context.SessionID + ":" + strconv.FormatInt(context.Offset, 10)
		if context.SessionID == "" {
			id = "grok:" + strconv.FormatInt(context.Offset, 10)
		}
	}
	at := parseTimestamp(record["timestamp"])
	switch extractString(record["type"]) {
	case RoleSystem:
		text := grokText(record["content"])
		if strings.TrimSpace(text) == "" {
			return nil, false
		}
		return &Record{ID: id, Role: RoleSystem, At: at, Blocks: []Block{{Type: BlockText, Text: text}}}, false
	case RoleUser:
		text := grokText(record["content"])
		if strings.TrimSpace(text) == "" {
			return nil, extractString(record["synthetic_reason"]) == ""
		}
		role := RoleUser
		if extractString(record["synthetic_reason"]) != "" {
			role = RoleSystem
		}
		return &Record{ID: id, Role: role, At: at, Blocks: []Block{{Type: BlockText, Text: text}}}, false
	case RoleAssistant:
		blocks := make([]Block, 0, 2)
		if text := grokText(record["content"]); strings.TrimSpace(text) != "" {
			blocks = append(blocks, Block{Type: BlockText, Text: text})
		}
		if calls, ok := record["tool_calls"].([]any); ok {
			for _, item := range calls {
				call := asRecord(item)
				if call == nil {
					continue
				}
				blocks = append(blocks, Block{
					Type:   BlockToolCall,
					CallID: extractString(call["id"]),
					Name:   toolName(call["name"]),
					Input:  grokArguments(call["arguments"]),
				})
			}
		}
		if len(blocks) == 0 {
			return nil, false
		}
		return &Record{ID: id, Role: RoleAssistant, At: at, Blocks: blocks}, false
	case "tool_result":
		return &Record{ID: id, Role: RoleTool, At: at, Blocks: []Block{{
			Type:    BlockToolResult,
			CallID:  extractString(record["tool_call_id"]),
			Output:  grokText(record["content"]),
			IsError: isTrue(record["is_error"]),
		}}}, false
	case "reasoning":
		return nil, false
	default:
		return nil, false
	}
}

func grokText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return toolResultOutput(value)
}

func grokArguments(value any) json.RawMessage {
	if text, ok := value.(string); ok && json.Valid([]byte(strings.TrimSpace(text))) {
		return json.RawMessage(strings.TrimSpace(text))
	}
	return rawJSON(value)
}
