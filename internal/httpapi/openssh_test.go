package httpapi

import (
	"context"
	"testing"

	"github.com/riba2534/herdrx/internal/store"
)

func TestSystemSSHAdminBoundary(t *testing.T) {
	a, db, srv, admin, info := authFixture(t)
	csrf := info["csrf_token"].(string)
	body := map[string]any{"name": "System SSH", "transport": "ssh", "hostname": "devbox", "auth_method": "system_ssh"}
	postJSON(t, admin, srv.URL+"/api/hosts/", csrf, body, 400)
	a.config.SSHBinary = "/usr/bin/ssh"
	result := postJSON(t, admin, srv.URL+"/api/hosts/", csrf, body, 201)
	host := result["host"].(map[string]any)
	id := host["id"].(string)
	stored, err := db.HostByID(context.Background(), info["user"].(map[string]any)["id"].(string), id)
	if err != nil || stored.CredentialID != "" || stored.Port != 0 || stored.Username != "" {
		t.Fatalf("alias defaults/credentials: %+v %v", stored, err)
	}
	requestJSON(t, admin, "PUT", srv.URL+"/api/hosts/"+id+"/ssh", csrf, body, 200)
	body["secret"] = "must-not-save"
	postJSON(t, admin, srv.URL+"/api/hosts/", csrf, body, 400)
	delete(body, "secret")
	body["proxy_jump"] = "jump@example.test:2222"
	postJSON(t, admin, srv.URL+"/api/hosts/", csrf, body, 400)
	delete(body, "proxy_jump")
	invite := postJSON(t, admin, srv.URL+"/api/admin/invites", csrf, nil, 201)
	member := newTestClient(t)
	m := postJSON(t, member, srv.URL+"/api/register", "", map[string]any{"email": "member@example.test", "display_name": "Member", "password": "fixture-password", "invite_code": invite["code"]}, 200)
	postJSON(t, member, srv.URL+"/api/hosts/", m["csrf_token"].(string), body, 400)
	requestJSON(t, member, "GET", srv.URL+"/api/hosts/"+id, "", nil, 404)
	uid := m["user"].(map[string]any)["id"].(string)
	if err := db.CreateHost(t.Context(), store.Host{ID: "forged", OwnerID: uid, Name: "forged", Transport: "ssh", AuthMethod: "system_ssh"}); err != store.ErrAdminRequired {
		t.Fatalf("store accepted non-admin system credentials: %v", err)
	}
	body["auth_method"], body["username"], body["secret"] = "password", "test", "fixture-password"
	requestJSON(t, admin, "PUT", srv.URL+"/api/hosts/"+id+"/ssh", csrf, body, 200)
	stored, err = db.HostByID(t.Context(), stored.OwnerID, id)
	if err != nil || stored.CredentialID == "" || stored.Port != 22 {
		t.Fatalf("switch back to password: %+v %v", stored, err)
	}
	body["auth_method"] = "system_ssh"
	delete(body, "secret")
	requestJSON(t, admin, "PUT", srv.URL+"/api/hosts/"+id+"/ssh", csrf, body, 200)
}

func TestPasswordProxyJumpSurvivesSystemSSHIntegration(t *testing.T) {
	_, db, srv, admin, info := authFixture(t)
	body := map[string]any{
		"name": "Password via jump", "transport": "ssh", "hostname": "target.example.test",
		"username": "developer", "auth_method": "password", "secret": "fixture-password",
		"proxy_jump": "jump-user@jump.example.test:2222",
	}
	result := postJSON(t, admin, srv.URL+"/api/hosts/", info["csrf_token"].(string), body, 201)
	id := result["host"].(map[string]any)["id"].(string)
	owner := info["user"].(map[string]any)["id"].(string)
	host, err := db.HostByID(t.Context(), owner, id)
	if err != nil || host.ProxyJump != body["proxy_jump"] || host.Port != 22 || host.CredentialID == "" {
		t.Fatalf("password ProxyJump configuration lost: %+v %v", host, err)
	}
}
