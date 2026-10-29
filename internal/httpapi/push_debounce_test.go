package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/herdr"
	pushmessage "github.com/riba2534/herdrx/internal/push"
	"github.com/riba2534/herdrx/internal/store"
)

// pushSnapshotPool serves whatever snapshot the test sets, for any host.
type pushSnapshotPool struct {
	mu       sync.Mutex
	snapshot herdr.Snapshot
}

func (p *pushSnapshotPool) set(agents ...herdr.Agent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshot = herdr.Snapshot{
		Workspaces: []herdr.Workspace{{ID: "w1", Label: "后端"}},
		Tabs:       []herdr.Tab{{ID: "t1", WorkspaceID: "w1", Label: "修复"}},
		Panes:      []herdr.Pane{{ID: "w1:p1", WorkspaceID: "w1", TabID: "t1", TerminalTitleStripped: "整理推送文案"}},
		Agents:     agents,
	}
}
func (p *pushSnapshotPool) Open(context.Context, store.Host) (herdr.Endpoint, error) {
	return pushSnapshotEndpoint{p}, nil
}
func (*pushSnapshotPool) CloseHost(string) {}
func (*pushSnapshotPool) Close()           {}

type pushSnapshotEndpoint struct{ pool *pushSnapshotPool }

func (e pushSnapshotEndpoint) Snapshot(context.Context) (herdr.Snapshot, error) {
	e.pool.mu.Lock()
	defer e.pool.mu.Unlock()
	return e.pool.snapshot, nil
}
func (pushSnapshotEndpoint) Call(context.Context, string, any) (json.RawMessage, error) {
	return nil, errors.New("unexpected mutation")
}
func (pushSnapshotEndpoint) OpenTerminal(context.Context, herdr.TerminalOpen) (herdr.TerminalProcess, error) {
	return nil, errors.New("unexpected terminal")
}
func (pushSnapshotEndpoint) StageImage(context.Context, string, io.Reader) (string, error) {
	return "", errors.New("unexpected image")
}
func (pushSnapshotEndpoint) Close() error { return nil }

type pushDebounceFixture struct {
	t     *testing.T
	api   *API
	db    *store.Store
	pool  *pushSnapshotPool
	state *pushPollState
	job   pushHostJob
	clock time.Time
}

func newPushDebounceFixture(t *testing.T) *pushDebounceFixture {
	a, db, _, _, _ := authFixture(t)
	a.stopBackground()
	a.hosts.Close()
	pool := &pushSnapshotPool{}
	a.hosts = pool
	ctx := context.Background()
	user := store.User{ID: "push-debounce", Email: "debounce@example.test", DisplayName: "Debounce", Role: "user", PasswordHash: "fixture"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	subscription := store.PushSubscription{ID: "sub-debounce", UserID: user.ID, Endpoint: "https://push.example.test/debounce", P256DH: "unused", Auth: "unused"}
	if err := db.UpsertPushSubscription(ctx, subscription); err != nil {
		t.Fatal(err)
	}
	host := store.Host{ID: "host-debounce", OwnerID: user.ID, Name: "构建机", Transport: "ssh", Port: 22}
	if err := db.CreateHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	f := &pushDebounceFixture{t: t, api: a, db: db, pool: pool, state: newPushPollState(nil), job: pushHostJob{host: host, subscriptions: []store.PushSubscription{subscription}}, clock: time.Unix(1_800_000_000, 0)}
	f.state.now = func() time.Time { return f.clock }
	return f
}

// poll advances the injected clock and collects one round for the host.
func (f *pushDebounceFixture) poll(after time.Duration, agents ...herdr.Agent) []pushmessage.Notification {
	f.t.Helper()
	f.clock = f.clock.Add(after)
	f.pool.set(agents...)
	return f.api.collectPushHost(context.Background(), f.job, f.state)
}

func pushAgent(status string, seq int64) herdr.Agent {
	return herdr.Agent{Name: "claude", Agent: "claude", AgentStatus: status, PaneID: "w1:p1", WorkspaceID: "w1", TabID: "t1", StateChangeSeq: seq}
}

func TestPushBlockedIsImmediateWithDeepLinkAndChineseText(t *testing.T) {
	f := newPushDebounceFixture(t)
	if got := f.poll(0, pushAgent("working", 1)); len(got) != 0 {
		t.Fatal("first sighting must only record history", got)
	}
	got := f.poll(5*time.Second, pushAgent("blocked", 2))
	if len(got) != 1 {
		t.Fatalf("blocked must notify immediately: %v", got)
	}
	message := got[0]
	if message.Title != "claude 需要你" || message.Body != "整理推送文案\n构建机 · 后端 › 修复" {
		t.Fatalf("unexpected text: %q / %q", message.Title, message.Body)
	}
	if message.Tag != "push-debounce:host-debounce:w1:p1" || message.Data["url"] != "/h/host-debounce?pane=w1%3Ap1" || message.Data["pane_id"] != "w1:p1" {
		t.Fatalf("unexpected tag or data: %q %v", message.Tag, message.Data)
	}
	if got := f.poll(5*time.Second, pushAgent("blocked", 2)); len(got) != 0 {
		t.Fatal("unchanged blocked state notified twice")
	}
}

func TestPushDoneWaitsSixtySecondsAndSkipsSeenRounds(t *testing.T) {
	ctx := context.Background()
	t.Run("unread", func(t *testing.T) {
		f := newPushDebounceFixture(t)
		f.poll(0, pushAgent("working", 4))
		if got := f.poll(5*time.Second, pushAgent("done", 5)); len(got) != 0 {
			t.Fatal("done must not notify before the delay")
		}
		if got := f.poll(pushDoneDelay-time.Second, pushAgent("done", 5)); len(got) != 0 {
			t.Fatal("done notified before 60 seconds")
		}
		got := f.poll(time.Second, pushAgent("done", 5))
		if len(got) != 1 || got[0].Title != "claude 已完成" || !strings.HasSuffix(got[0].Body, "构建机 · 后端 › 修复") {
			t.Fatalf("expected one done notification, got %v", got)
		}
		if got := f.poll(pushDoneDelay, pushAgent("done", 5)); len(got) != 0 {
			t.Fatal("done round notified twice")
		}
	})
	t.Run("seen", func(t *testing.T) {
		f := newPushDebounceFixture(t)
		f.poll(0, pushAgent("working", 4))
		f.poll(5*time.Second, pushAgent("done", 5))
		if err := f.db.MarkAgentSeen(ctx, f.job.host.OwnerID, f.job.host.ID, "w1:p1", 5); err != nil {
			t.Fatal(err)
		}
		if got := f.poll(pushDoneDelay, pushAgent("done", 5)); len(got) != 0 {
			t.Fatal("a round the user already saw was announced")
		}
	})
	t.Run("older marker", func(t *testing.T) {
		f := newPushDebounceFixture(t)
		if err := f.db.MarkAgentSeen(ctx, f.job.host.OwnerID, f.job.host.ID, "w1:p1", 4); err != nil {
			t.Fatal(err)
		}
		f.poll(0, pushAgent("working", 4))
		f.poll(5*time.Second, pushAgent("done", 5))
		if got := f.poll(pushDoneDelay, pushAgent("done", 5)); len(got) != 1 {
			t.Fatal("a marker from an older round suppressed the new one")
		}
	})
	t.Run("working again", func(t *testing.T) {
		f := newPushDebounceFixture(t)
		f.poll(0, pushAgent("working", 4))
		f.poll(5*time.Second, pushAgent("done", 5))
		f.poll(10*time.Second, pushAgent("working", 6))
		if len(f.state.pending) != 0 {
			t.Fatal("working again kept the waiting round")
		}
		if got := f.poll(pushDoneDelay, pushAgent("working", 6)); len(got) != 0 {
			t.Fatal("a cancelled round was announced")
		}
	})
	t.Run("new round between polls", func(t *testing.T) {
		f := newPushDebounceFixture(t)
		f.poll(0, pushAgent("working", 4))
		f.poll(5*time.Second, pushAgent("done", 5))
		// It worked and finished again within one poll interval: restart the delay.
		if got := f.poll(pushDoneDelay-10*time.Second, pushAgent("done", 7)); len(got) != 0 {
			t.Fatal("a changed sequence did not restart the delay")
		}
		if got := f.poll(pushDoneDelay-time.Second, pushAgent("done", 7)); len(got) != 0 {
			t.Fatal("the restarted delay ended early")
		}
		if got := f.poll(time.Second, pushAgent("done", 7)); len(got) != 1 {
			t.Fatal("the newer round was never announced")
		}
	})
	t.Run("pane closed", func(t *testing.T) {
		f := newPushDebounceFixture(t)
		f.poll(0, pushAgent("working", 4))
		f.poll(5*time.Second, pushAgent("done", 5))
		f.poll(5 * time.Second)
		if len(f.state.pending) != 0 || len(f.state.previous) != 0 {
			t.Fatal("a closed pane kept its pending round")
		}
	})
}
