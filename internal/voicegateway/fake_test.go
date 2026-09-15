package voicegateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"
)

// quietLogger keeps deliberately-triggered failure paths from spamming the
// test output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeFrame is one message on a fake connection.
type fakeFrame struct {
	typ     FrameType
	payload []byte
}

// fakeConn is an in-memory Conn. It replaces both the browser and the upstream
// gateway so the whole relay can be exercised without a network, a credential
// or a realtime model.
type fakeConn struct {
	inbox  chan fakeFrame
	outbox chan fakeFrame
	closed chan struct{}

	mu        sync.Mutex
	closeCode int
	closeText string
	readLimit int64
	writeErr  error
	closeOnce sync.Once

	// recorded mirrors everything written, so a test can observe the relay's
	// output without stealing frames from the scripted upstream consumer.
	recorded []fakeFrame
	cursor   int
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		inbox:  make(chan fakeFrame, 64),
		outbox: make(chan fakeFrame, 64),
		closed: make(chan struct{}),
	}
}

func (c *fakeConn) Read(ctx context.Context) (FrameType, []byte, error) {
	select {
	case <-ctx.Done():
		return FrameText, nil, ctx.Err()
	case <-c.closed:
		return FrameText, nil, ErrConnClosed
	case frame := <-c.inbox:
		return frame.typ, frame.payload, nil
	}
}

func (c *fakeConn) Write(ctx context.Context, typ FrameType, payload []byte) error {
	c.mu.Lock()
	err := c.writeErr
	if err == nil {
		c.recorded = append(c.recorded, fakeFrame{typ: typ, payload: payload})
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return ErrConnClosed
	case c.outbox <- fakeFrame{typ: typ, payload: payload}:
		return nil
	}
}

func (c *fakeConn) Close(code int, reason string) error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closeCode = code
		c.closeText = reason
		c.mu.Unlock()
		close(c.closed)
	})
	return nil
}

func (c *fakeConn) SetReadLimit(limit int64) {
	c.mu.Lock()
	c.readLimit = limit
	c.mu.Unlock()
}

// push injects a frame the relay will read.
func (c *fakeConn) push(typ FrameType, payload []byte) {
	c.inbox <- fakeFrame{typ: typ, payload: payload}
}

// next returns the next frame the relay wrote, or fails the test on timeout.
func (c *fakeConn) next(t *testing.T) fakeFrame {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		if c.cursor < len(c.recorded) {
			frame := c.recorded[c.cursor]
			c.cursor++
			c.mu.Unlock()
			return frame
		}
		c.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for a relayed frame")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// nextJSON decodes the next relayed text frame.
func (c *fakeConn) nextJSON(t *testing.T) map[string]any {
	t.Helper()
	frame := c.next(t)
	if frame.typ != FrameText {
		t.Fatalf("expected a text frame, got binary of %d bytes", len(frame.payload))
	}
	var decoded map[string]any
	if err := json.Unmarshal(frame.payload, &decoded); err != nil {
		t.Fatalf("relayed frame is not JSON: %v (%s)", err, frame.payload)
	}
	return decoded
}

func (c *fakeConn) awaitClose(t *testing.T) (int, string) {
	t.Helper()
	select {
	case <-c.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the connection to close")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCode, c.closeText
}

// fakeDialer hands out a prepared upstream connection and records how it was
// asked to dial.
type fakeDialer struct {
	conn   Conn
	err    error
	target string
	header http.Header
}

func (d *fakeDialer) Dial(_ context.Context, target string, header http.Header) (Conn, error) {
	d.target = target
	d.header = header
	if d.err != nil {
		return nil, d.err
	}
	return d.conn, nil
}

// scriptedUpstream answers the relay's control frames the way an
// OpenAI-Realtime-compatible gateway does, so the handshake and the two
// transcription channels are exercised end to end.
type scriptedUpstream struct {
	conn     *fakeConn
	model    string
	partials []string
	finals   []string
	reject   bool // answer session.update with an error instead of session.updated
	once     sync.Once
}

func newScriptedUpstream() *scriptedUpstream {
	return &scriptedUpstream{conn: newFakeConn(), model: "gpt-realtime"}
}

// serve consumes what the relay wrote and answers. It stops when the
// connection closes.
func (u *scriptedUpstream) serve(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-u.conn.closed:
			return
		case frame := <-u.conn.outbox:
			u.answer(frame)
		}
	}
}

func (u *scriptedUpstream) answer(frame fakeFrame) {
	if frame.typ != FrameText {
		return
	}
	var envelope struct {
		Type string `json:"type"`
		Audio string `json:"audio"`
	}
	if json.Unmarshal(frame.payload, &envelope) != nil {
		return
	}
	switch envelope.Type {
	case "session.update":
		if u.reject {
			u.conn.push(FrameText, []byte(`{"type":"error","error":{"type":"invalid_request_error","code":"invalid_request_error","message":"secret-account-detail-42"}}`))
			return
		}
		u.conn.push(FrameText, []byte(`{"type":"session.created","session":{"model":"`+u.model+`"}}`))
		u.conn.push(FrameText, []byte(`{"type":"session.updated"}`))
	case "input_audio_buffer.append":
		// A real gateway would emit these as the VAD runs; the fake emits one
		// delta and one completion per appended frame.
		u.conn.push(FrameText, []byte(`{"type":"conversation.item.input_audio_transcription.delta","delta":"hello"}`))
		u.conn.push(FrameText, []byte(`{"type":"conversation.item.input_audio_transcription.completed","transcript":"hello world"}`))
	}
}

func testConfig(t *testing.T) Config {
	t.Helper()
	cfg := Config{
		Enabled:               true,
		BaseURL:               "https://voice.example.com",
		APIKey:                "test-key-not-a-real-credential",
		Model:                 "gpt-realtime",
		Path:                  "/v1/realtime",
		InputHz:               24000,
		OutputHz:              24000,
		AudioReply:            AudioReplyOff,
		MaxSessionsPerUser:    1,
		MaxSessions:           4,
		MaxSessionSeconds:     600,
		IdleSeconds:           30,
		MaxUplinkAudioSeconds: 120,
		MaxUplinkBytes:        8 << 20,
		MaxControlBytes:       4096,
		MaxAudioFrameBytes:    64 << 10,
		CreatePerMinute:       10,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config must validate: %v", err)
	}
	return cfg
}

// openSession wires a gateway, a scripted upstream and a browser fake together.
func openSession(t *testing.T, cfg Config, upstream *scriptedUpstream) (*Gateway, *Session, *fakeConn) {
	t.Helper()
	dialer := &fakeDialer{conn: upstream.conn}
	gateway, err := NewWithOptions(Options{Config: cfg, Dialer: dialer, Logger: quietLogger()})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	t.Cleanup(gateway.Close)

	ticket, err := gateway.IssueTicket("user-1", "login-1")
	if err != nil {
		t.Fatalf("issue ticket: %v", err)
	}
	session, err := gateway.Open(context.Background(), "user-1", "login-1", ticket.Value)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	browser := newFakeConn()
	return gateway, session, browser
}
