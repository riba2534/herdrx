package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/store"
)

type pushTestPool struct {
	mu                                sync.Mutex
	active, peak                      int
	calls                             map[string]int
	slowStarted, healthyDone, release chan struct{}
	startedOnce, healthyOnce          sync.Once
}

func (p *pushTestPool) Open(_ context.Context, h store.Host) (herdr.Endpoint, error) {
	return &pushTestEndpoint{pool: p, host: h}, nil
}
func (*pushTestPool) CloseHost(string) {}
func (*pushTestPool) Close()           {}

type pushTestEndpoint struct {
	pool *pushTestPool
	host store.Host
}

func (e *pushTestEndpoint) Snapshot(ctx context.Context) (herdr.Snapshot, error) {
	p := e.pool
	p.mu.Lock()
	p.active++
	p.peak = max(p.peak, p.active)
	p.calls[e.host.ID]++
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.active--; p.mu.Unlock() }()
	if e.host.ID == "host-000" {
		p.startedOnce.Do(func() { close(p.slowStarted) })
		select {
		case <-ctx.Done():
			return herdr.Snapshot{}, ctx.Err()
		case <-p.release:
			return herdr.Snapshot{}, errors.New("fixture host unavailable")
		}
	}
	p.healthyOnce.Do(func() { close(p.healthyDone) })
	return herdr.Snapshot{Agents: []herdr.Agent{{PaneID: "w1:p1", AgentStatus: "working"}}}, nil
}
func (*pushTestEndpoint) Call(context.Context, string, any) (json.RawMessage, error) {
	return nil, errors.New("unexpected mutation")
}
func (*pushTestEndpoint) OpenTerminal(context.Context, herdr.TerminalOpen) (herdr.TerminalProcess, error) {
	return nil, errors.New("unexpected terminal")
}
func (*pushTestEndpoint) StageImage(context.Context, string, io.Reader) (string, error) {
	return "", errors.New("unexpected image")
}
func (*pushTestEndpoint) Close() error { return nil }

func TestPushSlowHostDoesNotBlockOtherHostsAndHistoryIsPruned(t *testing.T) {
	a, db, _, _, _ := authFixture(t)
	a.stopBackground()
	a.hosts.Close()
	pool := &pushTestPool{calls: make(map[string]int), slowStarted: make(chan struct{}), healthyDone: make(chan struct{}), release: make(chan struct{})}
	a.hosts = pool
	ctx := context.Background()
	for i := range 2 {
		userID := fmt.Sprintf("poll-user-%d", i)
		if err := db.CreateUser(ctx, store.User{ID: userID, Email: fmt.Sprintf("poll-%d@example.test", i), DisplayName: "Poll", Role: "user", PasswordHash: "fixture"}); err != nil {
			t.Fatal(err)
		}
		if err := db.UpsertPushSubscription(ctx, store.PushSubscription{ID: "sub-" + userID, UserID: userID, Endpoint: "https://push.example.test/" + userID, P256DH: "unused", Auth: "unused"}); err != nil {
			t.Fatal(err)
		}
		for j := range 50 {
			index := i*50 + j
			host := store.Host{ID: fmt.Sprintf("host-%03d", index), OwnerID: userID, Name: "Fixture", Transport: "ssh", Port: 22}
			if err := db.CreateHost(ctx, host); err != nil {
				t.Fatal(err)
			}
		}
	}
	state := newPushPollState(map[string]string{"poll-user-0:removed-host:w1:p1": "working", "poll-user-0:host-001:w1:removed-pane": "working"})
	done := make(chan struct{})
	go func() { defer close(done); a.collectPushHosts(ctx, state) }()
	select {
	case <-pool.slowStarted:
	case <-time.After(time.Second):
		t.Fatal("slow host did not start")
	}
	select {
	case <-pool.healthyDone:
	case <-time.After(time.Second):
		t.Fatal("slow host blocked healthy hosts")
	}
	close(pool.release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("poll batch did not finish")
	}
	a.collectPushHosts(ctx, state)
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.peak > pushWorkers || pool.active != 0 {
		t.Fatalf("unbounded poll workers: peak=%d active=%d", pool.peak, pool.active)
	}
	if pool.calls["host-000"] != 1 {
		t.Fatal("failed host ignored backoff")
	}
	for id, count := range pool.calls {
		if id != "host-000" && count != 2 {
			t.Fatalf("healthy host skipped: %s=%d", id, count)
		}
	}
	if len(pool.calls) != 100 || len(state.previous) != 99 {
		t.Fatalf("wrong collection/history size: calls=%d history=%d", len(pool.calls), len(state.previous))
	}
	if _, ok := state.previous["poll-user-0:host-001:w1:removed-pane"]; ok {
		t.Fatal("removed pane history retained")
	}
	if _, ok := state.previous["poll-user-0:removed-host:w1:p1"]; ok {
		t.Fatal("deleted host history retained")
	}
}

func TestRenewedPushSubscriptionRejectsStaleJobsAndExpiry(t *testing.T) {
	a, db, _, _, _ := authFixture(t)
	ctx := context.Background()
	user := store.User{ID: "push-renew", Email: "renew@example.test", DisplayName: "Renew", Role: "user", PasswordHash: "fixture"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	old := store.PushSubscription{ID: "old", UserID: user.ID, Endpoint: "https://push.example.test/renew", P256DH: "old-key", Auth: "old-auth"}
	if err := db.UpsertPushSubscription(ctx, old); err != nil {
		t.Fatal(err)
	}
	renewed := old
	renewed.ID = "renewed"
	renewed.P256DH = "new-key"
	if err := db.UpsertPushSubscription(ctx, renewed); err != nil {
		t.Fatal(err)
	}
	if live := a.livePushSubscriptions(ctx, user.ID, []store.PushSubscription{old}); len(live) != 0 {
		t.Fatal("stale job retained renewed subscription access")
	}
	if err := db.DeletePushSubscriptionIfCurrent(ctx, old); err != nil {
		t.Fatal(err)
	}
	current, err := db.PushSubscriptionByEndpoint(ctx, user.ID, old.Endpoint)
	if err != nil || current.ID != renewed.ID || current.P256DH != renewed.P256DH {
		t.Fatal("late expiry deleted renewed subscription", err)
	}
}
