package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"

	"tailscale.com/derp/derpserver"
	"tailscale.com/net/stun"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

// ManagedRelay shares the workbench HTTP listener and delegates TLS to the
// deployer's existing reverse proxy. Admission is fail-closed and independent
// of browser cookies, which DERP clients do not carry.
type ManagedRelay struct {
	Server    *derpserver.Server
	ProbeKey  key.NodePrivate
	admission *http.Server
	stun      net.PacketConn
	handler   http.Handler
	slots     chan struct{}
}

func NewManagedRelay(allow func(context.Context, key.NodePublic) bool) (*ManagedRelay, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("relay admission listener: %w", err)
	}
	r := &ManagedRelay{Server: derpserver.New(key.NewNode(), logger.Discard), ProbeKey: key.NewNode(), slots: make(chan struct{}, 128)}
	r.admission = &http.Server{ReadHeaderTimeout: 3 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var input tailcfg.DERPAdmitClientRequest
		allowed := false
		if req.Method == "POST" && json.NewDecoder(io.LimitReader(req.Body, 4096)).Decode(&input) == nil && !input.NodePublic.IsZero() {
			allowed = input.NodePublic == r.ProbeKey.Public() || allow(req.Context(), input.NodePublic)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tailcfg.DERPAdmitClientResponse{Allow: allowed})
	})}
	r.Server.SetVerifyClientURL("http://" + ln.Addr().String())
	r.Server.SetVerifyClientURLFailOpen(false)
	r.Server.UpdateRateLimits(derpserver.RateConfig{PerClientRateLimitBytesPerSec: 8 << 20, PerClientRateBurstBytes: 8 << 20})
	r.handler = derpserver.AddWebSocketSupport(r.Server, derpserver.Handler(r.Server))
	go func() { _ = r.admission.Serve(ln) }()
	return r, nil
}

func (r *ManagedRelay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
		r.handler.ServeHTTP(w, req)
	default:
		http.Error(w, "relay connection limit reached", http.StatusServiceUnavailable)
	}
}

func (r *ManagedRelay) ListenSTUN(address string) (int, error) {
	pc, err := net.ListenPacket("udp", address)
	if err != nil {
		return -1, err
	}
	r.stun = pc
	go func() {
		buffer := make([]byte, 65536)
		for {
			n, from, err := pc.ReadFrom(buffer)
			if err != nil {
				return
			}
			id, err := stun.ParseBindingRequest(buffer[:n])
			if err != nil {
				continue
			}
			addr, err := netip.ParseAddrPort(from.String())
			if err != nil {
				continue
			}
			_, _ = pc.WriteTo(stun.Response(id, addr), from)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr).Port, nil
}

func (r *ManagedRelay) Close() {
	_ = r.Server.Close()
	_ = r.admission.Close()
	if r.stun != nil {
		_ = r.stun.Close()
	}
}
