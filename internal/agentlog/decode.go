package agentlog

import (
	"bytes"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxBlockBytes 是单个块的字节上限（契约常量 STRUCTURED_CHAT_MAX_BLOCK_BYTES）。
// 超过后正文被截断，并在块之后补一条说明用 text 块。
const MaxBlockBytes = 64 * 1024

// ElisionPrefix 是截断说明块的固定前缀（契约常量 STRUCTURED_CHAT_ELISION_PREFIX）。
const ElisionPrefix = "…（已截断"

// LineContext 是解码一行所需的、来自「文件」而不是「行」的上下文。
type LineContext struct {
	// Agent 是 CHAT_AGENTS 之一。
	Agent string
	// SessionID 只对 Codex 有意义，用来拼 `<session_id>:<offset>` 形式的记录 id。
	SessionID string
	// Offset 是该行首字节在源文件中的绝对偏移。
	Offset int64
	// ResponseItemPath 由文件级判定给出：该文件里出现过任何 response_item 帧。
	// Codex 双记形态靠它选规范消息路径，且这个选择是**按文件**做的，不做全局文本去重。
	ResponseItemPath bool
}

// DecodeLine 解码一行 JSONL。第二个返回值表示「这条消息形状的行没能产出任何内容」，
// 即契约里的 skipped。
func DecodeLine(context LineContext, line string) (*Record, bool) {
	switch context.Agent {
	case AgentClaude:
		return decodeClaudeLine(context, line)
	case AgentCodex:
		return decodeCodexLine(context, line)
	default:
		return nil, false
	}
}

// RangeEntry 是源文件里的一行。Record 为 nil 表示这一行没有产出记录（元数据、
// 设计上省略的 reasoning，或未能识别的内容）。
type RangeEntry struct {
	Record *Record
	// Start 是该行首字节的绝对偏移。
	Start int64
	// End 是该行结束的绝对偏移（含换行）。
	End int64
}

// RangeResult 是一段字节区间的解码结果。
type RangeResult struct {
	// Entries 严格按源文件行序排列。去重是**逐行**语义：分页停在行边界上，
	// 因此这里不预先合并，交给 UpsertEntries。
	Entries []RangeEntry
	// Skipped 是这段区间里消息形状但解码不出来的源记录条数。
	Skipped int
	// Consumed 是最后一条**完整行**之后的绝对偏移。尾部未闭合的半行不解析、不产出、
	// 也不推进游标；下次续读时整行重读。
	Consumed int64
}

// DecodeRange 解析一段 JSONL 字节区间。
//
// data[0] 的绝对偏移是 start。只有以 '\n' 结尾的完整行会被解析：尾部未闭合的半行属于
// 「Agent 正在写」，服务端不解析、不产出记录、游标也不前进。
func DecodeRange(context LineContext, data []byte, start int64) RangeResult {
	var (
		entries []RangeEntry
		skipped int
		offset  = start
	)
	line := context
	line.Offset = start
	for len(data) > 0 {
		indexByte := bytes.IndexByte(data, '\n')
		if indexByte < 0 {
			break
		}
		text := strings.TrimSuffix(string(data[:indexByte]), "\r")
		line.Offset = offset
		record, missed := DecodeLine(line, text)
		if missed {
			skipped++
		}
		entry := RangeEntry{Start: offset, End: offset + int64(indexByte) + 1}
		if record != nil {
			elided := ElideOversized(*record, MaxBlockBytes)
			entry.Record = &elided
		}
		entries = append(entries, entry)
		offset = entry.End
		data = data[indexByte+1:]
	}
	if entries == nil {
		entries = []RangeEntry{}
	}
	return RangeResult{Entries: entries, Skipped: skipped, Consumed: offset}
}

// UpsertEntries 按记录 id **原地更新**：同一 id 只保留一条，内容是最后一次出现的版本，
// 位置是第一次出现的位置。
//
// 契约明确禁止按「角色 + 归一化文本」去重 —— 那会把同源的两次相同 prompt 吞成一条，
// 正好丢掉用户真实问过的第二次。id 相同只代表 provider 重发/补齐了同一条记录。
func UpsertEntries(entries []RangeEntry) []Record {
	records := make([]Record, 0, len(entries))
	index := make(map[string]int, len(entries))
	for _, entry := range entries {
		if entry.Record == nil {
			continue
		}
		if position, exists := index[entry.Record.ID]; exists {
			records[position] = *entry.Record
			continue
		}
		index[entry.Record.ID] = len(records)
		records = append(records, *entry.Record)
	}
	return records
}

// FileShape 是从文件头若干行里读到的会话身份。
type FileShape struct {
	// CWD 是文件里真实记录的 cwd。取不到就是空串，调用方必须按「不匹配」处理，绝不猜。
	// 目录名编码是有损的（`/a-b` 与 `/a/b` 同码），所以归属只能由它精确相等来判定。
	CWD string
	// SessionID 是 provider 侧的会话 id（Codex 的 session_meta.id）。Claude 不写，留空。
	SessionID string
	// ResponseItemPath 表示该文件出现过 response_item 帧（含旧式未包裹形态）。
	ResponseItemPath bool
}

// ReadFileShape 从文件头窗口里读出会话身份。
//
// 只看完整行；窗口是文件头的一段，所以结论只在这一段内成立 —— Codex 从第一行
// session_meta 之后就开始交替写 response_item / event_msg，头窗口足够判定规范路径。
func ReadFileShape(agent string, data []byte) FileShape {
	var shape FileShape
	for _, line := range completeLines(data) {
		record := parseJSONObject(line)
		if record == nil {
			continue
		}
		switch agent {
		case AgentClaude:
			if shape.CWD == "" {
				if cwd := extractString(record["cwd"]); strings.HasPrefix(cwd, "/") {
					shape.CWD = cwd
				}
			}
		case AgentCodex:
			recordType := extractString(record["type"])
			payload := asRecord(record["payload"])
			if recordType == codexRecordResponseItem {
				shape.ResponseItemPath = true
			}
			if payload == nil {
				if codexResponseItemType(recordType) {
					// 旧式未包裹的 response_item：记录本身就是 item。
					shape.ResponseItemPath = true
				}
				continue
			}
			if recordType != "session_meta" {
				continue
			}
			if shape.CWD == "" {
				if cwd := extractString(payload["cwd"]); strings.HasPrefix(cwd, "/") {
					shape.CWD = cwd
				}
			}
			if shape.SessionID == "" {
				shape.SessionID = extractString(payload["id"])
			}
		}
	}
	return shape
}

// codexResponseItemType 报告该 type 是否属于未包裹 response_item 的 item 判别式。
func codexResponseItemType(recordType string) bool {
	switch recordType {
	case "message", "reasoning", "function_call", "local_shell_call", "custom_tool_call",
		"function_call_output", "custom_tool_call_output":
		return true
	}
	return false
}

// completeLines 切出完整行（以 '\n' 结尾）。尾部未闭合的半行被丢弃。
func completeLines(data []byte) []string {
	lines := make([]string, 0, 8)
	for len(data) > 0 {
		indexByte := bytes.IndexByte(data, '\n')
		if indexByte < 0 {
			break
		}
		lines = append(lines, strings.TrimSuffix(string(data[:indexByte]), "\r"))
		data = data[indexByte+1:]
	}
	return lines
}

// ElideOversized 把超过 limit 字节的块正文截断，并在块之后补一条说明用 text 块。
//
// 被截断的原始块本身保持结构完整：type 与其判别式字段都在，只把正文缩短到 limit 以内。
// 客户端据此可以识别并提示，无需新增响应字段。
func ElideOversized(record Record, limit int) Record {
	changed := false
	blocks := make([]Block, 0, len(record.Blocks)+1)
	for _, block := range record.Blocks {
		shortened, removed := elideBlock(block, limit)
		if removed == 0 {
			blocks = append(blocks, block)
			continue
		}
		changed = true
		blocks = append(blocks, shortened)
		blocks = append(blocks, Block{Type: BlockText, Text: ElisionPrefix + " " + itoa(removed) + " 字节）"})
	}
	if !changed {
		return record
	}
	record.Blocks = blocks
	return record
}

func elideBlock(block Block, limit int) (Block, int) {
	switch block.Type {
	case BlockText:
		if cut, removed := truncateUTF8(block.Text, limit); removed > 0 {
			block.Text = cut
			return block, removed
		}
	case BlockToolResult:
		if cut, removed := truncateUTF8(block.Output, limit); removed > 0 {
			block.Output = cut
			return block, removed
		}
	}
	return block, 0
}

// truncateUTF8 按 UTF-8 边界截断，返回截断后的字符串与被移除的字节数。
func truncateUTF8(value string, limit int) (string, int) {
	if limit < 0 || len(value) <= limit {
		return value, 0
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut], len(value) - cut
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[position:])
}

// ClaudeProjectDir 返回 cwd 对应的 Claude 项目目录名。
//
// 编码规则与 Claude Code 自身一致：每个非 [A-Za-z0-9] 字符替换成一个 '-'，
// **连续符号不折叠**，所以 `/home/x/.claude` 编码成 `-home-x--claude`。
//
// 这套编码是**有损**的：`/a-b` 与 `/a/b` 编码结果相同。因此它只能用来缩小搜索范围，
// 目录名绝不能作为 cwd 归属的证明 —— 归属必须落到记录内容里真实 cwd 的精确相等。
func ClaudeProjectDir(cwd string) string {
	trimmed := cwd
	if trimmed != "/" {
		trimmed = strings.TrimRight(trimmed, "/")
	}
	var builder strings.Builder
	builder.Grow(len(trimmed))
	// 按**字符**而不是字节替换：Claude Code 是逐字符替换的，按字节会把一个汉字
	// 变成三个 '-'，与磁盘上的真实目录名对不上。
	for _, character := range trimmed {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') {
			builder.WriteRune(character)
			continue
		}
		builder.WriteByte('-')
	}
	return builder.String()
}

var (
	// Claude 只收项目目录的直属 `<会话 id>.jsonl`：首字符必须是字母数字（挡掉隐藏文件
	// 与 ".."），不含路径分隔符，长度有界。
	claudeSessionFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.jsonl$`)
	// Codex 只收 rollout 文件，同样不含分隔符。
	codexSessionFile = regexp.MustCompile(`^rollout-[A-Za-z0-9][A-Za-z0-9._-]{0,191}\.jsonl$`)
)

// ClaudeSessionFile 报告文件名是否是合法的 Claude 会话日志名。
func ClaudeSessionFile(name string) bool { return claudeSessionFile.MatchString(name) }

// CodexSessionFile 报告文件名是否是合法的 Codex 会话日志名。
func CodexSessionFile(name string) bool { return codexSessionFile.MatchString(name) }

// SessionStem 去掉 `.jsonl` 后缀，得到 provider 侧会话 id（文件名即会话 id 时使用）。
func SessionStem(name string) string { return strings.TrimSuffix(name, ".jsonl") }
