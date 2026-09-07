package httpapi

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/store"
)

type snapshotBudgetPool struct {
	herdr.Endpoint
	t      *testing.T
	closed atomic.Int32
	failed atomic.Bool
}

func (p *snapshotBudgetPool) Open(ctx context.Context, _ store.Host) (herdr.Endpoint, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > time.Second {
		p.t.Error("connection did not honor configured dial budget")
	}
	return p, nil
}
func (p *snapshotBudgetPool) CloseHost(string) { p.closed.Add(1) }
func (p *snapshotBudgetPool) Close() error     { return nil }
func (p *snapshotBudgetPool) Snapshot(ctx context.Context) (herdr.Snapshot, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < 9*time.Second || ctx.Err() != nil {
		p.t.Error("snapshot inherited the expired/short connection budget")
	}
	if p.failed.Load() {
		return herdr.Snapshot{}, errors.New("forward remote herdr socket: context deadline exceeded")
	}
	return herdr.Snapshot{Protocol: 1}, nil
}

// The pool and endpoint have different Close signatures, so wrap pool cleanup.
type snapshotPoolAdapter struct{ *snapshotBudgetPool }

func (*snapshotPoolAdapter) Close() {}

func TestSnapshotHasIndependentRPCBudgetAndDiscardsUnresponsiveTransport(t *testing.T) {
	a, db, srv, client, info := authFixture(t)
	a.stopBackground()
	a.hosts.Close()
	a.config.HostDialTimeout = time.Second
	pool := &snapshotBudgetPool{t: t}
	a.hosts = &snapshotPoolAdapter{pool}
	owner := info["user"].(map[string]any)["id"].(string)
	host := store.Host{ID: "snapshot-budget", OwnerID: owner, Name: "Snapshot", Transport: "tailcat", Port: 22}
	if err := db.CreateHost(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	url := srv.URL + "/api/hosts/" + host.ID + "/snapshot"
	requestJSON(t, client, "GET", url, "", nil, 200)
	if pool.closed.Load() != 0 {
		t.Fatal("successful snapshot discarded transport")
	}
	pool.failed.Store(true)
	requestJSON(t, client, "GET", url, "", nil, 502)
	if pool.closed.Load() != 1 {
		t.Fatal("unresponsive transport remained cached")
	}
}
