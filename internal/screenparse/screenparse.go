// Package screenparse 从 Herdr pane.read 的 ANSI 画面里认出 Agent 的选择对话框与
// Claude 输入框里的未发送草稿。
//
// 这里只做纯解析：输入是一屏文本，输出是认出的结构或“认不出”。规则保守——只认
// 底部带光标的编号选项、或带按键提示页脚的不编号列表；认不出就返回 nil / 无草稿，
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
	// 编号选项：`  2. No`；光标所在的一行带 `❯`（Claude）或 `›`（Codex），由分组 1 标出。
	numberedRow = regexp.MustCompile(`^\s*([❯›]\s*)?(\d{1,2})\.\s+(\S.*)$`)
	// 不编号列表的光标行：`❯ No, exit`。
	plainCursor = regexp.MustCompile(`^(\s*)❯\s+(\S.*)$`)
	// 选择器把名称和说明排成两列，中间是一串空格。
	columnGap = regexp.MustCompile(`\s{2,}`)
	// 按键提示页脚：`Enter to confirm · Esc to cancel`、`↑/↓ to navigate`。
	footerPattern = regexp.MustCompile(`(?i)\b(?:enter to (?:confirm|select|submit|continue)|esc to (?:cancel|go back|exit|close|interrupt))\b|↑/?↓ to (?:navigate|select|move)`)
)

// 对话框往上最多看这么多行找说明文字；往下最多看这么多行找页脚。
const (
	maxDialogContext = 20
	maxScanLines     = 40
)

// ParseDialog 认出画面底部的对话框，认不出返回 nil。
func ParseDialog(ansi string) *Dialog {
	plain, _ := splitLines(ansi)
	if len(plain) == 0 {
		return nil
	}
	lines := make([]string, len(plain))
	for i, line := range plain {
		lines[i] = unbox(line)
	}
	if dialog := parseNumbered(lines); dialog != nil {
		return dialog
	}
	return parseUnnumbered(lines)
}

// inputBoxBelow：光标行之下还有 Claude 输入框（横线后紧跟 `❯`/`>`/`!` 提示行）时，
// 那个 `❯ 1. …` 是历史或输入内容，不是正在等待的对话框。
func inputBoxBelow(lines []string, row int) bool {
	for i := row + 1; i < len(lines); i++ {
		if !isRule(lines[i]) {
			continue
		}
		for _, next := range lines[i+1:] {
			text := strings.TrimSpace(next)
			if text == "" {
				continue
			}
			if strings.HasPrefix(text, "❯") || strings.HasPrefix(text, ">") || strings.HasPrefix(text, "!") || isRule(text) {
				return true
			}
			break
		}
	}
	return false
}

func hasFooter(lines []string) bool {
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < 4; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		seen++
		if footerPattern.MatchString(lines[i]) {
			return true
		}
	}
	return false
}

type optionRow struct {
	line     int
	index    int
	label    string
	selected bool
}

func parseNumbered(lines []string) *Dialog {
	start := max(0, len(lines)-maxScanLines)
	cursor := -1
	for i := len(lines) - 1; i >= start; i-- {
		if match := numberedRow.FindStringSubmatch(lines[i]); match != nil && match[1] != "" {
			cursor = i
			break
		}
	}
	if cursor < 0 || inputBoxBelow(lines, cursor) {
		return nil
	}
	parse := func(i int) (optionRow, bool) {
		match := numberedRow.FindStringSubmatch(lines[i])
		if match == nil {
			return optionRow{}, false
		}
		index, _ := strconv.Atoi(match[2])
		return optionRow{line: i, index: index, label: strings.TrimSpace(match[3]), selected: match[1] != ""}, true
	}
	current, _ := parse(cursor)
	rows := []optionRow{current}
	// 向上收集编号递减的选项；中间允许选项自己的说明行（非空、非横线）。
	for i := cursor - 1; i >= start && rows[0].index > 1; i-- {
		if isRule(lines[i]) {
			break
		}
		if row, ok := parse(i); ok {
			if row.index != rows[0].index-1 {
				break
			}
			rows = append([]optionRow{row}, rows...)
		}
	}
	// 向下收集编号递增的选项。
	for i := cursor + 1; i < len(lines); i++ {
		if row, ok := parse(i); ok {
			if row.index != rows[len(rows)-1].index+1 {
				break
			}
			rows = append(rows, row)
		}
	}
	if rows[0].index != 1 || len(rows) < 2 {
		return nil
	}
	return build(lines, rows, true)
}

func parseUnnumbered(lines []string) *Dialog {
	if !hasFooter(lines) {
		return nil
	}
	start := max(0, len(lines)-maxScanLines)
	cursor, indent := -1, 0
	for i := len(lines) - 1; i >= start; i-- {
		if match := plainCursor.FindStringSubmatch(lines[i]); match != nil {
			cursor, indent = i, len([]rune(match[1]))+2
			break
		}
	}
	if cursor < 0 || inputBoxBelow(lines, cursor) {
		return nil
	}
	// 兄弟选项与光标行的文字对齐：前 indent 列为空白，第 indent 列起是文字。
	sibling := func(line string) bool {
		runes := []rune(line)
		return len(runes) > indent && strings.TrimSpace(string(runes[:indent])) == "" && runes[indent] != ' ' && !footerPattern.MatchString(line)
	}
	first, last := cursor, cursor
	for first-1 >= start && sibling(lines[first-1]) {
		first--
	}
	for last+1 < len(lines) && sibling(lines[last+1]) {
		last++
	}
	if first == last {
		return nil
	}
	var rows []optionRow
	for i := first; i <= last; i++ {
		label := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "❯"))
		rows = append(rows, optionRow{line: i, index: i - first + 1, label: label, selected: i == cursor})
	}
	return build(lines, rows, false)
}

// build 补上选项上方的说明：一直往上到对话框顶边（横线或边框）为止。
func build(lines []string, rows []optionRow, numbered bool) *Dialog {
	top := max(0, rows[0].line-maxDialogContext)
	for i := rows[0].line - 1; i >= top; i-- {
		if isRule(lines[i]) || isBoxEdge(lines[i]) {
			top = i + 1
			break
		}
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
		hash.Write([]byte("\x00o" + strconv.Itoa(row.index) + "\x00" + row.label))
	}
	if numbered {
		hash.Write([]byte("\x00numbered"))
	}
	dialog.Fingerprint = hex.EncodeToString(hash.Sum(nil))[:16]
	return dialog
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
