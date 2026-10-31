package httpapi

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/riba2534/herdrx/internal/testpaths"
)

// slowListMethod is a read-only Herdr call the fake answers only after a delay,
// so a workbench "call" occupies the read loop like a slow remote host does.
const slowListMethod = "worktree.list"

// keepaliveHerdrSocket answers like fakeHerdrSocket but delays slowListMethod.
// It counts every other method beyond the snapshot/observer reads.
func keepaliveHerdrSocket(t *testing.T, delay time.Duration) *atomic.Int32 {
	t.Helper()
	dir := testpaths.ShortTempDir(t)
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "herdr"), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "herdr", "herdr.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	calls := new(atomic.Int32)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var request struct {
					Method string `json:"method"`
				}
				if json.NewDecoder(conn).Decode(&request) != nil {
					return
				}
				switch request.Method {
				case slowListMethod:
					time.Sleep(delay)
				case "session.snapshot", "ping", "pane.read", "pane.process_info", "events.subscribe":
				default:
					calls.Add(1)
				}
				_ = json.NewEncoder(conn).Encode(map[string]any{"id": "herdrx", "result": map[string]any{"type": "ok", "snapshot": map[string]any{"version": "0.9.1", "protocol": 22, "panes": []map[string]any{{"pane_id": "p_fixture", "terminal_id": "term_fixture"}}}}})
			}()
		}
	}()
	return calls
}

// keepaliveFixture shortens the ping cadence and returns a ready workbench
// socket plus a probe for how many Web access leases are still attached. It
// fails the test if the keepalive ever reached a remote Herdr operation.
func keepaliveFixture(t *testing.T, interval, timeout, slowCall time.Duration) (context.Context, *websocket.Conn, func() int) {
	t.Helper()
	oldInterval, oldTimeout := workbenchPingInterval, workbenchPingTimeout
	workbenchPingInterval, workbenchPingTimeout = interval, timeout
	t.Cleanup(func() { workbenchPingInterval, workbenchPingTimeout = oldInterval, oldTimeout })
	calls := keepaliveHerdrSocket(t, slowCall)
	t.Cleanup(func() {
		if calls.Load() != 0 {
			t.Error("keepalive issued a remote Herdr operation")
		}
	})
	a, db, srv, admin, info := authFixture(t)
	id := info["user"].(map[string]any)["id"].(string)
	if err := db.CreateHost(context.Background(), store.Host{ID: "hst_fixture", OwnerID: id, Name: "Isolated fixture", Transport: "local", Port: 22}); err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	t.Cleanup(cancel)
	conn := fixtureSocket(t, c, srv, admin)
	leases := func() int {
		a.access.mu.Lock()
		defer a.access.mu.Unlock()
		return len(a.access.leases)
	}
	return c, conn, leases
}

// readMessages keeps reading (and so answering pings) and hands each text
// message to the returned channel. Once the test stops receiving, the reader
// blocks on the send and the client goes silent without closing the socket.
func readMessages(ctx context.Context, conn *websocket.Conn) (<-chan map[string]any, <-chan error) {
	messages, readErr := make(chan map[string]any), make(chan error, 1)
	go func() {
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				readErr <- err
				return
			}
			var message map[string]any
			_ = json.Unmarshal(raw, &message)
			select {
			case messages <- message:
			case <-ctx.Done():
				return
			}
		}
	}()
	return messages, readErr
}

func waitLeasesReleased(t *testing.T, leases func() int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for leases() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("server kept the silent socket past interval+timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWorkbenchKeepaliveKeepsResponsiveClient(t *testing.T) {
	interval := 20 * time.Millisecond
	ctx, conn, leases := keepaliveFixture(t, interval, time.Second, 0)
	messages, readErr := readMessages(ctx, conn)
	until := time.After(10 * interval)
	for alive := true; alive; {
		select {
		case err := <-readErr:
			t.Fatalf("connection closed across ping cycles: %v", err)
		case <-messages:
		case <-until:
			alive = false
		}
	}
	if n := leases(); n != 1 {
		t.Fatalf("access leases=%d, expected the live socket to keep 1", n)
	}
}

func TestWorkbenchKeepaliveDropsSilentClient(t *testing.T) {
	interval, timeout := 50*time.Millisecond, 100*time.Millisecond
	_, _, leases := keepaliveFixture(t, interval, timeout, 0)
	// The client never reads again, so its library cannot answer pings.
	waitLeasesReleased(t, leases, interval+timeout+2*time.Second)
}

func TestWorkbenchKeepaliveWaitsOutSlowMessage(t *testing.T) {
	interval, timeout := 50*time.Millisecond, 100*time.Millisecond
	slow := 6 * (interval + timeout)
	ctx, conn, leases := keepaliveFixture(t, interval, timeout, slow)
	messages, readErr := readMessages(ctx, conn)
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"t":"call","id":"slow-list","method":"`+slowListMethod+`"}`)); err != nil {
		t.Fatal(err)
	}
	// The server read loop sits in the call well past interval+timeout while
	// this client keeps answering pings it cannot see the pongs for.
	for answered := false; !answered; {
		select {
		case err := <-readErr:
			t.Fatalf("keepalive dropped a healthy socket during a slow call: %v", err)
		case message := <-messages:
			answered = message["t"] == "res" && message["id"] == "slow-list"
		}
	}
	if n := leases(); n != 1 {
		t.Fatalf("access leases=%d after the slow call, expected 1", n)
	}
	// Keepalive resumes once the loop reads again: a client gone silent now is
	// dropped. Not receiving from messages stalls the reader goroutine.
	waitLeasesReleased(t, leases, interval+timeout+2*time.Second)
}
