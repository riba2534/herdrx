package agentcli

import (
	"context"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/tunnel"
	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
)

func TestWorkbenchSelectionFallsBackWithoutPublishingUnreachableRegion(t *testing.T) {
	public := &tailcfg.DERPRegion{RegionID: 1, RegionCode: "public", Nodes: []*tailcfg.DERPNode{{HostName: "8.8.8.8"}}}
	cfg := Config{Node: *tailcat.NewPrivateKey()}
	cfg.Node.Public.Region = []*tailcfg.DERPRegion{public}
	bootstrap := &tunnel.RelayBootstrap{Workbench: "https://127.0.0.1", Token: strings.Repeat("x", 43)}
	region, status := selectWorkbenchRelay(context.Background(), cfg, bootstrap, nil)
	if region != public || !strings.HasPrefix(status, "public fallback:") {
		t.Fatalf("fallback=%v status=%s", region, status)
	}
	if cfg.Node.Public.Region[0] != public {
		t.Fatal("selection mutated persisted region before enrollment")
	}
	workbench := &tailcfg.DERPRegion{RegionID: 901, RegionCode: "herdrx", Nodes: []*tailcfg.DERPNode{{HostName: "127.0.0.1"}}}
	cfg.Node.Public.Region = []*tailcfg.DERPRegion{workbench}
	region, _ = selectWorkbenchRelay(context.Background(), cfg, bootstrap, nil)
	if region != nil {
		t.Fatal("fallback reused the unreachable workbench")
	}
	region, status = selectWorkbenchRelay(context.Background(), cfg, nil, nil)
	if region != workbench || status != "" {
		t.Fatal("plain connect unexpectedly migrated an existing region")
	}
}
