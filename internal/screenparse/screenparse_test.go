package screenparse

import (
	"reflect"
	"strings"
	"testing"
)

// 夹具按真实 Claude Code / Codex 画面的形态手工构造（路径、命令均为示例），
// 每行用 \r\n 结尾，与 pane.read 的输出一致。
const rule = "────────────────────────────────────────────────────────────"

func screen(lines ...string) string { return strings.Join(lines, "\r\n") + "\r\n" }

const (
	dim     = "\x1b[2m"
	inverse = "\x1b[7m"
	reset   = "\x1b[0m"
	grey    = "\x1b[38;2;136;136;136m"
)

var permissionPrompt = screen(
	"● I'll clean the build directory first.",
	"",
	grey+rule+reset,
	" Bash command",
	"",
	"   rm -rf build",
	"   Remove the build output",
	"",
	" Do you want to proceed?",
	" \x1b[38;2;177;185;249m❯ 1. Yes"+reset,
	"   2. Yes, and don't ask again for rm commands in /workspace/example",
	"   3. No, and tell Claude what to do differently (esc)",
	"",
	"",
)

var planPrompt = func(selected int) string {
	labels := []string{"Yes, and auto-accept edits", "Yes, and manually approve edits", "No, keep planning"}
	rows := []string{rule, " Ready to code?", "", " Here is Claude's plan:", "   1. Add the parser", "   2. Wire the route", "", " Would you like to proceed?", ""}
	for i, label := range labels {
		prefix := "   "
		if i+1 == selected {
			prefix = " ❯ "
		}
		rows = append(rows, prefix+string(rune('1'+i))+". "+label)
	}
	return screen(rows...)
}

var askQuestion = screen(
	"● Let me ask which theme you prefer.",
	"",
	rule,
	" ☐ Theme",
	"",
	"Which color scheme should the dashboard use?",
	"",
	"  1. Light",
	"     Bright background for daytime",
	"❯ 2. Dark",
	"     Low-glare background",
	"  3. Type something.",
	"",
	rule,
	"  4. Chat about this",
	"",
	"Enter to select · ↑/↓ to navigate · Esc to cancel",
)

var trustPrompt = screen(
	"$ claude",
	"",
	rule,
	" Accessing workspace:",
	"",
	" /workspace/example",
	"",
	" Quick safety check: Is this a project you created or one you trust?",
	"",
	" Security guide",
	"",
	" \x1b[1m❯ No, exit"+reset,
	"   Yes, I trust this folder",
	"",
	" Enter to confirm · Esc to cancel",
)

var boxedPrompt = screen(
	"╭──────────────────────────────────────────╮",
	"│ Edit file                                │",
	"│ src/app.ts                               │",
	"│                                          │",
	"│ Do you want to make this edit?           │",
	"│ ❯ 1. Yes                                 │",
	"│   2. No, and tell Claude what to do      │",
	"╰──────────────────────────────────────────╯",
)

var codexApproval = screen(
	"• Running cargo test",
	"",
	"  Allow command?",
	"",
	"  $ cargo test --workspace",
	"",
	"› 1. Yes, proceed",
	"  2. Yes, and don't ask again for this command",
	"  3. No, and tell Codex what to do differently",
	"",
	"  Press enter to confirm or esc to cancel",
)

func inputBox(rows ...string) string {
	lines := []string{"● Done. The tests pass.", "", grey + rule + reset}
	lines = append(lines, rows...)
	lines = append(lines, grey+rule+reset, "  ? for shortcuts")
	return screen(lines...)
}

func TestParseDialogRecognizesKnownDialogs(t *testing.T) {
	cases := []struct {
		name     string
		screen   string
		title    string
		lines    []string
		options  []Option
		numbered bool
	}{
		{"permission", permissionPrompt, "Do you want to proceed?", []string{"Bash command", "rm -rf build", "Remove the build output"}, []Option{
			{1, "Yes", true}, {2, "Yes, and don't ask again for rm commands in /workspace/example", false}, {3, "No, and tell Claude what to do differently (esc)", false},
		}, true},
		{"plan", planPrompt(2), "Would you like to proceed?", []string{"Ready to code?", "Here is Claude's plan:", "1. Add the parser", "2. Wire the route"}, []Option{
			{1, "Yes, and auto-accept edits", false}, {2, "Yes, and manually approve edits", true}, {3, "No, keep planning", false},
		}, true},
		{"trust", trustPrompt, "Quick safety check: Is this a project you created or one you trust?", []string{"Accessing workspace:", "/workspace/example", "Security guide"}, []Option{
			{1, "No, exit", true}, {2, "Yes, I trust this folder", false},
		}, false},
		{"boxed", boxedPrompt, "Do you want to make this edit?", []string{"Edit file", "src/app.ts"}, []Option{
			{1, "Yes", true}, {2, "No, and tell Claude what to do", false},
		}, true},
		{"codex", codexApproval, "Allow command?", []string{"$ cargo test --workspace"}, []Option{
			{1, "Yes, proceed", true}, {2, "Yes, and don't ask again for this command", false}, {3, "No, and tell Codex what to do differently", false},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dialog := ParseDialog(tc.screen)
			if dialog == nil {
				t.Fatal("dialog not recognized")
			}
			if dialog.Title != tc.title || !reflect.DeepEqual(dialog.Lines, tc.lines) || dialog.Numbered != tc.numbered {
				t.Fatalf("title=%q lines=%q numbered=%v", dialog.Title, dialog.Lines, dialog.Numbered)
			}
			if !reflect.DeepEqual(dialog.Options, tc.options) {
				t.Fatalf("options=%+v", dialog.Options)
			}
			if len(dialog.Fingerprint) != 16 {
				t.Fatalf("fingerprint=%q", dialog.Fingerprint)
			}
		})
	}
}

// AskUserQuestion 的选项中间夹着说明行，“Chat about this”与前面的选项隔着一条横线：
// 横线下不是输入框，编号接续，照样认。
func TestParseDialogAskUserQuestion(t *testing.T) {
	dialog := ParseDialog(askQuestion)
	if dialog == nil {
		t.Fatal("dialog not recognized")
	}
	want := []Option{{1, "Light", false}, {2, "Dark", true}, {3, "Type something.", false}, {4, "Chat about this", false}}
	if !reflect.DeepEqual(dialog.Options, want) || dialog.Title != "Which color scheme should the dashboard use?" || !reflect.DeepEqual(dialog.Lines, []string{"☐ Theme"}) {
		t.Fatalf("dialog=%+v", dialog)
	}
}

func TestParseDialogRejectsWhatItCannotRecognize(t *testing.T) {
	cases := map[string]string{
		"empty":       "",
		"blank":       screen("", "", ""),
		"idle box":    inputBox("❯ "),
		"typed box":   inputBox("❯ 1. fix the parser"),
		"plain shell": screen("$ ls", "a b c", "$ "),
		// Agent 回复里的编号列表没有光标，不是对话框。
		"numbered output": screen("● Plan:", "  1. Parse", "  2. Route", "  3. Test", "", grey+rule+reset, "❯ ", grey+rule+reset),
		// 历史里的旧对话框被输入框压在下面。
		"history dialog": screen(" Do you want to proceed?", " ❯ 1. Yes", "   2. No", "", rule, "❯ ", rule, "  ? for shortcuts"),
		// 一个选项不成对话框。
		"single option": screen(rule, " Continue?", " ❯ 1. Yes", "", " Esc to cancel"),
		// 编号不从 1 起。
		"not from one": screen(rule, " Pick", "   2. A", " ❯ 3. B", "   4. C"),
		// 不编号列表必须有按键提示页脚。
		"unnumbered without footer": screen(rule, " Pick one", " ❯ No, exit", "   Yes, I trust this folder"),
		"unnumbered single row":     screen(rule, " Trust?", " ❯ Yes", "", " Enter to confirm · Esc to cancel"),
		// 没有页脚时，编号选项上方必须有对话框顶边。
		"numbered without footer or edge": screen("● Steps:", " Do you want to proceed?", " ❯ 1. Yes", "   2. No"),
		// Codex 历史里回显的用户消息 `› 1. …`，底部是 Codex 自己的输入框。
		"codex history echo": screen("› 1. fix the parser", "› 2. add tests", "", "• Working (3s)", "", "› ", "", "  ⏎ send   ⇧⏎ newline   ⌃C quit"),
		// 输入框里的编号草稿。
		"numbered draft": inputBox("❯ 1. first step", "  2. second step"),
		// Shell 里 cat 出来的同形文本：页脚下面还有提示符。
		"cat output": screen("$ cat notes.txt", " Do you want to proceed?", " ❯ 1. Yes", "   2. No", "", " Enter to confirm · Esc to cancel", "$ "),
		// Claude 工作中的状态行不是对话框页脚。
		"working status": screen("● Plan:", " ❯ 1. Parse", "   2. Route", "", "✻ Thinking… (esc to interrupt)"),
		// 选项之间隔着空段：不是同一组选项。
		"options split by blank": screen(rule, " Proceed?", " ❯ 1. Yes", "", "   2. No", "", " Esc to cancel"),
		// 不编号选项折行：续行与下一个选项对齐在同一列，无法区分。
		"wrapped unnumbered option": screen(rule, " Trust?", " ❯ Yes, I trust this folder and every project below it too", "   and its subfolders", "   No, exit", "", " Enter to confirm · Esc to cancel"),
		// 页脚上方紧挨的不是选项。
		"footer under prose": screen(rule, " Some help text", "", " Esc to cancel"),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if dialog := ParseDialog(text); dialog != nil {
				t.Fatalf("unexpected dialog: %+v", dialog)
			}
		})
	}
}

func TestDialogFingerprint(t *testing.T) {
	first, second := ParseDialog(planPrompt(1)), ParseDialog(planPrompt(3))
	if first == nil || second == nil {
		t.Fatal("plan prompt not recognized")
	}
	if first.Fingerprint != second.Fingerprint {
		t.Fatal("moving the cursor must not change the fingerprint")
	}
	if first.Selected() != 0 || second.Selected() != 2 {
		t.Fatalf("selected=%d,%d", first.Selected(), second.Selected())
	}
	plain := ParseDialog(Plain(permissionPrompt))
	colored := ParseDialog(permissionPrompt)
	if plain == nil || colored == nil || plain.Fingerprint != colored.Fingerprint {
		t.Fatal("colors must not change the fingerprint")
	}
	changed := ParseDialog(strings.Replace(permissionPrompt, "rm -rf build", "rm -rf dist", 1))
	if changed == nil || changed.Fingerprint == colored.Fingerprint {
		t.Fatal("a different command must change the fingerprint")
	}
	relabeled := ParseDialog(strings.Replace(permissionPrompt, "2. Yes, and", "2. Sure, and", 1))
	if relabeled == nil || relabeled.Fingerprint == colored.Fingerprint {
		t.Fatal("a different option must change the fingerprint")
	}
	if colored.Find(3) != 2 || colored.Find(9) != -1 {
		t.Fatal("Find by index")
	}
}

func TestHasDraft(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   bool
	}{
		{"empty box", inputBox("❯ "), false},
		{"empty box with cursor", inputBox("❯ " + inverse + " " + reset), false},
		{"typed", inputBox("❯ fix the login bug"), true},
		{"typed with cursor", inputBox("❯ fix" + inverse + " " + reset), true},
		{"cursor on one typed char", inputBox("❯ " + inverse + "x" + reset), true},
		{"dim suggestion", inputBox("❯ " + dim + "add all three to the README" + reset), false},
		{"cursor on suggestion", inputBox("❯ " + inverse + "a" + reset + dim + "dd tests" + reset), false},
		{"typed then suggestion", inputBox("❯ run" + dim + " the tests" + reset), true},
		{"truecolor two is not dim", inputBox("❯ \x1b[38;2;2;2;2mhello" + reset), true},
		{"256 color two is not dim", inputBox("❯ \x1b[38;5;2mhello" + reset), true},
		{"dim reset by 22", inputBox("❯ " + dim + "hint\x1b[22mtyped"), true},
		{"multiline second row", inputBox("❯ first line", "  second line"), true},
		{"multiline dim", inputBox("❯ "+dim+"first", "  second"+reset), false},
		{"bash mode", inputBox("! ls -la"), true},
		{"legacy prompt", inputBox("> hello"), true},
		{"paste placeholder", inputBox("❯ " + dim + "[Pasted text #1 +20 lines]" + reset), true},
		{"image placeholder", inputBox("❯ [Image #1] "), true},
		{"no box", screen("$ ls", "a b"), false},
		{"one rule", screen(rule, "❯ hello"), false},
		{"not a prompt row", inputBox("  Esc to cancel"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasDraft(tc.screen); got != tc.want {
				t.Fatalf("HasDraft=%v want %v", got, tc.want)
			}
		})
	}
}

func TestPlainStripsEscapes(t *testing.T) {
	got := Plain("\x1b]0;title\x07\x1b[1;31mred\x1b[0m\x1b7 x\r")
	if got != "red x" {
		t.Fatalf("Plain=%q", got)
	}
}

// 新版选择器：顶边是 `▔▔▔▔`（带提示文字），没有问句时第一行作标题；两列选项用 · 连起来。
func TestParseDialogPickerWithoutQuestion(t *testing.T) {
	picker := screen(
		"● Earlier reply",
		"▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔ Ctrl+Y to paste deleted text ▔",
		"  Select model",
		"  Switch between models for new sessions.",
		"",
		"    1. Default (recommended)   Use the default model",
		"  ❯ 2. Fast                    Quick answers ✔",
		"    3. Large                   Hardest tasks",
		"",
		"  Enter to confirm · Esc to exit",
	)
	dialog := ParseDialog(picker)
	if dialog == nil {
		t.Fatal("picker not recognized")
	}
	if dialog.Title != "Select model" || !reflect.DeepEqual(dialog.Lines, []string{"Switch between models for new sessions."}) {
		t.Fatalf("title=%q lines=%q", dialog.Title, dialog.Lines)
	}
	want := []Option{{1, "Default (recommended) · Use the default model", false}, {2, "Fast · Quick answers ✔", true}, {3, "Large · Hardest tasks", false}}
	if !reflect.DeepEqual(dialog.Options, want) {
		t.Fatalf("options=%+v", dialog.Options)
	}
}

// 终端折行的长句接回一行；中文续行不补空格。
func TestParseDialogJoinsWrappedLines(t *testing.T) {
	text := screen(
		strings.Repeat("─", 40),
		" This folder is new to the agent. Do you",
		" trust it?",
		" 这个目录里的文件会被读取编辑和执行请先",
		" 确认来源。",
		"",
		" ❯ Yes",
		"   No",
		"",
		" Enter to confirm · Esc to cancel",
	)
	dialog := ParseDialog(text)
	if dialog == nil {
		t.Fatal("dialog not recognized")
	}
	if dialog.Title != "This folder is new to the agent. Do you trust it?" || !reflect.DeepEqual(dialog.Lines, []string{"这个目录里的文件会被读取编辑和执行请先确认来源。"}) {
		t.Fatalf("title=%q lines=%q", dialog.Title, dialog.Lines)
	}
}

// 助手回复里的编号列表在对话框正上方（没有顶边）：选项只收紧挨页脚的那一组，说明不越过回复标记。
func TestParseDialogDoesNotPullAgentListsIntoTheDialog(t *testing.T) {
	for name, text := range map[string]string{
		"unbounded": screen("● Steps:", "  1. Read the config", "  2. Write the parser", "", " Do you want to proceed?", " ❯ 1. Yes", "   2. No", "", " Esc to cancel"),
		"edge":      screen("● Steps:", "  1. Read the config", "  2. Write the parser", "▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔", "  3. Extra", " ❯ 1. Yes", "   2. No", "", " Esc to cancel"),
	} {
		t.Run(name, func(t *testing.T) {
			dialog := ParseDialog(text)
			if dialog == nil {
				t.Fatal("dialog not recognized")
			}
			if want := []Option{{1, "Yes", true}, {2, "No", false}}; !reflect.DeepEqual(dialog.Options, want) {
				t.Fatalf("options=%+v", dialog.Options)
			}
			for _, line := range append([]string{dialog.Title}, dialog.Lines...) {
				if strings.Contains(line, "Read the config") || strings.Contains(line, "Steps") {
					t.Fatalf("agent text leaked into the dialog: %+v", dialog)
				}
			}
		})
	}
}

// 多选题不能用一次按键作答；光标停在自由输入项上时，键入的文字进入对话框自己的输入框。
func TestParseDialogMarksMultiSelectAndFreeText(t *testing.T) {
	multi := ParseDialog(screen(rule, " Which toppings?", " ❯ 1. [ ] Cheese", "   2. [x] Basil", "   3. [ ] Olives", "", " Enter to select · Space to toggle · Esc to cancel"))
	if multi == nil || !multi.Multiple {
		t.Fatalf("multi-select not marked: %+v", multi)
	}
	if single := ParseDialog(planPrompt(1)); single == nil || single.Multiple || single.FreeText {
		t.Fatalf("plain dialog marked: %+v", single)
	}
	free := ParseDialog(strings.Replace(strings.Replace(askQuestion, "❯ 2. Dark", "  2. Dark", 1), "  3. Type something.", "❯ 3. Type something.", 1))
	if free == nil || !free.FreeText || free.Selected() != 2 {
		t.Fatalf("free-text cursor not marked: %+v", free)
	}
	if ParseDialog(askQuestion).FreeText {
		t.Fatal("cursor on a regular option must not be free text")
	}
}

// 漏判会让正文里的数字或 y/n 加回车直接确认光标项，这些真实变体都必须认出来。
func TestParseDialogRecognizesBottomVariants(t *testing.T) {
	wide := strings.Repeat("─", 60)
	cases := map[string]struct {
		screen   string
		options  int
		selected int
	}{
		"status under footer":                 {askQuestion + "  ⏵⏵ accept edits on (shift+tab to cycle)\r\n", 4, 1},
		"status under options without footer": {strings.TrimRight(permissionPrompt, "\r\n") + "\r\n  ⏵⏵ accept edits on (shift+tab to cycle)\r\n", 3, 0},
		"footer inside the box": {screen(
			"╭──────────────────────────────────────────╮",
			"│ Do you want to make this edit?           │",
			"│ ❯ 1. Yes                                 │",
			"│   2. No                                  │",
			"│                                          │",
			"│ Esc to cancel                            │",
			"╰──────────────────────────────────────────╯",
		), 2, 0},
		"top edge scrolled away": {screen(" Do you want to proceed?", " ❯ 1. Yes", "   2. No, and tell Claude what to do differently", "", " Esc to cancel"), 2, 0},
		"wrapped option at the number column": {screen(
			wide,
			" Do you want to proceed?",
			" ❯ 1. Yes, and don't ask again for rm commands in this project dir",
			"   /workspace/example",
			"   2. No",
			"",
			" Esc to cancel",
		), 2, 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dialog := ParseDialog(tc.screen)
			if dialog == nil || len(dialog.Options) != tc.options || dialog.Selected() != tc.selected {
				t.Fatalf("dialog=%+v", dialog)
			}
			if !LooksLikeDialog(tc.screen) {
				t.Fatal("LooksLikeDialog must agree")
			}
		})
	}
}

// 认不出但像对话框：发送前兜底拒绝。历史回显、输入框草稿、Shell 输出不算。
func TestLooksLikeDialog(t *testing.T) {
	suspicious := map[string]string{
		"footerless without edge": screen("● Steps:", " Do you want to proceed?", " ❯ 1. Yes", "   2. No"),
		"unparsed footer":         screen(rule, " Some help text", "", " Esc to cancel"),
		"wrapped unnumbered":      screen(rule, " Trust?", " ❯ Yes, I trust this folder and every project below it too", "   and its subfolders", "   No, exit", "", " Enter to confirm · Esc to cancel"),
	}
	for name, text := range suspicious {
		if !LooksLikeDialog(text) {
			t.Errorf("%s: not flagged", name)
		}
	}
	calm := map[string]string{
		"codex history echo": screen("› 1. fix the parser", "› 2. add tests", "", "• Working (3s)", "", "› ", "", "  ⏎ send   ⇧⏎ newline   ⌃C quit"),
		"numbered draft":     inputBox("❯ 1. first step", "  2. second step"),
		"cat output":         screen("$ cat notes.txt", " Do you want to proceed?", " ❯ 1. Yes", "   2. No", "", " Enter to confirm · Esc to cancel", "$ "),
		"idle box":           inputBox("❯ "),
		"plain shell":        screen("$ ls", "a b c", "$ "),
		"agent list":         screen("● Plan:", "  1. Parse", "  2. Route", "", grey+rule+reset, "❯ ", grey+rule+reset, "  ? for shortcuts"),
	}
	for name, text := range calm {
		if LooksLikeDialog(text) {
			t.Errorf("%s: flagged", name)
		}
	}
}

// 自由输入项只认整行精确的文案。
func TestFreeTextIsAnExactLabel(t *testing.T) {
	question := func(label string) string {
		return screen(rule, " Which one?", "  1. Alpha", "❯ 2. "+label, "", " Enter to select · Esc to cancel")
	}
	for label, want := range map[string]bool{"Type something.": true, "Type something": true, "Other": true, "Other…": true, "Other projects": false, "Type something else entirely": false, "Others": false} {
		dialog := ParseDialog(question(label))
		if dialog == nil || dialog.FreeText != want {
			t.Errorf("%q: dialog=%+v want free text %v", label, dialog, want)
		}
	}
}
