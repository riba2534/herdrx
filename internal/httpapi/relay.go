package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/tunnel"
	"github.com/tailscale/tailcat"
	"golang.org/x/sync/singleflight"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

type relayTicket struct {
	owner   string
	expires time.Time
	nodes   map[key.NodePublic]bool
}
type relayGrant struct {
	owner   string
	expires time.Time
}
type workbenchRelay struct {
	server   *tunnel.ManagedRelay
	stunPort int
	mu       sync.Mutex
	tickets  map[string]*relayTicket
	grants   map[key.NodePublic]relayGrant
	region   *tailcfg.DERPRegion
	checked  time.Time
	check    singleflight.Group
}

func (a *API) initRelay() error {
	a.relay = &workbenchRelay{stunPort: -1, tickets: make(map[string]*relayTicket), grants: make(map[key.NodePublic]relayGrant)}
	if a.config.DERPRelayMode == "off" || !strings.HasPrefix(a.config.PublicURL, "https://") {
		return nil
	}
	server, err := tunnel.NewManagedRelay(a.allowRelayNode)
	if err != nil {
		return err
	}
	a.relay.server = server
	if a.config.DERPSTUNAddr != "" && a.config.DERPSTUNAddr != "off" {
		port, err := server.ListenSTUN(a.config.DERPSTUNAddr)
		if err != nil {
			a.logger.Warn("workbench STUN unavailable; DERP remains available", "error", err)
		} else {
			a.relay.stunPort = port
		}
	}
	return nil
}

func (a *API) relayRegion(ctx context.Context) *tailcfg.DERPRegion {
	r := a.relay
	if r == nil || r.server == nil {
		return nil
	}
	// One bounded probe per instance, never holding a mutex across network I/O.
	result := r.check.DoChan("region", func() (any, error) {
		r.mu.Lock()
		region, checked := r.region, r.checked
		r.mu.Unlock()
		ttl := 15 * time.Second
		if region != nil {
			ttl = 2 * time.Minute
		}
		if time.Since(checked) < ttl {
			return region, nil
		}
		candidate, err := tunnel.WorkbenchRegion(a.config.PublicURL, r.stunPort)
		if err == nil {
			probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ok := false
			for _, probe := range tunnel.ProbeDERPRegionWithKey(probeCtx, candidate, r.server.ProbeKey) {
				ok = ok || probe.Reachable
			}
			if !ok {
				candidate = nil
			}
		}
		r.mu.Lock()
		r.region = candidate
		r.checked = time.Now()
		r.mu.Unlock()
		return candidate, nil
	})
	select {
	case <-ctx.Done():
		return nil
	case value := <-result:
		region, _ := value.Val.(*tailcfg.DERPRegion)
		return region
	}
}

func (a *API) createRelayOffer(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithTimeout(req.Context(), 6*time.Second)
	defer cancel()
	region := a.relayRegion(ctx)
	if region == nil {
		writeJSON(w, 200, map[string]any{"available": false})
		return
	}
	owner := userFromContext(req.Context()).ID
	token, err := secure.Token(32)
	if err != nil {
		writeError(w, 500, "relay_ticket_failed", "无法生成连接命令，请重试")
		return
	}
	r := a.relay
	r.mu.Lock()
	r.pruneLocked()
	if len(r.tickets) >= 256 {
		r.mu.Unlock()
		writeError(w, 429, "relay_busy", "连接命令生成过于频繁，请稍后重试")
		return
	}
	expires := time.Now().Add(20 * time.Minute)
	r.tickets[string(secure.TokenHash(token))] = &relayTicket{owner: owner, expires: expires, nodes: make(map[key.NodePublic]bool)}
	r.mu.Unlock()
	address := region.Nodes[0].IPv4
	if address == "" || address == "none" {
		address = region.Nodes[0].IPv6
	}
	writeJSON(w, 200, map[string]any{"available": true, "workbench": a.config.PublicURL, "token": token, "address": address, "expires_at": expires})
}

func (r *workbenchRelay) pruneLocked() {
	for token, ticket := range r.tickets {
		if time.Now().After(ticket.expires) {
			delete(r.tickets, token)
		}
	}
	for node, grant := range r.grants {
		if time.Now().After(grant.expires) {
			delete(r.grants, node)
		}
	}
}

func (a *API) registerRelayNodes(w http.ResponseWriter, req *http.Request) {
	r := a.relay
	if r == nil || r.server == nil {
		writeError(w, 503, "relay_unavailable", "工作台中继未启用")
		return
	}
	token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	if len(token) < 32 || len(token) > 128 {
		writeError(w, 403, "relay_ticket_invalid", "连接命令已失效，请重新复制")
		return
	}
	var input tunnel.RelayRegistration
	req.Body = http.MaxBytesReader(w, req.Body, 4096)
	if decodeJSON(req, &input) != nil || len(input.Nodes) < 1 || len(input.Nodes) > 5 {
		writeError(w, 400, "invalid_relay_nodes", "invalid relay nodes")
		return
	}
	for _, node := range input.Nodes {
		if node.IsZero() {
			writeError(w, 400, "invalid_relay_nodes", "invalid relay node")
			return
		}
	}
	hash := string(secure.TokenHash(token))
	r.mu.Lock()
	r.pruneLocked()
	ticket := r.tickets[hash]
	owner := ""
	if ticket != nil {
		owner = ticket.owner
	}
	r.mu.Unlock()
	user, err := a.store.UserByID(req.Context(), owner)
	if ticket == nil || err != nil || user.Disabled {
		writeError(w, 403, "relay_ticket_invalid", "连接命令已失效，请重新复制")
		return
	}
	region := a.relayRegion(req.Context())
	if region == nil {
		writeError(w, 503, "relay_unavailable", "工作台中继暂不可用")
		return
	}
	r.mu.Lock()
	ticket = r.tickets[hash]
	if ticket == nil || time.Now().After(ticket.expires) {
		r.mu.Unlock()
		writeError(w, 403, "relay_ticket_invalid", "连接命令已失效，请重新复制")
		return
	}
	count := len(ticket.nodes)
	unique := make(map[key.NodePublic]bool)
	for _, node := range input.Nodes {
		if !ticket.nodes[node] && !unique[node] {
			count++
			unique[node] = true
		}
	}
	if count > 12 {
		r.mu.Unlock()
		writeError(w, 429, "relay_ticket_used", "请重新复制连接命令")
		return
	}
	for _, node := range input.Nodes {
		ticket.nodes[node] = true
		r.grants[node] = relayGrant{owner: owner, expires: ticket.expires}
	}
	r.mu.Unlock()
	writeJSON(w, 200, region)
}

func (a *API) allowRelayNode(ctx context.Context, node key.NodePublic) bool {
	r := a.relay
	r.mu.Lock()
	grant, exists := r.grants[node]
	r.mu.Unlock()
	if exists && time.Now().Before(grant.expires) {
		user, err := a.store.UserByID(ctx, grant.owner)
		if err == nil && !user.Disabled {
			return true
		}
	}
	credentials, err := a.store.RelayCredentials(ctx)
	if err != nil {
		return false
	}
	for _, credential := range credentials {
		data, err := hostruntime.OpenCredential(a.vault, credential.OwnerID, credential.ID, credential.Kind, credential.Ciphertext)
		if err != nil {
			continue
		}
		var secret hostruntime.TailcatCredential
		if json.Unmarshal(data, &secret) != nil {
			continue
		}
		if secret.RelayProbeNode == node.String() {
			return true
		}
		if !secret.NodePrivate.IsZero() && secret.NodePrivate.Public() == node {
			return true
		}
		if !secret.BootstrapClient.IsZero() && secret.BootstrapClient.Public() == node {
			return true
		}
		for _, address := range []string{secret.FormalTailcatAddr, secret.RawFormalAddr, secret.BootstrapAddr} {
			ci, err := tailcat.ParseAddr(tailcat.Addr(address))
			if err == nil && ci.ServerPublic.NodePublic == node {
				return true
			}
		}
	}
	return false
}
