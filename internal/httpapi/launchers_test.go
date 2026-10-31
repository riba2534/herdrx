package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/config"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
)

func TestLaunchersFollowTheAccount(t *testing.T) {
	dataDir := t.TempDir()
	dataStore, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	vault, err := secure.OpenVault(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Addr: "127.0.0.1:0", DataDir: dataDir, PublicURL: "http://example.test", BootstrapToken: "bootstrap-test-token",
		SessionTTL: time.Hour, HerdrBinary: "herdr", AllowPrivateHosts: true, DERPRegionID: 304,
	}
	api, err := New(cfg, dataStore, vault, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	api.cliReleases.client.Transport = releaseTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("[]"))}, nil
	})
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	requestJSON(t, newTestClient(t), http.MethodGet, server.URL+"/api/me/launchers", "", nil, http.StatusUnauthorized)

	admin := newTestClient(t)
	bootstrap := postJSON(t, admin, server.URL+"/api/bootstrap", "", map[string]any{
		"email": "admin@example.test", "display_name": "Admin", "password": "correct-horse-battery-staple", "token": "bootstrap-test-token",
	}, http.StatusOK)
	csrf := bootstrap["csrf_token"].(string)
	empty := requestJSON(t, admin, http.MethodGet, server.URL+"/api/me/launchers", "", nil, http.StatusOK)
	if rows, _ := empty["launchers"].([]any); len(rows) != 0 {
		t.Fatalf("expected no custom launchers, got %#v", empty["launchers"])
	}
	requestJSON(t, admin, http.MethodPut, server.URL+"/api/me/launchers", "wrong", map[string]any{
		"launchers": []any{map[string]string{"id": "review", "name": "审查", "command": "codex review"}},
	}, http.StatusForbidden)
	requestJSON(t, admin, http.MethodPut, server.URL+"/api/me/launchers", csrf, map[string]any{
		"launchers": []any{map[string]string{"id": "review", "name": "审查", "command": "echo\nhi"}},
	}, http.StatusBadRequest)
	saved := requestJSON(t, admin, http.MethodPut, server.URL+"/api/me/launchers", csrf, map[string]any{
		"launchers": []any{map[string]string{"id": "review", "name": "审查", "command": "codex review"}},
	}, http.StatusOK)
	rows := saved["launchers"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["command"] != "codex review" {
		t.Fatalf("saved %#v", saved)
	}

	requestJSON(t, admin, "PATCH", server.URL+"/api/admin/settings", csrf, map[string]any{"registration": "invite", "revision": 0}, http.StatusOK)
	invite := postJSON(t, admin, server.URL+"/api/admin/invites", csrf, nil, http.StatusCreated)
	member := newTestClient(t)
	registered := postJSON(t, member, server.URL+"/api/register", "", map[string]any{
		"email": "member@example.test", "display_name": "Member", "password": "another-correct-password", "invite_code": invite["code"],
	}, http.StatusOK)
	memberRows := requestJSON(t, member, http.MethodGet, server.URL+"/api/me/launchers", "", nil, http.StatusOK)
	if rows, _ := memberRows["launchers"].([]any); len(rows) != 0 {
		t.Fatalf("member saw another account's launchers: %#v", memberRows)
	}
	if registered["user"].(map[string]any)["role"] != "user" {
		t.Fatal("invited member has the wrong role")
	}
}
