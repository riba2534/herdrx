package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/store"
)

func applyScreenDiff(previous []string, drop int, total int, set [][2]any) []string {
	next := append([]string(nil), previous[drop:]...)
	for len(next) < total {
		next = append(next, "")
	}
	next = next[:total]
	for _, item := range set {
		next[item[0].(int)] = item[1].(string)
	}
	return next
}

func TestScreenDiffReconstructsNextScreen(t *testing.T) {
	cases := []struct {
		name     string
		previous []string
		next     []string
		drop     int
		sets     int
	}{
		{"unchanged", []string{"a", "b"}, []string{"a", "b"}, 0, 0},
		{"appended output scrolls the window", []string{"a", "b", "c", "d"}, []string{"c", "d", "e"}, 2, 1},
		{"edit in place", []string{"$ ls", "x"}, []string{"$ ls -l", "x"}, 0, 1},
		{"shrink", []string{"a", "b", "c"}, []string{"a"}, 0, 0},
		{"clear", []string{"a", "b"}, []string{}, 0, 0},
		{"from empty", []string{}, []string{"a"}, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			drop, set, changed := diffScreenLines(tc.previous, tc.next)
			if drop != tc.drop || len(set) != tc.sets {
				t.Fatalf("drop=%d sets=%d, want drop=%d sets=%d", drop, len(set), tc.drop, tc.sets)
			}
			if changed != (tc.name != "unchanged") {
				t.Fatalf("changed=%v", changed)
			}
			if got := applyScreenDiff(tc.previous, drop, len(tc.next), set); strings.Join(got, "\n") != strings.Join(tc.next, "\n") {
				t.Fatalf("reconstructed %q, want %q", got, tc.next)
			}
		})
	}
}

func TestScreenDiffRandomSequencesRoundTrip(t *testing.T) {
	random := rand.New(rand.NewSource(7))
	words := []string{"", "$ ", "build ok", "\x1b[0m\x1b[38;5;1merror\x1b[0m", "中文 输出", "x", "y"}
	screen := []string{}
	for step := 0; step < 2000; step++ {
		next := append([]string(nil), screen...)
		switch random.Intn(4) {
		case 0: // output appended, window keeps the newest 30 lines
			for count := random.Intn(5); count >= 0; count-- {
				next = append(next, words[random.Intn(len(words))])
			}
			if len(next) > 30 {
				next = next[len(next)-30:]
			}
		case 1: // a spinner or prompt redraws one line
			if len(next) > 0 {
				next[random.Intn(len(next))] = fmt.Sprintf("tick %d", step)
			}
		case 2: // clear screen
			next = next[:random.Intn(len(next)+1)]
		default:
		}
		drop, set, _ := diffScreenLines(screen, next)
		if got := applyScreenDiff(screen, drop, len(next), set); strings.Join(got, "\x00") != strings.Join(next, "\x00") {
			t.Fatalf("step %d: reconstructed %q, want %q", step, got, next)
		}
		screen = next
	}
}

func TestSplitScreenLinesHandlesHerdrLineEndings(t *testing.T) {
	got := splitScreenLines("\x1b[0m\x1b[38;5;1mred\x1b[0m\r\n\r\nplain\r\n")
	want := []string{"\x1b[0m\x1b[38;5;1mred\x1b[0m", "", "plain"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
	if lines := splitScreenLines(""); len(lines) != 0 {
		t.Fatalf("empty screen produced %q", lines)
	}
	long := splitScreenLines(strings.Repeat("中", screenMaxLineRunes+10))
	if n := len([]rune(long[0])); n != screenMaxLineRunes+len([]rune("\x1b[0m…")) || !strings.HasSuffix(long[0], "…") {
		t.Fatalf("long line not capped at a rune boundary: %d runes", n)
	}
}

func TestScreenReadsShareOneFetchAcrossWindows(t *testing.T) {
	var reads screenReads
	var fetches atomic.Int32
	release := make(chan struct{})
	key := screenReadKey{hostID: "h", paneID: "p", lines: 200}
	fetch := func(context.Context) screenReadResult {
		fetches.Add(1)
		<-release
		return screenReadResult{text: "shared"}
	}
	var wg sync.WaitGroup
	results := make([]screenReadResult, 4)
	for index := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index] = reads.read(context.Background(), key, time.Now().Add(-screenShareWindow), fetch)
		}(index)
	}
	deadline := time.Now().Add(2 * time.Second)
	for fetches.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if fetches.Load() != 1 {
		t.Fatalf("fetches = %d, want 1", fetches.Load())
	}
	for _, result := range results {
		if result.text != "shared" || result.err != nil {
			t.Fatalf("unexpected result %+v", result)
		}
	}
	// A reread after input refuses a read that began before the input.
	reads.read(context.Background(), key, time.Now(), func(context.Context) screenReadResult { fetches.Add(1); return screenReadResult{} })
	if fetches.Load() != 2 {
		t.Fatalf("reread after input reused an older read: fetches = %d", fetches.Load())
	}
	time.Sleep(screenShareWindow + 20*time.Millisecond)
	reads.read(context.Background(), key, time.Now().Add(-screenShareWindow), func(context.Context) screenReadResult { fetches.Add(1); return screenReadResult{} })
	if fetches.Load() != 3 {
		t.Fatalf("stale result reused: fetches = %d", fetches.Load())
	}
}

func TestScreenReadIgnoresAnotherWindowsCancellation(t *testing.T) {
	a := &API{}
	endpoint := &screenTestEndpoint{}
	endpoint.setScreen("ready")
	started := make(chan struct{})
	block := make(chan struct{})
	endpoint.beforeRead = func(ctx context.Context) error {
		select {
		case started <- struct{}{}:
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-block:
			}
		default:
		}
		return nil
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { _, _, err := a.readScreen(firstCtx, "h", endpoint, "p", 200, time.Time{}); firstDone <- err }()
	<-started
	secondDone := make(chan string, 1)
	go func() { text, _, _ := a.readScreen(context.Background(), "h", endpoint, "p", 200, time.Time{}); secondDone <- text }()
	time.Sleep(20 * time.Millisecond)
	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first reader error = %v", err)
	}
	select {
	case text := <-secondDone:
		if text != "ready" {
			t.Fatalf("second reader got %q", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second reader did not retry after the first window left")
	}
	close(block)
}

type screenTestEndpoint struct {
	mu         sync.Mutex
	screen     string
	readErr    error
	reads      []map[string]any
	inputs     atomic.Int32
	beforeRead func(context.Context) error
}

func (e *screenTestEndpoint) setScreen(text string) {
	e.mu.Lock()
	e.screen = text
	e.mu.Unlock()
}

func (e *screenTestEndpoint) readCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.reads)
}

func (e *screenTestEndpoint) Snapshot(context.Context) (herdr.Snapshot, error) {
	return herdr.Snapshot{Version: "0.9.1", Protocol: 22, Panes: []herdr.Pane{{ID: "p_fixture", TerminalID: "term_fixture"}}}, nil
}

func (e *screenTestEndpoint) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	switch method {
	case "pane.read":
		if e.beforeRead != nil {
			if err := e.beforeRead(ctx); err != nil {
				return nil, err
			}
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		e.reads = append(e.reads, params.(map[string]any))
		if e.readErr != nil {
			return nil, e.readErr
		}
		return json.Marshal(map[string]any{"type": "pane_read", "read": map[string]any{"text": e.screen, "truncated": false}})
	case "pane.send_input":
		e.inputs.Add(1)
		return json.RawMessage(`{}`), nil
	}
	return nil, errors.New("unexpected method " + method)
}

func (*screenTestEndpoint) OpenTerminal(context.Context, herdr.TerminalOpen) (herdr.TerminalProcess, error) {
	return nil, errors.New("the screen view must not open a terminal stream")
}
func (*screenTestEndpoint) StageImage(context.Context, string, io.Reader) (string, error) {
	return "", errors.New("unexpected")
}
func (*screenTestEndpoint) Close() error { return nil }

type screenTestPool struct{ endpoint *screenTestEndpoint }

func (p screenTestPool) Open(context.Context, store.Host) (herdr.Endpoint, error) {
	return p.endpoint, nil
}
func (screenTestPool) CloseHost(string) {}
func (screenTestPool) Close()           {}

type screenBrowser struct {
	t   *testing.T
	ctx context.Context
	ws  *websocket.Conn
}

func screenFixture(t *testing.T) (*screenTestEndpoint, *screenBrowser) {
	t.Helper()
	a, db, srv, client, admin := authFixture(t)
	endpoint := &screenTestEndpoint{}
	a.hosts.Close()
	a.hosts = screenTestPool{endpoint}
	owner := admin["user"].(map[string]any)["id"].(string)
	if err := db.CreateHost(t.Context(), store.Host{ID: "hst_fixture", OwnerID: owner, Name: "Screen", Transport: "ssh", Port: 22}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	ws := fixtureSocketHello(t, ctx, srv, client, `{"t":"hello","protocol":1,"browser_instance_id":"screen-browser-instance"}`)
	return endpoint, &screenBrowser{t: t, ctx: ctx, ws: ws}
}

func (b *screenBrowser) send(message map[string]any) {
	b.t.Helper()
	data, _ := json.Marshal(message)
	if err := b.ws.Write(b.ctx, websocket.MessageText, data); err != nil {
		b.t.Fatal(err)
	}
}

// next returns the next message of one of the wanted types, skipping snapshots.
func (b *screenBrowser) next(wanted ...string) map[string]any {
	b.t.Helper()
	for {
		_, data, err := b.ws.Read(b.ctx)
		if err != nil {
			b.t.Fatal(err)
		}
		var message map[string]any
		_ = json.Unmarshal(data, &message)
		for _, want := range wanted {
			if message["t"] == want {
				return message
			}
		}
		if message["t"] == "error" {
			b.t.Fatalf("unexpected error: %s", data)
		}
	}
}

func frameLines(t *testing.T, current []string, frame map[string]any) []string {
	t.Helper()
	if frame["full"] == true {
		next := []string{}
		if lines, ok := frame["lines"].([]any); ok {
			for _, line := range lines {
				next = append(next, line.(string))
			}
		}
		return next
	}
	drop, _ := frame["drop"].(float64)
	set := [][2]any{}
	if items, ok := frame["set"].([]any); ok {
		for _, item := range items {
			pair := item.([]any)
			set = append(set, [2]any{int(pair[0].(float64)), pair[1].(string)})
		}
	}
	return applyScreenDiff(current, int(drop), int(frame["total"].(float64)), set)
}

func TestScreenWatchStreamsDiffsAndRereadsAfterInput(t *testing.T) {
	endpoint, browser := screenFixture(t)
	endpoint.setScreen("\x1b[0m$ \r\n")
	browser.send(map[string]any{"t": "screen.watch", "id": "w1", "pane_id": "p_fixture", "lines": 120})
	watching := browser.next("screen.watching")
	if watching["id"] != "w1" || watching["lines"].(float64) != 120 {
		t.Fatalf("watching reply %v", watching)
	}
	gen := watching["gen"]
	first := browser.next("screen")
	if first["full"] != true || first["gen"] != gen || first["requested"].(float64) != 120 {
		t.Fatalf("first frame %v", first)
	}
	screen := frameLines(t, nil, first)
	if strings.Join(screen, "|") != "\x1b[0m$ " {
		t.Fatalf("screen %q", screen)
	}
	endpoint.mu.Lock()
	params := endpoint.reads[0]
	endpoint.mu.Unlock()
	if params["source"] != "recent_unwrapped" || params["format"] != "ansi" || params["lines"] != 120 || params["pane_id"] != "p_fixture" {
		t.Fatalf("pane.read params %v", params)
	}

	endpoint.setScreen("\x1b[0m$ echo hi\r\nhi\r\n$ ")
	started := time.Now()
	browser.send(map[string]any{"t": "call", "id": "c1", "method": "pane.send_input", "params": map[string]any{"pane_id": "p_fixture", "text": "echo hi", "keys": []string{"Enter"}}})
	frame := browser.next("screen")
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("input did not trigger a prompt reread: %v", elapsed)
	}
	screen = frameLines(t, screen, frame)
	if strings.Join(screen, "|") != "\x1b[0m$ echo hi|hi|$ " {
		t.Fatalf("screen after input %q", screen)
	}
	if endpoint.inputs.Load() != 1 {
		t.Fatalf("input calls = %d", endpoint.inputs.Load())
	}

	browser.send(map[string]any{"t": "screen.unwatch", "pane_id": "p_fixture"})
	time.Sleep(100 * time.Millisecond)
	settled := endpoint.readCount()
	endpoint.setScreen("changed after unwatch")
	time.Sleep(700 * time.Millisecond)
	if endpoint.readCount() != settled {
		t.Fatalf("reads continued after unwatch: %d -> %d", settled, endpoint.readCount())
	}
}

func TestScreenWatchLimitAndErrors(t *testing.T) {
	endpoint, browser := screenFixture(t)
	endpoint.setScreen("x")
	for index := 1; index <= screenMaxWatches; index++ {
		browser.send(map[string]any{"t": "screen.watch", "id": fmt.Sprintf("w%d", index), "pane_id": fmt.Sprintf("p%d", index)})
		browser.next("screen.watching")
	}
	browser.send(map[string]any{"t": "screen.watch", "id": "over", "pane_id": "p_over"})
	for {
		_, data, err := browser.ws.Read(browser.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var message map[string]any
		_ = json.Unmarshal(data, &message)
		if message["t"] == "error" && message["id"] == "over" {
			if message["code"] != "screen_limit" {
				t.Fatalf("limit error %v", message)
			}
			break
		}
	}
	for index := 1; index <= screenMaxWatches; index++ {
		browser.send(map[string]any{"t": "screen.unwatch", "pane_id": fmt.Sprintf("p%d", index)})
	}

	endpoint.mu.Lock()
	endpoint.readErr = &herdr.APIError{Code: "pane_not_found", Message: "no pane"}
	endpoint.mu.Unlock()
	browser.send(map[string]any{"t": "screen.watch", "id": "gone", "pane_id": "p_gone"})
	message := browser.next("screen.error")
	if message["code"] != "pane_unavailable" || message["pane_id"] != "p_gone" {
		t.Fatalf("screen error %v", message)
	}
}

func TestScreenWatchReplacementTagsANewGeneration(t *testing.T) {
	endpoint, browser := screenFixture(t)
	endpoint.setScreen("a\r\nb")
	browser.send(map[string]any{"t": "screen.watch", "id": "w1", "pane_id": "p_fixture", "lines": 50})
	firstGen := browser.next("screen.watching")["gen"]
	browser.next("screen")
	browser.send(map[string]any{"t": "screen.watch", "id": "w2", "pane_id": "p_fixture", "lines": 400})
	second := browser.next("screen.watching")
	if second["gen"] == firstGen || second["lines"].(float64) != 400 {
		t.Fatalf("replacement reply %v", second)
	}
	for {
		frame := browser.next("screen")
		if frame["gen"] == second["gen"] {
			if frame["full"] != true {
				t.Fatalf("a new generation must start with a full frame: %v", frame)
			}
			break
		}
	}
}
