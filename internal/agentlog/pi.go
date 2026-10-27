package agentlog

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Pi 的会话文件是 `~/.pi/agent/sessions/--<cwd>--/<时间戳>_<id>.jsonl`。
// 目录名来自 session-manager.ts：去掉开头的 `/`，把 `/`、`\`、`:` 换成 `-`，
// 两侧再包 `--`。所以 `/Users/shengtao/openlark` 变成 `--Users-shengtao-openlark--`。
//
// 行类型以官方 session-format 为准。`message.message.role` 是
// user / assistant / system / toolResult。assistant 的 content 是块数组：
// `text`、`thinking`（丢掉，契约没有 reasoning）、`toolCall`（id、name、arguments）。
// toolResult 用 toolCallId 配对。model_change、thinking_level_change 和其它
// 元数据行跳过。按文件顺序输出，不按 parentId 回溯分支：分页只能看见窗口里的行。

// PiProjectDir 是 pane cwd 对应的那一个会话目录名。
func PiProjectDir(cwd string) string {
	trimmed := strings.TrimLeft(cwd, `/\`)
	var builder strings.Builder
	builder.Grow(len(trimmed) + 4)
	builder.WriteString("--")
	for _, character := range trimmed {
		switch character {
		case '/', '\\', ':':
			builder.WriteByte('-')
		default:
			builder.WriteRune(character)
		}
	}
	builder.WriteString("--")
	return builder.String()
}

// PiSessionFile 报告文件名是不是 `<时间戳>_<会话 id>.jsonl`。
// 时间戳来自 ISO 时间并把 `:`、`.` 换成 `-`，所以第一个 `_` 之前没有下划线。
func PiSessionFile(name string) bool {
	return PiSessionID(name) != ""
}

// PiSessionID 从文件名取出会话 id。不符合形状时返回空串。
func PiSessionID(name string) string {
	stem, ok := strings.CutSuffix(name, ".jsonl")
	if !ok {
		return ""
	}
	timestamp, id, ok := strings.Cut(stem, "_")
	if !ok || timestamp == "" || strings.Contains(timestamp, "_") || !safePathSegment(id) {
		return ""
	}
	return id
}

// PiSessionRel 是 `<编码目录>/<时间戳>_<id>.jsonl`。
func PiSessionRel(rel string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) != 2 || !safePathSegment(parts[0]) {
		return false
	}
	return PiSessionFile(parts[1])
}

func decodePiLine(context LineContext, line string) (*Record, bool) {
	record := parseJSONObject(line)
	if record == nil {
		return nil, true
	}
	if extractString(record["type"]) != "message" {
		return nil, false
	}
	message := asRecord(record["message"])
	if message == nil {
		return nil, true
	}
	role := extractString(message["role"])
	id := extractString(record["id"])
	if id == "" {
		id = "pi:" + strconv.FormatInt(context.Offset, 10)
	}
	at := parseTimestamp(record["timestamp"])
	switch role {
	case RoleUser, RoleAssistant, RoleSystem:
		blocks, unrecognized := piContentBlocks(role, message["content"])
		if len(blocks) == 0 {
			return nil, unrecognized
		}
		return &Record{ID: id, Role: piRole(role, blocks), At: at, Blocks: blocks}, false
	case "toolResult":
		block := Block{
			Type:    BlockToolResult,
			CallID:  extractString(message["toolCallId"]),
			Output:  piToolOutput(message["content"]),
			IsError: isTrue(message["isError"]),
		}
		if strings.TrimSpace(block.Output) == "" && block.CallID == "" {
			return nil, true
		}
		return &Record{ID: id, Role: RoleTool, At: at, Blocks: []Block{block}}, false
	default:
		return nil, false
	}
}

func piRole(role string, blocks []Block) string {
	if role != RoleUser && role != RoleAssistant {
		return role
	}
	onlyResults := true
	for _, block := range blocks {
		if block.Type != BlockToolResult {
			onlyResults = false
			break
		}
	}
	if onlyResults && len(blocks) > 0 {
		return RoleTool
	}
	return role
}

func piContentBlocks(role string, content any) ([]Block, bool) {
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
				// 契约没有 reasoning。
			case "toolCall":
				if role == RoleSystem {
					unrecognized = true
					continue
				}
				blocks = append(blocks, Block{
					Type:   BlockToolCall,
					CallID: extractString(record["id"]),
					Name:   toolName(record["name"]),
					Input:  piArguments(record["arguments"]),
				})
			case "image":
			default:
				unrecognized = true
			}
		}
		return blocks, unrecognized
	case nil:
		return nil, false
	default:
		return nil, true
	}
}

func piArguments(value any) json.RawMessage {
	if text, ok := value.(string); ok && json.Valid([]byte(text)) {
		return json.RawMessage(text)
	}
	return rawJSON(value)
}

func piToolOutput(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	blocks, _ := piContentBlocks(RoleTool, value)
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == BlockText && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return toolResultOutput(value)
}
