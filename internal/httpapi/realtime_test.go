package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/riba2534/herdrx/internal/terminalwire"
)

// realtimeEndpoint scripts Herdr's pushed events, observer frames and screen
// text so tests can tell event-driven refreshes from polling.
type realtimeEndpoint struct {
	mu        sync.Mutex
	agents    []herdr.Agent
	screen    string
	snapshots atomic.Int32
	reads     atomic.Int32
	mutations atomic.Int32

	subscribeErr error
	attempts     atomic.Int32
	// rejectSubscribe, when set, may refuse one subscription like Herdr does.
	rejectSubscribe func([]herdr.EventSubscription) error
	subscriptions   chan []herdr.EventSubscription
	push            chan herdr.Event
	end             chan error // ends the live subscription with this error

	opens     chan herdr.TerminalOpen
	processes chan *realtimeProcess
}

func newRealtimeEndpoint() *realtimeEndpoint {
	return &realtimeEndpoint{
		subscriptions: make(chan []herdr.EventSubscription, 16), push: make(chan herdr.Event, 16), end: make(chan error, 1),
		opens: make(chan herdr.TerminalOpen, 16), processes: make(chan *realtimeProcess, 16),
	}
}

func (e *realtimeEndpoint) Snapshot(context.Context) (herdr.Snapshot, error) {
	e.snapshots.Add(1)
	e.mu.Lock()
	defer e.mu.Unlock()
	return herdr.Snapshot{
		Version: "0.9.1", Protocol: 22,
		Panes:   []herdr.Pane{{ID: "p_fixture", TerminalID: "term_fixture"}},
		Layouts: []herdr.Layout{{Panes: []herdr.LayoutPane{{PaneID: "p_fixture", Rect: herdr.Rect{Width: 132, Height: 41}}}}},
		Agents:  slices.Clone(e.agents),
	}, nil
}

func (e *realtimeEndpoint) Call(_ context.Context, method string, _ any) (json.RawMessage, error) {
	if method == "pane.read" {
		e.reads.Add(1)
		e.mu.Lock()
		defer e.mu.Unlock()
		return json.Marshal(map[string]any{"read": map[string]any{"text": e.screen}})
	}
	e.mutations.Add(1)
	return json.RawMessage(`{}`), nil
}

func (e *realtimeEndpoint) SubscribeEvents(ctx context.Context, subscriptions []herdr.EventSubscription, onEvent func(herdr.Event)) error {
	e.attempts.Add(1)
	if e.subscribeErr != nil {
		return e.subscribeErr
	}
	if e.rejectSubscribe != nil {
		if err := e.rejectSubscribe(subscriptions); err != nil {
			return err
		}
	}
	e.subscriptions <- subscriptions
	onEvent(herdr.Event{Event: herdr.EventSubscriptionStarted})
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-e.push:
			onEvent(event)
		case err := <-e.end:
			return err
		}
	}
}

func (e *realtimeEndpoint) OpenTerminal(ctx context.Context, open herdr.TerminalOpen) (herdr.TerminalProcess, error) {
	reader, writer := io.Pipe()
	process := &realtimeProcess{reader: reader, writer: writer, done: make(chan struct{})}
	context.AfterFunc(ctx, func() { _ = process.Close() })
	e.opens <- open
	e.processes <- process
	return process, nil
}
func (*realtimeEndpoint) StageImage(context.Context, string, io.Reader) (string, error) {
	return "", errors.New("unexpected")
}
func (*realtimeEndpoint) Close() error { return nil }

func (e *realtimeEndpoint) setScreen(text string) {
	e.mu.Lock()
	e.screen = text
	e.mu.Unlock()
}

type realtimeProcess struct {
	reader *io.PipeReader
	writer *io.PipeWriter
	done   chan struct{}
	once   sync.Once
}

func (p *realtimeProcess) Stdout() io.Reader     { return p.reader }
func (p *realtimeProcess) Stdin() io.WriteCloser { return nopWriteCloser{} }
func (p *realtimeProcess) Wait() error           { <-p.done; return nil }
func (p *realtimeProcess) Close() error {
	p.once.Do(func() {
		close(p.done)
		_ = p.reader.Close()
		_ = p.writer.Close()
	})
	return nil
}

// frame writes one Herdr observer frame; ansi is raw terminal output.
func (p *realtimeProcess) frame(t *testing.T, seq uint64, full bool, ansi string) {
	t.Helper()
	line, _ := json.Marshal(map[string]any{"type": "terminal.frame", "seq": seq, "full": full, "width": 80, "height": 24, "bytes": base64.StdEncoding.EncodeToString([]byte(ansi))})
	if _, err := p.writer.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
}

type nopWriteCloser struct{}

func (nopWriteCloser) Write(data []byte) (int, error) { return len(data), nil }
func (nopWriteCloser) Close() error                   { return nil }

type realtimePool struct{ endpoint *realtimeEndpoint }

func (p realtimePool) Open(context.Context, store.Host) (herdr.Endpoint, error) {
	return p.endpoint, nil
}
func (realtimePool) CloseHost(string) {}
func (realtimePool) Close()           {}

func realtimeFixture(t *testing.T) (*realtimeEndpoint, *httptest.Server, *http.Client) {
	t.Helper()
	a, db, srv, client, admin := authFixture(t)
	endpoint := newRealtimeEndpoint()
	a.hosts.Close()
	a.hosts = realtimePool{endpoint}
	owner := admin["user"].(map[string]any)["id"].(string)
	if err := db.CreateHost(t.Context(), store.Host{ID: "hst_fixture", OwnerID: owner, Name: "Realtime", Transport: "ssh", Port: 22}); err != nil {
		t.Fatal(err)
	}
	return endpoint, srv, client
}

func receive[T any](t *testing.T, values <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

type realtimeMessage struct {
	text   map[string]any
	binary *terminalwire.Frame
	at     time.Time
}

// realtimeBrowser collects every message in the background and acknowledges
// terminal frames like the web client.
type realtimeBrowser struct {
	t        *testing.T
	ctx      context.Context
	ws       *websocket.Conn
	messages chan realtimeMessage
}

func newRealtimeBrowser(t *testing.T, srv *httptest.Server, client *http.Client, id string) *realtimeBrowser {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	ws := fixtureSocketHello(t, ctx, srv, client, `{"t":"hello","protocol":1,"browser_instance_id":"`+id+`"}`)
	b := &realtimeBrowser{t: t, ctx: ctx, ws: ws, messages: make(chan realtimeMessage, 256)}
	go func() {
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				close(b.messages)
				return
			}
			message := realtimeMessage{at: time.Now()}
			if typ == websocket.MessageBinary {
				frame, err := terminalwire.Decode(data)
				if err != nil {
					continue
				}
				frame.Payload = append([]byte(nil), frame.Payload...)
				message.binary = &frame
				_ = ws.Write(ctx, websocket.MessageBinary, terminalwire.Encode(terminalwire.Frame{Opcode: terminalwire.OpcodeAck, StreamID: frame.StreamID, Seq: frame.Seq}))
			} else {
				_ = json.Unmarshal(data, &message.text)
			}
			b.messages <- message
		}
	}()
	return b
}

func (b *realtimeBrowser) send(message map[string]any) {
	b.t.Helper()
	data, _ := json.Marshal(message)
	if err := b.ws.Write(b.ctx, websocket.MessageText, data); err != nil {
		b.t.Fatal(err)
	}
}

// next waits up to within for a text message of type want, or a terminal
// frame when want is "frame"; ok is false on timeout.
func (b *realtimeBrowser) next(want string, within time.Duration) (realtimeMessage, bool) {
	b.t.Helper()
	timer := time.NewTimer(within)
	defer timer.Stop()
	for {
		select {
		case message, open := <-b.messages:
			if !open {
				b.t.Fatal("workbench closed")
			}
			if want == "frame" && message.binary != nil {
				return message, true
			}
			if message.text != nil && message.text["t"] == want {
				return message, true
			}
		case <-timer.C:
			return realtimeMessage{}, false
		}
	}
}

func (b *realtimeBrowser) must(want string, within time.Duration) realtimeMessage {
	b.t.Helper()
	message, ok := b.next(want, within)
	if !ok {
		b.t.Fatalf("no %s within %v", want, within)
	}
	return message
}

func TestSnapshotFollowsHerdrEventsAndSlowsPolling(t *testing.T) {
	endpoint, srv, client := realtimeFixture(t)
	// The hello helper consumes the initial snapshot.
	browser := newRealtimeBrowser(t, srv, client, "realtime-browser-events")
	first := receive(t, endpoint.subscriptions, "the event subscription")
	if !slices.ContainsFunc(first, func(s herdr.EventSubscription) bool { return s["type"] == "layout.updated" }) ||
		slices.ContainsFunc(first, func(s herdr.EventSubscription) bool { return s["type"] == "pane.agent_status_changed" }) {
		t.Fatalf("first subscription %v", first)
	}
	// subscription_started refreshes once to cover the gap before the stream.
	browser.must("snapshot", time.Second)

	sent := time.Now()
	endpoint.push <- herdr.Event{Event: "pane_created"}
	endpoint.push <- herdr.Event{Event: "layout_updated"}
	message := browser.must("snapshot", time.Second)
	if elapsed := message.at.Sub(sent); elapsed > 500*time.Millisecond {
		t.Fatalf("event refresh took %v", elapsed)
	}
	// The burst is merged into that one snapshot, and polling slows to the fallback.
	if extra, ok := browser.next("snapshot", snapshotPollInterval+700*time.Millisecond); ok {
		t.Fatalf("snapshot without an event while the stream is live: %v", extra.text)
	}

	endpoint.mu.Lock()
	endpoint.agents = []herdr.Agent{{PaneID: "p_agent", Agent: "claude"}}
	endpoint.mu.Unlock()
	endpoint.push <- herdr.Event{Event: "pane_agent_detected"}
	browser.must("snapshot", time.Second)
	select {
	case next := <-endpoint.subscriptions:
		if !slices.ContainsFunc(next, func(s herdr.EventSubscription) bool {
			return s["type"] == "pane.agent_status_changed" && s["pane_id"] == "p_agent"
		}) {
			t.Fatalf("resubscription %v", next)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a new agent pane did not renew the subscription")
	}
	if endpoint.mutations.Load() != 0 {
		t.Fatal("event refresh called a Herdr mutation")
	}
}

func TestSnapshotPollsWhenHerdrRejectsEvents(t *testing.T) {
	endpoint, srv, client := realtimeFixture(t)
	endpoint.subscribeErr = &herdr.APIError{Code: "invalid_request", Message: "unknown method"}
	browser := newRealtimeBrowser(t, srv, client, "realtime-browser-polling")
	browser.must("snapshot", snapshotPollInterval+time.Second)
	// An unknown method waits the long backoff instead of retrying.
	if got := endpoint.attempts.Load(); got != 1 {
		t.Fatalf("subscribe attempts = %d", got)
	}
}

func TestSubscriptionRecoversFromAPaneClosedBeforeSubscribing(t *testing.T) {
	endpoint, srv, client := realtimeFixture(t)
	endpoint.agents = []herdr.Agent{{PaneID: "p_gone", Agent: "claude"}}
	endpoint.rejectSubscribe = func(subscriptions []herdr.EventSubscription) error {
		if !slices.ContainsFunc(subscriptions, func(s herdr.EventSubscription) bool { return s["pane_id"] == "p_gone" }) {
			return nil
		}
		// The pane closed between the snapshot and the subscription.
		endpoint.mu.Lock()
		endpoint.agents = []herdr.Agent{{PaneID: "p_live", Agent: "codex"}}
		endpoint.mu.Unlock()
		return &herdr.APIError{Code: "pane_not_found", Message: "pane p_gone not found"}
	}
	started := time.Now()
	newRealtimeBrowser(t, srv, client, "realtime-browser-pane-gone")
	subscription := receive(t, endpoint.subscriptions, "the renewed subscription")
	if elapsed := time.Since(started); elapsed > 900*time.Millisecond {
		t.Fatalf("resubscribed after %v, expected no backoff", elapsed)
	}
	if !slices.ContainsFunc(subscription, func(s herdr.EventSubscription) bool { return s["pane_id"] == "p_live" }) {
		t.Fatalf("renewed subscription %v", subscription)
	}
}

func TestTerminalOutputMergesBurstsAndKeepsEchoesImmediate(t *testing.T) {
	endpoint, srv, client := realtimeFixture(t)
	browser := newRealtimeBrowser(t, srv, client, "realtime-browser-frames")
	browser.must("snapshot", 5*time.Second)
	browser.send(map[string]any{"t": "terminal.open", "id": "open", "pane_id": "p_fixture", "mode": "observe", "cols": 80, "rows": 24})
	receive(t, endpoint.opens, "the terminal observer")
	process := receive(t, endpoint.processes, "the terminal observer process")
	browser.must("terminal.opened", 2*time.Second)
	payload := func(message realtimeMessage) string { return string(message.binary.Payload[4:]) }

	process.frame(t, 1, true, "screen")
	if got := browser.must("frame", time.Second); payload(got) != "screen" || got.binary.Seq != 1 || got.binary.Flags&terminalwire.FlagFull == 0 {
		t.Fatalf("first frame %q seq %d", payload(got), got.binary.Seq)
	}
	for seq, ansi := range []string{"a", "b", "c", "d"} {
		process.frame(t, uint64(seq+2), false, ansi)
	}
	merged := browser.must("frame", time.Second)
	if payload(merged) != "abcd" || merged.binary.Seq != 5 || merged.binary.Flags&terminalwire.FlagFull != 0 {
		t.Fatalf("merged diff %q seq %d flags %d", payload(merged), merged.binary.Seq, merged.binary.Flags)
	}

	// A whole frame inside a burst replaces the diffs held before it.
	process.frame(t, 6, false, "x")
	process.frame(t, 7, false, "y")
	process.frame(t, 8, true, "fresh")
	process.frame(t, 9, false, "!")
	full := browser.must("frame", time.Second)
	if payload(full) != "fresh!" || full.binary.Seq != 9 || full.binary.Flags&terminalwire.FlagFull == 0 {
		t.Fatalf("full merge %q seq %d flags %d", payload(full), full.binary.Seq, full.binary.Flags)
	}

	time.Sleep(100 * time.Millisecond)
	sent := time.Now()
	process.frame(t, 10, false, "k")
	echo := browser.must("frame", time.Second)
	if payload(echo) != "k" || echo.at.Sub(sent) > 25*time.Millisecond {
		t.Fatalf("echo %q held for %v", payload(echo), echo.at.Sub(sent))
	}
	if _, ok := browser.next("frame", 150*time.Millisecond); ok {
		t.Fatal("unexpected extra frame")
	}
}

func TestScreenViewFollowsSharedObserver(t *testing.T) {
	endpoint, srv, client := realtimeFixture(t)
	endpoint.setScreen("$ ")
	first := newRealtimeBrowser(t, srv, client, "realtime-screen-window-1")
	second := newRealtimeBrowser(t, srv, client, "realtime-screen-window-2")
	first.must("snapshot", 5*time.Second)
	second.must("snapshot", 5*time.Second)
	first.send(map[string]any{"t": "screen.watch", "id": "w1", "pane_id": "p_fixture"})
	first.must("screen", 2*time.Second)
	open := receive(t, endpoint.opens, "the screen observer")
	if open.Mode != "observe" || open.Takeover || open.Cols != 132 || open.Rows != 41 {
		t.Fatalf("screen observer open %+v", open)
	}
	process := receive(t, endpoint.processes, "the screen observer process")
	second.send(map[string]any{"t": "screen.watch", "id": "w2", "pane_id": "p_fixture"})
	second.must("screen", 2*time.Second)
	process.frame(t, 1, true, "")

	// The observer drives reads: a redraw becomes a screen frame for both windows.
	time.Sleep(300 * time.Millisecond)
	endpoint.setScreen("$ ls\r\nREADME\r\n$ ")
	sent := time.Now()
	process.frame(t, 2, false, "ls")
	for _, browser := range []*realtimeBrowser{first, second} {
		message := browser.must("screen", time.Second)
		if elapsed := message.at.Sub(sent); elapsed > 400*time.Millisecond {
			t.Fatalf("observer-driven read took %v", elapsed)
		}
	}
	select {
	case extra := <-endpoint.opens:
		t.Fatalf("a second window opened another observer: %+v", extra)
	default:
	}

	// Without a redraw signal the polling schedule is off.
	reads := endpoint.reads.Load()
	endpoint.setScreen("changed silently")
	if _, ok := first.next("screen", 1200*time.Millisecond); ok {
		t.Fatal("screen polled while the observer was live")
	}
	if got := endpoint.reads.Load(); got != reads {
		t.Fatalf("reads while idle: %d -> %d", reads, got)
	}

	// Losing the observer falls back to polling.
	_ = process.Close()
	first.must("screen", 1500*time.Millisecond)
	if endpoint.mutations.Load() != 0 {
		t.Fatal("screen view called a Herdr mutation")
	}
}

func TestScreenSignalRegistryIsSharedAndReleased(t *testing.T) {
	var reads screenReads
	opened := make(chan *realtimeProcess, 4)
	open := func(ctx context.Context) (herdr.TerminalProcess, error) {
		reader, writer := io.Pipe()
		process := &realtimeProcess{reader: reader, writer: writer, done: make(chan struct{})}
		context.AfterFunc(ctx, func() { _ = process.Close() })
		opened <- process
		return process, nil
	}
	key := screenSignalKey{hostID: "h", paneID: "p"}
	a, releaseA := reads.subscribeSignal(key, open)
	b, releaseB := reads.subscribeSignal(key, open)
	process := receive(t, opened, "the shared observer")
	process.frame(t, 1, true, "")
	for _, sub := range []*screenSignalSub{a, b} {
		select {
		case <-sub.changed:
		case <-time.After(time.Second):
			t.Fatal("subscriber missed a redraw")
		}
	}
	releaseA()
	select {
	case <-process.done:
		t.Fatal("observer closed while another window still watches")
	case <-time.After(50 * time.Millisecond):
	}
	releaseB()
	select {
	case <-b.ended:
	case <-time.After(time.Second):
		t.Fatal("observer outlived its last window")
	}
	if len(opened) != 0 {
		t.Fatal("subscribers did not share one observer")
	}
}

func TestSubscriptionStartResetsQuickRetries(t *testing.T) {
	endpoint, srv, client := realtimeFixture(t)
	endpoint.agents = []herdr.Agent{{PaneID: "p_0", Agent: "claude"}}
	var rejections atomic.Int32
	reject := atomic.Bool{}
	reject.Store(true)
	endpoint.rejectSubscribe = func([]herdr.EventSubscription) error {
		if !reject.Load() {
			return nil
		}
		n := rejections.Add(1)
		if n == 3 {
			// The third rejection uses up the quick retries; the next one succeeds.
			reject.Store(false)
		}
		endpoint.mu.Lock()
		endpoint.agents = []herdr.Agent{{PaneID: fmt.Sprintf("p_%d", n), Agent: "claude"}}
		endpoint.mu.Unlock()
		return &herdr.APIError{Code: "pane_not_found", Message: "pane closed"}
	}
	newRealtimeBrowser(t, srv, client, "realtime-browser-retry-reset")
	receive(t, endpoint.subscriptions, "the subscription after three closed panes")

	// The stream ran, so another closed pane is retried at once again.
	reject.Store(true)
	rejections.Store(2)
	ended := time.Now()
	endpoint.end <- &herdr.APIError{Code: "events_lost", Message: "subscriber fell behind"}
	receive(t, endpoint.subscriptions, "the subscription after the stream ended")
	if elapsed := time.Since(ended); elapsed > 1800*time.Millisecond {
		t.Fatalf("resubscribed after %v: quick retries were not reset by the live stream", elapsed)
	}
}

func TestSubscriptionBacksOffWhenPanesKeepClosing(t *testing.T) {
	endpoint, srv, client := realtimeFixture(t)
	endpoint.rejectSubscribe = func([]herdr.EventSubscription) error {
		return &herdr.APIError{Code: "pane_not_found", Message: "pane closed"}
	}
	newRealtimeBrowser(t, srv, client, "realtime-browser-retry-bound")
	time.Sleep(1500 * time.Millisecond)
	// Three quick retries, then the exponential backoff: 1 + 3 + one after 1 s.
	if got := endpoint.attempts.Load(); got < 4 || got > 5 {
		t.Fatalf("subscribe attempts in 1.5 s = %d", got)
	}
}
