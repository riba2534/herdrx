package voicegateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// FrameType is the relay's own frame classification. It exists so the relay
// never has to import a WebSocket library to be unit-tested with fakes.
type FrameType int

const (
	// FrameText carries JSON control frames.
	FrameText FrameType = iota
	// FrameBinary carries raw PCM16LE mono audio.
	FrameBinary
)

// ErrConnClosed reports a read on a connection that has ended.
var ErrConnClosed = errors.New("voice connection closed")

// Conn is one end of the relay: either the browser or the upstream gateway.
type Conn interface {
	Read(ctx context.Context) (FrameType, []byte, error)
	Write(ctx context.Context, typ FrameType, payload []byte) error
	// Close sends a close handshake. It must be safe to call more than once.
	Close(code int, reason string) error
	// SetReadLimit caps one inbound message. Zero means the transport default.
	SetReadLimit(limit int64)
}

// Dialer opens the upstream leg. It is an interface so tests can run the whole
// relay against an in-process fake without a network or a credential.
type Dialer interface {
	Dial(ctx context.Context, target string, header http.Header) (Conn, error)
}

// WSDialer is the production dialer: a plain upstream WebSocket with a
// server-owned Authorization header.
type WSDialer struct {
	// DialTimeout bounds the upstream handshake only. The session lifetime is
	// governed by the relay context, not by this.
	DialTimeout time.Duration
}

// Dial implements Dialer.
func (d WSDialer) Dial(ctx context.Context, target string, header http.Header) (Conn, error) {
	timeout := d.DialTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, target, &websocket.DialOptions{
		HTTPHeader: header,
		// The upstream is a gateway fronted by its own proxy; compression is
		// disabled to keep the relay byte-for-byte and predictable.
		CompressionMode: websocket.CompressionDisabled,
		HTTPClient:      &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}},
	})
	if err != nil {
		return nil, err
	}
	return &wsConn{conn: conn}, nil
}

// wsConn adapts *websocket.Conn to Conn.
type wsConn struct {
	conn *websocket.Conn
}

func (c *wsConn) Read(ctx context.Context) (FrameType, []byte, error) {
	typ, payload, err := c.conn.Read(ctx)
	if err != nil {
		return FrameText, nil, err
	}
	if typ == websocket.MessageBinary {
		return FrameBinary, payload, nil
	}
	return FrameText, payload, nil
}

func (c *wsConn) Write(ctx context.Context, typ FrameType, payload []byte) error {
	messageType := websocket.MessageText
	if typ == FrameBinary {
		messageType = websocket.MessageBinary
	}
	// Writes must not inherit a canceled access lease directly: the WebSocket
	// library tears the transport down when a write context ends, which would
	// truncate an already-admitted frame. Bound the write instead.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.conn.Write(writeCtx, messageType, payload)
}

func (c *wsConn) Close(code int, reason string) error {
	if code < 1000 || code > 4999 {
		code = int(websocket.StatusNormalClosure)
	}
	timer := time.AfterFunc(500*time.Millisecond, func() { _ = c.conn.CloseNow() })
	defer timer.Stop()
	return c.conn.Close(websocket.StatusCode(code), reason)
}

func (c *wsConn) SetReadLimit(limit int64) {
	if limit > 0 {
		c.conn.SetReadLimit(limit)
	}
}
