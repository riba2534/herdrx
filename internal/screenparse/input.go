package screenparse

import (
	"regexp"
	"strings"
)

// 粘贴或图片在输入框里折叠成的占位，无论颜色都算草稿。
var placeholderPattern = regexp.MustCompile(`\[(?:Pasted text #\d+|Image #\d+)`)

// HasDraft 判断 Claude 输入框（画面最后两条横线之间，首行以 `❯` 或 `>` 开头）
// 是否有用户在电脑上打了但没发出去的文字。灰色（SGR 2 dim）文字是 Claude 的预测建议，
// 不算草稿；Claude 自己的光标（反显，落在第一个灰字上）也不算。bash 模式（`!`）算草稿：
// 发进去的文字会被当成命令执行。认不出输入框时返回 false，不猜。
func HasDraft(ansi string) bool {
	plain, raw := splitLines(ansi)
	bottom := -1
	for i := len(plain) - 1; i >= 0; i-- {
		if isRule(plain[i]) {
			bottom = i
			break
		}
	}
	if bottom < 0 {
		return false
	}
	top := -1
	for i := bottom - 1; i >= 0; i-- {
		if isRule(plain[i]) {
			top = i
			break
		}
	}
	if top < 0 || top+1 == bottom {
		return false
	}
	rows, rawRows := plain[top+1:bottom], raw[top+1:bottom]
	first := strings.TrimLeft(rows[0], " ")
	switch {
	case strings.HasPrefix(first, "!"):
		return true
	case strings.HasPrefix(first, "❯"), strings.HasPrefix(first, ">"):
	default:
		return false
	}
	if placeholderPattern.MatchString(strings.Join(rows, " ")) {
		return true
	}
	return typedText(rawRows)
}

// typedText 扫描提示符之后的字符：任何非 dim 的可见字符都是用户打的字，例外是第一个字符
// 上的反显光标——它后面跟着灰字时，是光标停在建议文字上。
func typedText(rows []string) bool {
	prompt, first, cursor, grey := false, true, false, false
	var style sgrState
	for _, row := range rows {
		var runs []styledRun
		runs, style = styleRuns(row, style)
		for _, run := range runs {
			for _, char := range run.text {
				if !prompt {
					prompt = char == '❯' || char == '>'
					continue
				}
				if char == ' ' || char == '\t' || char == '\u00a0' {
					continue
				}
				switch {
				case run.dim:
					grey = true
				case first && run.inverse:
					cursor = true
				default:
					return true
				}
				first = false
			}
		}
	}
	// 反显光标下是一个用户打的字、后面没有灰字时，也算草稿。
	return cursor && !grey
}

type styledRun struct {
	text         string
	dim, inverse bool
}

// sgrState 是跨行延续的 dim / 反显状态：一个灰字建议可能折到下一行，而下一行不一定重发 SGR。
type sgrState struct{ dim, inverse bool }

// styleRuns 按 dim（SGR 2）与反显（SGR 7）切分一行。38/48/58 颜色参数整体跳过，
// 免得 `38;2;r;g;b` 里的 2 被当成 dim；冒号形式（`38:2::r:g:b`）本身就是一个参数。
func styleRuns(line string, state sgrState) ([]styledRun, sgrState) {
	var runs []styledRun
	dim, inverse := state.dim, state.inverse
	offset := 0
	for _, match := range escapePattern.FindAllStringSubmatchIndex(line, -1) {
		if match[0] > offset {
			runs = append(runs, styledRun{line[offset:match[0]], dim, inverse})
		}
		offset = match[1]
		if match[4] < 0 || line[match[4]:match[5]] != "m" {
			continue
		}
		params := "0"
		if match[3] > match[2] {
			params = line[match[2]:match[3]]
		}
		codes := strings.Split(params, ";")
		for i := 0; i < len(codes); i++ {
			code := codes[i]
			if strings.Contains(code, ":") {
				continue
			}
			switch code {
			case "38", "48", "58":
				if i+1 < len(codes) {
					switch codes[i+1] {
					case "5":
						i += 2
					case "2":
						i += 4
					}
				}
			case "", "0", "00":
				dim, inverse = false, false
			case "2":
				dim = true
			case "22":
				dim = false
			case "7":
				inverse = true
			case "27":
				inverse = false
			}
		}
	}
	if offset < len(line) {
		runs = append(runs, styledRun{line[offset:], dim, inverse})
	}
	return runs, sgrState{dim, inverse}
}
