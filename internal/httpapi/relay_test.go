package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/riba2534/herdrx/internal/tunnel"
	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

func TestRelayTicketsRequireLoginExpireAndBoundNodeRegistration(t *testing.T) {
	a, _, server, client, info := authFixture(t)
	csrf := info["csrf_token"].(string)
	requestJSON(t, newTestClient(t), "POST", server.URL+"/api/tailcat/relay-offer", "", nil, 401)
	requestJSON(t, client, "POST", server.URL+"/api/tailcat/relay-offer", "invalid", nil, 403)
	if value := requestJSON(t, client, "POST", server.URL+"/api/tailcat/relay-offer", csrf, nil, 200); value["available"] != false {
		t.Fatal("HTTP-only deployment advertised a relay")
	}
	// The public address and handshake are covered by tunnel integration tests.
	// Seed a recent successful observation to isolate ticket authorization.
	managed, err := tunnel.NewManagedRelay(a.allowRelayNode)
	if err != nil {
		t.Fatal(err)
	}
	a.relay.server = managed
	a.relay.region = &tailcfg.DERPRegion{RegionID: 901, Nodes: []*tailcfg.DERPNode{{Name: "workbench", HostName: "8.8.8.8", DERPPort: 443, STUNPort: -1}}}
	a.relay.checked = time.Now()
	value := requestJSON(t, client, "POST", server.URL+"/api/tailcat/relay-offer", csrf, nil, 200)
	token := value["token"].(string)
	node := key.NewNode().Public()
	register := func(ticket string, nodes []key.NodePublic, status int) {
		t.Helper()
		encoded, _ := json.Marshal(tunnel.RelayRegistration{Nodes: nodes})
		req, _ := http.NewRequest("POST", server.URL+"/api/relay/register", bytes.NewReader(encoded))
		req.Header.Set("Origin", a.config.PublicURL)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+ticket)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode != status {
			t.Fatalf("register status=%d want=%d body=%s", res.StatusCode, status, data)
		}
	}
	register(strings.Repeat("x", 43), []key.NodePublic{node}, 403)
	if a.allowRelayNode(context.Background(), node) {
		t.Fatal("unknown key admitted")
	}
	register(token, []key.NodePublic{{}}, 400)
	register(token, []key.NodePublic{node}, 200)
	register(token, []key.NodePublic{node}, 200)
	if !a.allowRelayNode(context.Background(), node) {
		t.Fatal("registered key rejected")
	}
	for range 11 {
		register(token, []key.NodePublic{key.NewNode().Public()}, 200)
	}
	register(token, []key.NodePublic{key.NewNode().Public()}, 429)
	a.relay.mu.Lock()
	a.relay.tickets[string(secure.TokenHash(token))].expires = time.Now().Add(-time.Second)
	a.relay.grants[node] = relayGrant{owner: info["user"].(map[string]any)["id"].(string), expires: time.Now().Add(-time.Second)}
	a.relay.mu.Unlock()
	register(token, []key.NodePublic{node}, 403)
	if a.allowRelayNode(context.Background(), node) {
		t.Fatal("expired grant still admitted")
	}
}

func TestRelayAdmissionRestoresFromBindingAndRejectsDisabledOrDeletedHosts(t *testing.T) {
	a, db, _, _, info := authFixture(t)
	ctx := context.Background()
	admin := info["user"].(map[string]any)["id"].(string)
	owner := "relay-owner"
	if err := db.CreateUser(ctx, store.User{ID: owner, Email: "relay@example.test", Role: "user", DisplayName: "Relay", PasswordHash: "unused"}); err != nil {
		t.Fatal(err)
	}
	node := tailcat.NewPrivateKey()
	node.Public.Region = []*tailcfg.DERPRegion{{RegionID: 1, Nodes: []*tailcfg.DERPNode{{HostName: "8.8.8.8", STUNPort: -1}}}}
	controller, probe := key.NewNode(), key.NewNode()
	secret := hostruntime.TailcatCredential{NodePrivate: controller, FormalTailcatAddr: string(node.Public.Addr()), RelayProbeNode: probe.Public().String()}
	encoded, _ := json.Marshal(secret)
	ciphertext, err := hostruntime.SealCredential(a.vault, owner, "relay-credential", "tailcat", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCredential(ctx, store.Credential{ID: "relay-credential", OwnerID: owner, Kind: "tailcat", Ciphertext: ciphertext}); err != nil {
		t.Fatal(err)
	}
	host := store.Host{ID: "relay-host", OwnerID: owner, Name: "Relay", Transport: "tailcat", Port: 22, CredentialID: "relay-credential"}
	if err := db.CreateHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	for _, public := range []key.NodePublic{node.Private.Public(), controller.Public(), probe.Public()} {
		if !a.allowRelayNode(ctx, public) {
			t.Fatal("persisted binding did not restore relay admission")
		}
	}
	if a.allowRelayNode(ctx, key.NewNode().Public()) {
		t.Fatal("unrelated node admitted")
	}
	if err := db.SetUserDisabled(ctx, admin, owner, true, store.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	if a.allowRelayNode(ctx, controller.Public()) {
		t.Fatal("disabled owner admitted")
	}
	if err := db.SetUserDisabled(ctx, admin, owner, false, store.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteHost(ctx, owner, host.ID); err != nil {
		t.Fatal(err)
	}
	if a.allowRelayNode(ctx, controller.Public()) {
		t.Fatal("orphaned credential admitted")
	}
}
