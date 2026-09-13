package httpapi

import (
	"testing"
)

func TestWorkbenchSessionHandoffRoundTrip(t *testing.T) {
	_, _, srv, admin, info := authFixture(t)
	csrf := info["csrf_token"].(string)
	anon := newTestClient(t)
	requestJSON(t, anon, "GET", srv.URL+"/api/me/workbench-session", "", nil, 401)
	empty := requestJSON(t, admin, "GET", srv.URL+"/api/me/workbench-session", "", nil, 200)
	if empty["session"] != nil {
		t.Fatalf("fresh user should have no session: %v", empty)
	}
	created := postJSON(t, admin, srv.URL+"/api/hosts/", csrf, map[string]any{
		"name": "Desk", "transport": "local", "hostname": "", "port": 0, "username": "", "session_name": "", "auth_method": "generated", "secret": "",
	}, 201)
	hostID := created["host"].(map[string]any)["id"].(string)
	requestJSON(t, admin, "PUT", srv.URL+"/api/me/workbench-session", "", map[string]any{
		"host_id": hostID, "workspace_id": "w2", "tab_id": "w2:t1", "pane_id": "w2:p1", "device_id": "pc-device-1", "client_class": "desktop",
	}, 403)
	requestJSON(t, admin, "PUT", srv.URL+"/api/me/workbench-session", csrf, map[string]any{
		"host_id": hostID, "workspace_id": "w2", "tab_id": "w2:t1", "pane_id": "w2:p1", "device_id": "pc-device-1", "client_class": "tablet",
	}, 400)
	requestJSON(t, admin, "PUT", srv.URL+"/api/me/workbench-session", csrf, map[string]any{
		"host_id": "hst_missing", "workspace_id": "w2", "device_id": "pc-device-1", "client_class": "desktop",
	}, 404)

	invite := postJSON(t, admin, srv.URL+"/api/admin/invites", csrf, nil, 201)
	member := newTestClient(t)
	registered := postJSON(t, member, srv.URL+"/api/register", "", map[string]any{
		"email": "member@example.test", "display_name": "Member", "password": "fixture-member-password", "invite_code": invite["code"],
	}, 200)
	requestJSON(t, member, "PUT", srv.URL+"/api/me/workbench-session", registered["csrf_token"].(string), map[string]any{
		"host_id": hostID, "workspace_id": "w2", "device_id": "phone-device-1", "client_class": "mobile",
	}, 404)

	saved := requestJSON(t, admin, "PUT", srv.URL+"/api/me/workbench-session", csrf, map[string]any{
		"host_id": hostID, "workspace_id": "w2", "tab_id": "w2:t1", "pane_id": "w2:p1", "device_id": "pc-device-1", "client_class": "desktop",
	}, 200)
	session := saved["session"].(map[string]any)
	if session["host_id"] != hostID || session["workspace_id"] != "w2" || session["tab_id"] != "w2:t1" || session["pane_id"] != "w2:p1" || session["client_class"] != "desktop" {
		t.Fatalf("saved session: %v", session)
	}
	got := requestJSON(t, admin, "GET", srv.URL+"/api/me/workbench-session", "", nil, 200)
	restored := got["session"].(map[string]any)
	if restored["host_id"] != hostID || restored["workspace_id"] != "w2" || restored["pane_id"] != "w2:p1" {
		t.Fatalf("read session: %v", restored)
	}
	handoff := requestJSON(t, admin, "PUT", srv.URL+"/api/me/workbench-session", csrf, map[string]any{
		"host_id": hostID, "workspace_id": "w2", "tab_id": "w2:t1", "pane_id": "w2:p1", "device_id": "phone-device-9", "client_class": "mobile",
	}, 200)
	if handoff["session"].(map[string]any)["client_class"] != "mobile" || handoff["session"].(map[string]any)["device_id"] != "phone-device-9" {
		t.Fatalf("handoff write: %v", handoff)
	}
	again := requestJSON(t, admin, "GET", srv.URL+"/api/me/workbench-session", "", nil, 200)
	if again["session"].(map[string]any)["client_class"] != "mobile" {
		t.Fatalf("phone client did not become the last writer: %v", again)
	}
}
