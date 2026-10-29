// Package screenparse 从 Herdr pane.read 的 ANSI 画面里认出 Agent 的选择对话框与
// Claude 输入框里的未发送草稿。
//
// 这里只做纯解析：输入是一屏文本，输出是认出的结构或“认不出”。规则保守——只认
// 画面最底部带按键提示页脚、选项紧贴页脚的选择框；认不出就返回 nil / 无草稿，
// 由调用方回到终端操作，不猜。
package screenparse

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Option 是对话框里的一个选项。Index 对编号对话框是它的编号，否则是从 1 起的位置。
type Option struct {
	Index    int    `json:"index"`
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
}

// Dialog 是画面底部正在等待选择的对话框。Fingerprint 只覆盖问题与选项文字，
// 不含光标位置：方向键移动光标不改变用户看到的那个问题。
type Dialog struct {
	Title       string   `json:"title,omitempty"`
	Lines       []string `json:"lines"`
	Options     []Option `json:"options"`
	Numbered    bool     `json:"numbered"`
	Fingerprint string   `json:"fingerprint"`
	// Multiple：多选题（选项带勾选框）。卡片不作答，用户回终端操作。
	Multiple bool `json:"-"`
	// FreeText：光标停在“Type something”之类的自由输入项上，键入的文字进入对话框自己的输入框。
	FreeText bool `json:"-"`
}

// Selected 返回当前光标所在的选项位置（Options 下标），没有时为 -1。
func (d *Dialog) Selected() int {
	for i, option := range d.Options {
		if option.Selected {
			return i
		}
	}
	return -1
}

// Find 按 Index 找选项位置，没有时为 -1。
func (d *Dialog) Find(index int) int {
	for i, option := range d.Options {
		if option.Index == index {
			return i
		}
	}
	return -1
}

// escapePattern 匹配一个终端转义：CSI（分组 1 是参数、2 是终止字节，SGR 的终止字节是 m）、
// OSC，以及其他两字节转义（ESC 7、ESC = 等）。
var escapePattern = regexp.MustCompile(`\x1b(?:\[([0-?]*)[ -/]*([@-~])|\][^\x07\x1b]*(?:\x07|\x1b\\)|[ -/]*[0-~])`)

// Plain 去掉 ANSI 转义、回车与不换行空格。
func Plain(text string) string {
	text = escapePattern.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "\r", "")
	return strings.ReplaceAll(text, "\u00a0", " ")
}

// splitLines 把画面切成去掉右侧空白的纯文本行，并去掉末尾空行。
func splitLines(ansi string) (plain []string, raw []string) {
	raw = strings.Split(strings.ReplaceAll(ansi, "\r", ""), "\n")
	plain = make([]string, len(raw))
	for i, line := range raw {
		plain[i] = strings.TrimRight(Plain(line), " \t")
	}
	end := len(plain)
	for end > 0 && strings.TrimSpace(plain[end-1]) == "" {
		end--
	}
	return plain[:end], raw[:end]
}

// isRule：一整行横线（Claude 输入框上下沿、对话框顶边）。
func isRule(line string) bool {
	trimmed := strings.TrimSpace(line)
	if utf8.RuneCountInString(trimmed) < 8 {
		return false
	}
	return strings.Trim(trimmed, "─━═") == "" || labeledRule.MatchString(trimmed)
}

// 带标签的横线：`──── session-name ─`。
var labeledRule = regexp.MustCompile(`^─{2,}\s.*\s─+$`)

// isBoxEdge：对话框边框的顶/底边 `╭───╮`、`╰───╯`。
// 也认 Claude 新版选择器顶上的 `▔▔▔▔`（可能带一段提示文字）。
func isBoxEdge(line string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "▔▔▔▔") || strings.HasPrefix(trimmed, "▁▁▁▁") {
		return true
	}
	return (strings.HasPrefix(trimmed, "╭") || strings.HasPrefix(trimmed, "╰")) && strings.Trim(trimmed, "╭╮╰╯─ ") == ""
}

// unbox 去掉对话框左右边框 `│ … │`，保留内部缩进。
func unbox(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	if !strings.HasPrefix(trimmed, "│") {
		return line
	}
	inner := strings.TrimPrefix(trimmed, "│")
	inner = strings.TrimRight(inner, " ")
	inner = strings.TrimSuffix(inner, "│")
	return strings.TrimRight(inner, " ")
}

var (
	// 编号选项：`  2. No`；光标所在的一行带 `❯`（Claude）或 `›`（Codex），由分组 2 标出。
	numberedRow = regexp.MustCompile(`^(\s*)([❯›]\s*)?(\d{1,2})\.\s+(\S.*)$`)
	// 不编号列表的光标行：`❯ No, exit`。
	plainCursor = regexp.MustCompile(`^(\s*)❯\s+(\S.*)$`)
	// 选择器把名称和说明排成两列，中间是一串空格。
	columnGap = regexp.MustCompile(`\s{2,}`)
	// 按键提示页脚：`Enter to confirm · Esc to cancel`、`Press enter to confirm or esc to cancel`、
	// `↑/↓ to navigate`。不含 Claude 工作中的 `esc to interrupt`：那不是对话框。
	footerPattern = regexp.MustCompile(`(?i)\b(?:enter to (?:confirm|select|submit|continue)|esc to (?:cancel|go back|exit|close))\b|↑/?↓ to (?:navigate|select|move)`)
	// 多选题的勾选框：`[ ] Red`、`[x] Blue`、`☐ Green`。
	checkboxPattern = regexp.MustCompile(`^(?:\[[ xX✓✔]\]|[☐☑☒◻◼])\s`)
	// 自由输入类选项（按 Claude 提问框的实际文案列白名单，整行精确匹配）：选中后键入的文字进入
	// 对话框自己的输入框。不能前缀匹配，否则“Other projects”之类的普通选项也会放行。
	freeTextPattern = regexp.MustCompile(`(?i)^(?:type something|type something else|other)\s*(?:\.|…|\.\.\.)?$`)
)

// 对话框往上最多看这么多行找选项与说明文字。
const (
	maxDialogContext = 20
	maxScanLines     = 40
)

// ParseDialog 认出画面底部的对话框，认不出返回 nil。
//
// 只认确定的形态，选项必须在画面底部：
//   - 最后几行里有按键提示页脚（Claude 的选择框、Codex 的审批框；页脚下方可以还有一两行状态，
//     如 `⏵⏵ accept edits on`），选项紧贴在页脚上方，中间只有空行；
//   - 或者没有页脚，带光标的编号选项在画面最底部（下方至多两行状态），且上方有对话框顶边
//     （横线、边框、▔▔▔▔），如 Claude 的权限确认、Rewind。
//
// 选项或页脚下方出现横线或提示符（输入框、Shell、Codex 的输入行）时不算：输入框里的草稿、
// Codex 历史里回显的 `› 1. …`、Shell 输出都不会被当成对话框。认不出时调用方用 LooksLikeDialog
// 兜底，宁可不发送。
func ParseDialog(ansi string) *Dialog {
	lines := screenLines(ansi)
	if footer := footerLine(lines); footer >= 0 {
		region := trimDialogTail(lines[:footer])
		if len(region) == 0 {
			return nil
		}
		if numberedRow.MatchString(region[len(region)-1]) || hasNumberedCursor(region) {
			return parseNumbered(region, false)
		}
		return parseUnnumbered(region)
	}
	region, ok := belowStatus(lines, 0)
	if !ok || len(region) == 0 || !hasNumberedCursor(region) {
		return nil
	}
	return parseNumbered(region, true)
}

// LooksLikeDialog：画面底部像是有一个对话框（最后几行里有按键提示页脚，或底部有带光标的编号选项），
// 不管 ParseDialog 认不认得出。用于发送前兜底：认不出但像对话框时也不打字。
func LooksLikeDialog(ansi string) bool {
	lines := screenLines(ansi)
	if footerLine(lines) >= 0 {
		return true
	}
	region, ok := belowStatus(lines, 4)
	return ok && hasNumberedCursor(region)
}

// screenLines 是去掉边框左右竖线、末尾空行与边框底边后的纯文本行。
func screenLines(ansi string) []string {
	plain, _ := splitLines(ansi)
	lines := make([]string, len(plain))
	for i, line := range plain {
		lines[i] = unbox(line)
	}
	return trimDialogTail(lines)
}

// trimDialogTail 去掉末尾的空行和对话框边框的底边 `╰──╯`。
func trimDialogTail(lines []string) []string {
	end := len(lines)
	for end > 0 && (strings.TrimSpace(lines[end-1]) == "" || isBoxBottom(lines[end-1])) {
		end--
	}
	return lines[:end]
}

func isBoxBottom(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "╰") && isBoxEdge(line)
}

// 页脚在最后这么多行非空文字里找；页脚下方的行不能是横线或提示符。
const footerReach = 4

// footerLine 返回页脚所在行，没有时为 -1。
func footerLine(lines []string) int {
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < footerReach; i-- {
		line := lines[i]
		if strings.TrimSpace(line) == "" || isBoxBottom(line) {
			continue
		}
		if footerPattern.MatchString(line) {
			return i
		}
		if isEdge(line) || promptRow(line) {
			return -1
		}
		seen++
	}
	return -1
}

// belowStatus 去掉底部至多两行状态（再加 extra 行），返回其上方的选项区。状态行不能是横线、
// 提示符或编号选项本身；遇到横线或提示符说明下面是输入框或 Shell，返回 false。
func belowStatus(lines []string, extra int) ([]string, bool) {
	end := len(lines)
	for skipped := 0; end > 0; {
		line := lines[end-1]
		switch {
		case strings.TrimSpace(line) == "" || isBoxBottom(line):
		case isEdge(line) || promptRow(line):
			return nil, false
		case numberedRow.MatchString(line):
			return trimDialogTail(lines[:end]), true
		default:
			if skipped == 2+extra {
				return trimDialogTail(lines[:end]), true
			}
			skipped++
		}
		end--
	}
	return nil, true
}

// promptRow：输入框或 Shell 的提示符行（`❯ `、`› `、`> `、`$ `、`! ` 开头且不是编号选项）。
func promptRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || numberedRow.MatchString(line) {
		return false
	}
	for _, prefix := range []string{"❯", "›", ">", "$", "!", "%"} {
		if trimmed == prefix || strings.HasPrefix(trimmed, prefix+" ") {
			return true
		}
	}
	return false
}

// hasNumberedCursor：选项区（最后一个空段之后）里有带光标的编号行。
func hasNumberedCursor(lines []string) bool {
	for i := len(lines) - 1; i >= max(0, len(lines)-maxScanLines); i-- {
		if strings.TrimSpace(lines[i]) == "" || isEdge(lines[i]) {
			return false
		}
		if match := numberedRow.FindStringSubmatch(lines[i]); match != nil && match[2] != "" {
			return true
		}
	}
	return false
}

// isEdge：对话框的边线（横线、带标签的横线、边框顶/底边、▔▔▔▔）。
func isEdge(line string) bool {
	return isRule(line) || isBoxEdge(line)
}

type optionRow struct {
	line     int
	index    int
	label    string
	selected bool
}

// parseNumbered 从选项区底部往上收集连续的编号选项（直到编号 1）。选项下方可以有缩进更深的
// 说明行；空行、边线或缩进不够的文字都结束选项区。唯一的例外是 Claude 提问框在最后的
// “Chat about this”前画的一条分隔线：它上方紧挨着前一个编号时跳过。
// requireEdge：没有页脚时，对话框上方必须有顶边。
func parseNumbered(lines []string, requireEdge bool) *Dialog {
	start := max(0, len(lines)-maxScanLines)
	width := 0
	for _, line := range lines {
		width = max(width, cells(line))
	}
	var rows []optionRow
	below := len(lines)
	for i := len(lines) - 1; i >= start; i-- {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			break
		}
		if isEdge(line) {
			// 分隔线（上方可隔着空行）紧挨着前一个编号时跳过，否则是对话框顶边。
			prev := i - 1
			for prev >= start && strings.TrimSpace(lines[prev]) == "" {
				prev--
			}
			if len(rows) == 0 || prev < start || below != i+1 {
				break
			}
			above := numberedRow.FindStringSubmatch(lines[prev])
			if index, _ := strconv.Atoi(valueAt(above, 3)); above == nil || index != rows[0].index-1 {
				break
			}
			i, below = prev+1, prev+1
			continue
		}
		match := numberedRow.FindStringSubmatch(line)
		if match == nil {
			continue // 说明行，归属在遇到它上方的选项时检查
		}
		index, _ := strconv.Atoi(match[3])
		if len(rows) > 0 && index != rows[0].index-1 {
			break
		}
		if !detailsBelong(line, lines[i+1:below], utf8.RuneCountInString(match[1]+match[2]), width) {
			return nil
		}
		rows = append([]optionRow{{line: i, index: index, label: strings.TrimSpace(match[4]), selected: match[2] != ""}}, rows...)
		below = i
		if index == 1 {
			break
		}
	}
	if len(rows) < 2 || rows[0].index != 1 || selectedCount(rows) != 1 {
		return nil
	}
	return build(lines, rows, true, requireEdge)
}

// detailsBelong：option 下方这些行都属于它——说明行（缩进比编号所在列更深）、分隔线，或折行的
// 续行（缩进可以与编号列对齐，但上一行几乎占满画面宽度）。缩进不够又不像折行时无法确定，返回 false。
func detailsBelong(option string, lines []string, numberColumn, width int) bool {
	previous := option
	for _, line := range lines {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if !isEdge(line) && indent <= numberColumn && cells(previous) < width-12 {
			return false
		}
		previous = line
	}
	return true
}

func valueAt(match []string, index int) string {
	if index < len(match) {
		return match[index]
	}
	return ""
}

func selectedCount(rows []optionRow) int {
	n := 0
	for _, row := range rows {
		if row.selected {
			n++
		}
	}
	return n
}

// parseUnnumbered 认不编号的列表：光标行 `❯ label` 与其余选项的文字对齐在同一列，连续排列。
// 折行的选项续行也对齐在这一列，与下一个选项无法区分：有选项几乎占满画面宽度时不出卡片。
func parseUnnumbered(lines []string) *Dialog {
	start := max(0, len(lines)-maxScanLines)
	cursor, indent := -1, 0
	for i := len(lines) - 1; i >= start; i-- {
		if strings.TrimSpace(lines[i]) == "" || isEdge(lines[i]) {
			break
		}
		if match := plainCursor.FindStringSubmatch(lines[i]); match != nil {
			cursor, indent = i, utf8.RuneCountInString(match[1])+2
			break
		}
	}
	if cursor < 0 {
		return nil
	}
	sibling := func(line string) bool {
		runes := []rune(line)
		return len(runes) > indent && strings.TrimSpace(string(runes[:indent])) == "" && runes[indent] != ' '
	}
	first := cursor
	for first-1 >= start && sibling(lines[first-1]) {
		first--
	}
	// 光标行以下到选项区末尾必须都是兄弟选项。
	for i := cursor + 1; i < len(lines); i++ {
		if !sibling(lines[i]) {
			return nil
		}
	}
	last := len(lines) - 1
	if first == last {
		return nil
	}
	width := 0
	for _, line := range lines {
		width = max(width, cells(line))
	}
	var rows []optionRow
	for i := first; i <= last; i++ {
		if i < last && cells(lines[i]) >= width-12 {
			return nil
		}
		label := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "❯"))
		rows = append(rows, optionRow{line: i, index: i - first + 1, label: label, selected: i == cursor})
	}
	return build(lines, rows, false, false)
}

// build 补上选项上方的说明：一直往上到对话框顶边（横线或边框）为止；找不到顶边时
// 只取紧挨着选项的那一段，免得把上方 Agent 的回复当成说明。
func build(lines []string, rows []optionRow, numbered, requireEdge bool) *Dialog {
	top, bounded := max(0, rows[0].line-maxDialogContext), false
	for i := rows[0].line - 1; i >= top; i-- {
		if isEdge(lines[i]) {
			top, bounded = i+1, true
			break
		}
	}
	if !bounded {
		if requireEdge {
			return nil
		}
		top = unboundedTop(lines, rows[0].line, top)
	}

	// 终端折行的长句接回一行：上一行几乎占满画面宽度（以最宽的一行为准，按列计）时，下一行是它的续行。
	width := 0
	for _, line := range lines {
		width = max(width, cells(line))
	}
	var context []string
	wrapped := false
	for _, line := range lines[top:rows[0].line] {
		text := strings.TrimSpace(line)
		if text == "" || isBoxEdge(line) {
			wrapped = false
			continue
		}
		if wrapped {
			context[len(context)-1] = joinWrapped(context[len(context)-1], text)
		} else {
			context = append(context, text)
		}
		wrapped = cells(line) >= width-12
	}
	dialog := &Dialog{Numbered: numbered, Lines: []string{}}
	// 标题取最后一行问句（信任目录提示末尾是“Security guide”链接），其余作说明行。
	question := -1
	for i := len(context) - 1; i >= 0; i-- {
		if strings.ContainsAny(context[i], "?？") {
			question = i
			break
		}
	}
	// 没有问句时（模型选择器、Rewind）第一行就是标题。
	if question < 0 && len(context) > 0 {
		question = 0
	}
	for i, text := range context {
		if i == question {
			dialog.Title = text
		} else {
			dialog.Lines = append(dialog.Lines, text)
		}
	}
	hash := sha256.New()
	hash.Write([]byte(dialog.Title))
	for _, line := range dialog.Lines {
		hash.Write([]byte("\x00l" + line))
	}
	for _, row := range rows {
		dialog.Options = append(dialog.Options, Option{Index: row.index, Label: columnGap.ReplaceAllString(row.label, " · "), Selected: row.selected})
		dialog.Multiple = dialog.Multiple || checkboxPattern.MatchString(row.label)
		dialog.FreeText = dialog.FreeText || (row.selected && freeTextPattern.MatchString(row.label))
		hash.Write([]byte("\x00o" + strconv.Itoa(row.index) + "\x00" + row.label))
	}
	if numbered {
		hash.Write([]byte("\x00numbered"))
	}
	dialog.Fingerprint = hex.EncodeToString(hash.Sum(nil))[:16]
	return dialog
}

// agentMarker：Agent 回复与工具调用的行首标记，说明文字不会越过它们。
var agentMarker = regexp.MustCompile(`^\s*[●•⏺]`)

// unboundedTop：对话框没有顶边时，从选项往上按段收集说明，收到问句所在的段为止（最多三段），
// 遇到 Agent 回复的行首标记就停。
func unboundedTop(lines []string, first, limit int) int {
	top := first
	for paragraphs := 0; paragraphs < 3; paragraphs++ {
		i := top - 1
		for i >= limit && strings.TrimSpace(lines[i]) == "" {
			i--
		}
		question := false
		for i >= limit && strings.TrimSpace(lines[i]) != "" {
			if agentMarker.MatchString(lines[i]) {
				return top
			}
			question = question || strings.ContainsAny(lines[i], "?？")
			i--
		}
		if i+1 == top {
			return top
		}
		top = i + 1
		if question {
			break
		}
	}
	return top
}

// joinWrapped 接上折行：中日韩文字之间不补空格。
func joinWrapped(head, tail string) string {
	last, _ := utf8.DecodeLastRuneInString(head)
	first, _ := utf8.DecodeRuneInString(tail)
	if wide(last) || wide(first) {
		return head + tail
	}
	return head + " " + tail
}

func wide(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) || (r >= 0x3000 && r <= 0x303f) || (r >= 0xff00 && r <= 0xffef)
}

// cells 是一行在终端里占的列数（中日韩全角字占两列）。
func cells(line string) int {
	n := 0
	for _, r := range line {
		n++
		if wide(r) {
			n++
		}
	}
	return n
}
