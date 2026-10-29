package store

import (
	"context"
	"errors"
	"testing"
)

func TestAgentSeenIsOwnerScopedAndMonotonic(t *testing.T) {
	dataStore, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	ctx := context.Background()
	for _, user := range []User{
		{ID: "usr_a", Email: "a@example.test", PasswordHash: "hash", DisplayName: "A", Role: "user"},
		{ID: "usr_b", Email: "b@example.test", PasswordHash: "hash", DisplayName: "B", Role: "user"},
	} {
		if err := dataStore.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	if err := dataStore.CreateHost(ctx, Host{ID: "hst_a", OwnerID: "usr_a", Name: "A", Transport: "ssh", Hostname: "a.example.test", Port: 22}); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.MarkAgentSeen(ctx, "usr_b", "hst_a", "w1:p1", 5); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant marked a host: %v", err)
	}
	if err := dataStore.MarkAgentSeen(ctx, "usr_a", "hst_a", "w1:p1", 5); err != nil {
		t.Fatal(err)
	}
	// An older page must not move the marker back.
	if err := dataStore.MarkAgentSeen(ctx, "usr_a", "hst_a", "w1:p1", 3); err != nil {
		t.Fatal(err)
	}
	seen, err := dataStore.AgentSeenForUser(ctx, "usr_a")
	if err != nil || seen[AgentSeenKey("hst_a", "w1:p1")].StateChangeSeq != 5 {
		t.Fatalf("seen = %#v, %v", seen, err)
	}
	if other, err := dataStore.AgentSeenForUser(ctx, "usr_b"); err != nil || len(other) != 0 {
		t.Fatalf("other tenant read markers: %#v, %v", other, err)
	}
	if err := dataStore.MarkAgentSeen(ctx, "usr_a", "hst_a", "w1:p2", 1); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.PruneAgentSeen(ctx, "usr_a", "hst_a", map[string]bool{"w1:p2": true}); err != nil {
		t.Fatal(err)
	}
	seen, _ = dataStore.AgentSeenForUser(ctx, "usr_a")
	if _, ok := seen[AgentSeenKey("hst_a", "w1:p1")]; ok || len(seen) != 1 {
		t.Fatalf("prune kept stale pane: %#v", seen)
	}
	if err := dataStore.DeleteHost(ctx, "usr_a", "hst_a"); err != nil {
		t.Fatal(err)
	}
	if seen, _ = dataStore.AgentSeenForUser(ctx, "usr_a"); len(seen) != 0 {
		t.Fatalf("deleted host kept markers: %#v", seen)
	}
}
