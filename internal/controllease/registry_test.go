package controllease

import (
	"testing"
	"time"
)

func TestLeaseOwnershipAndExpiry(t *testing.T) {
	registry := New()
	if _, ok := registry.Acquire("pane", "browser-a", 20*time.Millisecond); !ok {
		t.Fatal("first owner could not acquire")
	}
	if _, ok := registry.Acquire("pane", "browser-b", time.Second); ok {
		t.Fatal("second owner acquired a live lease")
	}
	time.Sleep(25 * time.Millisecond)
	if _, ok := registry.Acquire("pane", "browser-b", time.Second); !ok {
		t.Fatal("second owner could not acquire expired lease")
	}
	if registry.Renew("pane", "browser-a", time.Second) {
		t.Fatal("stale owner renewed replacement lease")
	}
}
