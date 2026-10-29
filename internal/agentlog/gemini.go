package agentlog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// Gemini CLI 的会话文件是 `~/.gemini/tmp/<项目目录>/chats/session-*.json`（整份文档）
// 或 `session-*.jsonl`（事件回放）；项目目录是旧版的 sha256 或新版的 slug。消息字段来自 connector_gemini.rs 与 gemini.rs：
// `type` 为 user / model / gemini，`content` 是字符串或 `{text}` 数组。
//
// 归属认 projectHash：Gemini CLI 的 `getProjectHash(projectRoot)`（utils/paths.ts）是项目根
// 绝对路径的 SHA-256 十六进制，会话头里同样记着它（chatRecordingService.ts）。不从正文里猜
// 工作目录。项目目录的认定见 herdr/transcript_gemini_projects.go。

// GeminiProjectHash 是 Gemini CLI 对项目根绝对路径算的目录名。
func GeminiProjectHash(cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return hex.EncodeToString(sum[:])
}

// GeminiDocument 是一份已经回放完的 Gemini 会话。
type GeminiDocument struct {
	SessionID   string
	ProjectHash string
	StartTime   string
	Records     []Record
}

// GeminiSessionRel 报告相对路径是不是 `<项目目录>/chats/[<parentId>/]session-*.json[l]`。
func GeminiSessionRel(rel string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) < 3 || len(parts) > 4 || parts[1] != "chats" || !safePathSegment(parts[0]) {
		return false
	}
	if len(parts) == 4 && !safePathSegment(parts[2]) {
		return false
	}
	return GeminiSessionFile(parts[len(parts)-1])
}

// GeminiProjectSlug 报告目录名是否符合 Gemini CLI 项目注册表分配的 slug 形状（`[a-z0-9-]+`）。
func GeminiProjectSlug(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	for _, character := range name {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
			return false
		}
	}
	return true
}

// GeminiSessionFile 报告文件名是不是 Gemini 会话文件。
func GeminiSessionFile(name string) bool {
	lower := strings.ToLower(name)
	if !strings.HasPrefix(name, "session-") {
		return false
	}
	return strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".jsonl")
}

// DecodeGemini 把一份会话文件解成记录。截断的半行不参与回放。
func DecodeGemini(rel string, data []byte) GeminiDocument {
	return DecodeGeminiAt(rel, data, 0, "")
}

// DecodeGeminiAt 同 DecodeGemini，data 是文件里从 base 开始的一段，sessionID 是从文件头
// 已知的会话 id（窗口里通常没有头）。没有 id 的事件行按它在文件里的绝对字节位置命名，
// 换一个读取窗口 id 也不变、不撞号。
func DecodeGeminiAt(rel string, data []byte, base int64, sessionID string) GeminiDocument {
	parsed := geminiMessages(data, base)
	if parsed.SessionID == "" {
		parsed.SessionID = sessionID
	}
	if parsed.SessionID == "" {
		parsed.SessionID = geminiFileStem(rel)
	}
	records := make([]Record, 0, len(parsed.messages))
	for _, message := range parsed.messages {
		id := extractString(message.fields["id"])
		if id == "" {
			id = parsed.SessionID + ":" + message.anon
		}
		records = append(records, geminiRecords(id, message.fields)...)
	}
	return GeminiDocument{SessionID: parsed.SessionID, ProjectHash: parsed.ProjectHash, StartTime: parsed.StartTime, Records: records}
}

func geminiFileStem(rel string) string {
	name := rel
	if index := strings.LastIndexByte(rel, '/'); index >= 0 {
		name = rel[index+1:]
	}
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".jsonl"):
		return name[:len(name)-len(".jsonl")]
	case strings.HasSuffix(lower, ".json"):
		return name[:len(name)-len(".json")]
	default:
		return name
	}
}

type geminiMessage struct {
	// anon 只在消息自身没有 id 时使用，见 DecodeGeminiAt。
	anon   string
	fields map[string]any
}

type geminiParsed struct {
	SessionID   string
	ProjectHash string
	StartTime   string
	messages    []geminiMessage
}

// header 记下会话头字段，先出现的为准。
func (p *geminiParsed) header(record map[string]any) {
	if value := extractString(record["sessionId"]); value != "" && p.SessionID == "" {
		p.SessionID = value
	}
	if value := extractString(record["projectHash"]); value != "" && p.ProjectHash == "" {
		p.ProjectHash = value
	}
	if value := extractString(record["startTime"]); value != "" && p.StartTime == "" {
		p.StartTime = value
	}
}

func geminiMessages(data []byte, base int64) geminiParsed {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed) {
		var document map[string]any
		if json.Unmarshal(trimmed, &document) == nil {
			if _, ok := document["messages"]; ok || document["$set"] == nil && document["$patch"] == nil && document["$rewindTo"] == nil {
				if messages, ok := document["messages"].([]any); ok || document["sessionId"] != nil || document["projectHash"] != nil {
					var parsed geminiParsed
					parsed.header(document)
					// 整份文档总是完整读入，下标在同一份文档里稳定。
					for index, message := range geminiMessageMaps(messages) {
						parsed.messages = append(parsed.messages, geminiMessage{anon: "#" + strconv.Itoa(index), fields: message})
					}
					return parsed
				}
			}
		}
	}
	return replayGemini(data, base)
}

func geminiMessageMaps(values []any) []map[string]any {
	messages := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if record := asRecord(value); record != nil {
			messages = append(messages, record)
		}
	}
	return messages
}

type geminiReplay struct {
	order []string
	byID  map[string]map[string]any
	// line 是当前行在文件里的绝对字节位置，给没有 id 的消息命名。
	line string
}

func replayGemini(data []byte, base int64) geminiParsed {
	replay := geminiReplay{byID: map[string]map[string]any{}}
	var parsed geminiParsed
	offset := base
	for {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			break
		}
		replay.line = "@" + strconv.FormatInt(offset, 10)
		record := parseJSONObject(string(data[:end]))
		offset += int64(end) + 1
		data = data[end+1:]
		if record == nil {
			continue
		}
		parsed.header(record)
		switch {
		case extractString(record["$rewindTo"]) != "":
			replay.rewind(extractString(record["$rewindTo"]))
		case asRecord(record["$patch"]) != nil:
			replay.applyPatch(asRecord(record["$patch"]))
		case extractString(record["id"]) != "":
			replay.upsert(record, "")
		case asRecord(record["$set"]) != nil:
			replay.applyFields(asRecord(record["$set"]), true)
		case extractString(record["kind"]) == "main" || (extractString(record["sessionId"]) != "" && extractString(record["projectHash"]) != ""):
			replay.applyFields(record, false)
		case extractString(record["type"]) != "":
			replay.upsert(record, "")
		}
	}
	for _, id := range replay.order {
		if message := replay.byID[id]; message != nil {
			parsed.messages = append(parsed.messages, geminiMessage{anon: id, fields: message})
		}
	}
	return parsed
}

// upsert 按 id 合并消息。没有 id 的消息用「行位置 + 行内序号」作 key：同一行里的多条
// 消息（`$set.messages`）不会互相覆盖，不同窗口读到的同一行得到同一个 key。
func (r *geminiReplay) upsert(message map[string]any, suffix string) {
	if asRecord(message["$patch"]) != nil {
		return
	}
	id := extractString(message["id"])
	if id == "" {
		id = r.line + suffix
	}
	if _, ok := r.byID[id]; !ok {
		r.order = append(r.order, id)
	}
	r.byID[id] = message
}

func (r *geminiReplay) rewind(id string) {
	index := -1
	for position, current := range r.order {
		if current == id {
			index = position
			break
		}
	}
	if index < 0 {
		r.order = nil
		r.byID = map[string]map[string]any{}
		return
	}
	for _, dropped := range r.order[index:] {
		delete(r.byID, dropped)
	}
	r.order = r.order[:index]
}

func (r *geminiReplay) applyFields(fields map[string]any, replace bool) {
	if fields == nil {
		return
	}
	if messages, ok := fields["messages"].([]any); ok {
		if replace {
			r.order = nil
			r.byID = map[string]map[string]any{}
		}
		for index, message := range geminiMessageMaps(messages) {
			r.upsert(message, "#"+strconv.Itoa(index))
		}
	}
}

func (r *geminiReplay) applyPatch(patch map[string]any) {
	if patch == nil {
		return
	}
	if id := extractString(patch["id"]); id != "" {
		if message := r.byID[id]; message != nil && patch["content"] != nil {
			message["content"] = patch["content"]
		}
	}
}

func geminiRecords(id string, message map[string]any) []Record {
	role := geminiRole(extractString(message["type"]))
	if role == "" {
		return nil
	}
	blocks := geminiBlocks(message, role)
	if len(blocks) == 0 {
		return nil
	}
	at := parseTimestamp(message["timestamp"])
	visible := make([]Block, 0, len(blocks))
	var results []Block
	for _, block := range blocks {
		if block.Type == BlockToolResult {
			results = append(results, block)
			continue
		}
		visible = append(visible, block)
	}
	records := make([]Record, 0, 1+len(results))
	if len(visible) > 0 {
		records = append(records, Record{ID: id, Role: role, At: at, Blocks: visible})
	}
	// 工具结果按它在消息里的位置命名：CallID 可能为空或重复，不能当 id 用。
	for position, block := range results {
		records = append(records, Record{
			ID: id + ":tool:" + strconv.Itoa(position), Role: RoleTool, At: at, Blocks: []Block{block},
		})
	}
	return records
}

func geminiRole(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "user":
		return RoleUser
	case "model", "gemini", "":
		if kind == "" {
			return ""
		}
		return RoleAssistant
	case "info", "error":
		return RoleSystem
	case "tool":
		return RoleTool
	default:
		return ""
	}
}

func geminiBlocks(message map[string]any, role string) []Block {
	blocks := geminiContentBlocks(message["content"])
	if role != RoleAssistant {
		return blocks
	}
	calls, _ := message["toolCalls"].([]any)
	for _, item := range calls {
		call := asRecord(item)
		if call == nil {
			continue
		}
		name := extractString(call["name"])
		if name == "" {
			continue
		}
		blocks = append(blocks, Block{
			Type:   BlockToolCall,
			CallID: extractString(call["id"]),
			Name:   name,
			Input:  rawJSON(call["args"]),
		})
		if output, isError := geminiToolOutput(call["result"]); output != "" {
			blocks = append(blocks, Block{
				Type:    BlockToolResult,
				CallID:  extractString(call["id"]),
				Output:  output,
				IsError: isError,
			})
		}
	}
	return blocks
}

func geminiContentBlocks(content any) []Block {
	switch typed := content.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []Block{{Type: BlockText, Text: typed}}
	case []any:
		blocks := make([]Block, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				if strings.TrimSpace(text) != "" {
					blocks = append(blocks, Block{Type: BlockText, Text: text})
				}
				continue
			}
			record := asRecord(item)
			// `thought: true` 的 part 是模型的思考过程；契约没有 reasoning，不下发。
			if record == nil || record["thought"] == true {
				continue
			}
			if text := rawString(record["text"]); strings.TrimSpace(text) != "" && (extractString(record["type"]) == "" || extractString(record["type"]) == "text") {
				blocks = append(blocks, Block{Type: BlockText, Text: text})
			}
		}
		return blocks
	default:
		return nil
	}
}

func geminiToolOutput(result any) (string, bool) {
	switch typed := result.(type) {
	case string:
		return strings.TrimSpace(typed), false
	case map[string]any:
		if text := extractString(typed["output"]); text != "" {
			return text, false
		}
		if response := asRecord(typed["response"]); response != nil {
			if text := extractString(response["output"]); text != "" {
				return text, false
			}
			if text := extractString(response["error"]); text != "" {
				return text, true
			}
		}
		if nested := asRecord(typed["functionResponse"]); nested != nil {
			return geminiToolOutput(nested)
		}
	}
	return "", false
}
