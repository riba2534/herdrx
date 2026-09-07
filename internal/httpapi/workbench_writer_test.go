package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Hold an actual frame write after the HTTP upgrade, so revocation happens
// during network I/O rather than before the writer's access check.
type frameWriteGate struct {
	armed   atomic.Bool
	started chan struct{}
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
}

type gatedWriteListener struct {
	net.Listener
	gate *frameWriteGate
}

func (l *gatedWriteListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &gatedWriteConn{Conn: conn, gate: l.gate}, nil
}

type gatedWriteConn struct {
	net.Conn
	gate *frameWriteGate
}

func (c *gatedWriteConn) Write(p []byte) (int, error) {
	if c.gate.armed.Swap(false) {
		close(c.gate.started)
		select {
		case <-c.gate.release:
		case <-c.gate.closed:
			return 0, net.ErrClosed
		}
	}
	return c.Conn.Write(p)
}

func (c *gatedWriteConn) Close() error {
	c.gate.once.Do(func() { close(c.gate.closed) })
	return c.Conn.Close()
}

func TestWorkbenchRevocationDuringFrameWrite(t *testing.T) {
	for _, binary := range []bool{false, true} {
		name := "json"
		if binary {
			name = "binary"
		}
		t.Run(name, func(t *testing.T) {
			gate := &frameWriteGate{started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(gate.release) }) }
			defer release()
			access, revoke := context.WithCancelCause(context.Background())
			defer revoke(context.Canceled)
			wrote := make(chan error, 1)
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					wrote <- err
					return
				}
				defer conn.CloseNow()
				writer := &socketWriter{conn: conn}
				gate.armed.Store(true)
				if binary {
					err = writer.Binary(access, []byte("admitted output"))
				} else {
					err = writer.JSON(access, map[string]string{"state": "ready"})
				}
				wrote <- err
				closeAccessWebSocket(conn, access)
			}))
			srv.Listener = &gatedWriteListener{Listener: srv.Listener, gate: gate}
			srv.Start()
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseNow()
			select {
			case <-gate.started:
			case <-ctx.Done():
				t.Fatal("frame write did not start")
			}
			revoke(errWorkbenchStopped)
			select {
			case <-gate.closed:
				t.Fatal("access cancellation aborted the transport before the close frame")
			case <-time.After(50 * time.Millisecond):
			}
			release()
			for {
				_, _, err = client.Read(ctx)
				if err != nil {
					break
				}
			}
			if websocket.CloseStatus(err) != websocket.StatusServiceRestart {
				t.Fatalf("close=%v, expected %d", err, websocket.StatusServiceRestart)
			}
			if err := <-wrote; err != nil {
				t.Fatalf("admitted frame was interrupted: %v", err)
			}
		})
	}
}
