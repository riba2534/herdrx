package agentlog

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// Gemini CLI 的会话文件是 `~/.gemini/tmp/<hash>/chats/session-*.json`（整份文档）
// 或 `session-*.jsonl`（事件回放）。消息字段来自 connector_gemini.rs 与 gemini.rs：
// `type` 为 user / model / gemini，`content` 是字符串或 `{text}` 数组。
//
// 目录哈希不是 cwd。工作区只认测试钉住的两段明确标记，以及 `/data/projects/<name>`
// 这一条同样被测试钉住的回退；没有这些标记就不认领。

// GeminiDocument 是一份已经回放完的 Gemini 会话。
type GeminiDocument struct {
	SessionID string
	CWD       string
	Records   []Record
}

// GeminiSessionRel 报告相对路径是不是 `<hash>/chats/session-*.json[l]`。
func GeminiSessionRel(rel string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) != 3 || parts[1] != "chats" || !safePathSegment(parts[0]) {
		return false
	}
	return GeminiSessionFile(parts[2])
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
	sessionID := geminiFileStem(rel)
	messages, headerID := geminiMessages(data)
	if headerID != "" {
		sessionID = headerID
	}
	records := make([]Record, 0, len(messages))
	for index, message := range messages {
		records = append(records, geminiRecords(sessionID, index, message)...)
	}
	return GeminiDocument{SessionID: sessionID, CWD: geminiWorkspace(records), Records: records}
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

func geminiMessages(data []byte) ([]map[string]any, string) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed) {
		var document map[string]any
		if json.Unmarshal(trimmed, &document) == nil {
			if _, ok := document["messages"]; ok || document["$set"] == nil && document["$patch"] == nil && document["$rewindTo"] == nil {
				if messages, ok := document["messages"].([]any); ok || document["sessionId"] != nil || document["projectHash"] != nil {
					return geminiMessageMaps(messages), extractString(document["sessionId"])
				}
			}
		}
	}
	return replayGemini(data)
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
	next  int
}

func replayGemini(data []byte) ([]map[string]any, string) {
	replay := geminiReplay{byID: map[string]map[string]any{}}
	var sessionID string
	for _, line := range completeLines(data) {
		record := parseJSONObject(line)
		if record == nil {
			continue
		}
		if id := extractString(record["sessionId"]); id != "" {
			sessionID = id
		}
		switch {
		case extractString(record["$rewindTo"]) != "":
			replay.rewind(extractString(record["$rewindTo"]))
		case asRecord(record["$patch"]) != nil:
			replay.applyPatch(asRecord(record["$patch"]))
		case extractString(record["id"]) != "":
			replay.upsert(record)
		case asRecord(record["$set"]) != nil:
			replay.applyFields(asRecord(record["$set"]), true)
		case extractString(record["kind"]) == "main" || (extractString(record["sessionId"]) != "" && extractString(record["projectHash"]) != ""):
			replay.applyFields(record, false)
		case extractString(record["type"]) != "":
			replay.upsert(record)
		}
	}
	messages := make([]map[string]any, 0, len(replay.order))
	for _, id := range replay.order {
		if message := replay.byID[id]; message != nil {
			messages = append(messages, message)
		}
	}
	return messages, sessionID
}

func (r *geminiReplay) upsert(message map[string]any) {
	if asRecord(message["$patch"]) != nil {
		return
	}
	if id := extractString(message["id"]); id != "" {
		if _, ok := r.byID[id]; ok {
			r.byID[id] = message
			return
		}
		r.byID[id] = message
		r.order = append(r.order, id)
		r.next++
		return
	}
	id := "#anon-" + strconv.Itoa(r.next)
	r.next++
	r.byID[id] = message
	r.order = append(r.order, id)
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
		for _, message := range geminiMessageMaps(messages) {
			r.upsert(message)
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

func geminiRecords(sessionID string, index int, message map[string]any) []Record {
	role := geminiRole(extractString(message["type"]))
	if role == "" {
		return nil
	}
	blocks := geminiBlocks(message, role)
	if len(blocks) == 0 {
		return nil
	}
	id := extractString(message["id"])
	if id == "" {
		id = sessionID + ":" + strconv.Itoa(index)
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
	for _, block := range results {
		records = append(records, Record{
			ID: id + ":tool:" + block.CallID, Role: RoleTool, At: at, Blocks: []Block{block},
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
			if record == nil {
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

func geminiWorkspace(records []Record) string {
	texts := make([]string, 0, len(records))
	for _, record := range records {
		var builder strings.Builder
		for _, block := range record.Blocks {
			if block.Type == BlockText {
				if builder.Len() > 0 {
					builder.WriteByte('\n')
				}
				builder.WriteString(block.Text)
			}
		}
		texts = append(texts, builder.String())
	}
	limit := len(texts)
	if limit > 50 {
		limit = 50
	}
	for _, text := range texts[:limit] {
		if path := geminiMarkedPath(text, "AGENTS.md instructions for "); path != "" {
			return path
		}
	}
	for _, text := range texts[:limit] {
		if path := geminiMarkedPath(text, "Working directory:"); path != "" {
			return path
		}
	}
	fallback := len(texts)
	if fallback > 5 {
		fallback = 5
	}
	for _, text := range texts[:fallback] {
		if index := strings.Index(text, "/data/projects/"); index >= 0 {
			if path := geminiPathAt(text[index:]); strings.HasPrefix(path, "/data/projects/") {
				return geminiProjectPath(path)
			}
		}
	}
	return ""
}

func geminiMarkedPath(text, marker string) string {
	index := strings.Index(text, marker)
	if index < 0 {
		return ""
	}
	return geminiPathAt(text[index+len(marker):])
}

func geminiPathAt(text string) string {
	text = strings.TrimLeft(text, " \t")
	if strings.HasPrefix(text, `"`) || strings.HasPrefix(text, `'`) {
		quote := text[0]
		text = text[1:]
		if end := strings.IndexByte(text, quote); end >= 0 {
			text = text[:end]
		}
	} else if end := strings.IndexAny(text, " \t\r\n>\"')],"); end >= 0 {
		text = text[:end]
	}
	text = strings.TrimRight(text, "/:")
	if !strings.HasPrefix(text, "/") || len(text) <= 3 {
		return ""
	}
	return text
}

func geminiProjectPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) >= 5 && parts[1] == "data" && parts[2] == "projects" && parts[3] != "" {
		return "/data/projects/" + parts[3]
	}
	return path
}
