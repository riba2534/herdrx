package voicegateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// CloseClassifier lets the HTTP layer map its own context causes (login
// expiry, access revocation, instance shutdown) onto a WebSocket close code and
// a browser `closed` reason. Returning handled=false falls back to the relay's
// own vocabulary.
type CloseClassifier func(cause error) (code int, reason string, handled bool)

// Relay end reasons. They are the only values that ever appear in the browser
// `{"t":"closed","reason":…}` frame.
const (
	ReasonUser      = "user"
	ReasonTimeout   = "timeout"
	ReasonIdle      = "idle"
	ReasonAuth      = "auth"
	ReasonServer    = "server"
	ReasonUpstream  = "upstream"
	ReasonLimit     = "limit"
	ReasonProtocol  = "protocol"
	ReasonTransport = "transport"
)

var (
	errRelayStopped  = errors.New("voice relay stopped")
	errClientStop    = errors.New("voice client stopped")
	errSessionLimit  = errors.New("voice session time limit reached")
	errIdleLimit     = errors.New("voice session idle")
	errUplinkLimit   = errors.New("voice uplink limit reached")
	errProtocolError = errors.New("voice protocol violation")
)

// Session is one live duplex relay between a browser and the upstream gateway.
// It owns the upstream leg and the accounting; the browser leg is supplied by
// the HTTP layer at Relay time.
type Session struct {
	gateway         *Gateway
	userID          string
	loggedInSession string
	output          string
	upstream        Conn
	state           relayState

	mu           sync.Mutex
	browser      Conn
	started      bool
	uplinkBytes  int64
	uplinkAudio  int64
	lastActivity time.Time
	ended        bool

	cancel     context.CancelCauseFunc
	released   sync.Once
	classifier CloseClassifier
}

// SetClassifier installs the HTTP layer's close-code mapping. It must be called
// before Relay.
func (s *Session) SetClassifier(classifier CloseClassifier) {
	s.mu.Lock()
	s.classifier = classifier
	s.mu.Unlock()
}

// closeReport is the resolved close outcome for the browser leg.
type closeReport struct {
	code   int
	text   string
	reason string
}

// Relay runs the full duplex relay until either side ends. It always closes
// both connections before returning, and it never replays a frame: a voice
// session has no retransmission semantics.
func (s *Session) Relay(ctx context.Context, browser Conn) error {
	s.mu.Lock()
	s.browser = browser
	s.lastActivity = s.gateway.now()
	s.mu.Unlock()

	isBrowserLimit := int64(s.gateway.cfg.MaxControlBytes)
	if frameLimit := int64(s.gateway.cfg.MaxAudioFrameBytes); frameLimit > isBrowserLimit {
		isBrowserLimit = frameLimit
	}
	browser.SetReadLimit(isBrowserLimit + 1024)

	relayCtx, cancel := context.WithCancelCause(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	writer := &browserWriter{conn: browser}
	watchdogs := []*time.Timer{
		time.AfterFunc(time.Duration(s.gateway.cfg.MaxSessionSeconds)*time.Second, func() { cancel(errSessionLimit) }),
	}
	stopIdle := make(chan struct{})
	go s.watchIdle(relayCtx, stopIdle)

	results := make(chan error, 2)
	go func() { results <- s.pumpBrowser(relayCtx, browser, writer) }()
	go func() { results <- s.pumpUpstream(relayCtx, writer) }()

	first := <-results
	cancel(first)
	close(stopIdle)

	report := s.report(first)
	// The relay context is already canceled; the farewell frame still has to
	// leave, so it gets its own short budget.
	closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(relayCtx), 2*time.Second)
	_ = writer.write(closeCtx, FrameText, closedFrame(report.reason))
	closeCancel()

	// Unblock the surviving pump, then wait for it. Closing the transports is
	// what makes the second result arrive; neither pump owns the other.
	_ = browser.Close(report.code, report.reason)
	_ = s.closeUpstream(report.code, report.reason)
	<-results

	for _, timer := range watchdogs {
		timer.Stop()
	}
	s.release()

	if errors.Is(first, errClientStop) || errors.Is(first, errSessionLimit) || errors.Is(first, errIdleLimit) {
		return nil
	}
	return first
}

// Close ends the relay from the server side. It is idempotent and safe to call
// from Gateway.Close while Relay is running.
func (s *Session) shutdown() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel(errRelayStopped)
		return
	}
	// Relay never started (or already finished): close the upstream directly so
	// the dial cannot be leaked.
	_ = s.closeUpstream(1012, "voice gateway stopping")
	s.release()
}

func (s *Session) release() {
	s.released.Do(func() {
		if s.upstream != nil {
			_ = s.upstream.Close(1000, "voice session ended")
		}
		s.gateway.forget(s)
		s.gateway.release(s.userID)
	})
}

func (s *Session) closeUpstream(code int, reason string) error {
	if s.upstream == nil {
		return nil
	}
	return s.upstream.Close(code, reason)
}

func (s *Session) touch() {
	s.mu.Lock()
	s.lastActivity = s.gateway.now()
	s.mu.Unlock()
}

func (s *Session) watchIdle(ctx context.Context, stop <-chan struct{}) {
	idle := time.Duration(s.gateway.cfg.IdleSeconds) * time.Second
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-timer.C:
			s.mu.Lock()
			stale := s.gateway.now().Sub(s.lastActivity)
			cancel := s.cancel
			s.mu.Unlock()
			if stale >= idle {
				if cancel != nil {
					cancel(errIdleLimit)
				}
				return
			}
			timer.Reset(idle - stale)
		}
	}
}

// pumpBrowser forwards browser frames upstream. It enforces the ordered
// handshake (`start` must be first), every per-frame size cap and the running
// uplink budget.
func (s *Session) pumpBrowser(ctx context.Context, browser Conn, writer *browserWriter) error {
	cfg := s.gateway.cfg
	for {
		typ, payload, err := browser.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			return fmt.Errorf("%w: %v", errBrowserTransport, err)
		}
		s.touch()
		s.mu.Lock()
		started := s.started
		s.mu.Unlock()

		switch typ {
		case FrameBinary:
			if !started {
				return s.protocolError(writer, ctx, "voice_start_required", "请先建立语音会话再发送音频")
			}
			if len(payload) == 0 {
				continue
			}
			if len(payload) > cfg.MaxAudioFrameBytes {
				return s.limitError(writer, ctx, "voice_audio_frame_too_large", "单帧语音数据超出上限，请降低采样率或缩短分片")
			}
			if err := s.account(int64(len(payload)), true); err != nil {
				return s.limitError(writer, ctx, "voice_uplink_exhausted", "本次语音会话的上行额度已用完，请重新开始")
			}
			if err := s.upstream.Write(ctx, FrameText, AppendAudio(payload)); err != nil {
				return err
			}

		case FrameText:
			if len(payload) > cfg.MaxControlBytes {
				return s.limitError(writer, ctx, "voice_control_too_large", "语音控制帧超出上限")
			}
			control, err := ParseClientControl(payload)
			if err != nil {
				return s.protocolError(writer, ctx, "voice_invalid_frame", "语音控制帧格式不正确")
			}
			if err := s.account(int64(len(payload)), false); err != nil {
				return s.limitError(writer, ctx, "voice_uplink_exhausted", "本次语音会话的上行额度已用完，请重新开始")
			}
			switch control.T {
			case clientStart:
				if started {
					continue
				}
				output := control.Output
				if output != "audio" {
					output = "text"
				}
				if output == "audio" && cfg.AudioReply != AudioReplyAllowed {
					return s.protocolError(writer, ctx, "voice_audio_disabled", "本实例未开启语音回答，请改用文字回复模式")
				}
				s.mu.Lock()
				s.started = true
				s.output = output
				s.state.output = output
				s.mu.Unlock()
				if err := s.upstream.Write(ctx, FrameText, SessionUpdate(cfg, output)); err != nil {
					return err
				}
			case clientCommit:
				if !started {
					return s.protocolError(writer, ctx, "voice_start_required", "请先建立语音会话再提交音频")
				}
				if err := s.upstream.Write(ctx, FrameText, commitRequest()); err != nil {
					return err
				}
			case clientCancel:
				if !started {
					return s.protocolError(writer, ctx, "voice_start_required", "请先建立语音会话再打断")
				}
				if err := s.upstream.Write(ctx, FrameText, cancelRequest()); err != nil {
					return err
				}
			case clientStop:
				return errClientStop
			default:
				return s.protocolError(writer, ctx, "voice_unknown_control", "不支持的语音控制指令")
			}
		}
	}
}

// pumpUpstream normalizes upstream frames into the frozen browser vocabulary.
// Unknown upstream frames are counted and dropped, never reflected.
func (s *Session) pumpUpstream(ctx context.Context, writer *browserWriter) error {
	for {
		typ, payload, err := s.upstream.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			return fmt.Errorf("%w: %v", errUpstreamTransport, err)
		}
		s.touch()
		frames, known := s.state.MapUpstream(typ, payload)
		if !known {
			continue
		}
		for _, frame := range frames {
			if err := writer.write(ctx, frame.Typ, frame.Payload); err != nil {
				return err
			}
		}
	}
}

func (s *Session) account(size int64, audio bool) error {
	cfg := s.gateway.cfg
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.uplinkBytes+size > cfg.MaxUplinkBytes {
		return errUplinkLimit
	}
	if audio {
		// PCM16LE mono: 2 bytes per sample.
		seconds := float64(s.uplinkAudio+size) / float64(2*cfg.InputHz)
		if seconds > float64(cfg.MaxUplinkAudioSeconds) {
			return errUplinkLimit
		}
		s.uplinkAudio += size
	}
	s.uplinkBytes += size
	return nil
}

func (s *Session) protocolError(writer *browserWriter, ctx context.Context, code, message string) error {
	_ = writer.write(ctx, FrameText, mustJSON(map[string]any{"t": "error", "code": code, "message": message}))
	return errProtocolError
}

func (s *Session) limitError(writer *browserWriter, ctx context.Context, code, message string) error {
	_ = writer.write(ctx, FrameText, mustJSON(map[string]any{"t": "error", "code": code, "message": message}))
	return errUplinkLimit
}

func (s *Session) report(cause error) closeReport {
	s.mu.Lock()
	classifier := s.classifier
	s.mu.Unlock()
	if classifier != nil {
		if code, reason, handled := classifier(cause); handled {
			return closeReport{code: code, text: reason, reason: reason}
		}
	}
	switch {
	case errors.Is(cause, errClientStop):
		return closeReport{code: 1000, text: "stopped", reason: ReasonUser}
	case errors.Is(cause, errSessionLimit):
		return closeReport{code: 1000, text: "time limit", reason: ReasonTimeout}
	case errors.Is(cause, errIdleLimit):
		return closeReport{code: 1000, text: "idle", reason: ReasonIdle}
	case errors.Is(cause, errUplinkLimit):
		return closeReport{code: 1000, text: "uplink limit", reason: ReasonLimit}
	case errors.Is(cause, errProtocolError):
		return closeReport{code: 1000, text: "protocol", reason: ReasonProtocol}
	case errors.Is(cause, errRelayStopped):
		return closeReport{code: 1012, text: "gateway stopping", reason: ReasonServer}
	case errors.Is(cause, errUpstreamTransport):
		return closeReport{code: 1011, text: "upstream lost", reason: ReasonUpstream}
	case errors.Is(cause, context.DeadlineExceeded):
		return closeReport{code: 1000, text: "time limit", reason: ReasonTimeout}
	case cause == nil, errors.Is(cause, errBrowserTransport), errors.Is(cause, context.Canceled):
		// The browser went away (refresh, navigation, tab close) without saying
		// stop. No retransmission: the session simply ends.
		return closeReport{code: 1000, text: "closed", reason: ReasonTransport}
	}
	return closeReport{code: 1000, text: "access ended", reason: ReasonAuth}
}

// errUpstreamTransport and errBrowserTransport mark which leg failed, so the
// close code tells the browser whether retrying could help.
var (
	errUpstreamTransport = errors.New("voice upstream transport ended")
	errBrowserTransport  = errors.New("voice browser transport ended")
)

// browserWriter serializes writes to the browser. Only the upstream pump and
// the relay's own control frames write, but they can overlap.
type browserWriter struct {
	mu   sync.Mutex
	conn Conn
}

func (w *browserWriter) write(ctx context.Context, typ FrameType, payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return w.conn.Write(ctx, typ, payload)
}

func closedFrame(reason string) []byte {
	return mustJSON(map[string]any{"t": "closed", "reason": reason})
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte(`{"t":"closed","reason":"server"}`)
	}
	return encoded
}
