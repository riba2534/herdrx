package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/store"
)

const promptRule = "────────────────────────────────────────"

func promptScreen(lines ...string) string { return strings.Join(lines, "\r\n") + "\r\n" }

var (
	idleScreen       = promptScreen("● Done.", "", promptRule, "❯ ", promptRule, "  ? for shortcuts")
	suggestionScreen = promptScreen("● Done.", "", promptRule, "❯ \x1b[2mrun the tests\x1b[0m", promptRule)
	draftScreen      = promptScreen("● Done.", "", promptRule, "❯ half-typed idea", promptRule)
	dialogScreen     = promptScreen(promptRule, " Bash command", "   rm -rf build", "", " Do you want to proceed?", " ❯ 1. Yes", "   2. No, and tell Claude what to do differently (esc)", "")
	trustScreen      = promptScreen(promptRule, " Is this a project you trust?", "", " ❯ No, exit", "   Yes, I trust this folder", "", " Enter to confirm · Esc to cancel")
)

type promptCall struct {
	method string
	params map[string]any
}

// promptTestEndpoint 记录每次调用；sendErrors 按 send_input 的顺序注入失败。
type promptTestEndpoint struct {
	mu         sync.Mutex
	screen     string
	readErr    error
	calls      []promptCall
	sendErrors []error
	keysErr    error
	beforeSend func()
}

func (e *promptTestEndpoint) Snapshot(context.Context) (herdr.Snapshot, error) {
	return herdr.Snapshot{}, nil
}

func (e *promptTestEndpoint) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method == "pane.send_input" && e.beforeSend != nil {
		e.beforeSend()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	values, _ := params.(map[string]any)
	e.calls = append(e.calls, promptCall{method, values})
	switch method {
	case "pane.read":
		if e.readErr != nil {
			return nil, e.readErr
		}
		return json.Marshal(map[string]any{"type": "pane_read", "read": map[string]any{"text": e.screen}})
	case "pane.send_input":
		if len(e.sendErrors) > 0 {
			err := e.sendErrors[0]
			e.sendErrors = e.sendErrors[1:]
			if err != nil {
				return nil, err
			}
		}
		return json.RawMessage(`{}`), nil
	case "pane.send_keys":
		if e.keysErr != nil {
			return nil, e.keysErr
		}
		return json.RawMessage(`{}`), nil
	}
	return nil, errors.New("unexpected method " + method)
}

func (e *promptTestEndpoint) setScreen(text string) {
	e.mu.Lock()
	e.screen = text
	e.mu.Unlock()
}

func (e *promptTestEndpoint) callsOf(method string) []promptCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []promptCall
	for _, call := range e.calls {
		if call.method == method {
			out = append(out, call)
		}
	}
	return out
}

func (*promptTestEndpoint) OpenTerminal(context.Context, herdr.TerminalOpen) (herdr.TerminalProcess, error) {
	return nil, errors.New("prompt must not open a terminal stream")
}
func (*promptTestEndpoint) StageImage(context.Context, string, io.Reader) (string, error) {
	return "", errors.New("unexpected")
}
func (*promptTestEndpoint) Close() error { return nil }

type promptTestPool struct {
	endpoint *promptTestEndpoint
	mu       sync.Mutex
	opened   int
}

func (p *promptTestPool) Open(context.Context, store.Host) (herdr.Endpoint, error) {
	p.mu.Lock()
	p.opened++
	p.mu.Unlock()
	return p.endpoint, nil
}
func (*promptTestPool) CloseHost(string) {}
func (*promptTestPool) Close()           {}

type promptFixture struct {
	api      *API
	db       *store.Store
	server   string
	client   *http.Client
	csrf     string
	endpoint *promptTestEndpoint
	pool     *promptTestPool
}

func newPromptFixture(t *testing.T, screen string) *promptFixture {
	t.Helper()
	value, db, server, client, info := authFixture(t)
	value.stopBackground()
	value.hosts.Close()
	owner := info["user"].(map[string]any)["id"].(string)
	if err := db.CreateHost(context.Background(), store.Host{ID: "prompt-host", OwnerID: owner, Name: "Prompt", Transport: "ssh", Port: 22}); err != nil {
		t.Fatal(err)
	}
	endpoint := &promptTestEndpoint{screen: screen}
	pool := &promptTestPool{endpoint: endpoint}
	value.hosts = pool
	return &promptFixture{api: value, db: db, server: server.URL, client: client, csrf: info["csrf_token"].(string), endpoint: endpoint, pool: pool}
}

func (f *promptFixture) url(suffix string) string {
	return f.server + "/api/hosts/prompt-host/panes/" + url.PathEscape("w1:p1") + suffix
}

func (f *promptFixture) prompt(t *testing.T, body map[string]any, want int) map[string]any {
	t.Helper()
	return postJSON(t, f.client, f.url("/prompt"), f.csrf, body, want)
}

func sentTexts(endpoint *promptTestEndpoint) []string {
	var out []string
	for _, call := range endpoint.callsOf("pane.send_input") {
		keys, _ := json.Marshal(call.params["keys"])
		out = append(out, call.params["text"].(string)+"|"+string(keys))
	}
	return out
}

func TestPromptSendsTextThenEnterAfterReadingTheScreen(t *testing.T) {
	for name, screen := range map[string]string{"idle": idleScreen, "suggestion": suggestionScreen, "no box": promptScreen("$ ")} {
		t.Run(name, func(t *testing.T) {
			f := newPromptFixture(t, screen)
			result := f.prompt(t, map[string]any{"text": "fix the bug", "submit": true, "client_msg_id": "m1"}, 200)
			if result["delivered"] != "sent" {
				t.Fatalf("result=%v", result)
			}
			if got := sentTexts(f.endpoint); strings.Join(got, ",") != `fix the bug|[],|["Enter"]` {
				t.Fatalf("sends=%q", got)
			}
			reads := f.endpoint.callsOf("pane.read")
			if len(reads) != 1 || reads[0].params["source"] != "visible" || reads[0].params["format"] != "ansi" || reads[0].params["pane_id"] != "w1:p1" {
				t.Fatalf("reads=%+v", reads)
			}
			if f.endpoint.calls[0].method != "pane.read" {
				t.Fatal("the screen must be read before anything is typed")
			}
		})
	}
}

func TestPromptWithoutSubmitSendsOnlyText(t *testing.T) {
	f := newPromptFixture(t, idleScreen)
	f.prompt(t, map[string]any{"text": "draft", "submit": false, "client_msg_id": "m1"}, 200)
	if got := sentTexts(f.endpoint); strings.Join(got, ",") != "draft|[]" {
		t.Fatalf("sends=%q", got)
	}
}

func TestPromptRefusesDialogs(t *testing.T) {
	for name, screen := range map[string]string{"numbered": dialogScreen, "trust": trustScreen} {
		t.Run(name, func(t *testing.T) {
			f := newPromptFixture(t, screen)
			for _, force := range []bool{false, true} {
				result := f.prompt(t, map[string]any{"text": "1", "submit": true, "client_msg_id": "m1", "force": force}, 409)
				dialog, _ := result["dialog"].(map[string]any)
				if result["code"] != "pane_dialog" || dialog == nil || dialog["fingerprint"] == "" || len(dialog["options"].([]any)) != 2 {
					t.Fatalf("result=%v", result)
				}
			}
			if len(f.endpoint.callsOf("pane.send_input")) != 0 {
				t.Fatal("text was typed into a dialog")
			}
			// 被拒绝的请求不记账：对话框关掉后同一个 id 照常发送。
			f.endpoint.setScreen(idleScreen)
			f.prompt(t, map[string]any{"text": "1", "submit": true, "client_msg_id": "m1"}, 200)
		})
	}
}

func TestPromptRefusesDraftUnlessForced(t *testing.T) {
	f := newPromptFixture(t, draftScreen)
	result := f.prompt(t, map[string]any{"text": "next", "submit": true, "client_msg_id": "m1"}, 409)
	if result["code"] != "pane_draft" || !strings.Contains(result["error"].(string), "未发送") {
		t.Fatalf("result=%v", result)
	}
	if len(f.endpoint.callsOf("pane.send_input")) != 0 {
		t.Fatal("text was appended to a draft")
	}
	f.prompt(t, map[string]any{"text": "next", "submit": true, "client_msg_id": "m1", "force": true}, 200)
	if got := sentTexts(f.endpoint); len(got) != 2 {
		t.Fatalf("sends=%q", got)
	}
}

func TestPromptReadFailureSendsNothing(t *testing.T) {
	f := newPromptFixture(t, idleScreen)
	f.endpoint.readErr = errors.New("read remote herdr response: EOF")
	result := f.prompt(t, map[string]any{"text": "hi", "submit": true, "client_msg_id": "m1"}, 502)
	if result["code"] != "pane_read_failed" || len(f.endpoint.callsOf("pane.send_input")) != 0 {
		t.Fatalf("result=%v calls=%+v", result, f.endpoint.calls)
	}
	f.endpoint.readErr = nil
	f.prompt(t, map[string]any{"text": "hi", "submit": true, "client_msg_id": "m1"}, 200)
}

func TestPromptDuplicateIsNotTypedTwice(t *testing.T) {
	f := newPromptFixture(t, idleScreen)
	f.prompt(t, map[string]any{"text": "once", "submit": true, "client_msg_id": "same"}, 200)
	result := f.prompt(t, map[string]any{"text": "once", "submit": true, "client_msg_id": "same"}, 200)
	if result["delivered"] != "duplicate" || len(f.endpoint.callsOf("pane.send_input")) != 2 {
		t.Fatalf("result=%v sends=%q", result, sentTexts(f.endpoint))
	}
	// 另一个 id 是新消息。
	f.prompt(t, map[string]any{"text": "again", "submit": true, "client_msg_id": "other"}, 200)
	if len(f.endpoint.callsOf("pane.send_input")) != 4 {
		t.Fatal("a new client_msg_id must be sent")
	}
}

func TestPromptConcurrentDuplicateWaitsForTheFirst(t *testing.T) {
	f := newPromptFixture(t, idleScreen)
	release := make(chan struct{})
	var once sync.Once
	entered := make(chan struct{})
	f.endpoint.beforeSend = func() {
		once.Do(func() { close(entered); <-release })
	}
	done := make(chan map[string]any, 1)
	go func() {
		done <- requestJSONNoFatal(f.client, f.url("/prompt"), f.csrf, map[string]any{"text": "slow", "submit": true, "client_msg_id": "dup"})
	}()
	<-entered
	second := make(chan map[string]any, 1)
	go func() {
		second <- requestJSONNoFatal(f.client, f.url("/prompt"), f.csrf, map[string]any{"text": "slow", "submit": true, "client_msg_id": "dup"})
	}()
	select {
	case result := <-second:
		t.Fatalf("duplicate returned before the first finished: %v", result)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if first := <-done; first["delivered"] != "sent" {
		t.Fatalf("first=%v", first)
	}
	if result := <-second; result["delivered"] != "duplicate" {
		t.Fatalf("second=%v", result)
	}
	if len(f.endpoint.callsOf("pane.send_input")) != 2 {
		t.Fatalf("sends=%q", sentTexts(f.endpoint))
	}
}

func requestJSONNoFatal(client *http.Client, target, csrf string, body any) map[string]any {
	encoded, _ := json.Marshal(body)
	request, _ := http.NewRequest(http.MethodPost, target, strings.NewReader(string(encoded)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://example.test")
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		return map[string]any{"transport_error": err.Error()}
	}
	defer response.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(response.Body).Decode(&payload)
	return payload
}

func TestPromptUncertainDeliveryIsNeverReplayed(t *testing.T) {
	cases := map[string][]error{
		"text leg":  {errors.New("read remote herdr response: EOF")},
		"enter leg": {nil, errors.New("timeout")},
	}
	for name, failures := range cases {
		t.Run(name, func(t *testing.T) {
			f := newPromptFixture(t, idleScreen)
			f.endpoint.sendErrors = failures
			result := f.prompt(t, map[string]any{"text": "maybe", "submit": true, "client_msg_id": "u1"}, 502)
			if result["code"] != "delivery_unknown" {
				t.Fatalf("result=%v", result)
			}
			sends := len(f.endpoint.callsOf("pane.send_input"))
			retry := f.prompt(t, map[string]any{"text": "maybe", "submit": true, "client_msg_id": "u1"}, 409)
			if retry["code"] != "delivery_unknown" || retry["error"] != "上一次发送结果不确定，请先查看终端再决定是否重发" {
				t.Fatalf("retry=%v", retry)
			}
			if len(f.endpoint.callsOf("pane.send_input")) != sends {
				t.Fatal("an uncertain send was replayed")
			}
		})
	}
}

func TestPromptHerdrRejectionIsSafeToRetry(t *testing.T) {
	f := newPromptFixture(t, idleScreen)
	f.endpoint.sendErrors = []error{&herdr.APIError{Code: "pane_not_found", Message: "pane not found"}}
	result := f.prompt(t, map[string]any{"text": "hi", "submit": true, "client_msg_id": "r1"}, 502)
	if result["code"] != "pane_send_failed" || len(f.endpoint.callsOf("pane.send_input")) != 1 {
		t.Fatalf("result=%v sends=%q", result, sentTexts(f.endpoint))
	}
	f.prompt(t, map[string]any{"text": "hi", "submit": true, "client_msg_id": "r1"}, 200)
}

func TestPromptValidatesInputAndOwnership(t *testing.T) {
	f := newPromptFixture(t, idleScreen)
	for _, id := range []string{"", "has space", strings.Repeat("a", 65), "汉字"} {
		result := f.prompt(t, map[string]any{"text": "hi", "submit": true, "client_msg_id": id}, 400)
		if result["code"] != "invalid_client_msg_id" {
			t.Fatalf("id=%q result=%v", id, result)
		}
	}
	f.prompt(t, map[string]any{"text": "", "submit": false, "client_msg_id": "m1"}, 400)
	postJSON(t, f.client, f.server+"/api/hosts/prompt-host/panes/bad%20pane/prompt", f.csrf, map[string]any{"text": "hi", "client_msg_id": "m1"}, 400)
	postJSON(t, f.client, f.url("/prompt"), "", map[string]any{"text": "hi", "client_msg_id": "m1"}, 403)

	member, err := f.api.createUserRecord(credentialsRequest{Email: "member@example.test", Password: "fixture-member-password", DisplayName: "Member"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.CreateUser(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	if err = f.db.CreateHost(context.Background(), store.Host{ID: "member-host", OwnerID: member.ID, Name: "Member", Transport: "ssh", Port: 22}); err != nil {
		t.Fatal(err)
	}
	other := strings.Replace(f.url(""), "prompt-host", "member-host", 1)
	postJSON(t, f.client, other+"/prompt", f.csrf, map[string]any{"text": "hi", "submit": true, "client_msg_id": "m1"}, 404)
	requestJSON(t, f.client, "GET", other+"/dialog", "", nil, 404)
	postJSON(t, f.client, other+"/dialog/answer", f.csrf, map[string]any{"fingerprint": "x", "option_index": 1}, 404)
	if f.pool.opened != 0 {
		t.Fatal("rejected requests opened a host connection")
	}
}

func TestPromptLogIsBoundedAndExpires(t *testing.T) {
	log := &promptLog{}
	start := time.Now()
	for i := 0; i < promptLogCapacity+10; i++ {
		entry, fresh := log.begin(promptKey{id: string(rune('a' + i%26)), pane: string(rune(i))}, start.Add(time.Duration(i)*time.Millisecond))
		if !fresh {
			t.Fatalf("entry %d was not fresh", i)
		}
		log.finish(promptKey{id: string(rune('a' + i%26)), pane: string(rune(i))}, entry, promptSent)
	}
	if len(log.entries) > promptLogCapacity {
		t.Fatalf("log grew to %d", len(log.entries))
	}
	key := promptKey{id: "late"}
	entry, _ := log.begin(key, start)
	log.finish(key, entry, promptSent)
	if _, fresh := log.begin(key, start.Add(promptTTL-time.Second)); fresh {
		t.Fatal("a sent id must be remembered within the TTL")
	}
	if _, fresh := log.begin(key, start.Add(promptTTL+time.Minute)); !fresh {
		t.Fatal("an id must be forgotten after the TTL")
	}
}
