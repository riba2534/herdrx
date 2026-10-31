package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/riba2534/herdrx/internal/store"
)

// keepaliveFixture shortens the ping cadence and returns a ready workbench
// socket plus a probe for how many Web access leases are still attached. It
// fails the test if the keepalive ever reached a remote Herdr operation.
func keepaliveFixture(t *testing.T, interval, timeout time.Duration) (context.Context, *websocket.Conn, func() int) {
	t.Helper()
	oldInterval, oldTimeout := workbenchPingInterval, workbenchPingTimeout
	workbenchPingInterval, workbenchPingTimeout = interval, timeout
	t.Cleanup(func() { workbenchPingInterval, workbenchPingTimeout = oldInterval, oldTimeout })
	calls := fakeHerdrSocket(t)
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

func TestWorkbenchKeepaliveKeepsResponsiveClient(t *testing.T) {
	interval := 20 * time.Millisecond
	ctx, conn, leases := keepaliveFixture(t, interval, time.Second)
	readErr := make(chan error, 1)
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				readErr <- err
				return
			}
		}
	}()
	select {
	case err := <-readErr:
		t.Fatalf("connection closed across ping cycles: %v", err)
	case <-time.After(10 * interval):
	}
	if n := leases(); n != 1 {
		t.Fatalf("access leases=%d, expected the live socket to keep 1", n)
	}
}

func TestWorkbenchKeepaliveDropsSilentClient(t *testing.T) {
	interval, timeout := 50*time.Millisecond, 100*time.Millisecond
	_, _, leases := keepaliveFixture(t, interval, timeout)
	// The client never reads again, so its library cannot answer pings.
	deadline := time.Now().Add(interval + timeout + 2*time.Second)
	for leases() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("server kept the silent socket past interval+timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
