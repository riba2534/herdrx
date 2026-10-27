package agentlog

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Antigravity CLI 把一次对话写成
// `~/.gemini/antigravity-cli/conversations/<uuid>.db`。`steps.step_payload`
// 是未加密的 protobuf（`gemini_coder.Step`）。字段号对照二进制里的
// CortexStepType / Step oneof，以及在真实库上看到的路径：
//
//	14 USER_INPUT        字段 19。正文优先 19.1 query，否则 19.2 user_response，
//	                     再否则 19.3.1。19.12 是 user_config，不进入对话。
//	15 PLANNER_RESPONSE  字段 20。正文优先 20.1 response，否则用户库里看到的
//	                     20.6.12。20.7 重复的是工具调用（1 id、2 name、3 JSON 参数）。
//	                     20.3 / 20.16 是思考过程，契约没有 reasoning，直接丢掉。
//	132 GENERIC          工具结果。调用 id 在 metadata 5.4.1，输出在 140.2.1。
//	101 SYSTEM_MESSAGE   字段 114.1。
//	17 ERROR_MESSAGE     字段 24.3 里的 1 / 2 / 6，取第一段非空文本。
//	23 CHECKPOINT        字段 30.5。这是检查点，不是 planner 回复，记成 system。
//
// 其它 step_type、step_format 非 0、或解不出正文的行都跳过，不当成格式错误。
// 角色来自上述枚举名，不来自「看起来像用户/模型」的猜测。

var agyUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// AgyUUID 报告值是不是会话文件名用的 uuid。不含路径。
func AgyUUID(value string) bool {
	return agyUUIDPattern.MatchString(value)
}

// AgyDBRel 只接受 `<uuid>.db` 这一个相对路径。拒绝目录、绝对路径和别的后缀。
func AgyDBRel(rel string) bool {
	name, ok := strings.CutSuffix(rel, ".db")
	return ok && name != "" && !strings.Contains(rel, "/") && !strings.Contains(rel, `\`) && AgyUUID(name)
}

// AgySessionID 从 `<uuid>.db` 取出 uuid。不是这个形状就返回空串。
func AgySessionID(rel string) string {
	if !AgyDBRel(rel) {
		return ""
	}
	return strings.TrimSuffix(rel, ".db")
}

// AgyView 是调试输出用的摘要。只有长度，没有正文。
type AgyView struct {
	Idx      int64
	StepType int
	Role     string
	Kinds    []string
	Lengths  []int
}

// DecodeAgyStep 把一行 steps 解成一条记录。第二个返回值永远有 idx 和 step_type；
// 没有记录时 Role 为空。status 7 是 CortexStepStatus ERROR，只影响工具结果的 is_error。
func DecodeAgyStep(idx int64, stepType, status, stepFormat int, payload []byte) (*Record, AgyView) {
	view := AgyView{Idx: idx, StepType: stepType}
	if stepFormat != 0 || len(payload) == 0 {
		return nil, view
	}
	fields, ok := pbParse(payload)
	if !ok {
		return nil, view
	}
	record := agyRecord(idx, stepType, status, fields)
	if record == nil {
		return nil, view
	}
	elided := ElideOversized(*record, MaxBlockBytes)
	record = &elided
	view.Role = record.Role
	for _, block := range record.Blocks {
		view.Kinds = append(view.Kinds, block.Type)
		switch block.Type {
		case BlockText:
			view.Lengths = append(view.Lengths, len(block.Text))
		case BlockToolCall:
			view.Lengths = append(view.Lengths, len(block.Input))
		case BlockToolResult:
			view.Lengths = append(view.Lengths, len(block.Output))
		}
	}
	return record, view
}

func agyRecord(idx int64, stepType, status int, fields []pbField) *Record {
	var blocks []Block
	var role string
	switch stepType {
	case 14:
		role = RoleUser
		if text := agyFirstText(fields, []int{19, 1}, []int{19, 2}, []int{19, 3, 1}); text != "" {
			blocks = append(blocks, Block{Type: BlockText, Text: text})
		}
	case 15:
		role = RoleAssistant
		if text := agyFirstText(fields, []int{20, 1}, []int{20, 6, 12}); text != "" {
			blocks = append(blocks, Block{Type: BlockText, Text: text})
		}
		for _, call := range pbChildren(fields, 20) {
			for _, tool := range pbChildren(call, 7) {
				if block, ok := agyToolCallBlock(tool); ok {
					blocks = append(blocks, block)
				}
			}
		}
	case 132:
		role = RoleTool
		callID := ""
		for _, meta := range pbChildren(fields, 5) {
			for _, tool := range pbChildren(meta, 4) {
				id, _, _, ok := agyToolCall(tool)
				if ok && id != "" {
					callID = id
					break
				}
			}
			if callID != "" {
				break
			}
		}
		if text := pbTextAt(fields, 140, 2, 1); strings.TrimSpace(text) != "" {
			blocks = append(blocks, Block{
				Type:    BlockToolResult,
				CallID:  callID,
				Output:  text,
				IsError: status == 7,
			})
		}
	case 101:
		role = RoleSystem
		if text := strings.TrimSpace(pbTextAt(fields, 114, 1)); text != "" {
			blocks = append(blocks, Block{Type: BlockText, Text: text})
		}
	case 17:
		role = RoleSystem
		if text := agyFirstText(fields, []int{24, 3, 1}, []int{24, 3, 2}, []int{24, 3, 6}); text != "" {
			blocks = append(blocks, Block{Type: BlockText, Text: text})
		}
	case 23:
		role = RoleSystem
		if text := strings.TrimSpace(pbTextAt(fields, 30, 5)); text != "" {
			blocks = append(blocks, Block{Type: BlockText, Text: text})
		}
	default:
		return nil
	}
	if role == "" || len(blocks) == 0 {
		return nil
	}
	record := &Record{ID: strconv.FormatInt(idx, 10), Role: role, Blocks: blocks}
	if at := agyTimestamp(fields); at != "" {
		record.At = at
	}
	return record
}

func agyFirstText(fields []pbField, paths ...[]int) string {
	for _, path := range paths {
		if text := strings.TrimSpace(pbTextAt(fields, path...)); text != "" {
			return text
		}
	}
	return ""
}

func agyToolCallBlock(fields []pbField) (Block, bool) {
	id, name, input, ok := agyToolCall(fields)
	if !ok {
		return Block{}, false
	}
	return Block{Type: BlockToolCall, CallID: id, Name: name, Input: input}, true
}

func agyToolCall(fields []pbField) (string, string, json.RawMessage, bool) {
	id := strings.TrimSpace(pbTextAt(fields, 1))
	name := strings.TrimSpace(pbTextAt(fields, 2))
	args := strings.TrimSpace(pbTextAt(fields, 3))
	if id == "" && name == "" && args == "" {
		return "", "", nil, false
	}
	var input json.RawMessage
	switch {
	case args != "" && json.Valid([]byte(args)):
		input = json.RawMessage(args)
	case args == "":
		input = json.RawMessage("null")
	default:
		encoded, err := json.Marshal(args)
		if err != nil {
			input = json.RawMessage("null")
		} else {
			input = encoded
		}
	}
	return id, name, input, true
}

func agyTimestamp(fields []pbField) string {
	metas := pbChildren(fields, 5)
	if len(metas) == 0 {
		return ""
	}
	created := pbChildren(metas[0], 1)
	if len(created) == 0 {
		return ""
	}
	var seconds, nanos int64
	seen := false
	for _, field := range created[0] {
		if field.Wire != 0 {
			continue
		}
		switch field.Num {
		case 1:
			seconds = int64(field.U64)
			seen = true
		case 2:
			nanos = int64(field.U64)
		}
	}
	if !seen || seconds <= 0 {
		return ""
	}
	if nanos < 0 {
		nanos = 0
	}
	return time.Unix(seconds, nanos).UTC().Format(time.RFC3339)
}

// FormatAgyDebugLine 只打印序号、类型、角色、块种类和文本长度。
func FormatAgyDebugLine(view AgyView) string {
	role := view.Role
	if role == "" {
		role = "-"
	}
	kinds := "-"
	if len(view.Kinds) > 0 {
		kinds = strings.Join(view.Kinds, ",")
	}
	lengths := "-"
	if len(view.Lengths) > 0 {
		parts := make([]string, len(view.Lengths))
		for index, length := range view.Lengths {
			parts[index] = strconv.Itoa(length)
		}
		lengths = strings.Join(parts, ",")
	}
	return fmt.Sprintf("idx=%d step_type=%d role=%s blocks=%s text_lengths=%s", view.Idx, view.StepType, role, kinds, lengths)
}

// WriteAgyDebug 按 idx 顺序把每个 step 的摘要写到 w。任何错误都不包含 payload 正文。
func WriteAgyDebug(w io.Writer, path string) error {
	var after int64
	hasAfter := false
	for {
		rows, end, err := QueryAgyDB(nil, path, AgyBounds{
			HasAfter:      hasAfter,
			After:         after,
			Limit:         64,
			MaxStepBytes:  8 << 20,
			MaxTotalBytes: 8 << 20,
		})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			record, view := DecodeAgyStep(row.Idx, row.StepType, row.Status, row.StepFormat, row.Payload)
			if row.Oversize {
				view = AgyView{Idx: row.Idx, StepType: row.StepType}
				record = nil
			}
			_ = record
			if _, err := io.WriteString(w, FormatAgyDebugLine(view)+"\n"); err != nil {
				return err
			}
			after = row.Idx
			hasAfter = true
		}
		if end {
			return nil
		}
	}
}
