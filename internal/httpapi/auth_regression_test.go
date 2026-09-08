package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"github.com/riba2534/herdrx/internal/config"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/riba2534/herdrx/internal/testpaths"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// All data and identities are isolated; real password hashing is covered by the existing HTTP flow test.
func authFixture(t *testing.T, openRegistration ...bool) (*API, *store.Store, *httptest.Server, *http.Client, map[string]any) {
	t.Helper()
	dir := testpaths.ShortTempDir(t)
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	vault, err := secure.OpenVault(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(config.Config{DataDir: dir, PublicURL: "http://example.test", BootstrapToken: "fixture-bootstrap-token", SessionTTL: time.Hour, HerdrBinary: "unused-fixture-binary", AllowPrivateHosts: true}, db, vault, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	a.hashPassword = func(password string) (string, error) { return "test-only:" + password, nil }
	a.verifyPassword = func(hash, password string) bool { return hash == "test-only:"+password }
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(func() { srv.Close(); a.Close(); db.Close() })
	client := newTestClient(t)
	admin := postJSON(t, client, srv.URL+"/api/bootstrap", "", map[string]any{"email": "admin@example.test", "display_name": "Admin", "password": "fixture-admin-password", "token": "fixture-bootstrap-token"}, http.StatusOK)
	if len(openRegistration) == 0 || openRegistration[0] {
		requestJSON(t, client, "PATCH", srv.URL+"/api/admin/settings", admin["csrf_token"].(string), map[string]any{"registration": "invite", "revision": 0}, 200)
	}
	return a, db, srv, client, admin
}

func TestAuthRegressionAdminAndInviteBoundaries(t *testing.T) {
	_, db, srv, admin, info := authFixture(t)
	csrf := info["csrf_token"].(string)
	anon := newTestClient(t)
	requestJSON(t, anon, "GET", srv.URL+"/api/admin/invites", "", nil, 401)
	postJSON(t, admin, srv.URL+"/api/admin/invites", "", nil, 403)
	inv := postJSON(t, admin, srv.URL+"/api/admin/invites", csrf, nil, 201)
	member := newTestClient(t)
	result := postJSON(t, member, srv.URL+"/api/register", "", map[string]any{"email": "member@example.test", "password": "fixture-member-password", "display_name": "Member", "invite_code": inv["code"]}, 200)
	if result["user"].(map[string]any)["role"] != "user" {
		t.Fatal("registration gained admin")
	}
	requestJSON(t, member, "GET", srv.URL+"/api/admin/invites", "", nil, 403)
	postJSON(t, member, srv.URL+"/api/admin/invites", result["csrf_token"].(string), nil, 403)
	postJSON(t, anon, srv.URL+"/api/register", "", map[string]any{"email": "other@example.test", "password": "fixture-member-password", "display_name": "Other", "invite_code": inv["code"]}, 400)
	requestJSON(t, admin, "GET", srv.URL+"/api/admin/invites", "", nil, 200)
	requestJSON(t, admin, "PATCH", srv.URL+"/api/admin/settings", csrf, map[string]any{"registration": "closed", "revision": 1}, 200)
	postJSON(t, anon, srv.URL+"/api/register", "", map[string]any{"email": "closed@example.test", "password": "fixture-member-password", "display_name": "Closed", "invite_code": "any"}, 403)
	if count, err := db.UserCount(context.Background()); err != nil || count != 2 {
		t.Fatalf("unexpected count %d %v", count, err)
	}
	t.Log("PASS: anonymous/member admin access denied, write CSRF enforced, registration stays user, invite single-use, closed mode enforced")
}

func TestAuthRegressionCrossOriginLogin(t *testing.T) {
	a, db, srv, _, _ := authFixture(t)
	member, err := a.createUserRecord(credentialsRequest{Email: "member@example.test", Password: "fixture-member-password", DisplayName: "Member"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CreateUser(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	victim := newTestClient(t)
	// Exact body produced by an HTML form with enctype=text/plain and one input.
	body := "{\"email\":\"member@example.test\",\"password\":\"fixture-member-password\",\"display_name\":\"=\"}\r\n"
	req, err := http.NewRequest("POST", srv.URL+"/api/login", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Origin", "https://attacker.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	response, err := victim.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode == 200 {
		me := requestJSON(t, victim, "GET", srv.URL+"/api/me", "", nil, 200)
		t.Fatalf("cross-origin form login accepted and issued a valid session for %s", me["user"].(map[string]any)["role"])
	}
}

func TestAuthRegressionProxyLoginIsolation(t *testing.T) {
	a, _, srv, _, _ := authFixture(t)
	a.config.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	client := newTestClient(t)
	send := func(forwarded, email, password string) int {
		encoded, _ := json.Marshal(map[string]string{"email": email, "password": password})
		req, _ := http.NewRequest("POST", srv.URL+"/api/login", bytes.NewReader(encoded))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", forwarded)
		req.Header.Set("Origin", "http://example.test")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return response.StatusCode
	}
	for i := 0; i < 10; i++ {
		if status := send("192.0.2.10", "absent@example.test", "wrong-password"); status != 401 {
			t.Fatalf("unexpected initial status %d", status)
		}
	}
	status := send("192.0.2.20", "admin@example.test", "fixture-admin-password")
	if status != 200 {
		t.Fatalf("a separate forwarded client with correct credentials was blocked after 10 other-client failures: HTTP %d", status)
	}
}

func TestAuthRegressionRegistrationRejectsInvalidInviteBeforeHash(t *testing.T) {
	a, _, srv, _, _ := authFixture(t)
	var hashes atomic.Int32
	a.hashPassword = func(string) (string, error) { hashes.Add(1); return "unused", nil }
	postJSON(t, newTestClient(t), srv.URL+"/api/register", "", map[string]any{"email": "absent@example.test", "password": "fixture-long-password", "display_name": "Uninvited", "invite_code": "not-a-valid-invitation"}, 400)
	if hashes.Load() != 0 {
		t.Fatal("invalid invitation reached password hashing")
	}
}

func fakeHerdrSocket(t *testing.T) *atomic.Int32 {
	t.Helper()
	dir := testpaths.ShortTempDir(t)
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "herdr"), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "herdr", "herdr.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	calls := new(atomic.Int32)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var request struct {
					Method string `json:"method"`
				}
				if json.NewDecoder(conn).Decode(&request) != nil {
					return
				}
				if request.Method != "session.snapshot" {
					calls.Add(1)
				}
				_ = json.NewEncoder(conn).Encode(map[string]any{"id": "herdrx", "result": map[string]any{"type": "ok", "snapshot": map[string]any{}}})
			}()
		}
	}()
	return calls
}

func fixtureSocket(t *testing.T, ctx context.Context, srv *httptest.Server, client *http.Client) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/hosts/hst_fixture/ws", &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Origin": []string{"http://example.test"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	hello := []byte(`{"t":"hello","protocol":1,"browser_instance_id":"fixture-browser-0001"}`)
	if err := ws.Write(ctx, websocket.MessageText, hello); err != nil {
		t.Fatal(err)
	}
	for {
		_, raw, err := ws.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(raw, &m)
		if m["state"] == "ready" {
			return ws
		}
	}
}

func TestAuthRegressionWorkbenchRevocation(t *testing.T) {
	for _, mode := range []string{"logout", "expired-command", "expired-binary", "expired-resize", "expired-open", "expired-scroll", "expired-idle", "disabled-command", "deadline", "single-login", "all-logins", "disable-user", "workbench-stop"} {
		t.Run(mode, func(t *testing.T) {
			calls := fakeHerdrSocket(t)
			a, db, srv, admin, info := authFixture(t)
			id := info["user"].(map[string]any)["id"].(string)
			if err := db.CreateHost(context.Background(), store.Host{ID: "hst_fixture", OwnerID: id, Name: "Isolated fixture", Transport: "local", Port: 22}); err != nil {
				t.Fatal(err)
			}
			fixtureDB, err := sql.Open("sqlite", db.DatabasePath())
			if err != nil {
				t.Fatal(err)
			}
			defer fixtureDB.Close()
			if mode == "deadline" {
				_, err = fixtureDB.Exec("UPDATE sessions SET expires_at=? WHERE user_id=?", time.Now().Add(500*time.Millisecond).UTC().Format(time.RFC3339Nano), id)
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
			defer cancel()
			ws := fixtureSocket(t, ctx, srv, admin)
			switch mode {
			case "logout":
				postJSON(t, admin, srv.URL+"/api/logout", info["csrf_token"].(string), nil, 200)
			case "expired-command", "expired-binary", "expired-resize", "expired-open", "expired-scroll", "expired-idle":
				_, err = fixtureDB.Exec("UPDATE sessions SET expires_at=? WHERE user_id=?", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), id)
			case "disabled-command":
				_, err = fixtureDB.Exec("UPDATE users SET disabled=1 WHERE id=?", id)
			case "single-login":
				requestJSON(t, admin, "DELETE", srv.URL+"/api/admin/users/"+id+"/sessions/"+info["session_id"].(string), info["csrf_token"].(string), nil, 200)
			case "all-logins":
				requestJSON(t, admin, "DELETE", srv.URL+"/api/admin/users/"+id+"/sessions", info["csrf_token"].(string), nil, 200)
			case "disable-user":
				second := store.User{ID: "usr_second", Email: "second@example.test", DisplayName: "Second", Role: "admin", PasswordHash: "test-only:fixture-second-password"}
				if err = db.CreateUser(ctx, second); err != nil {
					t.Fatal(err)
				}
				client := newTestClient(t)
				login := postJSON(t, client, srv.URL+"/api/login", "", map[string]string{"email": second.Email, "password": "fixture-second-password"}, 200)
				requestJSON(t, client, "PATCH", srv.URL+"/api/admin/users/"+id, login["csrf_token"].(string), map[string]bool{"disabled": true}, 200)
			case "workbench-stop":
				a.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "expired-binary":
				_ = ws.Write(ctx, websocket.MessageBinary, []byte{0x74, 1, 3, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 65})
			case "expired-resize":
				_ = ws.Write(ctx, websocket.MessageBinary, []byte{0x74, 1, 4, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 80, 0, 24, 0})
			case "expired-open":
				_ = ws.Write(ctx, websocket.MessageText, []byte(`{"t":"terminal.open","id":"blocked-open","pane_id":"p_fixture","mode":"observe","cols":80,"rows":24}`))
			case "expired-scroll":
				_ = ws.Write(ctx, websocket.MessageText, []byte(`{"t":"call","id":"blocked-scroll","method":"terminal.scroll","params":{"stream_id":1,"lines":-3,"column":0,"row":0}}`))
			}
			if strings.HasSuffix(mode, "-command") {
				_ = ws.Write(ctx, websocket.MessageText, []byte(`{"t":"call","id":"forbidden","method":"pane.send_text","params":{"pane_id":"p_fixture","text":"must not execute"}}`))
				_ = ws.Write(ctx, websocket.MessageBinary, []byte{0x74, 1, 3, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 65})
			}
			for {
				_, _, err = ws.Read(ctx)
				if err != nil {
					break
				}
			}
			want := websocket.StatusCode(4401)
			if mode == "workbench-stop" {
				want = websocket.StatusServiceRestart
			}
			if websocket.CloseStatus(err) != want {
				t.Fatalf("close=%v, expected %d", err, want)
			}
			if calls.Load() != 0 {
				t.Fatal("revoked WebSocket executed a remote operation")
			}
		})
	}
}

func TestAuthLeaseIsolationAndLateAttach(t *testing.T) {
	a, db, _, _, info := authFixture(t)
	ctx := context.Background()
	id := info["user"].(map[string]any)["id"].(string)
	sid := info["session_id"].(string)
	if err := db.CreateSession(ctx, store.Session{ID: "ses_other", UserID: id, ExpiresAt: time.Now().Add(time.Hour)}, []byte("other-login-hash"), "", ""); err != nil {
		t.Fatal(err)
	}
	first, err := a.access.attach(ctx, id, sid)
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()
	second, err := a.access.attach(ctx, id, "ses_other")
	if err != nil {
		t.Fatal(err)
	}
	defer second.release()
	background, err := a.access.attach(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	defer background.release()
	if err = a.access.change(id, sid, false, func() error { return db.DeleteSession(ctx, sid, id) }); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(first.check(), errLoginEnded) || second.check() != nil || background.check() != nil {
		t.Fatal("single login revocation affected another consumer")
	}
	if late, err := a.access.attach(ctx, id, sid); err == nil {
		late.release()
		t.Fatal("revoked login reattached")
	}
	if err = a.access.change(id, "", false, func() error {
		return db.RevokeUserSessions(ctx, id, id, "", store.AuditEvent{UserID: id, Action: "test.revoke"})
	}); err != nil {
		t.Fatal(err)
	}
	if second.check() == nil || background.check() != nil {
		t.Fatal("all-login revocation must preserve opted-in background access")
	}
}

func TestHashSlotsBoundWorkAfterCancellation(t *testing.T) {
	a, _, _, _, _ := authFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	registration, ok := a.hashSlot(ctx, true)
	if !ok {
		t.Fatal("registration slot unavailable")
	}
	cancel()
	if release, ok := a.hashSlot(context.Background(), true); ok {
		release()
		t.Fatal("canceled hashing released its registration slot before computation ended")
	}
	login, ok := a.hashSlot(context.Background(), false)
	if !ok {
		t.Fatal("registration consumed the login reserve")
	}
	if release, ok := a.hashSlot(context.Background(), false); ok {
		release()
		t.Fatal("global hashing capacity exceeded")
	}
	registration()
	login()
	release, ok := a.hashSlot(context.Background(), true)
	if !ok {
		t.Fatal("hash capacity leaked")
	}
	release()
}

func TestOriginJSONAndProxyBoundaries(t *testing.T) {
	a, _, srv, _, _ := authFixture(t)
	a.config.AllowedOrigins = []string{"https://alias.example.test"}
	for _, test := range []struct {
		origin, referer, site, media string
		status                       int
	}{
		{"https://attacker.example", "", "cross-site", "text/plain", 403},
		{"http://example.test", "", "same-origin", "text/plain", 415},
		{"http://example.test", "", "cross-site", "application/json", 403},
		{"null", "http://example.test/login", "", "application/json", 403},
		{"", "", "", "application/json", 403},
		{"http://example.test", "", "same-origin", "application/json", 200},
		{"https://alias.example.test", "", "same-site", "application/json; charset=utf-8", 200},
		{"", "http://example.test/login", "", "application/json", 200},
	} {
		req, _ := http.NewRequest("POST", srv.URL+"/api/login", strings.NewReader(`{"email":"admin@example.test","password":"fixture-admin-password"}`))
		req.Header.Set("Origin", test.origin)
		req.Header.Set("Referer", test.referer)
		req.Header.Set("Sec-Fetch-Site", test.site)
		req.Header.Set("Content-Type", test.media)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != test.status {
			t.Fatalf("%+v: got %d", test, res.StatusCode)
		}
	}
	a.config.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("192.0.2.1/32"), netip.MustParsePrefix("2001:db8::1/128")}
	for _, test := range []struct{ peer, xff, want string }{
		{"198.51.100.10:1234", "203.0.113.99", "198.51.100.10"},
		{"192.0.2.1:1234", "203.0.113.99, 198.51.100.10", "198.51.100.10"},
		{"192.0.2.1:1234", "198.51.100.10, 192.0.2.1", "198.51.100.10"},
		{"192.0.2.1:1234", "garbage, 198.51.100.10", "192.0.2.1"},
		{"[2001:db8::1]:1234", "2001:db8:1::5", "2001:db8:1::5"},
		{"[2001:db8::2]:1234", "203.0.113.1", "2001:db8::2"},
		{"192.0.2.1:1234", "198.51.100.10, 2001:db8::1", "198.51.100.10"},
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = test.peer
		req.Header.Set("X-Forwarded-For", test.xff)
		if got := a.clientIP(req); got != test.want {
			t.Fatalf("%+v: got %s", test, got)
		}
	}
}

func TestLogoutIdempotenceAndDatabaseFailure(t *testing.T) {
	a, db, srv, admin, info := authFixture(t)
	postJSON(t, admin, srv.URL+"/api/logout", "wrong", nil, 403)
	postJSON(t, admin, srv.URL+"/api/logout", info["csrf_token"].(string), nil, 200)
	postJSON(t, admin, srv.URL+"/api/logout", "", nil, 200)
	login := postJSON(t, admin, srv.URL+"/api/login", "", map[string]string{"email": "admin@example.test", "password": "fixture-admin-password"}, 200)
	a.access.close()
	db.Close()
	postJSON(t, admin, srv.URL+"/api/logout", login["csrf_token"].(string), nil, 503)
	requestJSON(t, admin, "GET", srv.URL+"/api/me", "", nil, 503)
}

func TestCanceledRegistrationRetainsHashCapacityUntilWorkEnds(t *testing.T) {
	a, _, srv, admin, info := authFixture(t)
	inv := postJSON(t, admin, srv.URL+"/api/admin/invites", info["csrf_token"].(string), nil, 201)
	started, finish := make(chan struct{}), make(chan struct{})
	a.hashPassword = func(string) (string, error) {
		close(started)
		<-finish
		return "test-only:fixture-member-password", nil
	}
	body, _ := json.Marshal(map[string]string{"email": "member@example.test", "password": "fixture-member-password", "display_name": "Member", "invite_code": inv["code"].(string)})
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest("POST", "/api/register", bytes.NewReader(body)).WithContext(ctx)
	request.Header.Set("Origin", "http://example.test")
	request.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() { a.Handler().ServeHTTP(httptest.NewRecorder(), request); close(done) }()
	<-started
	cancel()
	postJSON(t, newTestClient(t), srv.URL+"/api/register", "", map[string]string{"email": "other@example.test", "password": "fixture-member-password", "display_name": "Other", "invite_code": inv["code"].(string)}, 503)
	postJSON(t, newTestClient(t), srv.URL+"/api/login", "", map[string]string{"email": "admin@example.test", "password": "fixture-admin-password"}, 200)
	close(finish)
	<-done
	a.hashPassword = func(string) (string, error) { return "test-only:fixture-member-password", nil }
	postJSON(t, newTestClient(t), srv.URL+"/api/register", "", map[string]string{"email": "member@example.test", "password": "fixture-member-password", "display_name": "Member", "invite_code": inv["code"].(string)}, 200)
}

func TestLateAttachmentRacesWithRevocation(t *testing.T) {
	a, db, _, _, info := authFixture(t)
	id := info["user"].(map[string]any)["id"].(string)
	ctx := context.Background()
	for i := range 25 {
		sid := fmt.Sprintf("race-login-%d", i)
		if err := db.CreateSession(ctx, store.Session{ID: sid, UserID: id, ExpiresAt: time.Now().Add(time.Hour)}, []byte(sid), "", ""); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		attached := make(chan *accessLease)
		revoked := make(chan error, 1)
		go func() { <-start; lease, _ := a.access.attach(ctx, id, sid); attached <- lease }()
		go func() {
			<-start
			revoked <- a.access.change(id, sid, false, func() error { return db.DeleteSession(ctx, sid, id) })
		}()
		close(start)
		lease := <-attached
		if err := <-revoked; err != nil {
			t.Fatal(err)
		}
		if lease != nil {
			if lease.check() == nil {
				t.Error("a raced login remained authorized")
			}
			lease.release()
		}
	}
}

func TestAdminAPIFieldAndPermissionBoundaries(t *testing.T) {
	a, _, srv, admin, info := authFixture(t)
	id := info["user"].(map[string]any)["id"].(string)
	csrf := info["csrf_token"].(string)
	inv := postJSON(t, admin, srv.URL+"/api/admin/invites", csrf, nil, 201)
	member := newTestClient(t)
	m := postJSON(t, member, srv.URL+"/api/register", "", map[string]any{"email": "member@example.test", "password": "fixture-member-password", "display_name": "Member", "invite_code": inv["code"]}, 200)
	memberID := m["user"].(map[string]any)["id"].(string)
	for _, path := range []string{"/api/admin/users", "/api/admin/users/" + id + "/sessions", "/api/admin/invites", "/api/admin/audit"} {
		requestJSON(t, newTestClient(t), "GET", srv.URL+path, "", nil, 401)
		requestJSON(t, member, "GET", srv.URL+path, "", nil, 403)
		result := requestJSON(t, admin, "GET", srv.URL+path, "", nil, 200)
		raw, _ := json.Marshal(result)
		for _, forbidden := range []string{"password_hash", "token_hash", "csrf_token", "code_hash", "private_key"} {
			if bytes.Contains(raw, []byte(forbidden)) {
				t.Fatalf("admin list exposed %s", forbidden)
			}
		}
	}
	requestJSON(t, member, "PATCH", srv.URL+"/api/admin/users/"+id, m["csrf_token"].(string), map[string]bool{"disabled": true}, 403)
	requestJSON(t, admin, "PATCH", srv.URL+"/api/admin/users/"+memberID, "wrong", map[string]bool{"disabled": true}, 403)
	requestJSON(t, admin, "PATCH", srv.URL+"/api/admin/users/"+memberID, csrf, map[string]any{"disabled": false, "role": "admin"}, 400)
	requestJSON(t, admin, "GET", srv.URL+"/api/admin/users?limit=101", "", nil, 400)
	requestJSON(t, admin, "GET", srv.URL+"/api/admin/audit?since=bad", "", nil, 400)
	background, err := a.access.attach(context.Background(), memberID, "")
	if err != nil {
		t.Fatal(err)
	}
	defer background.release()
	requestJSON(t, admin, "PATCH", srv.URL+"/api/admin/users/"+memberID, csrf, map[string]bool{"disabled": true}, 200)
	if background.check() == nil {
		t.Fatal("disabled background access continued")
	}
}

func TestDisabledUserCannotFinishInFlightLogin(t *testing.T) {
	a, db, srv, admin, info := authFixture(t)
	user := store.User{ID: "member", Email: "member@example.test", DisplayName: "Member", Role: "user", PasswordHash: "test-only:fixture-member-password"}
	if err := db.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	started, finish := make(chan struct{}), make(chan struct{})
	a.verifyPassword = func(string, string) bool { close(started); <-finish; return true }
	status := make(chan int, 1)
	go func() {
		request, _ := http.NewRequest("POST", srv.URL+"/api/login", strings.NewReader(`{"email":"member@example.test","password":"fixture-member-password"}`))
		request.Header.Set("Origin", "http://example.test")
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			status <- 0
			return
		}
		defer response.Body.Close()
		status <- response.StatusCode
	}()
	<-started
	requestJSON(t, admin, "PATCH", srv.URL+"/api/admin/users/member", info["csrf_token"].(string), map[string]bool{"disabled": true}, 200)
	close(finish)
	if got := <-status; got != 401 {
		t.Fatalf("disabled user finished login: %d", got)
	}
	sessions, err := db.ListSessions(context.Background(), "member", store.Page{})
	if err != nil || len(sessions) != 0 {
		t.Fatal("in-flight login left a valid session")
	}
}

func TestAllInvalidInviteStatesAvoidHashing(t *testing.T) {
	a, db, srv, admin, info := authFixture(t)
	csrf := info["csrf_token"].(string)
	revoked := postJSON(t, admin, srv.URL+"/api/admin/invites", csrf, nil, 201)
	requestJSON(t, admin, "DELETE", srv.URL+"/api/admin/invites/"+revoked["invite"].(map[string]any)["id"].(string), csrf, nil, 200)
	used := postJSON(t, admin, srv.URL+"/api/admin/invites", csrf, nil, 201)
	postJSON(t, newTestClient(t), srv.URL+"/api/register", "", map[string]any{"email": "used@example.test", "password": "fixture-member-password", "display_name": "Used", "invite_code": used["code"]}, 200)
	if err := db.CreateInvite(context.Background(), store.Invite{ID: "expired", CreatedBy: info["user"].(map[string]any)["id"].(string), ExpiresAt: time.Now().Add(-time.Hour)}, secure.TokenHash("expired-code")); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	a.hashPassword = func(string) (string, error) { calls.Add(1); return "unused", nil }
	for _, code := range []string{"missing-code", "expired-code", revoked["code"].(string), used["code"].(string)} {
		postJSON(t, newTestClient(t), srv.URL+"/api/register", "", map[string]any{"email": "other@example.test", "password": "fixture-member-password", "display_name": "Other", "invite_code": code}, 400)
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid invitation reached the hash function %d times", calls.Load())
	}
}
