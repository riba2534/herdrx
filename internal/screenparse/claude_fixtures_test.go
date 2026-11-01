package screenparse

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// testdata/*.ansi.txt 是 Claude Code 2.1.316 全屏渲染（CLAUDE_CODE_NO_FLICKER=1）、免授权启动时
// `pane.read --format ansi` 读到的真实画面，用户名与主机名已脱敏。全屏渲染时对话框下方是一条带
// 会话名的横线（plan-dialog 时会话还没起名，没有这条线）。
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name + ".ansi.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const (
	tabsUnanswered = "←  ☐ 测试框架  ☐ 类型注解  ✔ Submit  →"
	planTitle      = "Claude has written up a plan and is ready to execute. Would you like to proceed?"
	planBypass     = "Yes, and switch to BYPASS PERMISSIONS (no further prompts) for this session"
	planFeedback   = "shift+tab to approve with this feedback"
)

func TestParseDialogClaudeFixtures(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		title    string
		lines    []string
		options  []Option
		numbered bool
		freeText bool
	}{
		{"question-multi", KindQuestion, "测试框架用 pytest 还是 unittest？", []string{tabsUnanswered}, []Option{
			{1, "pytest", true, "使用 pytest 作为测试框架"}, {2, "unittest", false, "使用 unittest 作为测试框架"}, {3, "Type something.", false, ""}, {4, "Chat about this", false, ""},
		}, true, false},
		{"question-multi-step2", KindQuestion, "要不要加类型注解？", []string{"←  ☒ 测试框架  ☐ 类型注解  ✔ Submit  →"}, []Option{
			{1, "要", true, "给函数加上类型注解"}, {2, "不要", false, "不加类型注解"}, {3, "Type something.", false, ""}, {4, "Chat about this", false, ""},
		}, true, false},
		// 确认页没有按键提示页脚。
		{"question-multi-submit", KindReview, "Ready to submit your answers?", []string{
			"←  ☒ 测试框架  ☒ 类型注解  ✔ Submit  →", "Review your answers", "● 测试框架用 pytest 还是 unittest？", "→ pytest", "● 要不要加类型注解？", "→ 要",
		}, []Option{{1, "Submit answers", true, ""}, {2, "Cancel", false, ""}}, true, false},
		{"question-typesomething", KindQuestion, "输出格式用 json 还是 yaml？", []string{"☐ 输出格式"}, []Option{
			{1, "json", false, "输出格式用 JSON"}, {2, "yaml", false, "输出格式用 YAML"}, {3, "Type something.", true, ""}, {4, "Chat about this", false, ""},
		}, true, true},
		// 打的字原地替换了“Type something.”。
		{"question-typed", KindQuestion, "输出格式用 json 还是 yaml？", []string{"☐ 输出格式"}, []Option{
			{1, "json", false, "输出格式用 JSON"}, {2, "yaml", false, "输出格式用 YAML"}, {3, "toml", true, ""}, {4, "Chat about this", false, ""},
		}, true, true},
		// 计划批准框的页脚只有编辑器快捷键和计划文件路径；计划正文在横线上方，不进说明。
		{"plan-dialog", KindPlan, planTitle, []string{}, []Option{
			{1, planBypass, true, ""}, {2, "Yes, manually approve edits", false, ""}, {3, "Tell Claude what to change", false, planFeedback},
		}, true, false},
		{"plan-feedback", KindPlan, planTitle, []string{}, []Option{
			{1, planBypass, false, ""}, {2, "Yes, manually approve edits", false, ""}, {3, "Tell Claude what to change", true, planFeedback},
		}, true, true},
		{"plan-feedback-typed", KindPlan, planTitle, []string{}, []Option{
			{1, planBypass, false, ""}, {2, "Yes, manually approve edits", false, ""}, {3, "请顺便加上类型注解", true, planFeedback},
		}, true, true},
		{"trust", KindTrust, "Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team). If not, take a moment to review what's in this folder first.", []string{
			"Accessing workspace:", "/tmp/herdrx-spike/proj3", "Claude Code'll be able to read, edit, and execute files here.", "Security guide",
		}, []Option{{1, "No, exit", true, ""}, {2, "Yes, I trust this folder", false, ""}}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := fixture(t, tc.name)
			dialog := ParseDialog(text)
			if dialog == nil {
				t.Fatal("dialog not recognized")
			}
			if dialog.Kind != tc.kind || dialog.Title != tc.title || !reflect.DeepEqual(dialog.Lines, tc.lines) || dialog.Numbered != tc.numbered || dialog.FreeText != tc.freeText || dialog.Multiple {
				t.Fatalf("kind=%q title=%q lines=%q numbered=%v free=%v multiple=%v", dialog.Kind, dialog.Title, dialog.Lines, dialog.Numbered, dialog.FreeText, dialog.Multiple)
			}
			if !reflect.DeepEqual(dialog.Options, tc.options) {
				t.Fatalf("options=%+v", dialog.Options)
			}
			if !LooksLikeDialog(text) || HasDraft(text) {
				t.Fatal("the send guard disagrees with the parser")
			}
			plain := ParseDialog(Plain(text))
			if plain == nil || plain.Fingerprint != dialog.Fingerprint || plain.Kind != dialog.Kind || plain.FreeText != dialog.FreeText {
				t.Fatalf("plain text parsed differently: %+v", plain)
			}
		})
	}
}

// 空闲与工作中的全屏画面（输入框、自定义状态栏、模式行、spinner 行）没有对话框，发送也不拦。
func TestClaudeIdleAndWorkingScreensAreNotDialogs(t *testing.T) {
	for _, name := range []string{"idle", "working-1", "working-2", "working-3", "working-4", "working-5", "working-6"} {
		t.Run(name, func(t *testing.T) {
			text := fixture(t, name)
			if dialog := ParseDialog(text); dialog != nil {
				t.Fatalf("dialog=%+v", dialog)
			}
			if LooksLikeDialog(text) || HasDraft(text) {
				t.Fatal("an idle input box was flagged")
			}
		})
	}
	// 输入框里的编号草稿：会话名横线是输入框的上沿，不是对话框底部的那条线。
	draft := strings.Replace(Plain(fixture(t, "idle")), "\n❯  \n", "\n❯ 1. fix the parser\n  2. add tests\n", 1)
	if !strings.Contains(draft, "add tests") {
		t.Fatal("draft not inserted")
	}
	if ParseDialog(draft) != nil || !HasDraft(draft) {
		t.Fatal("a numbered draft under the session rule was treated as a dialog")
	}
}

// 光标的位置决定能不能把文字直接送进对话框：只有提问框的自由输入项与计划的反馈项可以。
// 光标移动不改变指纹。
func TestClaudeDialogFreeTextFollowsTheCursor(t *testing.T) {
	// 认不出（光标没挪成）时直接失败；没挪动时各处对 Selected 的断言会失败。
	moved := func(name, from, to string) *Dialog {
		t.Helper()
		text := strings.Replace(Plain(fixture(t, name)), from, strings.Replace(from, "❯", " ", 1), 1)
		dialog := ParseDialog(strings.Replace(text, to, strings.Replace(to, " ", "❯", 1), 1))
		if dialog == nil {
			t.Fatalf("%s: dialog not recognized after moving the cursor", name)
		}
		return dialog
	}
	original := ParseDialog(fixture(t, "question-typed"))
	offTyped := moved("question-typed", "❯ 3. toml", "  1. json")
	if offTyped.FreeText || offTyped.Selected() != 0 || offTyped.Fingerprint != original.Fingerprint {
		t.Fatalf("cursor on a regular option: %+v", offTyped)
	}
	// 光标在横线下的“Chat about this”上：它是选项，不是输入框里的草稿。
	chat := moved("question-multi", "❯ 1. pytest", "  4. Chat about this")
	if chat.Kind != KindQuestion || chat.FreeText || chat.Selected() != 3 {
		t.Fatalf("cursor on chat: %+v", chat)
	}
	toSlot := moved("question-multi", "❯ 1. pytest", "  3. Type something.")
	if !toSlot.FreeText {
		t.Fatalf("cursor on the free-text slot: %+v", toSlot)
	}
	offFeedback := moved("plan-feedback-typed", "❯ 3. 请顺便加上类型注解", "  2. Yes, manually approve edits")
	if offFeedback.Kind != KindPlan || offFeedback.FreeText || offFeedback.Selected() != 1 {
		t.Fatalf("cursor off the plan feedback: %+v", offFeedback)
	}
	planCursor := ParseDialog(fixture(t, "plan-dialog"))
	if feedback := ParseDialog(fixture(t, "plan-feedback")); feedback.Fingerprint == "" || feedback.Fingerprint != planCursor.Fingerprint {
		t.Fatal("moving the cursor in the plan dialog changed the fingerprint")
	}
}

// 多选题的末项不是自由输入：卡片和发送都不接。
func TestClaudeMultiSelectQuestionIsNotFreeText(t *testing.T) {
	multi := ParseDialog(screen(rule, " ☐ Toppings", "", "Which toppings?", "", "  1. [ ] Cheese", "  2. [x] Basil", "❯ 3. [ ] Type something.", rule, "  4. Chat about this", "", "Enter to select · Space to toggle · Esc to cancel"))
	if multi == nil || multi.Kind != KindQuestion || !multi.Multiple || multi.FreeText {
		t.Fatalf("multi-select question: %+v", multi)
	}
}

// 计划页脚只认带计划文件路径的那一种；别的 `ctrl+g` 提示不是页脚。
func TestPlanFooterNeedsThePlanFile(t *testing.T) {
	plan := func(footer string) string {
		return screen(rule, " Would you like to proceed?", "", " ❯ 1. Yes", "   2. No", "", " "+footer)
	}
	if dialog := ParseDialog(plan("ctrl+g to edit in Vim · ~/.claude/plans/a-b.md")); dialog == nil || dialog.Kind != KindPlan {
		t.Fatalf("plan footer: %+v", dialog)
	}
	if dialog := ParseDialog(plan("ctrl+g to edit in VS Code · /srv/claude/plans/a.md")); dialog == nil || dialog.Kind != KindPlan {
		t.Fatalf("editor with a space: %+v", dialog)
	}
	for _, footer := range []string{"ctrl+g to edit in Vim", "ctrl+g to edit in Vim · notes/a.md", "ctrl+g to edit in Vim · ~/.claude/plans/a.md and more"} {
		if dialog := ParseDialog(plan(footer)); dialog != nil {
			t.Fatalf("%q: dialog=%+v", footer, dialog)
		}
	}
}

// 说明行：多行用换行连接，几乎占满宽度的说明折行接回同一行，选项文字的折行不算说明。
func TestOptionDescriptions(t *testing.T) {
	wide := strings.Repeat("─", 50)
	dialog := ParseDialog(screen(
		wide,
		"Pick one?",
		"",
		"❯ 1. Alpha",
		"     first line",
		"     second line",
		"  2. Beta option whose label runs all the way to the edge",
		"     of the screen",
		"  3. Gamma",
		"     a description that runs all the way to the edge of",
		"     the screen",
		"",
		"Enter to select · Esc to cancel",
	))
	if dialog == nil {
		t.Fatal("dialog not recognized")
	}
	want := []Option{{1, "Alpha", true, "first line\nsecond line"}, {2, "Beta option whose label runs all the way to the edge", false, ""}, {3, "Gamma", false, "a description that runs all the way to the edge of the screen"}}
	if !reflect.DeepEqual(dialog.Options, want) {
		t.Fatalf("options=%+v", dialog.Options)
	}
	// 缩进不比编号列深、又不是折行续行的行，归属无法确定：不出卡片。
	if stray := ParseDialog(screen(rule, " Proceed?", " ❯ 1. Yes", "   stray text", "   2. No", "", " Esc to cancel")); stray != nil {
		t.Fatalf("stray line under an option: %+v", stray)
	}
	// 说明不进指纹；没有说明时 JSON 里不出现 description，不是 Claude 对话框时不出现 kind。
	encoded, _ := json.Marshal(ParseDialog(permissionPrompt))
	if strings.Contains(string(encoded), "description") || strings.Contains(string(encoded), "kind") {
		t.Fatalf("json=%s", encoded)
	}
	encoded, _ = json.Marshal(ParseDialog(fixture(t, "question-multi")).Options[0])
	if string(encoded) != `{"index":1,"label":"pytest","selected":true,"description":"使用 pytest 作为测试框架"}` {
		t.Fatalf("json=%s", encoded)
	}
}
