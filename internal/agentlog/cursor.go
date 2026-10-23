package agentlog

import (
	"strconv"
	"strings"
)

// Cursor Agent 的主 transcript 是
// `<project>/agent-transcripts/<sid>/<sid>.jsonl`。
// 形状来自 franken_agent_detection `cursor.rs` 的 agent_transcript_tests：
// `{"role":"user","message":{"content":[{"type":"text","text":"..."}]}}`，
// assistant 行可以再带 `tool_use`。子代理、文件名与目录名不一致的 jsonl、
// 以及 Composer 的 state.vscdb 都不是这一份记录。
//
// 工作区不在 jsonl 里。只有同级 `.workspace-trusted` 的 `workspacePath`
// 能证明 cwd；项目目录名会把 `/` 和 `-` 折成同一个 slug，不能用来认领。

// CursorTranscriptRel 报告相对路径是不是一条主 Agent transcript。
func CursorTranscriptRel(rel string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) != 4 || parts[1] != "agent-transcripts" || !strings.HasSuffix(parts[3], ".jsonl") {
		return false
	}
	sessionID := strings.TrimSuffix(parts[3], ".jsonl")
	if sessionID == "" || sessionID != parts[2] || strings.Contains(rel, "/subagents/") {
		return false
	}
	return CursorSessionFile(parts[3]) && safePathSegment(parts[0]) && safePathSegment(sessionID)
}

// CursorSessionID 是 transcript 文件名的会话 id。路径不合法时返回空串。
func CursorSessionID(rel string) string {
	if !CursorTranscriptRel(rel) {
		return ""
	}
	parts := strings.Split(rel, "/")
	return strings.TrimSuffix(parts[3], ".jsonl")
}

// CursorWorkspaceRel 是这条 transcript 对应的 workspace 元数据。
// 它由服务端从 transcript 路径推导，不接受客户端传入。
func CursorWorkspaceRel(rel string) string {
	if !CursorTranscriptRel(rel) {
		return ""
	}
	return strings.Split(rel, "/")[0] + "/.workspace-trusted"
}

// CursorSessionFile 报告文件名是不是 `<sid>.jsonl`。
func CursorSessionFile(name string) bool {
	if !strings.HasSuffix(name, ".jsonl") {
		return false
	}
	stem := strings.TrimSuffix(name, ".jsonl")
	return safePathSegment(stem)
}

func safePathSegment(value string) bool {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, `/\`) {
		return false
	}
	return true
}

// CursorWorkspacePath 从 `.workspace-trusted` 里取出绝对 workspacePath。
// 缺少字段、相对路径或带控制字符都视为没有证明，不能改用目录名。
func CursorWorkspacePath(data []byte) string {
	record := parseJSONObject(string(data))
	if record == nil {
		return ""
	}
	path := extractString(record["workspacePath"])
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\r\n\x00") {
		return ""
	}
	return path
}

func decodeCursorLine(context LineContext, line string) (*Record, bool) {
	record := parseJSONObject(line)
	if record == nil {
		return nil, strings.TrimSpace(line) != ""
	}
	role := cursorRole(extractString(record["role"]))
	if role == "" {
		return nil, false
	}
	content := record["content"]
	if message := asRecord(record["message"]); message != nil && message["content"] != nil {
		content = message["content"]
	}
	blocks, skipped := cursorContentBlocks(content)
	if len(blocks) == 0 {
		return nil, skipped
	}
	id := extractString(record["uuid"])
	if id == "" {
		id = context.SessionID + ":" + strconv.FormatInt(context.Offset, 10)
	}
	return &Record{ID: id, Role: role, At: parseTimestamp(record["timestamp"]), Blocks: blocks}, skipped
}

func cursorRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user", "human":
		return RoleUser
	case "assistant", "ai", "bot":
		return RoleAssistant
	case "tool":
		return RoleTool
	case "system":
		return RoleSystem
	default:
		return ""
	}
}

func cursorContentBlocks(content any) ([]Block, bool) {
	switch typed := content.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, false
		}
		return []Block{{Type: BlockText, Text: typed}}, false
	case []any:
		blocks := make([]Block, 0, len(typed))
		skipped := false
		for _, item := range typed {
			if text, ok := item.(string); ok {
				if strings.TrimSpace(text) != "" {
					blocks = append(blocks, Block{Type: BlockText, Text: text})
				}
				continue
			}
			record := asRecord(item)
			if record == nil {
				skipped = true
				continue
			}
			switch extractString(record["type"]) {
			case "", "text", "input_text", "output_text":
				if text := rawString(record["text"]); strings.TrimSpace(text) != "" {
					blocks = append(blocks, Block{Type: BlockText, Text: text})
				}
			case "tool_use":
				blocks = append(blocks, Block{
					Type:   BlockToolCall,
					CallID: extractString(record["id"]),
					Name:   toolName(record["name"]),
					Input:  rawJSON(record["input"]),
				})
			case "thinking":
			default:
				skipped = true
			}
		}
		return blocks, skipped
	default:
		if content == nil {
			return nil, false
		}
		return nil, true
	}
}
