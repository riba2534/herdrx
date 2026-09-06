package tunnel

import (
	"context"
	"net/netip"
	"testing"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

func TestDialAddressPinsTheValidatedDNSResult(t *testing.T) {
	v := NewSSRFValidator()
	lookups := 0
	v.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		lookups++
		if lookups == 1 {
			return []netip.Addr{netip.MustParseAddr("203.0.113.20")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
	}
	ci := &tailcat.ConnInfo{ServerPublic: tailcat.NodePublic{NodePublic: key.NewNode().Public()}, PresharedKey: tailcat.NewPresharedKey(), Region: []*tailcfg.DERPRegion{{RegionID: 1, Nodes: []*tailcfg.DERPNode{{HostName: "relay.example.test", CanPort80: true}}}}}
	raw := string(ci.Addr())
	dial, err := v.ValidateDialAddr(raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := tailcat.ParseAddr(tailcat.Addr(dial))
	if err != nil {
		t.Fatal(err)
	}
	node := parsed.Region[0].Nodes[0]
	if lookups != 1 || node.IPv4 != "203.0.113.20" || node.IPv6 != "none" || node.CanPort80 {
		t.Fatalf("dial can re-resolve an unchecked address: lookups=%d node=%+v", lookups, node)
	}
	if string(ci.Addr()) != raw {
		t.Fatal("validation mutated signed raw address")
	}
	if _, err = v.ValidateDialAddr(raw); err == nil {
		t.Fatal("changed DNS record bypassed validation on a fresh dial")
	}
}

func TestImportedDERPOptionsCannotBypassValidation(t *testing.T) {
	for _, node := range []tailcfg.DERPNode{
		{HostName: "203.0.113.20", InsecureForTests: true},
		{HostName: "203.0.113.20", IPv4: "another.example.test"},
		{HostName: "203.0.113.20", IPv6: "203.0.113.21"},
		{HostName: "203.0.113.20", STUNPort: 65536},
		{HostName: "203.0.113.20", STUNTestIP: "169.254.169.254"},
	} {
		v := NewSSRFValidator()
		if err := v.validateDERPNode(&node); err == nil {
			t.Fatalf("invalid node accepted: %+v", node)
		}
	}
}
