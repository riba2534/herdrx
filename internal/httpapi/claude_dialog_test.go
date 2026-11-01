package httpapi

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// claudeScreen 读 screenparse 的真实全屏画面夹具（Claude Code 2.1.316，已脱敏）。
func claudeScreen(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "screenparse", "testdata", name+".ansi.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// 卡片接口原样带出对话框种类与选项说明。
func TestPaneDialogCarriesKindAndDescriptions(t *testing.T) {
	f := newPromptFixture(t, claudeScreen(t, "question-multi"))
	dialog := f.dialog(t)
	options, _ := dialog["options"].([]any)
	if dialog["kind"] != "question" || dialog["title"] != "测试框架用 pytest 还是 unittest？" || len(options) != 4 {
		t.Fatalf("dialog=%v", dialog)
	}
	if first := options[0].(map[string]any); first["label"] != "pytest" || first["description"] != "使用 pytest 作为测试框架" {
		t.Fatalf("first option=%v", first)
	}
	if last := options[3].(map[string]any); last["label"] != "Chat about this" {
		t.Fatalf("last option=%v", last)
	} else if _, present := last["description"]; present {
		t.Fatalf("an option without a description must omit the field: %v", last)
	}
	f.endpoint.setScreen(dialogScreen)
	if _, present := f.dialog(t)["kind"]; present {
		t.Fatal("a dialog Claude did not draw must omit kind")
	}
}

// 带编号的按数字键，信任目录（不带编号）从当前光标用方向键移动后回车。
func TestDialogAnswerPressesTheRightKeysForClaudeDialogs(t *testing.T) {
	cases := []struct {
		screen string
		option int
		keys   string
	}{
		{"question-multi", 2, `["2"]`},
		{"question-multi", 4, `["4"]`},
		{"question-typesomething", 1, `["1"]`},
		{"question-multi-submit", 1, `["1"]`},
		{"plan-dialog", 1, `["1"]`},
		{"plan-dialog", 3, `["3"]`},
		{"trust", 2, `["Down","Enter"]`},
		{"trust", 1, `["Enter"]`},
	}
	for _, tc := range cases {
		t.Run(tc.screen+"/"+strconv.Itoa(tc.option), func(t *testing.T) {
			f := newPromptFixture(t, claudeScreen(t, tc.screen))
			f.answer(t, f.dialog(t)["fingerprint"], tc.option, 200)
			if got := strings.Join(keysSent(f), ","); got != tc.keys {
				t.Fatalf("keys=%s want %s", got, tc.keys)
			}
		})
	}
}

// 光标停在提问框的自由输入项（含打字后被替换的那一项）或计划的反馈项上时，文字直接送进对话框；
// 其余 Claude 对话框照常拦截，什么都不打。
func TestPromptGuardOnClaudeDialogs(t *testing.T) {
	for _, name := range []string{"question-typesomething", "question-typed", "plan-feedback", "plan-feedback-typed"} {
		t.Run("allows "+name, func(t *testing.T) {
			f := newPromptFixture(t, claudeScreen(t, name))
			f.prompt(t, map[string]any{"text": "请顺便加上类型注解", "submit": true, "client_msg_id": "m1"}, 200)
			if got := strings.Join(sentTexts(f.endpoint), ","); got != `请顺便加上类型注解|[],|["Enter"]` {
				t.Fatalf("sends=%q", got)
			}
		})
	}
	for _, name := range []string{"question-multi", "question-multi-step2", "question-multi-submit", "plan-dialog", "trust"} {
		t.Run("refuses "+name, func(t *testing.T) {
			f := newPromptFixture(t, claudeScreen(t, name))
			result := f.prompt(t, map[string]any{"text": "1", "submit": true, "client_msg_id": "m1", "force": true}, 409)
			if dialog, _ := result["dialog"].(map[string]any); result["code"] != "pane_dialog" || dialog == nil || dialog["kind"] == nil {
				t.Fatalf("result=%v", result)
			}
			if len(f.endpoint.callsOf("pane.send_input")) != 0 {
				t.Fatal("text was typed into a dialog")
			}
		})
	}
	for _, name := range []string{"idle", "working-1"} {
		t.Run("sends into "+name, func(t *testing.T) {
			f := newPromptFixture(t, claudeScreen(t, name))
			f.prompt(t, map[string]any{"text": "继续", "submit": true, "client_msg_id": "m1"}, 200)
		})
	}
}
