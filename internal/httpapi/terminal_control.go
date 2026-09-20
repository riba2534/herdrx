package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/riba2534/herdrx/internal/herdr"
)

const geometryLeaseTTL = 20 * time.Second

// Geometry belongs to a live terminal, never to a browser supplied identity.
// The registry mutex only protects membership. Each terminal serializes its
// transport operations separately; no network operation holds the registry lock.
type geometryKey struct{ owner, host, runtime, terminal string }
type geometryRegistry struct {
	mu      sync.Mutex
	entries map[geometryKey]*geometryEntry
}
type geometryEntry struct {
	op               sync.Mutex
	mu               sync.Mutex
	notifyMu         sync.Mutex
	refs             int // registry.mu
	key              geometryKey
	watchers         map[*terminalStream]*workbenchSession
	lease            *geometryLease
	scroll           *herdr.ScrollController // op
	reason           string
	probing          bool
	lastProbe        time.Time
	viewportRevision uint64
	controllerSize   terminalSize
}
type geometryLease struct {
	session    *workbenchSession
	stream     *terminalStream
	generation string
	ctx        context.Context
	cancel     context.CancelFunc
	revoked    atomic.Bool
	expires    time.Time           // entry.mu
	timer      *time.Timer         // entry.mu
	confirmed  bool                // entry.mu
	controller *geometryController // op
	seq        uint64              // entry.mu
	cols, rows uint16              // entry.mu
	observed   bool                // entry.mu
	submitted  bool                // entry.mu
}
type geometryController struct {
	process  herdr.TerminalProcess
	ready    chan error
	done     chan struct{}
	stopOnce sync.Once
}

func (s *workbenchSession) runtimeGeneration() string {
	if endpoint, ok := s.endpoint.(interface{ RuntimeGeneration() string }); ok {
		return endpoint.RuntimeGeneration()
	}
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	return fmt.Sprintf("%s/%d", s.snapshot.Version, s.snapshot.Protocol)
}

func (s *workbenchSession) registerGeometry(stream *terminalStream) {
	s.snapshotMu.Lock()
	for _, pane := range s.snapshot.Panes {
		if pane.ID == stream.paneID {
			stream.terminalID = pane.TerminalID
			break
		}
	}
	s.snapshotMu.Unlock()
	if stream.terminalID == "" {
		return
	} // Observation remains available on older snapshots.
	key := geometryKey{s.owner, s.hostID, s.runtimeGeneration(), stream.terminalID}
	r := &s.api.geometry
	r.mu.Lock()
	if r.entries == nil {
		r.entries = make(map[geometryKey]*geometryEntry)
	}
	e := r.entries[key]
	if e == nil {
		e = &geometryEntry{key: key, watchers: make(map[*terminalStream]*workbenchSession)}
		r.entries[key] = e
	}
	e.refs++
	r.mu.Unlock()
	e.mu.Lock()
	e.watchers[stream] = s
	if e.lease != nil && !e.lease.revoked.Load() && e.controllerSize.cols != 0 {
		stream.queueViewport(e.controllerSize)
	}
	e.mu.Unlock()
	stream.geometry = e
}

func (s *workbenchSession) unregisterGeometry(stream *terminalStream) {
	e := stream.geometry
	if e == nil {
		return
	}
	e.mu.Lock()
	l := e.lease
	if l == nil || l.session != s || l.stream != stream {
		l = nil
	}
	delete(e.watchers, stream)
	e.mu.Unlock()
	if l != nil {
		e.release(l, "已释放此窗口的尺寸控制")
	}
	r := &s.api.geometry
	// Prevent a new entry for the same terminal until the final short controller
	// is gone. No network work is done under r.mu; the temporary reference keeps
	// the old entry discoverable while its last observer is detached.
	e.op.Lock()
	r.mu.Lock()
	last := e.refs == 1
	r.mu.Unlock()
	if last && e.scroll != nil {
		e.scroll.Close()
		e.scroll = nil
	}
	r.mu.Lock()
	e.refs--
	if e.refs == 0 {
		delete(r.entries, e.key)
	}
	r.mu.Unlock()
	e.op.Unlock()
}

func (s *workbenchSession) updateGeometrySnapshot(snapshot herdr.Snapshot) {
	s.snapshotMu.Lock()
	s.snapshot = snapshot
	s.snapshotMu.Unlock()
	runtime := s.runtimeGeneration()
	terminals := make(map[string]string, len(snapshot.Panes))
	for _, pane := range snapshot.Panes {
		terminals[pane.ID] = pane.TerminalID
	}
	s.streamsMu.Lock()
	var stale []*terminalStream
	for _, stream := range s.streams {
		if e := stream.geometry; e != nil && (e.key.runtime != runtime || terminals[stream.paneID] != stream.terminalID) {
			stale = append(stale, stream)
		}
	}
	s.streamsMu.Unlock()
	for _, stream := range stale {
		// A changed runtime must reconnect with a new full frame and capability set.
		if s.closeStream(stream.id) {
			_ = s.writer.JSON(s.ctx, map[string]any{"t": "terminal.closed", "stream_id": stream.id, "reason": "Herdr 会话或版本已变化，请重新连接终端"})
		}
	}
}

func (s *workbenchSession) authorized() error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.access != nil {
		return s.access.check()
	}
	return nil
}

func (s *workbenchSession) checkTerminalFeature(name string, probe bool) error {
	if cached, ok := s.endpoint.(interface {
		CachedHerdrCapabilities() (herdr.CapabilityReport, bool)
	}); ok {
		if report, found := cached.CachedHerdrCapabilities(); found {
			if feature := report.Features[name]; feature.State == herdr.CapabilityUnavailable {
				return errors.New(feature.Reason)
			}
			return nil
		}
	}
	if !probe {
		return nil
	}
	if provider, ok := s.endpoint.(herdr.CapabilityProvider); ok {
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		defer cancel()
		report, err := provider.HerdrCapabilities(ctx)
		if err != nil {
			return fmt.Errorf("无法确认 Herdr 连接能力，请稍后重试：%w", err)
		}
		if feature := report.Features[name]; feature.State == herdr.CapabilityUnavailable {
			return errors.New(feature.Reason)
		}
	}
	return nil
}

func (s *workbenchSession) writeGeometryError(m workbenchMessage, code, reason string) {
	s.writeGeometryErrorBefore(m, code, reason, nil)
}

func (s *workbenchSession) writeGeometryErrorBefore(m workbenchMessage, code, reason string, beforeWrite func()) {
	_ = s.writer.jsonBeforeWrite(s.ctx, map[string]any{"t": "error", "id": m.RequestID, "code": code, "message": reason, "stream_id": m.StreamID, "stream_epoch": m.StreamEpoch, "control_generation": m.ControlGeneration}, beforeWrite)
}

func (stream *terminalStream) beginGeometryAcquire() (finish func(), admitted bool) {
	if !stream.acquirePending.CompareAndSwap(false, true) {
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(func() { stream.acquirePending.Store(false) }) }, true
}

func (s *workbenchSession) handleGeometry(m workbenchMessage, finishAcquire func()) {
	// Clear this attempt before its final response becomes visible. Keep it
	// pending until the writer lock is held, so a slow socket cannot accumulate
	// completed acquisitions waiting to write. The caller's once-only fallback
	// also handles canceled writes without clearing a subsequent attempt.
	writeError := func(code, reason string) {
		s.writeGeometryErrorBefore(m, code, reason, finishAcquire)
	}
	if !s.controlV2 {
		writeError("client_update_required", "请刷新页面后使用尺寸控制")
		return
	}
	s.streamsMu.Lock()
	stream := s.streams[m.StreamID]
	s.streamsMu.Unlock()
	if stream == nil || stream.closed.Load() || stream.closing.Load() || stream.epoch != m.StreamEpoch || stream.geometry == nil {
		writeError("terminal_unavailable", "终端连接已变化，请重连后再选择尺寸控制")
		return
	}
	e := stream.geometry
	if e.key.runtime != s.runtimeGeneration() || s.authorized() != nil {
		writeError("control_expired", "连接权限或 Herdr 会话已变化，请重新连接")
		return
	}
	if m.Type == "terminal.control.acquire" {
		if !validGeometry(m.Cols, m.Rows) {
			writeError("invalid_terminal_size", "终端尺寸超出范围")
			return
		}
		if _, ok := stream.process.(herdr.ViewportObserver); !ok {
			writeError("control_unavailable", "当前接入无法同步观察画布，请检查主机尺寸读取能力或更新受控端 CLI；仍可观察和输入")
			return
		}
		if err := s.checkTerminalFeature("resize", true); err != nil {
			writeError("herdr_incompatible", err.Error())
			return
		}
		l, err := e.acquire(s, stream, m.Cols, m.Rows, m.Transfer)
		if err != nil {
			code := "control_unavailable"
			if errors.Is(err, errGeometryConflict) {
				code = "control_conflict"
			}
			writeError(code, err.Error())
			return
		}
		_ = s.writer.jsonBeforeWrite(s.ctx, map[string]any{"t": "terminal.control.acquired", "id": m.RequestID, "stream_id": stream.id, "stream_epoch": stream.epoch, "control_generation": l.generation, "expires_in_ms": geometryLeaseTTL.Milliseconds()}, finishAcquire)
		e.publish()
		return
	}
	e.mu.Lock()
	l := e.lease
	valid := l != nil && l.session == s && l.stream == stream && l.generation == m.ControlGeneration && !l.revoked.Load() && time.Now().Before(l.expires)
	e.mu.Unlock()
	if !valid {
		writeError("control_expired", "尺寸控制已释放，请重新选择“使用此窗口尺寸”")
		return
	}
	switch m.Type {
	case "terminal.control.release":
		e.release(l, "已保持远端尺寸")
		if m.RequestID != "" {
			_ = s.writer.JSON(s.ctx, map[string]any{"t": "terminal.control.released", "id": m.RequestID})
		}
	case "terminal.control.renew":
		e.mu.Lock()
		if e.lease == l && !l.revoked.Load() {
			l.expires = time.Now().Add(geometryLeaseTTL)
			e.armLocked(l)
		}
		e.mu.Unlock()
		if m.RequestID != "" {
			_ = s.writer.JSON(s.ctx, map[string]any{"t": "terminal.control.renewed", "id": m.RequestID, "expires_in_ms": geometryLeaseTTL.Milliseconds()})
		}
	case "terminal.resize_v2":
		if !validGeometry(m.Cols, m.Rows) || m.ResizeSeq == 0 || m.ResizeSeq > 1<<53-1 {
			writeError("invalid_terminal_size", "无效的尺寸或请求序号")
			return
		}
		if err := e.resize(l, m.ResizeSeq, m.Cols, m.Rows); err != nil {
			writeError("resize_failed", err.Error())
		}
	}
}
func validGeometry(cols, rows uint16) bool {
	return cols >= 10 && cols <= 1000 && rows >= 3 && rows <= 500
}

var errGeometryConflict = errors.New("另一个窗口正在控制尺寸，可保持远端尺寸，或确认转到此窗口控制")

func (e *geometryEntry) acquire(s *workbenchSession, stream *terminalStream, cols, rows uint16, transfer bool) (*geometryLease, error) {
	e.op.Lock()
	defer e.op.Unlock()
	if err := s.authorized(); err != nil {
		return nil, err
	}
	if stream.closed.Load() || stream.closing.Load() {
		return nil, errors.New("终端连接已关闭")
	}
	// A capability probe or another waiter may discover a new daemon while
	// handleGeometry is waiting for this operation lock.
	if e.key.runtime != s.runtimeGeneration() {
		return nil, errors.New("Herdr 会话或版本已变化，请重新连接终端")
	}
	e.mu.Lock()
	old := e.lease
	e.mu.Unlock()
	if old != nil {
		if !transfer && !old.revoked.Load() {
			return nil, errGeometryConflict
		}
		if !e.stopLocked(old, "尺寸控制已转到本站其他窗口") {
			return nil, errors.New("旧尺寸连接尚未结束，请稍后重试")
		}
	}
	if e.scroll != nil {
		e.scroll.Close()
		e.scroll = nil
	}
	if e.key.runtime != s.runtimeGeneration() || stream.closed.Load() || stream.closing.Load() {
		return nil, errors.New("Herdr 连接已变化，请重新连接终端")
	}
	ctx, cancel := context.WithCancel(s.ctx)
	l := &geometryLease{session: s, stream: stream, generation: rand.Text(), ctx: ctx, cancel: cancel, expires: time.Now().Add(geometryLeaseTTL), cols: cols, rows: rows}
	e.mu.Lock()
	e.lease = l
	e.viewportRevision++
	e.controllerSize = terminalSize{}
	e.reason = ""
	e.armLocked(l)
	e.mu.Unlock()
	e.publish()
	// The readiness timeout only ends this access client. It never owns Herdr.
	timer := time.AfterFunc(8*time.Second, cancel)
	process, err := s.endpoint.OpenTerminal(ctx, herdr.TerminalOpen{PaneID: stream.paneID, Mode: "control", Takeover: false, Cols: cols, Rows: rows})
	if err == nil {
		l.controller = newGeometryController(ctx, process, func(cols, rows uint16) { e.controllerViewport(l, cols, rows) })
		select {
		case err = <-l.controller.ready:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	timer.Stop()
	if err == nil {
		err = s.authorized()
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && l.revoked.Load() {
		err = errors.New("尺寸控制申请已取消")
	}
	if err == nil && e.key.runtime != s.runtimeGeneration() {
		err = errors.New("Herdr 会话或版本已变化，请重新连接终端")
	}
	if err != nil {
		e.stopLocked(l, "该终端的尺寸控制未建立；若已有其他控制连接，请先在对应客户端释放")
		e.publish()
		return nil, fmt.Errorf("尺寸控制未建立，请保持远端尺寸或稍后重试：%w", err)
	}
	e.mu.Lock()
	l.confirmed = true
	l.expires = time.Now().Add(geometryLeaseTTL)
	e.armLocked(l)
	e.mu.Unlock()
	go func() { <-l.controller.done; e.release(l, "尺寸控制连接已结束，继续保持远端尺寸") }()
	return l, nil
}

// Caller holds op. Invalidation is immediate; a slow close leaves a fenced
// record so a transfer cannot publish success while the previous attach lives.
func (e *geometryEntry) stopLocked(l *geometryLease, reason string) bool {
	l.revoked.Store(true)
	l.cancel()
	e.mu.Lock()
	if l.timer != nil {
		l.timer.Stop()
	}
	e.mu.Unlock()
	ended := true
	if c := l.controller; c != nil {
		c.stop()
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
			ended = false
		}
	}
	e.mu.Lock()
	if e.lease == l {
		e.viewportRevision++
		e.reason = reason
		if ended {
			e.lease = nil
		} else {
			e.reason = "旧尺寸连接正在释放，请稍后重试"
		}
	}
	e.mu.Unlock()
	return ended
}
func (e *geometryEntry) release(l *geometryLease, reason string) {
	// A stale callback can only revoke its own lease. This also interrupts writes
	// before waiting for the per-terminal operation mutex.
	l.revoked.Store(true)
	l.cancel()
	e.op.Lock()
	e.mu.Lock()
	current := e.lease == l
	e.mu.Unlock()
	if current {
		e.stopLocked(l, reason)
	}
	e.op.Unlock()
	if current {
		e.publish()
	}
}
func (e *geometryEntry) armLocked(l *geometryLease) {
	if l.timer != nil {
		l.timer.Stop()
	}
	expiry := l.expires
	l.timer = time.AfterFunc(time.Until(expiry), func() {
		e.mu.Lock()
		expired := e.lease == l && !time.Now().Before(l.expires)
		if expired {
			l.revoked.Store(true)
		}
		e.mu.Unlock()
		if expired {
			e.release(l, "尺寸控制续期已到期，继续保持远端尺寸")
		}
	})
}

func (e *geometryEntry) resize(l *geometryLease, seq uint64, cols, rows uint16) error {
	e.op.Lock()
	defer e.op.Unlock()
	if e.key.runtime != l.session.runtimeGeneration() || l.stream.closed.Load() || l.stream.closing.Load() {
		e.stopLocked(l, "Herdr 连接已变化，已释放尺寸控制")
		e.publish()
		return errors.New("Herdr 连接已变化，请重新连接终端")
	}
	e.mu.Lock()
	valid := e.lease == l && l.confirmed && !l.revoked.Load() && time.Now().Before(l.expires)
	if !valid {
		e.mu.Unlock()
		return errors.New("尺寸控制已失效")
	}
	if seq <= l.seq {
		e.mu.Unlock()
		return nil
	}
	l.seq = seq
	l.cols = cols
	l.rows = rows
	l.observed = false
	l.submitted = false
	e.mu.Unlock()
	if err := l.session.authorized(); err != nil {
		return err
	}
	if err := l.ctx.Err(); err != nil {
		return err
	}
	err := l.controller.write(l.ctx, map[string]any{"type": "terminal.resize", "cols": cols, "rows": rows})
	if err != nil {
		e.stopLocked(l, "尺寸调整失败，已释放控制")
		e.publish()
		return err
	}
	l.status(seq, "submitted", cols, rows)
	// An observer may deliver the target frame before the controller's write
	// returns. Record it immediately, but publish convergence only after submitted.
	e.mu.Lock()
	l.submitted = true
	observed := l.observed
	e.mu.Unlock()
	if observed {
		l.status(seq, "observed", cols, rows)
	}
	return nil
}
func (l *geometryLease) status(seq uint64, status string, cols, rows uint16) {
	_ = l.session.writer.JSON(l.session.ctx, map[string]any{"t": "terminal.resize.status", "stream_id": l.stream.id, "stream_epoch": l.stream.epoch, "control_generation": l.generation, "resize_seq": seq, "status": status, "cols": cols, "rows": rows})
}
func (s *workbenchSession) geometryObserved(stream *terminalStream, cols, rows uint16) {
	e := stream.geometry
	if e == nil {
		return
	}
	e.mu.Lock()
	l := e.lease
	if l == nil || l.session != s || l.stream != stream || !l.confirmed || l.revoked.Load() || l.seq == 0 || l.observed || cols != l.cols || rows != l.rows {
		e.mu.Unlock()
		return
	}
	l.observed = true
	seq := l.seq
	submitted := l.submitted
	e.mu.Unlock()
	// This is convergence of the latest target, not an upstream per-request ACK.
	if submitted {
		l.status(seq, "observed", cols, rows)
	}
}
func (e *geometryEntry) publish() {
	e.notifyMu.Lock()
	defer e.notifyMu.Unlock()
	e.mu.Lock()
	messages := make(map[*terminalStream]map[string]any, len(e.watchers))
	sessions := make(map[*terminalStream]*workbenchSession, len(e.watchers))
	for stream, s := range e.watchers {
		state := "available"
		m := map[string]any{"t": "terminal.control.state", "pane_id": stream.paneID, "terminal_id": e.key.terminal, "stream_id": stream.id, "stream_epoch": stream.epoch, "reason": e.reason}
		if l := e.lease; l != nil {
			switch {
			case l.revoked.Load():
				state = "blocked"
			case !l.confirmed:
				state = "pending"
			case l.session == s && l.stream == stream:
				state = "owned"
			default:
				state = "other"
			}
			if l.session == s && l.stream == stream {
				m["stream_id"] = stream.id
				m["stream_epoch"] = stream.epoch
				m["control_generation"] = l.generation
			}
		}
		m["state"] = state
		messages[stream] = m
		sessions[stream] = s
	}
	e.mu.Unlock()
	for stream, m := range messages {
		s := sessions[stream]
		_ = s.writer.JSON(s.ctx, m)
	}
}

func (s *workbenchSession) geometryScroll(stream *terminalStream, lines, column, row int) error {
	if err := s.checkTerminalFeature("preserve_scroll", false); err != nil {
		return err
	}
	e := stream.geometry
	if e == nil {
		return stream.scroll.Send(s.ctx, lines, column, row)
	}
	// A wheel request must not block this WebSocket reader behind a pending
	// attach; a following hidden-window release needs to cancel that attach.
	if !e.op.TryLock() {
		return errors.New("尺寸控制正在切换或调整，请稍后重试滚动")
	}
	defer e.op.Unlock()
	if err := s.authorized(); err != nil {
		return err
	}
	if e.key.runtime != s.runtimeGeneration() || stream.closed.Load() || stream.closing.Load() {
		return errors.New("Herdr 连接已变化，请重新连接终端后滚动")
	}
	e.mu.Lock()
	l := e.lease
	e.mu.Unlock()
	if l != nil {
		if l.revoked.Load() || l.ctx.Err() != nil {
			return errors.New("尺寸控制正在释放，请稍后滚动")
		}
		if err := l.session.authorized(); err != nil {
			return err
		}
		direction := "down"
		if lines < 0 {
			direction = "up"
			lines = -lines
		}
		var commands []any
		for lines > 0 {
			step := min(lines, 3)
			commands = append(commands, map[string]any{"type": "terminal.scroll", "direction": direction, "lines": step, "source": "wheel", "column": column, "row": row})
			lines -= step
		}
		return l.controller.write(s.ctx, commands...)
	}
	if e.scroll == nil {
		e.scroll = herdr.NewScrollController(context.Background(), s.endpoint, stream.paneID)
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	return e.scroll.Send(ctx, lines, column, row)
}

func newGeometryController(ctx context.Context, p herdr.TerminalProcess, onFrame ...func(uint16, uint16)) *geometryController {
	c := &geometryController{process: p, ready: make(chan error, 1), done: make(chan struct{})}
	go func() {
		stop := context.AfterFunc(ctx, c.stop)
		defer stop()
		scanner := bufio.NewScanner(p.Stdout())
		scanner.Buffer(make([]byte, 64*1024), 16<<20)
		ready := false
		var err error
		for scanner.Scan() {
			var frame struct {
				Type, Reason, Bytes string
				Width, Height       uint16
				Full                bool
			}
			if json.Unmarshal(scanner.Bytes(), &frame) != nil {
				continue
			}
			if frame.Type == "terminal.closed" {
				err = errors.New(frame.Reason)
				break
			}
			if frame.Type == "terminal.frame" && frame.Width > 0 && frame.Height > 0 && len(onFrame) > 0 {
				onFrame[0](frame.Width, frame.Height)
			}
			if !ready && frame.Type == "terminal.frame" && frame.Full && frame.Width > 0 && frame.Height > 0 {
				if data, decodeErr := base64.StdEncoding.DecodeString(frame.Bytes); decodeErr == nil && len(data) <= 8<<20 {
					ready = true
					c.ready <- nil
				}
			}
		}
		c.stop()
		if scanErr := scanner.Err(); err == nil {
			err = scanErr
		}
		if waitErr := p.Wait(); err == nil {
			err = waitErr
		}
		if !ready {
			if err == nil {
				err = errors.New("Herdr 未确认尺寸控制连接")
			}
			c.ready <- err
		}
		close(c.done)
	}()
	return c
}
func (c *geometryController) stop() { c.stopOnce.Do(func() { _ = c.process.Close() }) }
func (c *geometryController) write(ctx context.Context, commands ...any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var batch bytes.Buffer
	enc := json.NewEncoder(&batch)
	for _, command := range commands {
		if err := enc.Encode(command); err != nil {
			return err
		}
	}
	timer := time.AfterFunc(5*time.Second, c.stop)
	defer timer.Stop()
	n, err := c.process.Stdin().Write(batch.Bytes())
	if err == nil && n != batch.Len() {
		err = io.ErrShortWrite
	}
	return err
}
