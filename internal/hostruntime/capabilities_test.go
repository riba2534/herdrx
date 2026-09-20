package hostruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/store"
)

type runtimeCapabilityEndpoint struct {
	testEndpoint
	version atomic.Int64
	probes  atomic.Int32
}

func (e *runtimeCapabilityEndpoint) Snapshot(context.Context) (herdr.Snapshot, error) {
	return herdr.Snapshot{Version: fmt.Sprintf("0.9.%d", e.version.Load()), Protocol: 22}, nil
}
func (e *runtimeCapabilityEndpoint) HerdrCLIIdentity(context.Context) (herdr.CLIIdentity, error) {
	e.probes.Add(1)
	return herdr.CLIIdentity{Version: "0.9.1", Protocol: 22, Methods: []string{"session.snapshot"}}, nil
}
func (e *runtimeCapabilityEndpoint) Call(_ context.Context, method string, _ any) (json.RawMessage, error) {
	if method == "ping" {
		return json.Marshal(map[string]any{"version": fmt.Sprintf("0.9.%d", e.version.Load()), "protocol": 22})
	}
	return nil, &herdr.APIError{Code: "unknown_method", Message: method}
}

func TestSharedCapabilitiesFollowLiveIdentityAndReconnect(t *testing.T) {
	e := new(runtimeCapabilityEndpoint)
	e.version.Store(1)
	f := &Factory{dial: func(context.Context, store.Host) (herdr.Endpoint, error) { return e, nil }}
	defer f.Close()
	host := store.Host{ID: "capability-host", Transport: "ssh"}
	first, err := f.Open(t.Context(), host)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := f.Open(t.Context(), host)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	a, b := first.(*sharedHandle), second.(*sharedHandle)
	if a.RuntimeGeneration() != b.RuntimeGeneration() {
		t.Fatal("shared connection returned different generations")
	}
	r, err := a.HerdrCapabilities(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.CachedHerdrCapabilities(); !ok {
		t.Fatal("capability cache not shared")
	}
	_, _ = b.Call(t.Context(), "pane.send_text", nil)
	cached, _ := a.CachedHerdrCapabilities()
	if cached.Features["input"].State != herdr.CapabilityUnavailable || cached.Features["observe"].State != herdr.CapabilityAvailable {
		t.Fatalf("wrong method downgrade scope: %+v", cached)
	}
	e.version.Store(2)
	_, _ = b.Snapshot(t.Context())
	if a.RuntimeGeneration() == r.Generation {
		t.Fatal("live daemon change retained geometry generation")
	}
	if _, ok := a.CachedHerdrCapabilities(); ok {
		t.Fatal("live daemon change retained cache")
	}
	fresh, err := a.HerdrCapabilities(t.Context())
	if err != nil || e.probes.Load() != 2 || fresh.Features["input"].State != herdr.CapabilityAvailable {
		t.Fatalf("failed reprobe: %+v %v", fresh, err)
	}
	f.CloseHost(host.ID)
	third, err := f.Open(t.Context(), host)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	if third.(herdr.RuntimeGenerationProvider).RuntimeGeneration() == a.RuntimeGeneration() {
		t.Fatal("reconnect reused old geometry generation")
	}
}
