package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/screenparse"
	"github.com/riba2534/herdrx/internal/store"
)

func planScreen(selected int) string {
	rows := []string{promptRule, " Would you like to proceed?", ""}
	for i, label := range []string{"Yes, and auto-accept edits", "Yes, and manually approve edits", "No, keep planning"} {
		prefix := "   "
		if i+1 == selected {
			prefix = " ❯ "
		}
		rows = append(rows, prefix+string(rune('1'+i))+". "+label)
	}
	return promptScreen(rows...)
}

func (f *promptFixture) dialog(t *testing.T) map[string]any {
	t.Helper()
	result := requestJSON(t, f.client, "GET", f.url("/dialog"), "", nil, 200)
	dialog, _ := result["dialog"].(map[string]any)
	return dialog
}

func (f *promptFixture) answer(t *testing.T, fingerprint any, option int, want int) map[string]any {
	t.Helper()
	return postJSON(t, f.client, f.url("/dialog/answer"), f.csrf, map[string]any{"fingerprint": fingerprint, "option_index": option}, want)
}

func keysSent(f *promptFixture) []string {
	var out []string
	for _, call := range f.endpoint.callsOf("pane.send_keys") {
		encoded, _ := json.Marshal(call.params["keys"])
		out = append(out, string(encoded))
	}
	return out
}

func TestPaneDialogReturnsParsedDialogOrNull(t *testing.T) {
	f := newPromptFixture(t, dialogScreen)
	dialog := f.dialog(t)
	if dialog == nil || dialog["title"] != "Do you want to proceed?" || dialog["numbered"] != true || len(dialog["fingerprint"].(string)) != 16 {
		t.Fatalf("dialog=%v", dialog)
	}
	options := dialog["options"].([]any)
	if first := options[0].(map[string]any); first["index"] != float64(1) || first["label"] != "Yes" || first["selected"] != true {
		t.Fatalf("options=%v", options)
	}
	f.endpoint.setScreen(idleScreen)
	result := requestJSON(t, f.client, "GET", f.url("/dialog"), "", nil, 200)
	if value, present := result["dialog"]; !present || value != nil {
		t.Fatalf("result=%v", result)
	}
	f.endpoint.readErr = errors.New("boom")
	if result := requestJSON(t, f.client, "GET", f.url("/dialog"), "", nil, 502); result["code"] != "pane_read_failed" {
		t.Fatalf("result=%v", result)
	}
}

func TestDialogAnswerNumberedDialogPressesTheDigit(t *testing.T) {
	f := newPromptFixture(t, planScreen(1))
	fingerprint := f.dialog(t)["fingerprint"]
	f.answer(t, fingerprint, 3, 200)
	if got := keysSent(f); strings.Join(got, ",") != `["3"]` {
		t.Fatalf("keys=%q", got)
	}
	if len(f.endpoint.callsOf("pane.send_input")) != 0 {
		t.Fatal("an answer must not type text")
	}
	entries, err := f.db.ListAudit(context.Background(), store.AuditFilter{Page: store.Page{Limit: 20}, Action: "pane.dialog_answer"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Action == "pane.dialog_answer" && entry.TargetType == "host" && entry.TargetID == "prompt-host" && strings.Contains(string(entry.Details), `"option_index":3`) && strings.Contains(string(entry.Details), `"pane_id":"w1:p1"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit missing: %+v", entries)
	}
}

func TestDialogAnswerUnnumberedDialogMovesFromTheCursor(t *testing.T) {
	f := newPromptFixture(t, trustScreen)
	fingerprint := f.dialog(t)["fingerprint"]
	f.answer(t, fingerprint, 2, 200)
	f.answer(t, fingerprint, 1, 200)
	if got := keysSent(f); strings.Join(got, ",") != `["Down","Enter"],["Enter"]` {
		t.Fatalf("keys=%q", got)
	}
}

func TestDialogKeysFromCursorPosition(t *testing.T) {
	dialog := &screenparse.Dialog{Options: []screenparse.Option{{Index: 1}, {Index: 2}, {Index: 3, Selected: true}, {Index: 4}}}
	if got := strings.Join(dialogKeys(dialog, 0), ","); got != "Up,Up,Enter" {
		t.Fatalf("keys=%s", got)
	}
	if got := strings.Join(dialogKeys(dialog, 3), ","); got != "Down,Enter" {
		t.Fatalf("keys=%s", got)
	}
	numbered := &screenparse.Dialog{Numbered: true, Options: []screenparse.Option{{Index: 9}, {Index: 10, Selected: true}, {Index: 11}}}
	if got := strings.Join(dialogKeys(numbered, 0), ","); got != "9" {
		t.Fatalf("keys=%s", got)
	}
	if got := strings.Join(dialogKeys(numbered, 2), ","); got != "Down,Enter" {
		t.Fatalf("two-digit options move with arrows: %s", got)
	}
}

func TestDialogAnswerRefusesChangedOrGoneDialog(t *testing.T) {
	f := newPromptFixture(t, dialogScreen)
	fingerprint := f.dialog(t)["fingerprint"]
	f.endpoint.setScreen(planScreen(1))
	if result := f.answer(t, fingerprint, 1, 409); result["code"] != "dialog_changed" || result["error"] != "对话框已变化，请重新选择" {
		t.Fatalf("result=%v", result)
	}
	f.endpoint.setScreen(idleScreen)
	if result := f.answer(t, fingerprint, 1, 409); result["code"] != "dialog_gone" {
		t.Fatalf("result=%v", result)
	}
	// 光标移动不改变指纹：同一个对话框照常回答。
	f.endpoint.setScreen(planScreen(1))
	planFingerprint := f.dialog(t)["fingerprint"]
	f.endpoint.setScreen(planScreen(2))
	f.answer(t, planFingerprint, 1, 200)
	if got := keysSent(f); strings.Join(got, ",") != `["1"]` {
		t.Fatalf("keys=%q", got)
	}
}

func TestDialogAnswerRejectsBadInputWithoutPressing(t *testing.T) {
	f := newPromptFixture(t, dialogScreen)
	fingerprint := f.dialog(t)["fingerprint"]
	f.answer(t, fingerprint, 7, 400)
	f.answer(t, fingerprint, 0, 400)
	f.answer(t, "", 1, 400)
	postJSON(t, f.client, f.url("/dialog/answer"), "", map[string]any{"fingerprint": fingerprint, "option_index": 1}, 403)
	f.endpoint.readErr = errors.New("boom")
	if result := f.answer(t, fingerprint, 1, 502); result["code"] != "pane_read_failed" {
		t.Fatalf("result=%v", result)
	}
	if len(keysSent(f)) != 0 {
		t.Fatal("keys were pressed for a rejected answer")
	}
}

func TestDialogAnswerKeyFailureIsNotRetried(t *testing.T) {
	f := newPromptFixture(t, dialogScreen)
	fingerprint := f.dialog(t)["fingerprint"]
	f.endpoint.keysErr = errors.New("timeout")
	if result := f.answer(t, fingerprint, 2, 502); result["code"] != "answer_unknown" {
		t.Fatalf("result=%v", result)
	}
	if len(keysSent(f)) != 1 {
		t.Fatalf("keys=%q", keysSent(f))
	}
}
