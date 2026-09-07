package tunnel

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"tailscale.com/derp"
	"tailscale.com/derp/derphttp"
	"tailscale.com/net/netmon"
	"tailscale.com/net/stun"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

func TestManagedRelayAdmitsRegisteredNodesAndForwardsPackets(t *testing.T) {
	a, b := key.NewNode(), key.NewNode()
	r, err := NewManagedRelay(func(_ context.Context, node key.NodePublic) bool { return node == a.Public() || node == b.Public() })
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	server := httptest.NewUnstartedServer(r)
	server.Config.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
	server.StartTLS()
	defer server.Close()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	DefaultSSRFValidator.AllowPrivateEndpoint(netip.MustParseAddrPort("127.0.0.1:" + strconv.Itoa(port)))
	defer DefaultSSRFValidator.ClearAllowed()
	region := &tailcfg.DERPRegion{RegionID: 901, Nodes: []*tailcfg.DERPNode{{Name: "local", RegionID: 901, HostName: "127.0.0.1", IPv4: "127.0.0.1", IPv6: "none", DERPPort: port, STUNPort: -1, InsecureForTests: true}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if result := ProbeDERPRegionWithKey(ctx, region, key.NewNode()); result[0].Reachable {
		t.Fatal("unregistered node passed admission")
	}
	if result := ProbeDERPRegionWithKey(ctx, region, r.ProbeKey); !result[0].Reachable {
		t.Fatalf("self probe failed: %+v", result)
	}
	monitor := netmon.NewStatic()
	defer monitor.Close()
	connect := func(identity key.NodePrivate) *derphttp.Client {
		t.Helper()
		c := derphttp.NewRegionClient(identity, logger.Discard, monitor, func() *tailcfg.DERPRegion { return region })
		t.Cleanup(func() { _ = c.Close() })
		stop := context.AfterFunc(ctx, func() { _ = c.Close() })
		t.Cleanup(func() { stop() })
		if err := c.Connect(ctx); err != nil {
			t.Fatal(err)
		}
		message, err := c.Recv()
		if _, ok := message.(derp.ServerInfoMessage); err != nil || !ok {
			t.Fatalf("admission: %T %v", message, err)
		}
		return c
	}
	first, second := connect(a), connect(b)
	packet := bytes.Repeat([]byte("encrypted-tunnel-packet"), 1024)
	if err := first.Send(b.Public(), packet); err != nil {
		t.Fatal(err)
	}
	for {
		message, err := second.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if received, ok := message.(derp.ReceivedPacket); ok {
			if received.Source != a.Public() || !bytes.Equal(received.Data, packet) {
				t.Fatal("relay corrupted packet or source")
			}
			break
		}
	}
}

func TestManagedRelaySTUNReportsObservedSource(t *testing.T) {
	r, err := NewManagedRelay(func(context.Context, key.NodePublic) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	port, err := r.ListenSTUN("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	id := stun.NewTxID()
	if _, err := c.Write(stun.Request(id)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1024)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	received, source, err := stun.ParseResponse(buf[:n])
	if err != nil || received != id || source.String() != c.LocalAddr().String() {
		t.Fatalf("STUN source mismatch: %v %v", source, err)
	}
}

func TestWorkbenchRegionRejectsNonPublicOrUnverifiedOrigins(t *testing.T) {
	for _, origin := range []string{"http://8.8.8.8", "https://127.0.0.1", "https://10.0.0.1", "https://[::1]", "https://169.254.169.254", "https://100.64.1.1", "https://198.18.0.1", "https://user@8.8.8.8", "https://8.8.8.8/path", "https://8.8.8.8?redirect=other", "https://8.8.8.8#fragment"} {
		if _, err := WorkbenchRegion(origin, -1); err == nil {
			t.Errorf("accepted %s", origin)
		}
	}
	region, err := WorkbenchRegion("https://8.8.8.8:8443", 3478)
	if err != nil {
		t.Fatal(err)
	}
	n := region.Nodes[0]
	if n.IPv4 != "8.8.8.8" || n.IPv6 != "none" || n.DERPPort != 8443 || n.InsecureForTests || n.STUNPort != 3478 {
		t.Fatalf("incorrect pinned region: %+v", n)
	}
}

func TestWorkbenchCommandPinsPublicAddressWhenLocalDNSReturnsSyntheticIP(t *testing.T) {
	previous := DefaultSSRFValidator
	DefaultSSRFValidator = NewSSRFValidator()
	defer func() { DefaultSSRFValidator = previous }()
	DefaultSSRFValidator.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("198.18.1.10")}, nil
	}
	if _, err := WorkbenchRegion("https://workbench.example.com", -1); err == nil {
		t.Fatal("synthetic DNS address was treated as a public relay")
	}
	region, err := workbenchRegionAt("https://workbench.example.com", -1, "8.8.8.8")
	if err != nil {
		t.Fatal(err)
	}
	node := region.Nodes[0]
	if node.IPv4 != "8.8.8.8" || node.IPv6 != "none" || node.HostName != "workbench.example.com" || node.InsecureForTests || node.CertName != "" {
		t.Fatalf("pinned address lost TLS hostname verification: %+v", node)
	}
	for _, address := range []string{"198.18.1.10", "127.0.0.1", "10.0.0.1", "100.64.1.1", "::1", "fe80::1%eth0", "hostname.example.com"} {
		if _, err := workbenchRegionAt("https://workbench.example.com", -1, address); err == nil {
			t.Errorf("accepted non-public command address %s", address)
		}
	}
}
