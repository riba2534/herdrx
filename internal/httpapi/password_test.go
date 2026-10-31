package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/riba2534/herdrx/internal/store"
)

const (
	fixtureAdminPassword = "fixture-admin-password"
	fixtureNewPassword   = "fixture-changed-password"
)

func loginClient(t *testing.T, srv *httptest.Server, email, password string, want int) (*http.Client, map[string]any) {
	t.Helper()
	client := newTestClient(t)
	return client, postJSON(t, client, srv.URL+"/api/login", "", map[string]string{"email": email, "password": password}, want)
}

func registerMember(t *testing.T, srv *httptest.Server, admin *http.Client, adminCSRF string) (*http.Client, map[string]any) {
	t.Helper()
	invite := postJSON(t, admin, srv.URL+"/api/admin/invites", adminCSRF, nil, 201)
	member := newTestClient(t)
	result := postJSON(t, member, srv.URL+"/api/register", "", map[string]any{"email": "member@example.test", "password": "fixture-member-password", "display_name": "Member", "invite_code": invite["code"]}, 200)
	return member, result
}

func auditDump(t *testing.T, client *http.Client, srv *httptest.Server, csrf string) string {
	t.Helper()
	events := requestJSON(t, client, "GET", srv.URL+"/api/admin/audit?limit=100", csrf, nil, 200)
	var dump strings.Builder
	for _, raw := range events["events"].([]any) {
		event := raw.(map[string]any)
		details, _ := json.Marshal(event["details"])
		fmt.Fprintf(&dump, "%s %s %s\n", event["action"], event["target_id"], details)
	}
	return dump.String()
}

func dialStatus(t *testing.T, srv *httptest.Server, client *http.Client) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/hosts/hst_fixture/ws", &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Origin": []string{"http://example.test"}}})
	if err == nil {
		ws.CloseNow()
		return http.StatusSwitchingProtocols
	}
	if response == nil {
		t.Fatal(err)
	}
	return response.StatusCode
}

func TestChangePasswordValidatesAndKeepsOnlyCurrentLogin(t *testing.T) {
	calls := fakeHerdrSocket(t)
	_, db, srv, current, info := authFixture(t)
	csrf := info["csrf_token"].(string)
	id := info["user"].(map[string]any)["id"].(string)
	if err := db.CreateHost(context.Background(), store.Host{ID: "hst_fixture", OwnerID: id, Name: "Isolated fixture", Transport: "local", Port: 22}); err != nil {
		t.Fatal(err)
	}
	other, _ := loginClient(t, srv, "admin@example.test", fixtureAdminPassword, 200)
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	otherSocket := fixtureSocket(t, ctx, srv, other)
	currentSocket := fixtureSocket(t, ctx, srv, current)

	url := srv.URL + "/api/me/password"
	postJSON(t, current, url, "", map[string]string{"current_password": fixtureAdminPassword, "new_password": fixtureNewPassword}, 403)
	for _, tc := range []struct {
		current, next, code string
	}{
		{"wrong-admin-password", fixtureNewPassword, "invalid_current_password"},
		{"", fixtureNewPassword, "invalid_current_password"},
		{fixtureAdminPassword, "too-short", "invalid_password"},
		{fixtureAdminPassword, fixtureAdminPassword, "password_unchanged"},
	} {
		result := postJSON(t, current, url, csrf, map[string]string{"current_password": tc.current, "new_password": tc.next}, 400)
		if result["code"] != tc.code {
			t.Fatalf("%+v: code=%v", tc, result["code"])
		}
	}
	result := postJSON(t, current, url, csrf, map[string]string{"current_password": fixtureAdminPassword, "new_password": fixtureNewPassword}, 200)
	if result["revoked_sessions"] != float64(1) || result["user"].(map[string]any)["must_change_password"] != false {
		t.Fatalf("change result %v", result)
	}
	requestJSON(t, other, "GET", srv.URL+"/api/me", "", nil, 401)
	requestJSON(t, current, "GET", srv.URL+"/api/me", "", nil, 200)
	requestJSON(t, current, "GET", srv.URL+"/api/hosts/", "", nil, 200)
	for {
		if _, _, err := otherSocket.Read(ctx); err != nil {
			if websocket.CloseStatus(err) != 4401 {
				t.Fatalf("other login socket close=%v", err)
			}
			break
		}
	}
	// The current login keeps its socket: a short read times out instead of
	// observing a close frame.
	readCtx, readCancel := context.WithTimeout(ctx, 300*time.Millisecond)
	for {
		_, _, err := currentSocket.Read(readCtx)
		if err == nil {
			continue
		}
		if status := websocket.CloseStatus(err); status != -1 {
			t.Fatalf("current login socket closed with %d", status)
		}
		break
	}
	readCancel()
	if calls.Load() != 0 {
		t.Fatal("password change executed a remote Herdr operation")
	}
	loginClient(t, srv, "admin@example.test", fixtureAdminPassword, 401)
	loginClient(t, srv, "admin@example.test", fixtureNewPassword, 200)
	dump := auditDump(t, current, srv, csrf)
	if !strings.Contains(dump, "password.changed") || !strings.Contains(dump, "password.change_failed") {
		t.Fatalf("missing password audit:\n%s", dump)
	}
	for _, secret := range []string{fixtureAdminPassword, fixtureNewPassword, "wrong-admin-password"} {
		if strings.Contains(dump, secret) {
			t.Fatal("audit contains a password")
		}
	}
}

func TestConcurrentPasswordChangesOnlyOneWins(t *testing.T) {
	a, _, srv, client, info := authFixture(t)
	csrf := info["csrf_token"].(string)
	gate := make(chan struct{})
	a.hashPassword = func(password string) (string, error) { <-gate; return "test-only:" + password, nil }
	const attempts = 4
	statuses := make(chan int, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			request, _ := http.NewRequest("POST", srv.URL+"/api/me/password", strings.NewReader(`{"current_password":"`+fixtureAdminPassword+`","new_password":"fixture-racing-password-`+string(rune('a'+i))+`"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", "http://example.test")
			request.Header.Set("X-CSRF-Token", csrf)
			response, err := client.Do(request)
			if err != nil {
				t.Error(err)
				return
			}
			response.Body.Close()
			statuses <- response.StatusCode
		}()
	}
	time.Sleep(200 * time.Millisecond)
	close(gate)
	wg.Wait()
	close(statuses)
	wins := 0
	for status := range statuses {
		switch status {
		case 200:
			wins++
		case 409, 400, 503:
		default:
			t.Fatalf("unexpected status %d", status)
		}
	}
	if wins != 1 {
		t.Fatalf("%d concurrent password changes succeeded", wins)
	}
}

func TestAdminResetForcesPasswordChange(t *testing.T) {
	_, _, srv, admin, info := authFixture(t)
	adminCSRF := info["csrf_token"].(string)
	adminID := info["user"].(map[string]any)["id"].(string)
	member, registered := registerMember(t, srv, admin, adminCSRF)
	memberID := registered["user"].(map[string]any)["id"].(string)
	second, _ := loginClient(t, srv, "member@example.test", "fixture-member-password", 200)

	postJSON(t, member, srv.URL+"/api/admin/users/"+adminID+"/password-reset", registered["csrf_token"].(string), nil, 403)
	postJSON(t, admin, srv.URL+"/api/admin/users/"+memberID+"/password-reset", "", nil, 403)
	if result := postJSON(t, admin, srv.URL+"/api/admin/users/"+adminID+"/password-reset", adminCSRF, nil, 409); result["code"] != "self_reset" {
		t.Fatalf("self reset %v", result)
	}
	postJSON(t, admin, srv.URL+"/api/admin/users/usr_missing/password-reset", adminCSRF, nil, 404)
	reset := postJSON(t, admin, srv.URL+"/api/admin/users/"+memberID+"/password-reset", adminCSRF, nil, 200)
	temporary, _ := reset["temporary_password"].(string)
	if len(temporary) < minPasswordLength || reset["user_id"] != memberID {
		t.Fatalf("reset response %v", reset)
	}
	requestJSON(t, member, "GET", srv.URL+"/api/me", "", nil, 401)
	requestJSON(t, second, "GET", srv.URL+"/api/me", "", nil, 401)
	dump := auditDump(t, admin, srv, adminCSRF)
	if !strings.Contains(dump, "password.reset "+memberID) || strings.Contains(dump, temporary) {
		t.Fatalf("reset audit:\n%s", dump)
	}
	users := requestJSON(t, admin, "GET", srv.URL+"/api/admin/users", "", nil, 200)
	for _, raw := range users["users"].([]any) {
		if user := raw.(map[string]any); user["id"] == memberID && user["must_change_password"] != true {
			t.Fatal("admin list does not show the pending change")
		}
	}

	loginClient(t, srv, "member@example.test", "fixture-member-password", 401)
	forced, login := loginClient(t, srv, "member@example.test", temporary, 200)
	if login["user"].(map[string]any)["must_change_password"] != true {
		t.Fatalf("login does not report forced change: %v", login)
	}
	csrf := login["csrf_token"].(string)
	me := requestJSON(t, forced, "GET", srv.URL+"/api/me", "", nil, 200)
	if me["must_change_password"] != true {
		t.Fatalf("me %v", me)
	}
	for _, blocked := range []struct{ method, path string }{
		{"GET", "/api/hosts/"},
		{"GET", "/api/agents"},
		{"GET", "/api/me/workbench-session"},
		{"GET", "/api/push/config"},
		{"GET", "/api/cli-release"},
		{"POST", "/api/voice/sessions"},
		{"GET", "/api/voice/capabilities"},
	} {
		result := requestJSON(t, forced, blocked.method, srv.URL+blocked.path, csrf, map[string]any{}, 403)
		if result["code"] != "password_change_required" {
			t.Fatalf("%s %s: %v", blocked.method, blocked.path, result)
		}
	}
	if status := dialStatus(t, srv, forced); status != http.StatusForbidden {
		t.Fatalf("forced WebSocket status=%d", status)
	}
	// Logout stays available while the change is pending.
	extra, extraLogin := loginClient(t, srv, "member@example.test", temporary, 200)
	postJSON(t, extra, srv.URL+"/api/logout", extraLogin["csrf_token"].(string), nil, 200)

	postJSON(t, forced, srv.URL+"/api/me/password", csrf, map[string]string{"current_password": temporary, "new_password": fixtureNewPassword}, 200)
	if me := requestJSON(t, forced, "GET", srv.URL+"/api/me", "", nil, 200); me["must_change_password"] != false {
		t.Fatalf("flag not cleared: %v", me)
	}
	requestJSON(t, forced, "GET", srv.URL+"/api/hosts/", "", nil, 200)
	loginClient(t, srv, "member@example.test", fixtureNewPassword, 200)
}
