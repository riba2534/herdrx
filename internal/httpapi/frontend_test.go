package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/riba2534/herdrx/internal/config"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
)

func TestFrontendServesPrecompressedAssets(t *testing.T) {
	plainJS := []byte("console.log('herdrx-asset')")
	plainCSS := []byte("body{color:#142c3c}")
	gzJS := []byte("gzip-js-sidecar")
	brJS := []byte("br-js-sidecar")
	gzCSS := []byte("gzip-css-sidecar")
	brCSS := []byte("br-css-sidecar")
	inline := "document.documentElement.dataset.boot='1'"
	assets := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><script>" + inline + "</script><div id='root'></div>")},
		"assets/app.js":        {Data: plainJS},
		"assets/app.js.gz":     {Data: gzJS},
		"assets/app.js.br":     {Data: brJS},
		"assets/app.css":       {Data: plainCSS},
		"assets/app.css.gz":    {Data: gzCSS},
		"assets/app.css.br":    {Data: brCSS},
		"sw.js":                {Data: []byte("self.skipWaiting()")},
		"manifest.webmanifest": {Data: []byte(`{"name":"herdrx"}`)},
	}
	server := newFrontendServer(t, assets)
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}

	type want struct {
		accept       string
		path         string
		encoding     string
		body         []byte
		contentType  string
		cacheControl string
	}
	cases := []want{
		{accept: "gzip, deflate, br", path: "/assets/app.js", encoding: "br", body: brJS, contentType: "text/javascript; charset=utf-8", cacheControl: "public, max-age=31536000, immutable"},
		{accept: "gzip", path: "/assets/app.js", encoding: "gzip", body: gzJS, contentType: "text/javascript; charset=utf-8", cacheControl: "public, max-age=31536000, immutable"},
		{accept: "", path: "/assets/app.js", encoding: "", body: plainJS, contentType: "text/javascript; charset=utf-8", cacheControl: "public, max-age=31536000, immutable"},
		{accept: "gzip, br", path: "/assets/app.css", encoding: "br", body: brCSS, contentType: "text/css; charset=utf-8", cacheControl: "public, max-age=31536000, immutable"},
	}
	for _, tc := range cases {
		req, err := http.NewRequest(http.MethodGet, server.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tc.accept != "" {
			req.Header.Set("Accept-Encoding", tc.accept)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s %q: status %d", tc.path, tc.accept, res.StatusCode)
		}
		if got := res.Header.Get("Content-Encoding"); got != tc.encoding {
			t.Fatalf("%s %q: Content-Encoding %q, want %q", tc.path, tc.accept, got, tc.encoding)
		}
		if got := res.Header.Get("Vary"); got != "Accept-Encoding" {
			t.Fatalf("%s %q: Vary %q", tc.path, tc.accept, got)
		}
		if got := res.Header.Get("Content-Type"); got != tc.contentType {
			t.Fatalf("%s %q: Content-Type %q, want %q", tc.path, tc.accept, got, tc.contentType)
		}
		if got := res.Header.Get("Cache-Control"); got != tc.cacheControl {
			t.Fatalf("%s %q: Cache-Control %q", tc.path, tc.accept, got)
		}
		if !bytes.Equal(body, tc.body) {
			t.Fatalf("%s %q: unexpected body %q", tc.path, tc.accept, body)
		}
		if res.Header.Get("Content-Length") != strconv.Itoa(len(tc.body)) {
			t.Fatalf("%s %q: Content-Length %s, want %d", tc.path, tc.accept, res.Header.Get("Content-Length"), len(tc.body))
		}
	}

	reload, err := http.NewRequest(http.MethodGet, server.URL+"/assets/app.js", nil)
	if err != nil {
		t.Fatal(err)
	}
	reload.Header.Set("Accept-Encoding", "gzip, br")
	reload.Header.Set("Cache-Control", "no-cache")
	res, err := client.Do(reload)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.Header.Get("Content-Encoding") != "br" || !bytes.Equal(body, brJS) {
		t.Fatalf("cache:reload-style request did not receive brotli: encoding=%q", res.Header.Get("Content-Encoding"))
	}

	direct, err := client.Get(server.URL + "/assets/app.js.br")
	if err != nil {
		t.Fatal(err)
	}
	direct.Body.Close()
	if direct.StatusCode != http.StatusNotFound {
		t.Fatalf("compressed sidecar should not be a public URL, got %d", direct.StatusCode)
	}

	html, err := client.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	csp := html.Header.Get("Content-Security-Policy")
	html.Body.Close()
	sum := sha256.Sum256([]byte(inline))
	wantHash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if !strings.Contains(csp, wantHash) || !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("CSP missing inline script hash: %s", csp)
	}
}

func TestNegotiateAssetEncoding(t *testing.T) {
	if got := negotiateAssetEncoding("gzip, deflate, br", true, true); got != "br" {
		t.Fatalf("prefer br, got %q", got)
	}
	if got := negotiateAssetEncoding("gzip", true, true); got != "gzip" {
		t.Fatalf("gzip only, got %q", got)
	}
	if got := negotiateAssetEncoding("", true, true); got != "" {
		t.Fatalf("identity, got %q", got)
	}
	if got := negotiateAssetEncoding("br;q=0, gzip;q=1", true, true); got != "gzip" {
		t.Fatalf("q=0 br should fall through, got %q", got)
	}
}

func newFrontendServer(t *testing.T, assets fs.FS) *httptest.Server {
	t.Helper()
	dataDir := t.TempDir()
	dataStore, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dataStore.Close() })
	vault, err := secure.OpenVault(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Addr: "127.0.0.1:0", DataDir: dataDir, PublicURL: "http://example.test",
		SessionTTL: time.Hour, HerdrBinary: "herdr", AllowPrivateHosts: true,
	}
	api, err := New(cfg, dataStore, vault, assets, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { api.Close() })
	return httptest.NewServer(api.Handler())
}
