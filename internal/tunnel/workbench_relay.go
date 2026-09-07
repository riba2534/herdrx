package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

// RelayBootstrap is a short-lived permission to register tunnel identities.
// It never authorizes a Herdr command or changes an existing binding.
type RelayBootstrap struct {
	Workbench string `json:"workbench"`
	Token     string `json:"token"`
	Address   string `json:"address,omitempty"`
}

type RelayRegistration struct {
	Nodes []key.NodePublic `json:"nodes"`
}

// WorkbenchRegion only considers the configured, TLS-verified public origin.
// A public address is a candidate; callers must also test a real DERP handshake.
func WorkbenchRegion(origin string, stunPort int) (*tailcfg.DERPRegion, error) {
	return workbenchRegionAt(origin, stunPort, "")
}

// A copyable command can carry the workbench's verified public IP. This avoids
// selecting a local DNS proxy's synthetic address while retaining TLS hostname
// verification. Neither private IPs nor a TLS bypass are permitted.
func workbenchRegionAt(origin string, stunPort int, address string) (*tailcfg.DERPRegion, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("workbench relay requires a public HTTPS origin")
	}
	port := 443
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil {
			return nil, errors.New("invalid workbench port")
		}
	}
	node := &tailcfg.DERPNode{Name: "herdrx", HostName: u.Hostname(), DERPPort: port, STUNPort: stunPort}
	if address != "" {
		ip, err := netip.ParseAddr(address)
		if err != nil || ip.Zone() != "" || !publicWorkbenchIP(ip.Unmap()) {
			return nil, errors.New("workbench relay address must be a public IP")
		}
		node.IPv4, node.IPv6 = "none", "none"
		if ip.Unmap().Is4() {
			node.IPv4 = ip.Unmap().String()
		} else {
			node.IPv6 = ip.String()
		}
	}
	region, err := DefaultSSRFValidator.ValidateDERPRegion(&tailcfg.DERPRegion{
		RegionID: 901, RegionCode: "herdrx", RegionName: "Herdrx workbench",
		Nodes: []*tailcfg.DERPNode{node},
	})
	if err != nil {
		return nil, err
	}
	for _, n := range region.Nodes {
		for _, raw := range []string{n.IPv4, n.IPv6} {
			if raw == "none" {
				continue
			}
			ip, err := netip.ParseAddr(raw)
			if err != nil || !publicWorkbenchIP(ip) {
				return nil, errors.New("workbench address is not public")
			}
		}
	}
	return region, nil
}

func publicWorkbenchIP(ip netip.Addr) bool {
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(ip) && !netip.MustParsePrefix("198.18.0.0/15").Contains(ip)
}

// RegisterWorkbenchRelay pins DNS for the HTTPS request as well as the DERP
// dial, and never follows redirects or forwards the ticket to another origin.
func RegisterWorkbenchRelay(ctx context.Context, bootstrap RelayBootstrap, nodes []key.NodePublic, probeKey key.NodePrivate) (*tailcfg.DERPRegion, error) {
	if ProxyEnvSet() {
		return nil, errors.New("workbench relay discovery does not support proxy environment variables")
	}
	if len(bootstrap.Token) < 32 || len(bootstrap.Token) > 128 {
		return nil, errors.New("workbench relay ticket is missing or expired; copy a new command from the website")
	}
	candidate, err := workbenchRegionAt(bootstrap.Workbench, -1, bootstrap.Address)
	if err != nil {
		return nil, err
	}
	n := candidate.Nodes[0]
	transport := &http.Transport{ForceAttemptHTTP2: false}
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var last error
		for _, ip := range []string{n.IPv4, n.IPv6} {
			if ip == "none" {
				continue
			}
			conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip, strconv.Itoa(n.DERPPort)))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	encoded, _ := json.Marshal(RelayRegistration{Nodes: append(append([]key.NodePublic{}, nodes...), probeKey.Public())})
	req, err := http.NewRequestWithContext(ctx, "POST", bootstrap.Workbench+"/api/relay/register", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", bootstrap.Workbench)
	req.Header.Set("Authorization", "Bearer "+bootstrap.Token)
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("workbench relay discovery: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("workbench relay discovery returned HTTP %d; copy a new command from the website", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, MaxConnectionStringLen+1))
	if err != nil {
		return nil, err
	}
	region, err := ParseDERPConfig(data)
	if err != nil {
		return nil, err
	}
	// The ticket endpoint may describe only its own relay, never another host.
	if len(region.Nodes) != 1 || region.Nodes[0].HostName != n.HostName || region.Nodes[0].DERPPort != n.DERPPort || region.Nodes[0].InsecureForTests || region.Nodes[0].CertName != "" {
		return nil, errors.New("workbench advertised a different relay origin")
	}
	for _, probe := range ProbeDERPRegionWithKey(ctx, region, probeKey) {
		if probe.Reachable {
			return region, nil
		}
	}
	return nil, errors.New("workbench DERP handshake failed; check its HTTPS reverse proxy")
}
