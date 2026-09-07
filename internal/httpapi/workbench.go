package httpapi

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return w.write(ctx, websocket.MessageText, encoded)
}

func (w *socketWriter) Binary(ctx context.Context, encoded []byte) error {
	return w.write(ctx, websocket.MessageBinary, encoded)
}

func (w *socketWriter) write(ctx context.Context, typ websocket.MessageType, encoded []byte) error {
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
	Cols              uint16          `json:"cols,omitempty"`
	Rows              uint16          `json:"rows,omitempty"`
	StreamID          uint32          `json:"stream_id,omitempty"`
	Method            string          `json:"method,omitempty"`
	Params            json.RawMessage `json:"params,omitempty"`
}

type workbenchSession struct {
	api       *API
	hostID    string
	owner     string
	browserID string
	endpoint  herdr.Endpoint
	writer    *socketWriter
	ctx       context.Context
	access    *accessLease
	streamsMu sync.Mutex
	streams   map[uint32]*terminalStream
	nextID    atomic.Uint32
}

type terminalStream struct {
	id       uint32
	paneID   string
	mode     string
	process  herdr.TerminalProcess
	scroll   *herdr.ScrollController
	input    *herdr.InputQueue
	stdin    io.WriteCloser
	lastAck  atomic.Uint64
	closed   atomic.Bool
	closing  atomic.Bool
	creditMu sync.Mutex
	inFlight int
	pending  []pendingCredit
	creditCh chan struct{}
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
		ctx: ctx, access: lease, streams: make(map[uint32]*terminalStream),
	}
	defer session.closeStreams()
	if err := socketWriter.JSON(ctx, map[string]any{
		"t": "server_info", "protocol": 1, "version": Version, "host_id": host.ID,
		"features": map[string]any{"terminal_binary": 1, "terminal_ack": 1, "multi_writer": 1},
	}); err != nil {
		return
	}
	if err := session.sendSnapshot(); err != nil {
		if ctx.Err() == nil {
			session.invalidateEndpoint()
		}
		_ = socketWriter.JSON(ctx, map[string]any{"t": "error", "code": "snapshot_failed", "message": err.Error()})
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
	return s.writer.JSON(s.ctx, map[string]any{"t": "snapshot", "snapshot": snapshot, "at": time.Now().UTC()})
}

func (s *workbenchSession) invalidateEndpoint() {
	if endpoint, ok := s.endpoint.(interface{ Invalidate() }); ok {
		endpoint.Invalidate()
	}
}

func (s *workbenchSession) pollSnapshots() {
	failures := 0
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
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
		}
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
			if err := stream.input.Enqueue(frame.Payload); err != nil && s.ctx.Err() == nil {
				s.failInput(stream, err)
			}
		}
	case terminalwire.OpcodeResize:
		cols, rows, err := terminalwire.Dimensions(frame.Payload)
		if err != nil || cols < 10 || cols > 1000 || rows < 3 || rows > 500 {
			return
		}
		s.writeTerminalCommand(stream, map[string]any{"type": "terminal.resize", "cols": cols, "rows": rows})
	case terminalwire.OpcodeRelease:
		s.finishStream(stream.id)
	}
}

func (s *workbenchSession) openTerminal(message workbenchMessage) {
	if message.Mode != "observe" && message.Mode != "control" {
		s.writeRequestError(message.RequestID, "invalid_terminal_mode", "only Web pane terminals are supported")
		return
	}
	if message.Cols < 10 || message.Rows < 3 {
		s.writeRequestError(message.RequestID, "invalid_terminal_size", "terminal is too small")
		return
	}
	streamID := s.nextID.Add(1)
	stream := &terminalStream{id: streamID, paneID: message.PaneID, mode: message.Mode, creditCh: make(chan struct{}, 1)}
	process, err := s.endpoint.OpenTerminal(s.ctx, herdr.TerminalOpen{PaneID: message.PaneID, Mode: "observe", Takeover: false, Cols: message.Cols, Rows: message.Rows})
	if err != nil {
		s.writeRequestError(message.RequestID, "terminal_open_failed", err.Error())
		return
	}
	stream.process = process
	stream.scroll = herdr.NewScrollController(s.ctx, s.endpoint, message.PaneID)
	stream.input = herdr.NewInputQueue(s.ctx, s.endpoint, message.PaneID, func(err error) { s.failInput(stream, err) })
	stream.stdin = process.Stdin()
	s.api.store.Audit(s.ctx, s.owner, "terminal.connected", "pane", stream.paneID, "", `{"mode":"`+stream.mode+`"}`)
	s.streamsMu.Lock()
	s.streams[streamID] = stream
	s.streamsMu.Unlock()
	_ = s.writer.JSON(s.ctx, map[string]any{"t": "terminal.opened", "id": message.RequestID, "stream_id": streamID, "stream_epoch": time.Now().UnixNano(), "pane_id": message.PaneID, "mode": message.Mode})
	go s.forwardTerminal(stream)
}

func (s *workbenchSession) forwardTerminal(stream *terminalStream) {
	scanner := bufio.NewScanner(stream.process.Stdout())
	scanner.Buffer(make([]byte, 64*1024), 16<<20)
	closedReason := ""
	for scanner.Scan() {
		var source struct {
			Type   string `json:"type"`
			Seq    uint64 `json:"seq"`
			Width  uint16 `json:"width"`
			Height uint16 `json:"height"`
			Full   bool   `json:"full"`
			Bytes  string `json:"bytes"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &source); err != nil {
			continue
		}
		if source.Type == "terminal.closed" {
			closedReason = source.Reason
			break
		}
		ansi, err := base64.StdEncoding.DecodeString(source.Bytes)
		if err != nil || len(ansi) > 8<<20 {
			continue
		}
		flags := byte(0)
		if source.Full {
			flags |= terminalwire.FlagFull
		}
		frame := terminalwire.Encode(terminalwire.Frame{Opcode: terminalwire.OpcodeFrame, Flags: flags, StreamID: stream.id, Seq: source.Seq, Payload: terminalwire.TerminalPayload(source.Width, source.Height, ansi)})
		if !stream.reserveCredit(s.ctx, source.Seq, len(frame)) {
			closedReason = "browser is not consuming terminal output"
			break
		}
		if err := s.writer.Binary(s.ctx, frame); err != nil {
			break
		}
	}
	// A stalled observer must be detached before waiting for its process. Waiting
	// first can deadlock on a full output pipe and retain an unused remote client.
	if stream.input != nil {
		stream.input.Close()
	}
	_ = stream.process.Close()
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

func (s *workbenchSession) writeTerminalCommand(stream *terminalStream, command any) {
	encoded, err := json.Marshal(command)
	if err != nil {
		return
	}
	encoded = append(encoded, '\n')
	_, _ = stream.stdin.Write(encoded)
}

func (s *workbenchSession) call(message workbenchMessage) {
	if message.Method == "terminal.scroll" {
		var params struct {
			StreamID uint32 `json:"stream_id"`
			Lines    int    `json:"lines"`
			Column   int    `json:"column"`
			Row      int    `json:"row"`
		}
		if json.Unmarshal(message.Params, &params) != nil {
			s.writeRequestError(message.RequestID, "invalid_scroll", "invalid terminal scroll")
			return
		}
		s.streamsMu.Lock()
		stream := s.streams[params.StreamID]
		s.streamsMu.Unlock()
		if stream == nil || stream.closed.Load() {
			s.writeRequestError(message.RequestID, "terminal_unavailable", "终端已断开，请重新连接")
			return
		}
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		defer cancel()
		if err := stream.scroll.Send(ctx, params.Lines, params.Column, params.Row); err != nil {
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
		"pane.send_text": {}, "pane.send_keys": {}, "pane.read": {}, "agent.prompt": {}, "agent.wait": {}, "agent.send_keys": {},
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
