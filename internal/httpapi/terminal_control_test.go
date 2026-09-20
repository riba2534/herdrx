package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/riba2534/herdrx/internal/terminalwire"
)

type geometryTestEndpoint struct {
	mu              sync.Mutex
	processes       []*geometryTestProcess
	commands        []map[string]any
	control         *geometryTestProcess
	gate            chan struct{}
	external        bool
	input           atomic.Int32
	generation      atomic.Int64
	probeGeneration atomic.Bool
	onWrite         func(map[string]any)
}

func (e *geometryTestEndpoint) HerdrCapabilities(context.Context) (herdr.CapabilityReport, error) {
	if e.probeGeneration.Swap(false) {
		e.generation.Add(1)
	}
	return herdr.CapabilityReport{Features: map[string]herdr.FeatureCapability{
		"observe": {State: herdr.CapabilityAvailable}, "resize": {State: herdr.CapabilityAvailable},
	}}, nil
}

func (e *geometryTestEndpoint) RuntimeGeneration() string {
	return time.Unix(e.generation.Load(), 0).String()
}
func (e *geometryTestEndpoint) Snapshot(context.Context) (herdr.Snapshot, error) {
	return herdr.Snapshot{Version: "0.9.1", Protocol: 22, Panes: []herdr.Pane{{ID: "p_fixture", TerminalID: "term_fixture"}}}, nil
}
func (e *geometryTestEndpoint) Call(_ context.Context, method string, _ any) (json.RawMessage, error) {
	if method != "pane.send_text" {
		return nil, errors.New("unexpected mutation: " + method)
	}
	e.input.Add(1)
	return json.RawMessage(`{}`), nil
}
func (*geometryTestEndpoint) StageImage(context.Context, string, io.Reader) (string, error) {
	return "", errors.New("unexpected")
}
func (*geometryTestEndpoint) Close() error { return nil }
func (e *geometryTestEndpoint) OpenTerminal(ctx context.Context, o herdr.TerminalOpen) (herdr.TerminalProcess, error) {
	if o.Takeover {
		return nil, errors.New("takeover must never be sent")
	}
	r, w := io.Pipe()
	p := &geometryTestProcess{reader: r, writer: w, endpoint: e, done: make(chan struct{})}
	e.mu.Lock()
	e.processes = append(e.processes, p)
	conflict := false
	gate := e.gate
	if o.Mode == "control" {
		conflict = e.external || e.control != nil
		if !conflict {
			e.control = p
		}
	}
	e.mu.Unlock()
	go func() {
		stop := context.AfterFunc(ctx, func() { _ = p.Close() })
		defer stop()
		if conflict {
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "terminal.closed", "reason": "already has an attached client"})
			_ = p.Close()
			return
		}
		if o.Mode == "control" && gate != nil {
			select {
			case <-gate:
			case <-p.done:
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "terminal.frame", "full": true, "seq": 1, "width": o.Cols, "height": o.Rows, "bytes": ""})
		<-p.done
	}()
	return p, nil
}

type geometryTestProcess struct {
	viewport atomic.Uint32
	reader   *io.PipeReader
	writer   *io.PipeWriter
	endpoint *geometryTestEndpoint
	done     chan struct{}
	once     sync.Once
}

func (p *geometryTestProcess) ResizeViewport(_ context.Context, cols, rows uint16) error {
	p.viewport.Store(uint32(cols)<<16 | uint32(rows))
	return nil
}
func (p *geometryTestProcess) Stdout() io.Reader     { return p.reader }
func (p *geometryTestProcess) Stdin() io.WriteCloser { return p }
func (p *geometryTestProcess) Wait() error           { <-p.done; return nil }
func (p *geometryTestProcess) Close() error {
	p.once.Do(func() {
		p.endpoint.mu.Lock()
		if p.endpoint.control == p {
			p.endpoint.control = nil
		}
		p.endpoint.mu.Unlock()
		close(p.done)
		_ = p.reader.Close()
		_ = p.writer.Close()
	})
	return nil
}
func (p *geometryTestProcess) Write(data []byte) (int, error) {
	select {
	case <-p.done:
		return 0, io.ErrClosedPipe
	default:
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var m map[string]any
		if err := dec.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			return 0, err
		}
		p.endpoint.mu.Lock()
		p.endpoint.commands = append(p.endpoint.commands, m)
		onWrite := p.endpoint.onWrite
		p.endpoint.mu.Unlock()
		if onWrite != nil {
			onWrite(m)
		}
	}
	return len(data), nil
}

type geometryTestPool struct{ endpoint *geometryTestEndpoint }

func (p geometryTestPool) Open(context.Context, store.Host) (herdr.Endpoint, error) {
	return p.endpoint, nil
}
func (geometryTestPool) CloseHost(string) {}
func (geometryTestPool) Close()           {}

type geometryBrowser struct {
	t          *testing.T
	ctx        context.Context
	ws         *websocket.Conn
	stream     uint32
	epoch, gen string
}

func geometryFixture(t *testing.T) (*API, *geometryTestEndpoint, *httptest.Server, *http.Client, context.Context) {
	t.Helper()
	a, db, srv, client, admin := authFixture(t)
	e := &geometryTestEndpoint{}
	a.hosts.Close()
	a.hosts = geometryTestPool{e}
	owner := admin["user"].(map[string]any)["id"].(string)
	if err := db.CreateHost(t.Context(), store.Host{ID: "hst_fixture", OwnerID: owner, Name: "Geometry", Transport: "local", Port: 22}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return a, e, srv, client, ctx
}
func newGeometryBrowser(t *testing.T, ctx context.Context, srv *httptest.Server, client *http.Client) *geometryBrowser {
	b := &geometryBrowser{t: t, ctx: ctx, ws: fixtureSocketHello(t, ctx, srv, client, `{"t":"hello","protocol":1,"browser_instance_id":"untrusted-same-id","capabilities":{"terminal_control":1,"terminal_resize_v2":1}}`)}
	b.send(map[string]any{"t": "terminal.open", "id": "open", "pane_id": "p_fixture", "mode": "observe", "cols": 80, "rows": 24})
	m := b.read("terminal.opened")
	b.stream = uint32(m["stream_id"].(float64))
	b.epoch = m["stream_epoch"].(string)
	return b
}
func (b *geometryBrowser) send(m map[string]any) {
	b.t.Helper()
	data, _ := json.Marshal(m)
	if err := b.ws.Write(b.ctx, websocket.MessageText, data); err != nil {
		b.t.Fatal(err)
	}
}
func (b *geometryBrowser) read(want string) map[string]any {
	b.t.Helper()
	for {
		typ, data, err := b.ws.Read(b.ctx)
		if err != nil {
			b.t.Fatal(err)
		}
		if typ == websocket.MessageBinary {
			f, _ := terminalwire.Decode(data)
			_ = b.ws.Write(b.ctx, websocket.MessageBinary, terminalwire.Encode(terminalwire.Frame{Opcode: terminalwire.OpcodeAck, StreamID: f.StreamID, Seq: f.Seq}))
			continue
		}
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		if m["t"] == want {
			return m
		}
		if m["t"] == "error" {
			b.t.Fatalf("unexpected error: %s", data)
		}
	}
}
func (b *geometryBrowser) command(typ string, extra map[string]any) {
	m := map[string]any{"t": typ, "id": typ, "stream_id": b.stream, "stream_epoch": b.epoch, "control_generation": b.gen}
	for k, v := range extra {
		m[k] = v
	}
	b.send(m)
}
func (b *geometryBrowser) acquire(transfer bool) {
	b.command("terminal.control.acquire", map[string]any{"cols": 50, "rows": 20, "transfer": transfer})
	m := b.read("terminal.control.acquired")
	b.gen = m["control_generation"].(string)
}
func waitGeometry(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("geometry condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func geometryLeaseFor(t *testing.T, a *API) *geometryLease {
	t.Helper()
	a.geometry.mu.Lock()
	defer a.geometry.mu.Unlock()
	for _, e := range a.geometry.entries {
		e.mu.Lock()
		l := e.lease
		e.mu.Unlock()
		if l != nil {
			return l
		}
	}
	t.Fatal("no geometry lease")
	return nil
}

func TestGeometryTransferFencesOldWindowAndKeepsInput(t *testing.T) {
	a, e, srv, client, ctx := geometryFixture(t)
	first := newGeometryBrowser(t, ctx, srv, client)
	second := newGeometryBrowser(t, ctx, srv, client)
	first.acquire(false)
	old := geometryLeaseFor(t, a)
	second.command("terminal.control.acquire", map[string]any{"cols": 60, "rows": 20})
	if m := second.read("error"); m["code"] != "control_conflict" {
		t.Fatal(m)
	}
	second.acquire(true)
	// A delayed timer/release from the old connection cannot close the new owner.
	old.stream.geometry.release(old, "late callback")
	first.command("terminal.resize_v2", map[string]any{"resize_seq": 1, "cols": 22, "rows": 10})
	if m := first.read("error"); m["code"] != "control_expired" {
		t.Fatal(m)
	}
	second.command("terminal.resize_v2", map[string]any{"resize_seq": 2, "cols": 62, "rows": 21})
	second.read("terminal.resize.status")
	second.command("terminal.resize_v2", map[string]any{"resize_seq": 1, "cols": 22, "rows": 10})
	second.command("terminal.resize_v2", map[string]any{"stream_epoch": "old-epoch", "resize_seq": 3, "cols": 22, "rows": 10})
	second.read("error")
	first.send(map[string]any{"t": "call", "id": "wheel", "method": "terminal.scroll", "params": map[string]any{"stream_id": first.stream, "lines": -7, "column": 2, "row": 3}})
	first.read("res")
	_ = first.ws.Write(ctx, websocket.MessageBinary, terminalwire.Encode(terminalwire.Frame{Opcode: terminalwire.OpcodeInput, StreamID: first.stream, Payload: []byte("中文\r")}))
	waitGeometry(t, func() bool { return e.input.Load() == 1 })
	e.mu.Lock()
	commands := append([]map[string]any(nil), e.commands...)
	processes := len(e.processes)
	e.mu.Unlock()
	if len(commands) != 4 || commands[0]["cols"] != float64(62) || processes != 4 {
		t.Fatalf("commands=%v processes=%d", commands, processes)
	}
	second.command("terminal.control.release", nil)
	second.read("terminal.control.released")
	e.mu.Lock()
	alive := e.control != nil
	e.mu.Unlock()
	if alive {
		t.Fatal("released controller remains attached")
	}
	first.send(map[string]any{"t": "ping"})
	first.read("pong")
}
func TestGeometryRequiresRemoteConfirmationAndPreservesUnknownController(t *testing.T) {
	_, e, srv, client, ctx := geometryFixture(t)
	b := newGeometryBrowser(t, ctx, srv, client)
	e.mu.Lock()
	e.external = true
	e.mu.Unlock()
	b.command("terminal.control.acquire", map[string]any{"cols": 50, "rows": 20})
	if m := b.read("error"); m["code"] != "control_unavailable" {
		t.Fatal(m)
	}
	e.mu.Lock()
	e.external = false
	e.gate = make(chan struct{})
	gate := e.gate
	e.mu.Unlock()
	b.command("terminal.control.acquire", map[string]any{"cols": 50, "rows": 20})
	for {
		m := b.read("terminal.control.state")
		if m["state"] == "pending" {
			break
		}
	}
	close(gate)
	m := b.read("terminal.control.acquired")
	b.gen = m["control_generation"].(string)
	b.command("terminal.control.release", nil)
	b.read("terminal.control.released")
}

func TestGeometryFinalAcquireResponseAllowsImmediateRetry(t *testing.T) {
	for _, firstFails := range []bool{true, false} {
		name := "acquired"
		if firstFails {
			name = "error"
		}
		t.Run(name, func(t *testing.T) {
			a, e, srv, client, ctx := geometryFixture(t)
			b := newGeometryBrowser(t, ctx, srv, client)
			var stream *terminalStream
			var session *workbenchSession
			a.geometry.mu.Lock()
			for _, entry := range a.geometry.entries {
				entry.mu.Lock()
				for candidate, owner := range entry.watchers {
					if candidate.id == b.stream && candidate.epoch == b.epoch {
						stream, session = candidate, owner
					}
				}
				entry.mu.Unlock()
			}
			a.geometry.mu.Unlock()
			if stream == nil {
				t.Fatal("browser observer was not registered")
			}
			e.mu.Lock()
			e.external = firstFails
			e.mu.Unlock()
			finish, admitted := stream.beginGeometryAcquire()
			if !admitted {
				t.Fatal("first acquire was not admitted")
			}
			beforeResponse := make(chan struct{})
			allowResponse := make(chan struct{})
			allowReturn := make(chan struct{})
			done := make(chan struct{})
			writeResponse := sync.OnceFunc(func() { close(allowResponse) })
			returnHandler := sync.OnceFunc(func() { close(allowReturn) })
			defer writeResponse()
			defer returnHandler()
			go func() {
				defer close(done)
				defer finish()
				session.handleGeometry(workbenchMessage{Type: "terminal.control.acquire", RequestID: "first", StreamID: b.stream, StreamEpoch: b.epoch, Cols: 50, Rows: 20}, func() {
					finish()
					close(beforeResponse)
					<-allowResponse
				})
				// Keep the old fallback outstanding until the next request is
				// pending; its eventual defer must not clear the next guard.
				<-allowReturn
			}()
			select {
			case <-beforeResponse:
			case <-ctx.Done():
				t.Fatal("acquire did not reach its final response")
			}
			if stream.acquirePending.Load() {
				t.Fatal("final acquire response can become visible while its guard is still pending")
			}
			writeResponse()
			if firstFails {
				if m := b.read("error"); m["code"] != "control_unavailable" {
					t.Fatal(m)
				}
			} else {
				b.read("terminal.control.acquired")
			}
			gate := make(chan struct{})
			releaseRemote := sync.OnceFunc(func() { close(gate) })
			defer releaseRemote()
			e.mu.Lock()
			e.external = false
			e.gate = gate
			e.mu.Unlock()
			b.command("terminal.control.acquire", map[string]any{"cols": 60, "rows": 25, "transfer": true})
			for b.read("terminal.control.state")["state"] != "pending" {
			}
			returnHandler()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("previous acquire did not finish")
			}
			if !stream.acquirePending.Load() {
				t.Fatal("old acquire fallback cleared the subsequent pending guard")
			}
			b.command("terminal.control.acquire", map[string]any{"cols": 70, "rows": 30, "transfer": true})
			if m := b.read("error"); m["code"] != "control_pending" {
				t.Fatalf("additional work admitted while remote attach was pending: %v", m)
			}
			releaseRemote()
			b.read("terminal.control.acquired")
		})
	}
}

func TestGeometryExpiryAndDisconnectOnlyReleaseAccess(t *testing.T) {
	a, e, srv, client, ctx := geometryFixture(t)
	first := newGeometryBrowser(t, ctx, srv, client)
	second := newGeometryBrowser(t, ctx, srv, client)
	first.acquire(false)
	old := geometryLeaseFor(t, a)
	entry := old.stream.geometry
	entry.mu.Lock()
	old.expires = time.Now().Add(10 * time.Millisecond)
	entry.armLocked(old)
	entry.mu.Unlock()
	waitGeometry(t, func() bool { return old.revoked.Load() })
	second.acquire(false)
	current := geometryLeaseFor(t, a)
	first.command("terminal.control.renew", nil)
	first.read("error")
	if current.revoked.Load() {
		t.Fatal("old renew revoked current lease")
	}
	_ = second.ws.CloseNow()
	waitGeometry(t, func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.control == nil })
	first.send(map[string]any{"t": "ping"})
	first.read("pong")
	if e.input.Load() != 0 {
		t.Fatal("expiry mutated task")
	}
}
func TestGeometryRuntimeChangeFencesOldStream(t *testing.T) {
	_, e, srv, client, ctx := geometryFixture(t)
	b := newGeometryBrowser(t, ctx, srv, client)
	b.acquire(false)
	e.generation.Add(1)
	b.command("terminal.resize_v2", map[string]any{"resize_seq": 1, "cols": 22, "rows": 10})
	if m := b.read("error"); m["code"] != "control_expired" {
		t.Fatal(m)
	}
	b.read("terminal.closed")
	waitGeometry(t, func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.control == nil })
}

func TestGeometryProbeGenerationChangeCannotAcquireOldEntry(t *testing.T) {
	_, e, srv, client, ctx := geometryFixture(t)
	b := newGeometryBrowser(t, ctx, srv, client)
	e.probeGeneration.Store(true)
	b.command("terminal.control.acquire", map[string]any{"cols": 60, "rows": 20})
	if m := b.read("error"); m["code"] != "control_unavailable" {
		t.Fatal(m)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.control != nil || len(e.processes) != 1 {
		t.Fatalf("stale entry started controller: %d processes", len(e.processes))
	}
}

func TestGeometryQueuedOperationRechecksRuntime(t *testing.T) {
	for _, operation := range []string{"resize", "scroll"} {
		t.Run(operation, func(t *testing.T) {
			a, e, srv, client, ctx := geometryFixture(t)
			b := newGeometryBrowser(t, ctx, srv, client)
			b.acquire(false)
			lease := geometryLeaseFor(t, a)
			entry := lease.stream.geometry
			// Simulate an operation admitted before a reconnect and queued on op.
			entry.op.Lock()
			started := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				close(started)
				if operation == "resize" {
					done <- entry.resize(lease, 1, 40, 12)
				} else {
					done <- lease.session.geometryScroll(lease.stream, 3, 0, 0)
				}
			}()
			<-started
			e.generation.Add(1)
			entry.op.Unlock()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("stale queued operation succeeded")
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			e.mu.Lock()
			count := len(e.commands)
			e.mu.Unlock()
			if count != 0 {
				t.Fatalf("stale operation sent %d commands", count)
			}
		})
	}
}

func TestGeometryObservedBeforeWriteReturnsKeepsStatusOrder(t *testing.T) {
	a, e, srv, client, ctx := geometryFixture(t)
	b := newGeometryBrowser(t, ctx, srv, client)
	b.acquire(false)
	l := geometryLeaseFor(t, a)
	e.mu.Lock()
	e.onWrite = func(m map[string]any) {
		if m["type"] == "terminal.resize" {
			// Upstream output can win the race with completion of stdin.Write.
			l.session.geometryObserved(l.stream, uint16(m["cols"].(float64)), uint16(m["rows"].(float64)))
		}
	}
	e.mu.Unlock()
	b.command("terminal.resize_v2", map[string]any{"resize_seq": 1, "cols": 60, "rows": 20})
	for _, want := range []string{"submitted", "observed"} {
		if got := b.read("terminal.resize.status"); got["status"] != want || got["resize_seq"] != float64(1) {
			t.Fatalf("want %s for sequence 1, got %v", want, got)
		}
	}
}

func TestGeometryPendingAcquireCanBeCanceled(t *testing.T) {
	for _, action := range []string{"release", "wheel-release", "close", "auth"} {
		t.Run(action, func(t *testing.T) {
			a, e, srv, client, ctx := geometryFixture(t)
			b := newGeometryBrowser(t, ctx, srv, client)
			e.mu.Lock()
			e.gate = make(chan struct{}) // A remote client that never reaches ready.
			e.mu.Unlock()
			b.command("terminal.control.acquire", map[string]any{"cols": 60, "rows": 20})
			for {
				m := b.read("terminal.control.state")
				if m["state"] == "pending" {
					b.gen = m["control_generation"].(string)
					break
				}
			}
			l := geometryLeaseFor(t, a)
			waitGeometry(t, func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.control != nil })
			switch action {
			case "release", "wheel-release":
				if action == "wheel-release" {
					b.send(map[string]any{"t": "call", "id": "wheel-pending", "method": "terminal.scroll", "params": map[string]any{"stream_id": b.stream, "lines": 3, "column": 0, "row": 0}})
				}
				b.command("terminal.control.release", nil)
			case "close":
				b.command("terminal.close", nil)
			case "auth":
				l.session.access.cancel(errLoginEnded)
			}
			// This deadline is below the 8-second attach timeout: cancellation must
			// interrupt a pending attach, not merely wait for it to fail by itself.
			waitGeometry(t, func() bool {
				e.mu.Lock()
				closed := e.control == nil
				e.mu.Unlock()
				return closed && l.revoked.Load() && !l.stream.acquirePending.Load()
			})
			if e.input.Load() != 0 {
				t.Fatal("canceling pending access sent task input")
			}
			if action != "auth" {
				wheelRejected := false
				b.send(map[string]any{"t": "ping"})
				for {
					kind, data, err := b.ws.Read(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if kind != websocket.MessageText {
						continue
					}
					var m map[string]any
					_ = json.Unmarshal(data, &m)
					if m["t"] == "terminal.control.acquired" {
						t.Fatalf("canceled attach reported acquired: %s", data)
					}
					if m["t"] == "error" && m["id"] == "wheel-pending" && m["code"] == "scroll_failed" {
						wheelRejected = true
					}
					if m["t"] == "pong" {
						break
					}
				}
				if action == "wheel-release" && !wheelRejected {
					t.Fatal("pending wheel was not rejected before hidden release")
				}
			}
		})
	}
}

func TestGeometryLateObserverFollowsStableController(t *testing.T) {
	_, endpoint, srv, client, ctx := geometryFixture(t)
	owner := newGeometryBrowser(t, ctx, srv, client)
	owner.acquire(false)
	newGeometryBrowser(t, ctx, srv, client)
	endpoint.mu.Lock()
	observer := endpoint.processes[len(endpoint.processes)-1]
	endpoint.mu.Unlock()
	// The controller emits only its initial frame. Joining afterwards must catch
	// up without waiting for task output or reopening either stream.
	waitGeometry(t, func() bool { return observer.viewport.Load() == uint32(50)<<16|20 })
}
