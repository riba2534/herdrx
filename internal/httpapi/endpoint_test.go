package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/riba2534/herdrx/internal/tunnel"
	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

func TestEndpointImportPreservesBindingAndRejectsReplayOrOtherOwner(t *testing.T) {
	a, db, srv, client, info := authFixture(t)
	ctx := context.Background()
	owner := info["user"].(map[string]any)["id"].(string)
	csrf := info["csrf_token"].(string)
	root, public, err := secure.GenerateSSHKey("endpoint-root")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(root)
	if err != nil {
		t.Fatal(err)
	}
	node := tailcat.NewPrivateKey()
	node.Public.Region = []*tailcfg.DERPRegion{{RegionID: 1, Nodes: []*tailcfg.DERPNode{{HostName: "203.0.113.20", IPv4: "203.0.113.20", IPv6: "none"}}}}
	original := string(node.Public.Addr())
	controller := key.NewNode()
	secret := hostruntime.TailcatCredential{NodePrivate: controller, SSHPrivate: "preserved-encrypted-fixture", AgentID: "agent-endpoint", ControllerID: "controller-endpoint", BindingID: "binding-endpoint", RawFormalAddr: original, FormalTailcatAddr: original, SSHHostKey: string(public)}
	encoded, _ := json.Marshal(secret)
	ciphertext, err := hostruntime.SealCredential(a.vault, owner, "endpoint-credential", "tailcat", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCredential(ctx, store.Credential{ID: "endpoint-credential", OwnerID: owner, Kind: "tailcat", Ciphertext: ciphertext}); err != nil {
		t.Fatal(err)
	}
	host := store.Host{ID: "endpoint-host", OwnerID: owner, Name: "Endpoint", Transport: "tailcat", Port: 22, CredentialID: "endpoint-credential", HostKey: string(public)}
	if err := db.CreateHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	next := node.Public
	next.Region = []*tailcfg.DERPRegion{{RegionID: 2, Nodes: []*tailcfg.DERPNode{{HostName: "203.0.113.21", IPv4: "203.0.113.21", IPv6: "none"}}}}
	payload := tunnel.EndpointUpdate{Version: 1, AgentID: secret.AgentID, ControllerID: secret.ControllerID, BindingID: secret.BindingID, ClientNode: controller.Public().String(), Address: string(next.Addr()), Revision: 1, CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(10 * time.Minute).Unix()}
	packet := func(p tunnel.EndpointUpdate, signer ssh.Signer) string {
		t.Helper()
		value, err := tunnel.BuildEndpointUpdate(p, signer)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	update := packet(payload, signer)
	url := srv.URL + "/api/hosts/" + host.ID + "/endpoint"
	other, err := a.createUserRecord(credentialsRequest{Email: "other-endpoint@example.test", Password: "fixture-member-password", DisplayName: "Other"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	otherClient := newTestClient(t)
	otherInfo := postJSON(t, otherClient, srv.URL+"/api/login", "", map[string]any{"email": other.Email, "password": "fixture-member-password"}, 200)
	requestJSON(t, otherClient, "POST", url, otherInfo["csrf_token"].(string), map[string]string{"update": update}, 404)
	wrong := payload
	wrong.ControllerID = "another-controller"
	requestJSON(t, client, "POST", url, csrf, map[string]string{"update": packet(wrong, signer)}, 409)
	wrong = payload
	changedPSK := next
	changedPSK.PresharedKey = tailcat.NewPresharedKey()
	wrong.Address = string(changedPSK.Addr())
	requestJSON(t, client, "POST", url, csrf, map[string]string{"update": packet(wrong, signer)}, 409)
	forgedPrivate, _, err := secure.GenerateSSHKey("forged")
	if err != nil {
		t.Fatal(err)
	}
	forged, err := ssh.ParsePrivateKey(forgedPrivate)
	if err != nil {
		t.Fatal(err)
	}
	requestJSON(t, client, "POST", url, csrf, map[string]string{"update": packet(payload, forged)}, 409)
	requestJSON(t, client, "POST", url, csrf, map[string]string{"update": update}, 200)
	requestJSON(t, client, "POST", url, csrf, map[string]string{"update": update}, 200) // Response lost; exact retry is safe.
	payload.Revision = 2
	requestJSON(t, client, "POST", url, csrf, map[string]string{"update": packet(payload, signer)}, 200)
	requestJSON(t, client, "POST", url, csrf, map[string]string{"update": update}, 409)
	saved, err := db.CredentialByID(ctx, owner, host.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := hostruntime.OpenCredential(a.vault, owner, saved.ID, saved.Kind, saved.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	var result hostruntime.TailcatCredential
	if err := json.Unmarshal(plaintext, &result); err != nil {
		t.Fatal(err)
	}
	if !result.NodePrivate.Equal(controller) || result.SSHPrivate != secret.SSHPrivate || result.ControllerID != secret.ControllerID || result.BindingID != secret.BindingID || result.EndpointVersion != 2 || result.RawFormalAddr != payload.Address {
		t.Fatal("endpoint import changed binding credentials")
	}
	hosts, err := db.ListHosts(ctx, owner)
	if err != nil || len(hosts) != 1 || hosts[0].ID != host.ID || hosts[0].TailcatAddr != "" {
		t.Fatal("endpoint update duplicated host or exposed its sensitive address")
	}
	if err := db.UpdateTailcatEndpoint(ctx, host, ciphertext, ciphertext); err != store.ErrConflict {
		t.Fatal("stale credential write bypassed revision transaction", err)
	}
}
