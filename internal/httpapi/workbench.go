package httpapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/terminalwire"
)

const maxWSMessage = 8 << 20

type socketWriter struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (w *socketWriter) JSON(ctx context.Context, value any) error {
	return w.jsonBeforeWrite(ctx, value, nil)
}

func (w *socketWriter) jsonBeforeWrite(ctx context.Context, value any, beforeWrite func()) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return w.writeBefore(ctx, websocket.MessageText, encoded, beforeWrite)
}

func (w *socketWriter) Binary(ctx context.Context, encoded []byte) error {
	return w.write(ctx, websocket.MessageBinary, encoded)
}

func (w *socketWriter) write(ctx context.Context, typ websocket.MessageType, encoded []byte) error {
	return w.writeBefore(ctx, typ, encoded, nil)
}

func (w *socketWriter) writeBefore(ctx context.Context, typ websocket.MessageType, encoded []byte, beforeWrite func()) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Revocation must reject new output without canceling an admitted frame:
	// the WebSocket library closes the transport when a write context ends.
	// The dedicated closer owns access shutdown; this separate timeout also
	// bounds a stalled write while access remains valid.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if beforeWrite != nil {
		beforeWrite()
	}
	return w.conn.Write(writeCtx, typ, encoded)
}

type workbenchMessage struct {
	Type              string          `json:"t"`
	RequestID         string          `json:"id,omitempty"`
	Protocol          int             `json:"protocol,omitempty"`
	DeviceID          string          `json:"device_id,omitempty"`
	BrowserInstanceID string          `json:"browser_instance_id,omitempty"`
	PaneID            string          `json:"pane_id,omitempty"`
	Mode              string          `json:"mode,omitempty"`
	Takeover          bool            `json:"takeover,omitempty"`
	Responsive        bool            `json:"responsive,omitempty"`
	ResizeRemote      bool            `json:"resize_remote,omitempty"`
	Cols              uint16          `json:"cols,omitempty"`
	Rows              uint16          `json:"rows,omitempty"`
	StreamID          uint32          `json:"stream_id,omitempty"`
	Method            string          `json:"method,omitempty"`
	Params            json.RawMessage `json:"params,omitempty"`
	Capabilities      map[string]int  `json:"capabilities,omitempty"`
	StreamEpoch       string          `json:"stream_epoch,omitempty"`
	ControlGeneration string          `json:"control_generation,omitempty"`
	ResizeSeq         uint64          `json:"resize_seq,omitempty"`
	Transfer          bool            `json:"transfer,omitempty"`
	Lines             int             `json:"lines,omitempty"`
}

type workbenchSession struct {
	api        *API
	hostID     string
	owner      string
	browserID  string
	endpoint   herdr.Endpoint
	writer     *socketWriter
	ctx        context.Context
	access     *accessLease
	streamsMu  sync.Mutex
	streams    map[uint32]*terminalStream
	nextID     atomic.Uint32
	controlV2  bool
	snapshotMu sync.Mutex
	snapshot   herdr.Snapshot
	events     *snapshotEvents

	screenMu     sync.Mutex
	screens      map[string]*screenWatch
	screenGen    uint64
	screenClosed bool
}

type terminalStream struct {
	id               uint32
	paneID           string
	mode             string
	process          herdr.TerminalProcess
	scroll           *herdr.ScrollController
	input            *herdr.InputQueue
	lastAck          atomic.Uint64
	closed           atomic.Bool
	closing          atomic.Bool
	acquirePending   atomic.Bool
	creditMu         sync.Mutex
	inFlight         int
	pending          []pendingCredit
	creditCh         chan struct{}
	epoch            string
	terminalID       string
	geometry         *geometryEntry
	viewportRequests chan terminalSize
	viewportCancel   context.CancelFunc
}

type pendingCredit struct {
	seq  uint64
	size int
}

func (a *API) workbench(writer http.ResponseWriter, request *http.Request) {
	host, err := a.ownedHost(request)
	if err != nil {
		writeError(writer, http.StatusNotFound, "host_not_found", "host not found")
		return
	}
	// requireOrigin already checked the full scheme/host/port against configured
	// origins. The library's Host-only default cannot handle our explicit aliases.
	connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled, InsecureSkipVerify: true})
	if err != nil {
		return
	}
	connection.SetReadLimit(maxWSMessage)
	defer closeAccessWebSocket(connection, request.Context())
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	lease := accessFromContext(request.Context())
	// Keep the protocol reader alive long enough to deliver a useful close code.
	// Remote calls and output still use the immediately canceled access context.
	readCtx, readCancel := context.WithCancel(context.WithoutCancel(request.Context()))
	defer readCancel()
	go func() {
		select {
		case <-ctx.Done():
			closeAccessWebSocket(connection, ctx)
		case <-readCtx.Done():
		}
	}()
	socketWriter := &socketWriter{conn: connection}

	helloCtx, helloCancel := context.WithTimeout(readCtx, 15*time.Second)
	messageType, encoded, err := connection.Read(helloCtx)
	helloCancel()
	if err != nil || messageType != websocket.MessageText {
		_ = connection.Close(websocket.StatusCode(4001), "hello required")
		return
	}
	var hello workbenchMessage
	if json.Unmarshal(encoded, &hello) != nil || hello.Type != "hello" || hello.Protocol != 1 || len(hello.BrowserInstanceID) < 16 {
		_ = connection.Close(websocket.StatusCode(4002), "invalid hello")
		return
	}
	if lease == nil || lease.check() != nil {
		return
	}
	if err := socketWriter.JSON(ctx, map[string]any{"t": "conn", "state": "connecting"}); err != nil {
		return
	}
	endpoint, err := a.hosts.Open(ctx, host)
	if err != nil {
		detail := hostruntime.DescribeError(err)
		_ = socketWriter.JSON(ctx, map[string]any{"t": "error", "code": detail.Code, "message": detail.Error(), "retryable": !detail.Permanent, "retry_after_ms": detail.RetryAfter.Milliseconds()})
		code := websocket.StatusCode(4404)
		if detail.Permanent {
			code = 4405
		}
		_ = connection.Close(code, "host unavailable")
		return
	}
	defer endpoint.Close()
	session := &workbenchSession{
		api: a, hostID: host.ID, owner: userFromContext(request.Context()).ID,
		browserID: hello.BrowserInstanceID, endpoint: endpoint, writer: socketWriter,
		ctx: ctx, access: lease, streams: make(map[uint32]*terminalStream), events: newSnapshotEvents(),
		controlV2: hello.Capabilities["terminal_control"] == 1 && hello.Capabilities["terminal_resize_v2"] == 1,
	}
	defer session.closeStreams()
	defer session.closeScreens()
	if err := socketWriter.JSON(ctx, map[string]any{
		"t": "server_info", "protocol": 1, "version": Version, "host_id": host.ID,
		"features": map[string]any{"terminal_binary": 1, "terminal_ack": 1, "multi_writer": 1, "terminal_control": 1, "terminal_resize_v2": 1},
	}); err != nil {
		return
	}
	if err := session.sendSnapshot(); err != nil {
		if ctx.Err() == nil {
			session.invalidateEndpoint()
		}
		// The code stays snapshot_failed: the page offers to start Herdr for it.
		_ = socketWriter.JSON(ctx, map[string]any{"t": "error", "code": "snapshot_failed", "message": snapshotFailureMessage(err)})
		return
	}
	_ = socketWriter.JSON(ctx, map[string]any{"t": "conn", "state": "ready", "path": host.Transport})
	go session.pollSnapshots()
	for {
		messageType, encoded, err := connection.Read(readCtx)
		if err != nil {
			return
		}
		if lease.check() != nil {
			return
		}
		switch messageType {
		case websocket.MessageText:
			var message workbenchMessage
			if err := json.Unmarshal(encoded, &message); err != nil {
				_ = socketWriter.JSON(ctx, map[string]any{"t": "error", "code": "invalid_message", "message": "invalid JSON message"})
				continue
			}
			session.handleText(message)
		case websocket.MessageBinary:
			session.handleBinary(encoded)
		}
	}
}

// snapshotFailureMessage explains a failed first snapshot with the next step.
func snapshotFailureMessage(err error) string {
	if detail := hostruntime.DescribeError(err); detail.Code == "herdr_not_running" {
		return detail.Error()
	}
	return "读取 Herdr 工作区失败，请确认远程主机上的 Herdr 正常运行后点“立即重连”：" + err.Error()
}

func closeAccessWebSocket(conn *websocket.Conn, ctx context.Context) {
	code, reason := websocket.StatusNormalClosure, "closed"
	switch context.Cause(ctx) {
	case errLoginEnded:
		code, reason = websocket.StatusCode(4401), "login expired"
	case errAccessUnavailable:
		code, reason = websocket.StatusTryAgainLater, "authorization unavailable"
	case errWorkbenchStopped:
		code, reason = websocket.StatusServiceRestart, "workbench restarting"
	}
	closeWebSocket(conn, code, reason)
}

func closeWebSocket(conn *websocket.Conn, code websocket.StatusCode, reason string) {
	timer := time.AfterFunc(500*time.Millisecond, func() { _ = conn.CloseNow() })
	defer timer.Stop()
	_ = conn.Close(code, reason)
}

func (s *workbenchSession) sendSnapshot() error {
	if err := s.access.check(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	snapshot, err := s.endpoint.Snapshot(ctx)
	if err != nil {
		return err
	}
	s.updateGeometrySnapshot(snapshot)
	s.events.observe(snapshot)
	s.refreshViewports()
	return s.writer.JSON(s.ctx, map[string]any{"t": "snapshot", "snapshot": snapshot, "at": time.Now().UTC()})
}

func (s *workbenchSession) invalidateEndpoint() {
	if endpoint, ok := s.endpoint.(interface{ Invalidate() }); ok {
		endpoint.Invalidate()
	}
}

// Snapshot refresh: Herdr pushes layout and agent changes over events.subscribe,
// so a snapshot follows each burst of events. Polling remains the fallback while
// no stream is live (unsupported Herdr, transport without streams, reconnects),
// and a slow poll still catches anything a live stream missed.
var (
	snapshotPollInterval     = 2 * time.Second
	snapshotFallbackInterval = 10 * time.Second
	snapshotEventCoalesce    = 50 * time.Millisecond
	snapshotEventMaxBackoff  = time.Minute
)

// snapshotLifecycleEvents change which workspaces, tabs and panes exist, their
// names, focus or layout. Agent status is only published per pane.
var snapshotLifecycleEvents = []string{
	"workspace.created", "workspace.updated", "workspace.renamed", "workspace.closed", "workspace.moved", "workspace.focused",
	"tab.created", "tab.renamed", "tab.closed", "tab.moved", "tab.focused",
	"pane.created", "pane.closed", "pane.exited", "pane.updated", "pane.moved", "pane.focused", "pane.agent_detected",
	"layout.updated",
}

func snapshotSubscriptions(panes []string) []herdr.EventSubscription {
	subscriptions := make([]herdr.EventSubscription, 0, len(snapshotLifecycleEvents)+len(panes))
	for _, kind := range snapshotLifecycleEvents {
		subscriptions = append(subscriptions, herdr.EventSubscription{"type": kind})
	}
	for _, pane := range panes {
		subscriptions = append(subscriptions, herdr.EventSubscription{"type": "pane.agent_status_changed", "pane_id": pane})
	}
	return subscriptions
}

// snapshotAgentPanes lists the panes whose agent status needs a subscription.
func snapshotAgentPanes(snapshot herdr.Snapshot) []string {
	panes := make([]string, 0, len(snapshot.Agents))
	for _, agent := range snapshot.Agents {
		if agent.PaneID != "" {
			panes = append(panes, agent.PaneID)
		}
	}
	slices.Sort(panes)
	return slices.Compact(panes)
}

// errResubscribe ends a subscription whose agent panes changed.
var errResubscribe = errors.New("agent panes changed")

type snapshotEvents struct {
	live atomic.Bool   // a subscription is streaming
	kick chan struct{} // something changed: take a snapshot (buffered 1)

	mu    sync.Mutex
	panes []string                // the agent panes of the current subscription
	resub context.CancelCauseFunc // ends the current subscription for a new pane set
}

func newSnapshotEvents() *snapshotEvents {
	return &snapshotEvents{kick: make(chan struct{}, 1)}
}

func (e *snapshotEvents) poke() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// observe restarts the subscription when a snapshot shows agents in panes it
// does not cover. It only cancels a context; no network call holds the lock.
func (e *snapshotEvents) observe(snapshot herdr.Snapshot) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.resub == nil {
		return
	}
	if panes := snapshotAgentPanes(snapshot); !slices.Equal(panes, e.panes) {
		e.panes = panes
		e.resub(errResubscribe)
	}
}

// watchEvents keeps one subscription open while the workbench is connected and
// reopens it with exponential backoff after Herdr closes it.
func (s *workbenchSession) watchEvents(endpoint herdr.EventEndpoint) {
	e := s.events
	backoff := time.Second
	for s.ctx.Err() == nil {
		s.snapshotMu.Lock()
		panes := snapshotAgentPanes(s.snapshot)
		s.snapshotMu.Unlock()
		ctx, cancel := context.WithCancelCause(s.ctx)
		e.mu.Lock()
		e.panes, e.resub = panes, cancel
		e.mu.Unlock()
		started := time.Now()
		err := endpoint.SubscribeEvents(ctx, snapshotSubscriptions(panes), func(herdr.Event) {
			// The first event is subscription_started: a snapshot covers the gap before it.
			e.live.Store(true)
			e.poke()
		})
		e.mu.Lock()
		e.resub = nil
		e.mu.Unlock()
		resubscribe := context.Cause(ctx) == errResubscribe
		cancel(nil)
		if e.live.Swap(false) {
			// Polling resumes at full speed; refresh now for anything missed.
			e.poke()
		}
		if s.ctx.Err() != nil || errors.Is(err, herdr.ErrEventsUnsupported) {
			return
		}
		if resubscribe {
			continue
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		var apiError *herdr.APIError
		if errors.As(err, &apiError) && apiError.Code != "events_lost" {
			// This Herdr rejects the request (older version): stay on polling.
			backoff = snapshotEventMaxBackoff
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, snapshotEventMaxBackoff)
	}
}

func (s *workbenchSession) pollSnapshots() {
	if endpoint, ok := s.endpoint.(herdr.EventEndpoint); ok {
		go s.watchEvents(endpoint)
	}
	failures := 0
	timer := time.NewTimer(snapshotPollInterval)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.events.kick:
			// Merge a burst (a split emits several events) into one snapshot.
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(snapshotEventCoalesce):
			}
			select {
			case <-s.events.kick:
			default:
			}
		case <-timer.C:
		}
		if err := s.sendSnapshot(); err != nil {
			if s.ctx.Err() != nil {
				return
			}
			failures++
			_ = s.writer.JSON(s.ctx, map[string]any{"t": "conn", "state": "degraded", "message": err.Error()})
			if failures >= 3 {
				s.invalidateEndpoint()
				closeWebSocket(s.writer.conn, websocket.StatusTryAgainLater, "remote host did not respond")
				return
			}
		} else {
			failures = 0
		}
		interval := snapshotPollInterval
		if s.events.live.Load() {
			interval = snapshotFallbackInterval
		}
		timer.Reset(interval)
	}
}

func (s *workbenchSession) handleText(message workbenchMessage) {
	switch message.Type {
	case "ping":
		_ = s.writer.JSON(s.ctx, map[string]any{"t": "pong", "at": time.Now().UnixMilli()})
	case "presence":
		// Presence is retained for connection liveness; pane input is intentionally multi-writer.
	case "terminal.open":
		s.openTerminal(message)
	case "terminal.close":
		s.finishStream(message.StreamID)
	case "screen.watch":
		s.watchScreen(message)
	case "screen.unwatch":
		s.unwatchScreen(message.PaneID)
	case "terminal.control.acquire":
		// Keep reading release/close while remote attach waits for its first frame.
		// Only one acquisition per stream may wait, bounding asynchronous work.
		s.streamsMu.Lock()
		stream := s.streams[message.StreamID]
		s.streamsMu.Unlock()
		if stream == nil {
			s.handleGeometry(message, nil)
			return
		}
		finish, admitted := stream.beginGeometryAcquire()
		if !admitted {
			s.writeGeometryError(message, "control_pending", "尺寸控制正在连接，请等待完成或先释放控制")
			return
		}
		go func() {
			defer finish()
			s.handleGeometry(message, finish)
		}()
	case "terminal.control.release", "terminal.control.renew", "terminal.resize_v2":
		s.handleGeometry(message, nil)
	case "call":
		s.call(message)
	default:
		_ = s.writer.JSON(s.ctx, map[string]any{"t": "error", "id": message.RequestID, "code": "unknown_message", "message": "unknown message type"})
	}
}

func (s *workbenchSession) handleBinary(encoded []byte) {
	frame, err := terminalwire.Decode(encoded)
	if err != nil {
		_ = s.writer.JSON(s.ctx, map[string]any{"t": "error", "code": "invalid_terminal_frame", "message": err.Error()})
		return
	}
	s.streamsMu.Lock()
	stream := s.streams[frame.StreamID]
	s.streamsMu.Unlock()
	if stream == nil || stream.closed.Load() {
		return
	}
	if frame.Opcode == terminalwire.OpcodeAck {
		stream.lastAck.Store(frame.Seq)
		stream.acknowledge(frame.Seq)
		return
	}
	switch frame.Opcode {
	case terminalwire.OpcodeInput:
		if stream.closing.Load() {
			return
		}
		if stream.input != nil {
			if err := s.checkTerminalFeature("input", false); err != nil {
				s.failInput(stream, err)
				return
			}
			if err := stream.input.Enqueue(frame.Payload); err != nil && s.ctx.Err() == nil {
				s.failInput(stream, err)
			} else {
				s.kickScreen(stream.paneID)
			}
		}
	case terminalwire.OpcodeResize:
		// Version 1 carries neither a stream epoch nor a control generation.
		// It must never mutate a shared PTY, even after a v2 controller exists.
	case terminalwire.OpcodeRelease:
		s.finishStream(stream.id)
	}
}

func (s *workbenchSession) openTerminal(message workbenchMessage) {
	if message.Mode != "observe" && message.Mode != "control" {
		s.writeRequestError(message.RequestID, "invalid_terminal_mode", "only Web pane terminals are supported")
		return
	}
	if message.Cols < 10 || message.Cols > 1000 || message.Rows < 3 || message.Rows > 500 {
		s.writeRequestError(message.RequestID, "invalid_terminal_size", "terminal dimensions are out of range")
		return
	}
	if message.Responsive && message.ResizeRemote {
		s.writeRequestError(message.RequestID, "client_update_required", "尺寸控制协议已更新，请刷新页面后选择“使用此窗口尺寸”")
		return
	}
	s.snapshotMu.Lock()
	protocol := s.snapshot.Protocol
	s.snapshotMu.Unlock()
	if protocol != 20 && protocol != 22 {
		s.writeRequestError(message.RequestID, "herdr_incompatible", "工作台尚未适配此 Herdr 终端协议，请检查主机能力详情")
		return
	}
	if err := s.checkTerminalFeature("observe", s.controlV2); err != nil {
		s.writeRequestError(message.RequestID, "herdr_incompatible", err.Error())
		return
	}
	streamID := s.nextID.Add(1)
	// Old browser bundles restore responsive automatically. They must reconnect
	// as observers after an update; only a current-visit choice opts into resize.
	mode := "observe"
	stream := &terminalStream{id: streamID, paneID: message.PaneID, mode: mode, epoch: rand.Text(), creditCh: make(chan struct{}, 1)}
	// Direct control changes the shared PTY, including what native Herdr renders.
	// takeover=false only protects another direct controller, not a native TUI.
	process, err := s.openObserver(stream, protocol, message.Cols, message.Rows)
	if err != nil {
		s.writeRequestError(message.RequestID, "terminal_open_failed", err.Error())
		return
	}
	stream.process = process
	s.startViewportSync(stream)
	stream.scroll = herdr.NewScrollController(s.ctx, s.endpoint, message.PaneID)
	stream.input = herdr.NewInputQueue(s.ctx, s.endpoint, message.PaneID, func(err error) { s.failInput(stream, err) })
	s.api.store.Audit(s.ctx, s.owner, "terminal.connected", "pane", stream.paneID, "", `{"mode":"`+stream.mode+`"}`)
	s.registerGeometry(stream)
	s.streamsMu.Lock()
	s.streams[streamID] = stream
	s.streamsMu.Unlock()
	_ = s.writer.JSON(s.ctx, map[string]any{"t": "terminal.opened", "id": message.RequestID, "stream_id": streamID, "stream_epoch": stream.epoch, "terminal_id": stream.terminalID, "pane_id": message.PaneID, "mode": mode})
	if stream.geometry != nil {
		stream.geometry.publish()
	}
	go s.forwardTerminal(stream)
}

// terminalOutputGap merges output frames that arrive within this gap of the
// last frame sent. Herdr emits up to ~60 frames a second for a busy pane (a
// build log, a full-screen redraw); the first frame after a quiet spell still
// goes out at once, so typing echoes are never held back. Input is not gated.
var terminalOutputGap = 30 * time.Millisecond

const terminalMergedLimit = 8 << 20

// terminalOutput is one Herdr output frame, or several merged into one: the
// diff bytes are VT output applied in order, so they run on, and a full frame
// replaces everything before it. The merged frame carries the last sequence.
type terminalOutput struct {
	seq           uint64
	width, height uint16
	full          bool
	ansi          []byte
}

func (o *terminalOutput) merge(next terminalOutput) {
	if next.full {
		*o = next
		return
	}
	o.ansi = append(o.ansi, next.ansi...)
	o.seq, o.width, o.height = next.seq, next.width, next.height
}

// terminalSource is one parsed line of Herdr's observer stream: an output
// frame, or the reason the stream closed.
type terminalSource struct {
	output terminalOutput
	closed bool
	reason string
}

// readTerminalSource parses observer lines and hands them over one at a time,
// so a send waiting for browser credit stops reading and keeps backpressure on
// the observer.
func readTerminalSource(scanner *bufio.Scanner, sources chan<- terminalSource, stop <-chan struct{}) {
	defer close(sources)
	installedFull := false
	for scanner.Scan() {
		var line struct {
			Type   string `json:"type"`
			Seq    uint64 `json:"seq"`
			Width  uint16 `json:"width"`
			Height uint16 `json:"height"`
			Full   bool   `json:"full"`
			Bytes  []byte `json:"bytes"` // base64, decoded by encoding/json
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		var source terminalSource
		switch {
		case line.Type == "terminal.closed":
			source = terminalSource{closed: true, reason: line.Reason}
		case line.Type != "terminal.frame" || line.Width == 0 || line.Height == 0 || (!installedFull && !line.Full) || len(line.Bytes) > 8<<20:
			continue
		default:
			installedFull = true
			source.output = terminalOutput{seq: line.Seq, width: line.Width, height: line.Height, full: line.Full, ansi: line.Bytes}
		}
		select {
		case sources <- source:
		case <-stop:
			return
		}
		if source.closed {
			return
		}
	}
}

func (s *workbenchSession) forwardTerminal(stream *terminalStream) {
	scanner := bufio.NewScanner(stream.process.Stdout())
	scanner.Buffer(make([]byte, 64*1024), 16<<20)
	sources := make(chan terminalSource)
	stopReading := make(chan struct{})
	go readTerminalSource(scanner, sources, stopReading)
	closedReason := ""
	var last time.Time
	var pending *terminalOutput
	gate := time.NewTimer(time.Hour)
	gate.Stop()
	defer gate.Stop()
	send := func(output terminalOutput) bool {
		flags := byte(0)
		if output.full {
			flags |= terminalwire.FlagFull
		}
		frame := terminalwire.Encode(terminalwire.Frame{Opcode: terminalwire.OpcodeFrame, Flags: flags, StreamID: stream.id, Seq: output.seq, Payload: terminalwire.TerminalPayload(output.width, output.height, output.ansi)})
		// Credit is reserved for the merged frame's size under its last sequence.
		if !stream.reserveCredit(s.ctx, output.seq, len(frame)) {
			closedReason = "browser is not consuming terminal output"
			return false
		}
		last = time.Now()
		return s.writer.Binary(s.ctx, frame) == nil
	}
	flush := func() bool {
		gate.Stop()
		if pending == nil {
			return true
		}
		merged := *pending
		pending = nil
		return send(merged)
	}
read:
	for {
		var gateC <-chan time.Time
		if pending != nil {
			gateC = gate.C
		}
		select {
		case source, ok := <-sources:
			if !ok || source.closed {
				closedReason = source.reason
				// Output held by the gate still belongs before the close notice.
				flush()
				break read
			}
			s.geometryObserved(stream, source.output.width, source.output.height)
			switch {
			case pending != nil:
				pending.merge(source.output)
				if len(pending.ansi) >= terminalMergedLimit && !flush() {
					break read
				}
			case time.Since(last) < terminalOutputGap:
				pending = &source.output
				gate.Reset(terminalOutputGap - time.Since(last))
			case !send(source.output):
				break read
			}
		case <-gateC:
			if !flush() {
				break read
			}
		}
	}
	close(stopReading)
	// A stalled observer must be detached before waiting for its process. Waiting
	// first can deadlock on a full output pipe and retain an unused remote client.
	if stream.input != nil {
		stream.input.Close()
	}
	_ = stream.process.Close()
	for range sources {
	}
	if err := scanner.Err(); err != nil && closedReason == "" {
		closedReason = err.Error()
	}
	if err := stream.process.Wait(); err != nil && !errors.Is(err, context.Canceled) && closedReason == "" {
		closedReason = err.Error()
	}
	if s.ctx.Err() == nil && stream.closed.CompareAndSwap(false, true) {
		if closedReason == "" {
			closedReason = "终端观察进程已退出，请重新连接"
		}
		_ = s.writer.JSON(s.ctx, map[string]any{"t": "terminal.closed", "stream_id": stream.id, "reason": closedReason})
	}
	s.removeStream(stream.id)
}

func (stream *terminalStream) reserveCredit(ctx context.Context, seq uint64, size int) bool {
	const maxInFlight = 2 << 20
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		stream.creditMu.Lock()
		if stream.inFlight+size <= maxInFlight || stream.inFlight == 0 {
			stream.inFlight += size
			stream.pending = append(stream.pending, pendingCredit{seq: seq, size: size})
			stream.creditMu.Unlock()
			return true
		}
		stream.creditMu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-stream.creditCh:
		}
	}
}

func (stream *terminalStream) acknowledge(seq uint64) {
	stream.creditMu.Lock()
	index := 0
	for index < len(stream.pending) && stream.pending[index].seq <= seq {
		stream.inFlight -= stream.pending[index].size
		index++
	}
	if index > 0 {
		stream.pending = append([]pendingCredit(nil), stream.pending[index:]...)
	}
	stream.creditMu.Unlock()
	select {
	case stream.creditCh <- struct{}{}:
	default:
	}
}

func (s *workbenchSession) call(message workbenchMessage) {
	if message.Method == "terminal.scroll" {
		var params struct {
			StreamID uint32 `json:"stream_id"`
			Lines    int    `json:"lines"`
			Column   int    `json:"column"`
			Row      int    `json:"row"`
		}
		if json.Unmarshal(message.Params, &params) != nil || params.Lines == 0 || params.Lines < -100 || params.Lines > 100 || params.Column < 0 || params.Row < 0 {
			s.writeRequestError(message.RequestID, "invalid_scroll", "invalid terminal scroll")
			return
		}
		s.streamsMu.Lock()
		stream := s.streams[params.StreamID]
		s.streamsMu.Unlock()
		if stream == nil || stream.closed.Load() || stream.closing.Load() {
			s.writeRequestError(message.RequestID, "terminal_unavailable", "终端已断开，请重新连接")
			return
		}
		err := s.geometryScroll(stream, params.Lines, params.Column, params.Row)
		if err != nil {
			s.writeRequestError(message.RequestID, "scroll_failed", "终端滚动失败，请稍后重试："+err.Error())
			return
		}
		_ = s.writer.JSON(s.ctx, map[string]any{"t": "res", "id": message.RequestID, "result": map[string]bool{"ok": true}})
		return
	}
	if !allowedMethod(message.Method) {
		s.writeRequestError(message.RequestID, "method_forbidden", "method is not exposed by herdrx")
		return
	}
	var params any = map[string]any{}
	if len(message.Params) > 0 {
		if err := json.Unmarshal(message.Params, &params); err != nil {
			s.writeRequestError(message.RequestID, "invalid_params", "params must be valid JSON")
			return
		}
	}
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	result, err := s.endpoint.Call(ctx, message.Method, params)
	if paneID := screenInputPane(message.Method, params); paneID != "" {
		// Even a failed or uncertain write may have reached the PTY; reread now.
		s.kickScreen(paneID)
	}
	if err != nil {
		s.writeRequestError(message.RequestID, "herdr_error", err.Error())
		return
	}
	_ = s.writer.JSON(s.ctx, map[string]any{"t": "res", "id": message.RequestID, "result": json.RawMessage(result)})
	_ = s.sendSnapshot()
}

func allowedMethod(method string) bool {
	_, ok := map[string]struct{}{
		"pane.split": {}, "pane.close": {}, "pane.zoom": {}, "pane.focus_direction": {}, "pane.swap": {}, "pane.resize": {}, "pane.rename": {}, "pane.input.set": {},
		"layout.set_split_ratio": {}, "tab.create": {}, "tab.close": {}, "tab.rename": {}, "workspace.create": {}, "workspace.close": {}, "workspace.rename": {},
		"worktree.list": {}, "worktree.create": {}, "worktree.open": {}, "worktree.remove": {},
		"pane.send_text": {}, "pane.send_keys": {}, "pane.send_input": {}, "pane.read": {}, "agent.prompt": {}, "agent.wait": {}, "agent.send_keys": {},
	}[method]
	return ok
}

func (s *workbenchSession) writeRequestError(requestID, code, message string) {
	_ = s.writer.JSON(s.ctx, map[string]any{"t": "error", "id": requestID, "code": code, "message": message})
}

func (s *workbenchSession) finishStream(id uint32) {
	s.streamsMu.Lock()
	stream := s.streams[id]
	s.streamsMu.Unlock()
	if stream == nil || !stream.closing.CompareAndSwap(false, true) {
		return
	}
	if stream.geometry != nil {
		stream.geometry.mu.Lock()
		l := stream.geometry.lease
		stream.geometry.mu.Unlock()
		if l != nil && l.session == s && l.stream == stream {
			stream.geometry.release(l, "已释放此窗口的尺寸控制")
		}
	}
	if stream.input == nil {
		s.closeStream(id)
		return
	}
	done := stream.input.Finish()
	go func() {
		select {
		case <-done:
		case <-s.ctx.Done():
		}
		s.closeStream(id)
	}()
}

func (s *workbenchSession) closeStream(id uint32) bool {
	s.streamsMu.Lock()
	stream := s.streams[id]
	s.streamsMu.Unlock()
	if stream == nil || !stream.closed.CompareAndSwap(false, true) {
		return false
	}
	if stream.input != nil {
		stream.input.Close()
	}
	_ = stream.process.Close()
	s.removeStream(id)
	return true
}

func (s *workbenchSession) failInput(stream *terminalStream, err error) {
	if !s.closeStream(stream.id) {
		return
	}
	_ = s.writer.JSON(s.ctx, map[string]any{"t": "terminal.closed", "stream_id": stream.id, "reason": "输入未能确认送达，已停止后续输入。请检查终端内容并手动重连：" + err.Error()})
}

func (s *workbenchSession) removeStream(id uint32) {
	s.streamsMu.Lock()
	stream := s.streams[id]
	delete(s.streams, id)
	s.streamsMu.Unlock()
	if stream != nil {
		if stream.viewportCancel != nil {
			stream.viewportCancel()
		}
		s.unregisterGeometry(stream)
		stream.closed.Store(true)
		if stream.input != nil {
			stream.input.Close()
		}
		if stream.scroll != nil {
			stream.scroll.Close()
		}
		s.api.store.Audit(context.Background(), s.owner, "terminal.disconnected", "pane", stream.paneID, "", `{"mode":"`+stream.mode+`"}`)
	}
}

func (s *workbenchSession) closeStreams() {
	s.streamsMu.Lock()
	ids := make([]uint32, 0, len(s.streams))
	for id := range s.streams {
		ids = append(ids, id)
	}
	s.streamsMu.Unlock()
	for _, id := range ids {
		s.closeStream(id)
	}
}
