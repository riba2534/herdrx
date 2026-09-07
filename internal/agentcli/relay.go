package agentcli

import (
	"context"
	"net/url"
	"time"

	"github.com/riba2534/herdrx/internal/tunnel"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

type connectRequest struct {
	Relay *tunnel.RelayBootstrap `json:"relay,omitempty"`
}

func relayProbePublic(cfg Config) string {
	if cfg.RelayProbePrivate.IsZero() {
		return ""
	}
	return cfg.RelayProbePrivate.Public().String()
}

func probeConfiguredRelay(ctx context.Context, cfg Config) []tunnel.DERPProbe {
	if cfg.RelayProbePrivate.IsZero() {
		return tunnel.ProbeDERPRegion(ctx, regionFromConfig(cfg))
	}
	return tunnel.ProbeDERPRegionWithKey(ctx, regionFromConfig(cfg), cfg.RelayProbePrivate)
}

// selectWorkbenchRelay chooses once, before publishing the full Tailcat
// address. The controller consequently rendezvous at the same relay. It must
// never append independent, unmeshed public relays to the workbench region.
func selectWorkbenchRelay(ctx context.Context, cfg Config, bootstrap *tunnel.RelayBootstrap, nodes []key.NodePublic) (*tailcfg.DERPRegion, string) {
	if bootstrap == nil {
		return regionFromConfig(cfg), ""
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	region, err := tunnel.RegisterWorkbenchRelay(ctx, *bootstrap, nodes, key.NewNode())
	if err == nil {
		return region, "workbench"
	}
	// Reuse a previously pinned public region when possible, but never keep
	// the very workbench relay whose reachability test just failed.
	fallback := regionFromConfig(cfg)
	u, _ := url.Parse(bootstrap.Workbench)
	if fallback != nil && u != nil {
		for _, node := range fallback.Nodes {
			if node != nil && (node.HostName == u.Hostname() || fallback.RegionCode == "herdrx") {
				fallback = nil
				break
			}
		}
	}
	return fallback, "public fallback: " + err.Error()
}
