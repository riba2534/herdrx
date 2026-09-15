package agentlog

import (
	"bytes"
	"regexp"
	"strconv"
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

// lineOutcome 是一行 JSONL 的解码结果。
type lineOutcome struct {
	// record 非 nil 表示这一行产出了一条记录。
	record *Record
	// skipped 表示「这条消息形状的行没能产出任何内容」，即契约里的 skipped。
	skipped bool
	// superseded 表示这一行是落在模型可见 surface 上的改写，按设计不落地。
	// 它不是丢失：被遮蔽的原始记录仍然在自己的位置发布，只是这条副本不应用。
	superseded bool
	// failure 非 nil 表示这一行让整份日志不可信，调用方必须停止。
	failure *FormatFailure
}

// FormatFailure 报告「按格式必须理解、却无法安全理解」的内容。
//
// 它不是「某一行坏了」，而是「继续解释这份日志会得到错误的对话」。因此它必须向上传播成
// 明确的失败提示，不能被降级成空日志、错误格式或 skipped —— 那三种都会让用户以为
// 自己看到的是完整记录。
type FormatFailure struct {
	// Reason 是稳定的机器可读原因码。
	Reason string
	// Detail 是给人看的诊断，不含任何会话正文。
	Detail string
	// Offset 是该行首字节在源文件里的绝对偏移。
	Offset int64
}

// 稳定的失败原因码。
const (
	// FormatUnsupportedEvent 是 DSH v3 里出现了本实现不认识、又没有 ignorable 标记的事件。
	FormatUnsupportedEvent = "format_unsupported_event"
	// FormatUnsupportedVersion 是 DSH 日志的代际不是本实现支持的那一代。
	FormatUnsupportedVersion = "format_unsupported_version"
	// FormatUnsupportedSeeded 是 DSH 会话带继承前缀（isSeeded:true），本实现不支持。
	//
	// 继承前缀是父会话的上下文而不是本会话里用户当前说的话，切分边界要读完整个文件才知道，
	// 分页读取拿不到。宁可清楚地说「不支持」，也不能把继承来的上下文当成用户的提问发布。
	FormatUnsupportedSeeded = "format_unsupported_seeded"
	// FormatInvalidRow 是行的信封或 header 不符合该代际的准入规则。
	FormatInvalidRow = "format_invalid_row"
)

func (f *FormatFailure) Error() string {
	return "agentlog: " + f.Reason + " at offset " + strconv.FormatInt(f.Offset, 10) + ": " + f.Detail
}

// decodeLine 是带格式失败信号的逐行解码。
func decodeLine(context LineContext, line string) lineOutcome {
	switch context.Agent {
	case AgentClaude:
		record, skipped := decodeClaudeLine(context, line)
		return lineOutcome{record: record, skipped: skipped}
	case AgentCodex:
		record, skipped := decodeCodexLine(context, line)
		return lineOutcome{record: record, skipped: skipped}
	case AgentDSH:
		return decodeDSHLine(context, line)
	default:
		return lineOutcome{}
	}
}

// DecodeLine 解码一行 JSONL。第二个返回值表示「这条消息形状的行没能产出任何内容」，
// 即契约里的 skipped。
//
// 它丢掉了格式失败信号，只适合「一行一行地看看某一行解出什么」这种用法。
// 需要保证不静默丢掉必读内容的调用方用 DecodeLineChecked。
func DecodeLine(context LineContext, line string) (*Record, bool) {
	outcome := decodeLine(context, line)
	return outcome.record, outcome.skipped
}

// DecodeLineChecked 是 DecodeLine 的完整形态：第三个返回值非 nil 时，这一行让整份日志
// 不可信，调用方必须停止解码并如实报错。
func DecodeLineChecked(context LineContext, line string) (*Record, bool, *FormatFailure) {
	outcome := decodeLine(context, line)
	return outcome.record, outcome.skipped, outcome.failure
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
	// Replacements 是这段区间里落在模型可见 surface 上的改写条数（DSH 的 surfaceOp replace）。
	//
	// 这些记录**没有被应用**：应用它们意味着回头删掉已经发布的原始记录，分页读取下做不到，
	// 而假装应用会让用户以为看到的是模型侧的最新版本。它们也不是被丢弃 —— 原始记录仍在
	// 自己的位置发布，这里只是把「有多少条改写没有落地」如实报出来。
	Replacements int
	// Consumed 是最后一条**完整行**之后的绝对偏移。尾部未闭合的半行不解析、不产出、
	// 也不推进游标；下次续读时整行重读。
	Consumed int64
	// Failure 非 nil 表示这段区间里出现了无法安全解释的内容。此时 Consumed 停在该行
	// 首字节上（不越过它），Failure 之前已经解出的 Entries 仍然有效，但调用方必须把它
	// 当成终止条件：从同一个偏移重读只会得到同一个 Failure，不能当空日志重试。
	Failure *FormatFailure
}

// DecodeRange 解析一段 JSONL 字节区间。
//
// data[0] 的绝对偏移是 start。只有以 '\n' 结尾的完整行会被解析：尾部未闭合的半行属于
// 「Agent 正在写」，服务端不解析、不产出记录、游标也不前进。
func DecodeRange(context LineContext, data []byte, start int64) RangeResult {
	var (
		entries      []RangeEntry
		skipped      int
		replacements int
		offset       = start
		failure      *FormatFailure
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
		outcome := decodeLine(line, text)
		if outcome.failure != nil {
			outcome.failure.Offset = offset
			failure = outcome.failure
			break
		}
		if outcome.skipped {
			skipped++
		}
		if outcome.superseded {
			replacements++
		}
		entry := RangeEntry{Start: offset, End: offset + int64(indexByte) + 1}
		if outcome.record != nil {
			elided := ElideOversized(*outcome.record, MaxBlockBytes)
			entry.Record = &elided
		}
		entries = append(entries, entry)
		offset = entry.End
		data = data[indexByte+1:]
	}
	if entries == nil {
		entries = []RangeEntry{}
	}
	return RangeResult{Entries: entries, Skipped: skipped, Replacements: replacements, Consumed: offset, Failure: failure}
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
	// 目录名编码是有损的（`/a-b` 与 `/a/b` 同码，DSH 的 projectKey 连分隔符都会截断），
	// 所以归属只能由它精确相等来判定。
	CWD string
	// SessionID 是 provider 侧的会话 id（Codex 的 session_meta.id、DSH 的 header id）。
	// Claude 不写，留空。
	SessionID string
	// ResponseItemPath 表示该文件出现过 response_item 帧（含旧式未包裹形态）。
	ResponseItemPath bool
	// DSHVersion 是 DSH 日志 header 声明的会话格式代际。0 表示这不是 DSH 日志。
	//
	// 它不是「支持」的意思：只有等于 DSHFormatVersion 时 CWD/SessionID 才会被填上，
	// 别的代际只报告版本号，让调用方能如实说「这个版本暂不支持」，而不是含糊地说
	// 「不是 DSH 日志」，更不能用当前代际硬解。
	DSHVersion int
}

// ReadFileShape 从文件头窗口里读出会话身份。
//
// 只看完整行；窗口是文件头的一段，所以结论只在这一段内成立 —— Codex 从第一行
// session_meta 之后就开始交替写 response_item / event_msg，头窗口足够判定规范路径。
//
// DSH 只认**首行** header（上游同样把行 0 定为 header），且 data 必须是**已解压的明文行**。
// 调用方可以只喂第一个 zstd 帧的明文：那按上游构造恰好就是一行 header，后面的行（若有）
// 不会被读到。
func ReadFileShape(agent string, data []byte) FileShape {
	if agent == AgentDSH {
		return dshFileShape(data)
	}
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

// dshFileShape 只看首行。首行不是 header 就什么都不主张 —— 绝不往后面的行去找替补，
// header 是行 0 是格式本身的一部分，能读到别的东西说明这份文件已经不可信。
func dshFileShape(data []byte) FileShape {
	var shape FileShape
	lines := completeLines(data)
	if len(lines) == 0 {
		return shape
	}
	line := lines[0]
	version, ok := dshHeaderProbe(line)
	if !ok {
		return shape
	}
	shape.DSHVersion = version
	if version != DSHFormatVersion {
		// 陌生代际只报告版本号，不声明归属，也不按当前代际硬解。
		return shape
	}
	header, detail := parseDSHHeader(line)
	if detail != "" {
		// header 存在但准入不过：不声明任何归属。
		return shape
	}
	shape.CWD = header.CWD
	shape.SessionID = header.ID
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
