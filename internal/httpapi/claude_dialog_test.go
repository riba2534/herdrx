package httpapi

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/screenparse"
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
		// 光标停在自由输入项上（打没打字都一样）：数字会进输入框，从光标处用方向键移过去。
		{"question-typesomething", 1, `["Up","Up","Enter"]`},
		{"question-typed", 4, `["Down","Enter"]`},
		{"plan-feedback", 1, `["Up","Up","Enter"]`},
		{"plan-feedback-typed", 3, `["Enter"]`},
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

// 布局变了、“Chat about this”上面是带说明的真实选项：光标停在它上面时照常拦截发送。
func TestPromptRefusesARealOptionAboveChat(t *testing.T) {
	lines := strings.Split(screenparse.Plain(claudeScreen(t, "question-multi")), "\n")
	if i := slices.Index(lines, "  3. Type something."); i >= 0 {
		lines = slices.Delete(lines, i, i+2) // 去掉“Type something.”和它下面的分隔线
	}
	screen := strings.NewReplacer("  4. Chat about this", "  3. Chat about this", "❯ 1. pytest", "  1. pytest", "  2. unittest", "❯ 2. unittest").Replace(strings.Join(lines, "\n"))
	if dialog := screenparse.ParseDialog(screen); dialog == nil || dialog.Kind != screenparse.KindQuestion || dialog.Selected() != 1 {
		t.Fatalf("dialog=%+v", dialog)
	}
	f := newPromptFixture(t, screen)
	if result := f.prompt(t, map[string]any{"text": "1", "submit": true, "client_msg_id": "m1"}, 409); result["code"] != "pane_dialog" {
		t.Fatalf("result=%v", result)
	}
	if len(f.endpoint.callsOf("pane.send_input")) != 0 {
		t.Fatal("text was typed into a dialog")
	}
}

// 两个浏览器同时作答同一个对话框：读屏核对到按键之间按 pane 串行，后一个读到的是前一个按键
// 之后的下一题，指纹不符，什么都不按。
func TestConcurrentAnswersPressKeysOnce(t *testing.T) {
	f := newPromptFixture(t, claudeScreen(t, "question-multi"))
	fingerprint := f.dialog(t)["fingerprint"]
	next := claudeScreen(t, "question-multi-step2")
	var once sync.Once
	f.endpoint.beforeKeys = func() {
		once.Do(func() {
			// 给另一个请求读屏的机会（不串行时它会在这时读到还没变的画面），然后这次按键生效。
			deadline := time.Now().Add(500 * time.Millisecond)
			for len(f.endpoint.callsOf("pane.read")) < 3 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			f.endpoint.setScreen(next)
		})
	}
	results := make(chan map[string]any, 2)
	for range 2 {
		go func() {
			results <- requestJSONNoFatal(f.client, f.url("/dialog/answer"), f.csrf, map[string]any{"fingerprint": fingerprint, "option_index": 1})
		}()
	}
	first, second := <-results, <-results
	if len(keysSent(f)) != 1 {
		t.Fatalf("keys=%q", keysSent(f))
	}
	if !slices.Contains([]any{first["code"], second["code"]}, "dialog_changed") || (first["ok"] != true && second["ok"] != true) {
		t.Fatalf("first=%v second=%v", first, second)
	}
}

// 锁只管同一个 pane：一个 pane 的按键卡住时，另一个 pane 照常作答。
func TestAnswersOnDifferentPanesDoNotWait(t *testing.T) {
	f := newPromptFixture(t, claudeScreen(t, "question-multi"))
	fingerprint := f.dialog(t)["fingerprint"]
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	f.endpoint.beforeKeys = func() {
		// 只卡住第一次按键；sync.Once 会让后来的调用也等着，不能用。
		if first.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}
	defer close(release)
	go requestJSONNoFatal(f.client, f.url("/dialog/answer"), f.csrf, map[string]any{"fingerprint": fingerprint, "option_index": 1})
	<-entered
	other := make(chan map[string]any, 1)
	go func() {
		other <- requestJSONNoFatal(f.client, f.server+"/api/hosts/prompt-host/panes/"+url.PathEscape("w1:p2")+"/dialog/answer", f.csrf, map[string]any{"fingerprint": fingerprint, "option_index": 2})
	}()
	select {
	case result := <-other:
		if result["ok"] != true {
			t.Fatalf("other pane=%v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an answer on another pane waited for this pane's lock")
	}
}

// 作答锁按 host+pane 分开、等锁可以放弃，用完即删。
func TestPaneLocks(t *testing.T) {
	var locks paneLocks
	unlock, err := locks.lock(t.Context(), "h\x00p1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := locks.lock(t.Context(), "h\x00p2")
	if err != nil {
		t.Fatal("another pane waited for the first pane's lock")
	}
	other()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := locks.lock(ctx, "h\x00p1"); err == nil {
		t.Fatal("the same pane was locked twice")
	}
	unlock()
	again, err := locks.lock(t.Context(), "h\x00p1")
	if err != nil {
		t.Fatal(err)
	}
	again()
	if len(locks.locks) != 0 {
		t.Fatalf("locks left behind: %v", locks.locks)
	}
}

// 光标停在自由输入项上时不按数字键。
func TestDialogKeysFromAFreeTextCursor(t *testing.T) {
	dialog := &screenparse.Dialog{Numbered: true, FreeText: true, Options: []screenparse.Option{{Index: 1}, {Index: 2}, {Index: 3, Selected: true}, {Index: 4}}}
	if got := strings.Join(dialogKeys(dialog, 1), ","); got != "Up,Up,Enter" {
		t.Fatalf("keys=%s", got)
	}
	if got := strings.Join(dialogKeys(dialog, 4), ","); got != "Down,Enter" {
		t.Fatalf("keys=%s", got)
	}
}
