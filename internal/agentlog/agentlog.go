// Package agentlog 把 Agent 自己写下的 append-only 会话日志解析成带角色的对话记录。
//
// 权威数据源是磁盘上的 JSONL 文件 —— Claude Code 的
// `~/.claude/projects/<编码 cwd>/<session>.jsonl` 与 Codex 的
// `~/.codex/sessions/**/rollout-*.jsonl` —— 既不是 Herdr 协议的 RPC，也不是终端屏幕文本。
//
// 本包只做「一行 JSONL → 一条记录」的纯函数转换：不碰文件系统、不碰网络、不认识 Herdr，
// 因此可以用离线 fixture 完整测试，也便于对照上游 provider 的真实形状核对。
//
// 输出形状与冻结契约 `web/src/lib/structuredChatTypes.ts` 一一对应：
// 角色闭集合是 `user|assistant|tool|system`（**没有 reasoning**），块只有
// `text` / `tool-call` / `tool-result` 三种。解码语义对照公开参考实现
// `stablyai/orca@e86cba888b2eb88241a9c12dc019962f9c74307e` 的
// `transcript-line-decoders-{claude,codex}.ts` 与 `transcript-record-blocks.ts`，
// 但按本仓库契约做了两处收紧：不输出 image-ref / edit patch，不产生 reasoning 角色。
package agentlog

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 支持结构化记录的 Agent，闭集合。契约里同样是闭集合：其余 Agent（含与 claude/codex
// 同格式族的 openclaude / grok / omp）在各自的 decoder、日志根目录与 cwd 匹配规则被真实
// 会话日志验证之前一律不支持，不猜、不通配。
const (
	AgentClaude = "claude"
	AgentCodex  = "codex"
)

// Supported 报告该 agent 是否在本期支持范围内。
func Supported(agent string) bool {
	return agent == AgentClaude || agent == AgentCodex
}

// 记录角色，闭集合。`tool` 不是 provider 的原生角色，而是派生角色：
// provider 里 `type:'user'` 且 blocks 全部是 tool-result 的记录会被重判为 tool，
// 这样工具结果永远不会被渲染成用户问题。
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
	RoleSystem    = "system"
)

// 块类型，闭集合。
const (
	BlockText       = "text"
	BlockToolCall   = "tool-call"
	BlockToolResult = "tool-result"
)

// InterruptedNotice 是 provider 自己写下的「本轮被打断」通知对应的正文。
//
// Claude 用 `interruptedMessageId` 标记注入的中断通知，Codex 用 `turn_aborted` 事件。
// 两者的原文都是产品文案而不是模型输出，所以在这里定成一个常量，两个解码器共用同一句话，
// 避免各写一份后随版本分叉。
const InterruptedNotice = "本轮已被中断，Agent 未完成回复。"

// Block 是一个对话块。
//
// 字段是「按 type 取子集」的：text 用 Text，tool-call 用 CallID/Name/Input，
// tool-result 用 CallID/Output/IsError。MarshalJSON 保证序列化出来的 JSON 只带该类型
// 应有的字段，与契约里的三个判别式联合完全一致。
type Block struct {
	Type    string
	Text    string
	CallID  string
	Name    string
	Input   json.RawMessage
	Output  string
	IsError bool
}

// MarshalJSON 按块类型输出契约形状。契约里三个块类型的字段都是必填的，
// 所以这里不省略任何字段（缺省的字符串写空串，缺失的 input 写 null）。
func (b Block) MarshalJSON() ([]byte, error) {
	switch b.Type {
	case BlockText:
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{b.Type, b.Text})
	case BlockToolCall:
		input := b.Input
		if len(input) == 0 || !json.Valid(input) {
			input = json.RawMessage("null")
		}
		return json.Marshal(struct {
			Type   string          `json:"type"`
			CallID string          `json:"call_id"`
			Name   string          `json:"name"`
			Input  json.RawMessage `json:"input"`
		}{b.Type, b.CallID, b.Name, input})
	case BlockToolResult:
		return json.Marshal(struct {
			Type    string `json:"type"`
			CallID  string `json:"call_id"`
			Output  string `json:"output"`
			IsError bool   `json:"is_error"`
		}{b.Type, b.CallID, b.Output, b.IsError})
	default:
		return nil, fmt.Errorf("agentlog: unknown block type %q", b.Type)
	}
}

// Record 是一条对话记录。
//
// ID 在同一会话内稳定且唯一，是合并时的唯一键：同 id 必须**原地更新**，不是「已存在就
// 丢弃」，也不能再追加第二条。Claude 用 source record 的 uuid；Codex 没有稳定 uuid，
// 用 `<session_id>:<源文件字节 offset>` —— 字节 offset 天然区分重复内容，所以同一个问题
// 被问两次会得到两条不同 id 的记录，两条都必须保留。
//
// 禁止用「role + 归一化文本」的 hash 充当 id：那会把同源出现的两次相同 prompt 吞成一条，
// 正好丢掉用户真实问过的第二次。
type Record struct {
	ID     string  `json:"id"`
	Role   string  `json:"role"`
	At     string  `json:"at,omitempty"`
	Blocks []Block `json:"blocks"`
}

// Candidate 是一个可绑定的会话候选。
//
// 不含完整文件路径、不含 cwd 原文、不含任何用户 prompt 预览 —— 候选列表本身不泄露会话内容。
type Candidate struct {
	ID        string `json:"id"`
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	UpdatedAt string `json:"updated_at"`
}

// ── JSON 取值辅助。语义对齐参考实现的 session-scanner-values.ts，刻意保持宽松：
// provider 换字段时我们要「解不出来」而不是「解出垃圾」。 ──

// asRecord 把任意 JSON 值收窄成对象；不是对象则返回 nil。
func asRecord(value any) map[string]any {
	record, _ := value.(map[string]any)
	return record
}

// extractString 取非空字符串（去首尾空白后仍非空）。
func extractString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	return trimmed
}

// rawString 取原始字符串（不做 trim，空串也算有效内容）。
func rawString(value any) string {
	text, _ := value.(string)
	return text
}

// parseJSONObject 解析一行 JSONL；空行与非法 JSON 都返回 nil。
func parseJSONObject(line string) map[string]any {
	if strings.TrimSpace(line) == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(line), &value); err != nil {
		return nil
	}
	return asRecord(value)
}

// rawJSON 把任意 JSON 值重新编码成 RawMessage。provider 的入参对象原样透传，
// 这里只改变字节表示，不改变结构。
func rawJSON(value any) json.RawMessage {
	if value == nil {
		return json.RawMessage("null")
	}
	encoded, err := json.Marshal(value)
	if err != nil || !json.Valid(encoded) {
		return json.RawMessage("null")
	}
	return encoded
}

// isTrue 判断 JSON 布尔真值（只认真正的 true，不认 "true"）。
func isTrue(value any) bool {
	flag, ok := value.(bool)
	return ok && flag
}

// stringList 把数组里的字符串或 {text|content} 对象拼成一段文本。
func toolResultOutput(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				parts = append(parts, text)
				continue
			}
			record := asRecord(item)
			if record == nil {
				continue
			}
			if text := rawString(record["text"]); text != "" {
				parts = append(parts, text)
				continue
			}
			if text := rawString(record["content"]); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		if record := asRecord(value); record != nil {
			if text := rawString(record["text"]); text != "" {
				return text
			}
			if text := rawString(record["content"]); text != "" {
				return text
			}
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

// parseTimestamp 把 provider 的时间戳规整成 RFC3339。
//
// 接受 ISO 字符串与 epoch 数字（数字超过 1e12 视为毫秒，否则视为秒，与参考实现一致）。
// 解不出来就返回空串 —— 契约里 at 是可选字段，写空串会让客户端渲染成 1970 年。
func parseTimestamp(value any) string {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
			if parsed, err := time.Parse(layout, text); err == nil {
				return parsed.UTC().Format(time.RFC3339)
			}
		}
		return ""
	case float64:
		switch {
		case typed > 1_000_000_000_000:
			return time.UnixMilli(int64(typed)).UTC().Format(time.RFC3339)
		case typed > 0:
			return time.Unix(int64(typed), 0).UTC().Format(time.RFC3339)
		}
	}
	return ""
}
