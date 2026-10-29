package herdr

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/testprocess"
)

// The workbench's snapshot subscription must be accepted by real Herdr and
// deliver layout and agent-status events. Only a temporary session is used.
func TestSubscribeEventsWithRealHerdr(t *testing.T) {
	binary := os.Getenv("HERDRX_TEST_HERDR")
	if binary == "" {
		t.Skip("HERDRX_TEST_HERDR is not set")
	}
	dir, err := os.MkdirTemp("", "hxe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HERDR_CONFIG_PATH", filepath.Join(dir, "config.toml"))
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("onboarding = false\n[terminal]\ndefault_shell = \"/bin/sh\"\nshell_mode = \"non_login\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	const session = "events-integration"
	daemon := exec.CommandContext(ctx, binary, "--session", session, "server")
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	defer testprocess.StopHerdr(t, binary, session, daemon)
	endpoint, err := NewLocalEndpoint(binary, session)
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := endpoint.Snapshot(ctx); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("isolated Herdr did not start")
		case <-time.After(50 * time.Millisecond):
		}
	}
	created, err := endpoint.Call(ctx, "workspace.create", map[string]any{"cwd": dir, "label": "events", "focus": false})
	if err != nil {
		t.Fatal(err)
	}
	var workspace struct {
		RootPane struct {
			PaneID string `json:"pane_id"`
		} `json:"root_pane"`
	}
	if err := json.Unmarshal(created, &workspace); err != nil || workspace.RootPane.PaneID == "" {
		t.Fatalf("workspace.create %s: %v", created, err)
	}
	pane := workspace.RootPane.PaneID

	subscriptions := []EventSubscription{
		{"type": "workspace.created"}, {"type": "workspace.updated"}, {"type": "workspace.renamed"}, {"type": "workspace.closed"}, {"type": "workspace.moved"}, {"type": "workspace.focused"},
		{"type": "tab.created"}, {"type": "tab.renamed"}, {"type": "tab.closed"}, {"type": "tab.moved"}, {"type": "tab.focused"},
		{"type": "pane.created"}, {"type": "pane.closed"}, {"type": "pane.exited"}, {"type": "pane.updated"}, {"type": "pane.moved"}, {"type": "pane.focused"}, {"type": "pane.agent_detected"},
		{"type": "layout.updated"}, {"type": "pane.agent_status_changed", "pane_id": pane},
	}
	events := make(chan string, 64)
	streamCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() {
		done <- endpoint.SubscribeEvents(streamCtx, subscriptions, func(event Event) { events <- event.Event })
	}()
	expect := func(want string) {
		t.Helper()
		for {
			select {
			case got := <-events:
				if got == want {
					return
				}
			case err := <-done:
				t.Fatalf("stream ended before %s: %v", want, err)
			case <-ctx.Done():
				t.Fatalf("no %s event", want)
			}
		}
	}
	expect(EventSubscriptionStarted)
	if _, err := endpoint.Call(ctx, "pane.split", map[string]any{"pane_id": pane, "direction": "right", "focus": false}); err != nil {
		t.Fatal(err)
	}
	expect("pane_created")
	stop()
	if err := <-done; err != context.Canceled {
		t.Fatalf("stream end = %v", err)
	}
}
