package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/config"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
)

func TestBootstrapInviteAndTenantHostFlow(t *testing.T) {
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
	requestJSON(t, newTestClient(t), http.MethodGet, server.URL+"/api/cli-release", "", nil, http.StatusUnauthorized)
	admin := newTestClient(t)
	bootstrap := postJSON(t, admin, server.URL+"/api/bootstrap", "", map[string]any{
		"email": "admin@example.test", "display_name": "Admin", "password": "correct-horse-battery-staple", "token": "bootstrap-test-token",
	}, http.StatusOK)
	csrf := bootstrap["csrf_token"].(string)
	if result := requestJSON(t, admin, http.MethodGet, server.URL+"/api/cli-release", "", nil, http.StatusOK); result["status"] != "unpublished" {
		t.Fatal("authenticated release lookup failed")
	}
	postJSON(t, admin, server.URL+"/api/hosts/", "wrong", map[string]any{"name": "Local", "transport": "local"}, http.StatusForbidden)
	created := postJSON(t, admin, server.URL+"/api/hosts/", csrf, map[string]any{
		"name": "Local", "transport": "local", "hostname": "", "port": 0, "username": "", "session_name": "", "auth_method": "generated", "secret": "",
	}, http.StatusCreated)
	if created["host"].(map[string]any)["transport"] != "local" {
		t.Fatal("local host was not created")
	}
	requestJSON(t, admin, "PATCH", server.URL+"/api/admin/settings", csrf, map[string]any{"registration": "invite", "revision": 0}, 200)
	invite := postJSON(t, admin, server.URL+"/api/admin/invites", csrf, nil, http.StatusCreated)
	member := newTestClient(t)
	registered := postJSON(t, member, server.URL+"/api/register", "", map[string]any{
		"email": "member@example.test", "display_name": "Member", "password": "another-correct-password", "invite_code": invite["code"],
	}, http.StatusOK)
	if registered["user"].(map[string]any)["role"] != "user" {
		t.Fatal("invited member has the wrong role")
	}
	response, err := member.Get(server.URL + "/api/hosts/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var listed map[string]any
	if json.NewDecoder(response.Body).Decode(&listed) != nil {
		t.Fatal("could not decode empty host list")
	}
	hosts, ok := listed["hosts"].([]any)
	if !ok || len(hosts) != 0 {
		t.Fatal("member could see administrator hosts")
	}
	hostID := created["host"].(map[string]any)["id"].(string)
	hostURL := server.URL + "/api/hosts/" + hostID + "/"
	requestJSON(t, newTestClient(t), http.MethodPatch, hostURL, "", map[string]any{"name": "New"}, http.StatusUnauthorized)
	requestJSON(t, admin, http.MethodPatch, hostURL, "wrong", map[string]any{"name": "New"}, http.StatusForbidden)
	requestJSON(t, member, http.MethodPatch, hostURL, registered["csrf_token"].(string), map[string]any{"name": "Intruder"}, http.StatusNotFound)
	requestJSON(t, admin, http.MethodPatch, server.URL+"/api/hosts/missing/", csrf, map[string]any{"name": "New"}, http.StatusNotFound)
	for _, invalid := range []map[string]any{
		{}, {"name": "   "}, {"name": strings.Repeat("名", 81)}, {"name": "line\nbreak"}, {"name": "New", "hostname": "different-host"},
	} {
		requestJSON(t, admin, http.MethodPatch, hostURL, csrf, invalid, http.StatusBadRequest)
	}
	renamed := requestJSON(t, admin, http.MethodPatch, hostURL, csrf, map[string]any{"name": "  家里的工作站  "}, http.StatusOK)["host"].(map[string]any)
	if renamed["id"] != hostID || renamed["name"] != "家里的工作站" || renamed["transport"] != "local" {
		t.Fatalf("unexpected renamed host: %v", renamed)
	}
}

func TestBootstrapTokenPersistsAcrossRestart(t *testing.T) {
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
	cfg := config.Config{DataDir: dataDir, PublicURL: "http://example.test", SessionTTL: time.Hour, HerdrBinary: "herdr"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first, err := New(cfg, dataStore, vault, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	token := first.bootstrapToken
	first.Close()
	second, err := New(cfg, dataStore, vault, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if token == "" || second.bootstrapToken != token {
		t.Fatalf("bootstrap token changed across restart: %q != %q", second.bootstrapToken, token)
	}
}

func TestWorkbenchMethodAllowlistIncludesInteractiveMenus(t *testing.T) {
	for _, method := range []string{
		"pane.input.set", "pane.send_text", "pane.send_input", "pane.rename", "pane.swap",
		"tab.create", "tab.rename", "workspace.rename",
		"worktree.list", "worktree.create", "worktree.open", "worktree.remove",
	} {
		if !allowedMethod(method) {
			t.Errorf("expected %s to be allowed", method)
		}
	}
	if allowedMethod("server.stop") {
		t.Fatal("destructive server method must not be exposed")
	}
}

func newTestClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func postJSON(t *testing.T, client *http.Client, url, csrf string, body any, wantStatus int) map[string]any {
	t.Helper()
	return requestJSON(t, client, http.MethodPost, url, csrf, body, wantStatus)
}

func requestJSON(t *testing.T, client *http.Client, method, url, csrf string, body any, wantStatus int) map[string]any {
	t.Helper()
	encoded := []byte("{}")
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request, err := http.NewRequest(method, url, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://example.test")
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s status=%d want=%d payload=%v", method, url, response.StatusCode, wantStatus, payload)
	}
	return payload
}

func postMultipart(t *testing.T, client *http.Client, url, csrf, fieldName, fileName string, fileContent []byte, wantStatus int) map[string]any {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(fieldName, fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(fileContent); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Origin", "http://example.test")
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(response.Body).Decode(&payload)
	if response.StatusCode != wantStatus {
		t.Fatalf("POST multipart %s status=%d want=%d payload=%v", url, response.StatusCode, wantStatus, payload)
	}
	return payload
}

func TestPasteImageEndpointAndValidation(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dataDir)
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	if err := os.MkdirAll(filepath.Join(configDir, "herdr"), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(configDir, "herdr", "herdr.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	type pasteCall struct {
		Method string `json:"method"`
		Params struct {
			PaneID string   `json:"pane_id"`
			Text   string   `json:"text"`
			Keys   []string `json:"keys"`
		} `json:"params"`
	}
	calls := make(chan pasteCall, 8)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var call pasteCall
			if json.NewDecoder(conn).Decode(&call) == nil {
				calls <- call
				_ = json.NewEncoder(conn).Encode(map[string]any{"id": "herdrx", "result": map[string]any{"type": "ok"}})
			}
			_ = conn.Close()
		}
	}()
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
		Addr: "127.0.0.1:0", DataDir: dataDir, PublicURL: "http://example.test", BootstrapToken: "paste-test-token",
		SessionTTL: time.Hour, HerdrBinary: "herdr", AllowPrivateHosts: true,
	}
	api, err := New(cfg, dataStore, vault, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	// 1. Unauthenticated request -> 401
	unauthClient := newTestClient(t)
	postMultipart(t, unauthClient, server.URL+"/api/hosts/hst_unknown/panes/p1/paste-image", "", "file", "test.png", []byte("\x89PNG\r\n\x1a\nfake"), http.StatusUnauthorized)

	// 2. Setup user and host
	admin := newTestClient(t)
	bootstrap := postJSON(t, admin, server.URL+"/api/bootstrap", "", map[string]any{
		"email": "admin@example.test", "display_name": "Admin", "password": "correct-horse-battery-staple", "token": "paste-test-token",
	}, http.StatusOK)
	csrf := bootstrap["csrf_token"].(string)

	created := postJSON(t, admin, server.URL+"/api/hosts/", csrf, map[string]any{
		"name": "Local", "transport": "local", "hostname": "", "port": 0, "username": "", "session_name": "", "auth_method": "generated", "secret": "",
	}, http.StatusCreated)
	hostID := created["host"].(map[string]any)["id"].(string)

	// 3. Authenticated but missing CSRF -> 403
	postMultipart(t, admin, server.URL+"/api/hosts/"+hostID+"/panes/p1/paste-image", "", "file", "test.png", []byte("\x89PNG\r\n\x1a\nfake"), http.StatusForbidden)

	// 4. Invalid Host ID -> 404
	postMultipart(t, admin, server.URL+"/api/hosts/hst_nonexistent/panes/p1/paste-image", csrf, "file", "test.png", []byte("\x89PNG\r\n\x1a\nfake"), http.StatusNotFound)

	// 5. Invalid Pane ID -> 400
	postMultipart(t, admin, server.URL+"/api/hosts/"+hostID+"/panes/invalid;pane/paste-image", csrf, "file", "test.png", []byte("\x89PNG\r\n\x1a\nfake"), http.StatusBadRequest)

	// 6. Fake/Non-image content (text/plain) -> 400 invalid_image_type
	postMultipart(t, admin, server.URL+"/api/hosts/"+hostID+"/panes/p1/paste-image", csrf, "file", "fake.png", []byte("this is plain text not an image"), http.StatusBadRequest)

	// 7. Empty file -> 400
	postMultipart(t, admin, server.URL+"/api/hosts/"+hostID+"/panes/p1/paste-image", csrf, "file", "empty.png", []byte{}, http.StatusBadRequest)

	// 8. Valid PNG header with inject=false (only stage, skip socket inject call) -> 200 OK
	validPngHeader := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15c4")
	res := postMultipart(t, admin, server.URL+"/api/hosts/"+hostID+"/panes/p1/paste-image?inject=false", csrf, "file", "sample.png", validPngHeader, http.StatusOK)
	if res["ok"] != true {
		t.Fatalf("expected ok=true, got %v", res["ok"])
	}
	stagedPath, ok := res["path"].(string)
	if !ok || stagedPath == "" || !strings.HasSuffix(stagedPath, ".png") {
		t.Fatalf("expected valid staged .png path, got %v", res["path"])
	}
	if res["injected"] != false {
		t.Fatalf("expected injected=false, got %v", res["injected"])
	}

	// 9. Valid JPEG header with inject=false -> 200 OK
	validJpegHeader := append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x01\x00`\x00`\x00\x00"), bytes.Repeat([]byte{0}, 64)...)
	resJpeg := postMultipart(t, admin, server.URL+"/api/hosts/"+hostID+"/panes/p1/paste-image?inject=false", csrf, "file", "sample.jpg", validJpegHeader, http.StatusOK)
	if resJpeg["ok"] != true || !strings.HasSuffix(resJpeg["path"].(string), ".jpg") {
		t.Fatalf("expected valid staged .jpg path, got %v", resJpeg["path"])
	}

	// 10. Valid WebP header with inject=false -> 200 OK
	validWebpHeader := append([]byte("RIFF\x20\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{0}, 32)...)
	resWebp := postMultipart(t, admin, server.URL+"/api/hosts/"+hostID+"/panes/p1/paste-image?inject=false", csrf, "file", "sample.webp", validWebpHeader, http.StatusOK)
	if resWebp["ok"] != true || !strings.HasSuffix(resWebp["path"].(string), ".webp") {
		t.Fatalf("expected valid staged .webp path, got %v", resWebp["path"])
	}

	// Real frontend URL: encodeURIComponent('w27:p1') contains an escaped colon.
	// Verify staging AND actual RPC delivery, not just the inject=false shortcut.
	resPaste := postMultipart(t, admin, server.URL+"/api/hosts/"+hostID+"/panes/w27%3Ap1/paste-image", csrf, "file", "screenshot.png", validPngHeader, http.StatusOK)
	if resPaste["injected"] != true {
		t.Fatalf("paste was not delivered: %v", resPaste)
	}
	select {
	case call := <-calls:
		if call.Method != "pane.send_input" || call.Params.PaneID != "w27:p1" {
			t.Fatalf("wrong paste method or target: %+v", call)
		}
		if call.Params.Text != resPaste["path"] || len(call.Params.Keys) != 0 {
			t.Fatalf("image must be one exact-path paste without an Enter or trailing space: %+v", call)
		}
		content, err := os.ReadFile(call.Params.Text)
		if err != nil || !bytes.Equal(content, validPngHeader) {
			t.Fatalf("delivered path does not contain uploaded bytes: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("paste never reached Herdr")
	}
	for _, invalid := range []string{"w27%2Fp1", "w27%253Ap1", "w27%00p1"} {
		postMultipart(t, admin, server.URL+"/api/hosts/"+hostID+"/panes/"+invalid+"/paste-image", csrf, "file", "screenshot.png", validPngHeader, http.StatusBadRequest)
	}
}
