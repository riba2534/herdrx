package tunnel

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"tailscale.com/derp/derpserver"
	"tailscale.com/net/stun/stuntest"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

// RunTestDERPAndSTUN 在 127.0.0.1 启动隔离本地 DERP + STUN 服务
func RunTestDERPAndSTUN(t testing.TB, logf logger.Logf, ipAddress string) *tailcfg.DERPMap {
	t.Helper()

	d := derpserver.New(key.NewNode(), logf)

	ln, err := net.Listen("tcp", net.JoinHostPort(ipAddress, "0"))
	if err != nil {
		t.Fatal(err)
	}

	handler := derpserver.AddWebSocketSupport(d, derpserver.Handler(d))
	httpsrv := httptest.NewUnstartedServer(handler)
	_ = httpsrv.Listener.Close()
	httpsrv.Listener = ln
	httpsrv.Config.ErrorLog = logger.StdLogger(logf)
	httpsrv.Config.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))
	httpsrv.StartTLS()

	sAddr, cleanup := stuntest.Serve(t)

	derpPort := httpsrv.Listener.Addr().(*net.TCPAddr).Port

	m := &tailcfg.DERPMap{
		Regions: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{
			1: {
				RegionID:   1,
				RegionCode: "local-test",
				Nodes: []*tailcfg.DERPNode{
					{
						Name:             "node-local-1",
						RegionID:         1,
						HostName:         ipAddress,
						IPv4:             ipAddress,
						IPv6:             "none",
						STUNPort:         sAddr.Port,
						DERPPort:         derpPort,
						InsecureForTests: true,
						STUNTestIP:       ipAddress,
					},
				},
			},
		},
	}

	t.Cleanup(func() {
		httpsrv.CloseClientConnections()
		httpsrv.Close()
		d.Close()
		cleanup()
		_ = ln.Close()
	})

	return m
}
