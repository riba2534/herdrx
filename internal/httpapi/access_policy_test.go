package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/store"
)

func TestRegistrationPolicyDefaultAndAdminBoundaries(t *testing.T) {
	_, db, srv, admin, info := authFixture(t, false)
	csrf := info["csrf_token"].(string)
	anon := newTestClient(t)
	status := requestJSON(t, anon, "GET", srv.URL+"/api/bootstrap/status", "", nil, 200)
	if status["required"] != false || status["registration"] != "closed" {
		t.Fatalf("default bootstrap status: %v", status)
	}
	postJSON(t, anon, srv.URL+"/api/register", "", map[string]any{"email": "x@example.test"}, 403)
	postJSON(t, admin, srv.URL+"/api/admin/invites", csrf, nil, 403)
	requestJSON(t, anon, "GET", srv.URL+"/api/admin/settings", "", nil, 401)
	requestJSON(t, admin, "PATCH", srv.URL+"/api/admin/settings", "", map[string]any{"registration": "invite", "revision": 0}, 403)
	for _, body := range []map[string]any{{"registration": "open", "revision": 0}, {"registration": "invite"}, {"registration": "invite", "revision": -1}, {"registration": "invite", "revision": 0, "role": "admin"}} {
		requestJSON(t, admin, "PATCH", srv.URL+"/api/admin/settings", csrf, body, 400)
	}
	requestJSON(t, admin, "PATCH", srv.URL+"/api/admin/settings", csrf, map[string]any{"registration": "invite", "revision": 0}, 200)
	requestJSON(t, admin, "PATCH", srv.URL+"/api/admin/settings", csrf, map[string]any{"registration": "closed", "revision": 0}, 409)
	inv := postJSON(t, admin, srv.URL+"/api/admin/invites", csrf, nil, 201)
	member := newTestClient(t)
	body := map[string]any{"email": "member@example.test", "display_name": "Member", "password": "fixture-member-password", "invite_code": inv["code"], "role": "admin"}
	postJSON(t, member, srv.URL+"/api/register", "", body, 400)
	delete(body, "role")
	m := postJSON(t, member, srv.URL+"/api/register", "", body, 200)
	requestJSON(t, member, "GET", srv.URL+"/api/admin/settings", "", nil, 403)
	requestJSON(t, member, "PATCH", srv.URL+"/api/admin/settings", m["csrf_token"].(string), map[string]any{"registration": "closed", "revision": 1}, 403)
	requestJSON(t, admin, "PATCH", srv.URL+"/api/admin/settings", csrf, map[string]any{"registration": "closed", "revision": 1}, 200)
	postJSON(t, anon, srv.URL+"/api/login", "", map[string]any{"email": "member@example.test", "password": "fixture-member-password"}, 200)
	for _, suffix := range []string{"?q=MEMBER&role=user&status=enabled", "?role=admin"} {
		result := requestJSON(t, admin, "GET", srv.URL+"/api/admin/users"+suffix, "", nil, 200)
		if len(result["users"].([]any)) != 1 {
			t.Fatalf("incorrect filter: %v", result)
		}
	}
	for _, suffix := range []string{"?role=owner", "?status=active"} {
		requestJSON(t, admin, "GET", srv.URL+"/api/admin/users"+suffix, "", nil, 400)
	}
	users, err := db.ListUsers(context.Background(), store.Page{}, store.UserFilter{Query: "%"})
	if err != nil || len(users) != 0 {
		t.Fatal("search wildcard was not literal")
	}
	events, err := db.ListAudit(context.Background(), store.AuditFilter{Action: "registration.changed"})
	if err != nil || len(events) != 2 {
		t.Fatalf("settings audit: %+v %v", events, err)
	}
}

func TestClosingRegistrationRejectsInFlightPasswordHash(t *testing.T) {
	for _, reopen := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed", true: "closed-and-reopened"}[reopen], func(t *testing.T) {
			a, db, srv, admin, info := authFixture(t)
			csrf := info["csrf_token"].(string)
			invite := postJSON(t, admin, srv.URL+"/api/admin/invites", csrf, nil, 201)
			started, finish := make(chan struct{}), make(chan struct{})
			a.hashPassword = func(string) (string, error) { close(started); <-finish; return "test-only:password", nil }
			status := make(chan int, 1)
			go func() {
				body, _ := json.Marshal(map[string]any{"email": "pending@example.test", "display_name": "Pending", "password": "fixture-password", "invite_code": invite["code"]})
				req, _ := http.NewRequest("POST", srv.URL+"/api/register", strings.NewReader(string(body)))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Origin", "http://example.test")
				response, err := http.DefaultClient.Do(req)
				if err != nil {
					status <- 0
					return
				}
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				status <- response.StatusCode
			}()
			<-started
			requestJSON(t, admin, "PATCH", srv.URL+"/api/admin/settings", csrf, map[string]any{"registration": "closed", "revision": 1}, 200)
			want := 403
			if reopen {
				requestJSON(t, admin, "PATCH", srv.URL+"/api/admin/settings", csrf, map[string]any{"registration": "invite", "revision": 2}, 200)
				want = 409
			}
			close(finish)
			if got := <-status; got != want {
				t.Fatalf("in-flight registration status=%d want=%d", got, want)
			}
			if count, err := db.UserCount(context.Background()); err != nil || count != 1 {
				t.Fatal("closed registration created a user")
			}
			invites, err := db.ListInvites(context.Background())
			if err != nil || len(invites) != 1 || invites[0].Status != "active" {
				t.Fatal("closed registration consumed invite")
			}
		})
	}
}

func TestLocalAndRemoteHostHTTPAuthorization(t *testing.T) {
	a, db, srv, admin, info := authFixture(t)
	ctx := context.Background()
	csrf := info["csrf_token"].(string)
	invite := postJSON(t, admin, srv.URL+"/api/admin/invites", csrf, nil, 201)
	member := newTestClient(t)
	m := postJSON(t, member, srv.URL+"/api/register", "", map[string]any{"email": "member@example.test", "display_name": "Member", "password": "fixture-password", "invite_code": invite["code"]}, 200)
	mcsrf := m["csrf_token"].(string)
	uid := m["user"].(map[string]any)["id"].(string)
	postJSON(t, member, srv.URL+"/api/hosts/", mcsrf, map[string]any{"name": "Local", "transport": "local"}, 403)
	local := postJSON(t, admin, srv.URL+"/api/hosts/", csrf, map[string]any{"name": "Local", "transport": "local"}, 201)["host"].(map[string]any)["id"].(string)
	ssh := postJSON(t, member, srv.URL+"/api/hosts/", mcsrf, map[string]any{"name": "Own SSH", "transport": "ssh", "hostname": "192.0.2.1", "username": "tester", "auth_method": "password", "secret": "test-only"}, 201)["host"].(map[string]any)["id"].(string)
	if err := db.CreateHost(ctx, store.Host{ID: "tailcat", OwnerID: uid, Name: "Own Tailcat", Transport: "tailcat", Port: 22}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ssh, "tailcat"} {
		requestJSON(t, member, "GET", srv.URL+"/api/hosts/"+id, "", nil, 200)
		requestJSON(t, admin, "GET", srv.URL+"/api/hosts/"+id, "", nil, 404)
	}
	deny := func(id string) {
		t.Helper()
		root := srv.URL + "/api/hosts/" + id
		for _, suffix := range []string{"", "/snapshot", "/ws"} {
			requestJSON(t, member, "GET", root+suffix, "", nil, 404)
		}
		requestJSON(t, member, "PATCH", root, mcsrf, map[string]any{"name": "Intruder"}, 404)
		requestJSON(t, member, "POST", root+"/trust-host-key", mcsrf, map[string]any{}, 404)
		requestJSON(t, member, "POST", root+"/panes/w1G:p1/paste-image", mcsrf, map[string]any{}, 404)
		requestJSON(t, member, "DELETE", root, mcsrf, nil, 404)
	}
	deny(local)
	// Simulate a legacy local record and role loss through an independent SQLite connection.
	raw, err := sql.Open("sqlite", filepath.Join(a.config.DataDir, "herdrx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.Exec(`UPDATE users SET role='admin' WHERE id=?`, uid); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateHost(ctx, store.Host{ID: "legacy", OwnerID: uid, Name: "Legacy local", Transport: "local", Port: 22}); err != nil {
		t.Fatal(err)
	}
	lease, err := a.access.attach(ctx, uid, "")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.release()
	id := "legacy"
	lease.hostID.Store(&id)
	if err = lease.check(); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`UPDATE users SET role='user' WHERE id=?`, uid); err != nil {
		t.Fatal(err)
	}
	if err = lease.check(); err == nil {
		t.Fatal("existing local access survived role loss")
	}
	if lease.ctx.Err() == nil {
		t.Fatal("existing local connection not cancelled")
	}
	deny("legacy")
	hosts := requestJSON(t, member, "GET", srv.URL+"/api/hosts/", "", nil, 200)["hosts"].([]any)
	if len(hosts) != 2 {
		t.Fatalf("member list contains legacy local: %+v", hosts)
	}
}
