package httpapi

import (
	"context"
	"errors"
	"testing"

	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/store"
)

type startTestEndpoint struct {
	screenTestEndpoint
	result herdr.HerdrStartResult
	err    error
	starts int
}

func (e *startTestEndpoint) StartHerdr(context.Context) (herdr.HerdrStartResult, error) {
	e.starts++
	return e.result, e.err
}

type startTestPool struct {
	endpoint *startTestEndpoint
	closed   []string
}

func (p *startTestPool) Open(context.Context, store.Host) (herdr.Endpoint, error) {
	return p.endpoint, nil
}
func (p *startTestPool) CloseHost(id string) { p.closed = append(p.closed, id) }
func (*startTestPool) Close()                {}

func TestStartHerdrOnlyForOwnedSSHHostsWithCSRF(t *testing.T) {
	a, db, srv, client, admin := authFixture(t)
	endpoint := &startTestEndpoint{result: herdr.HerdrStartResult{Status: "started", Method: "systemd", Linger: "yes"}}
	pool := &startTestPool{endpoint: endpoint}
	a.hosts.Close()
	a.hosts = pool
	owner := admin["user"].(map[string]any)["id"].(string)
	csrf := admin["csrf_token"].(string)
	for _, host := range []store.Host{
		{ID: "hst_ssh", OwnerID: owner, Name: "SSH", Transport: "ssh", Hostname: "example.test", Port: 22},
		{ID: "hst_tailcat", OwnerID: owner, Name: "Tailcat", Transport: "tailcat", Port: 22},
	} {
		if err := db.CreateHost(t.Context(), host); err != nil {
			t.Fatal(err)
		}
	}
	postJSON(t, client, srv.URL+"/api/hosts/hst_ssh/herdr/start", "", nil, 403)
	if endpoint.starts != 0 {
		t.Fatal("a request without CSRF reached the host")
	}
	refused := postJSON(t, client, srv.URL+"/api/hosts/hst_tailcat/herdr/start", csrf, nil, 400)
	if refused["code"] != "unsupported_transport" {
		t.Fatalf("tailcat start %v", refused)
	}
	postJSON(t, client, srv.URL+"/api/hosts/hst_missing/herdr/start", csrf, nil, 404)

	started := postJSON(t, client, srv.URL+"/api/hosts/hst_ssh/herdr/start", csrf, nil, 200)
	result := started["result"].(map[string]any)
	if result["status"] != "started" || result["method"] != "systemd" || result["linger"] != "yes" {
		t.Fatalf("start result %v", started)
	}
	if len(pool.closed) != 1 || pool.closed[0] != "hst_ssh" {
		t.Fatalf("a fresh server must drop cached host state: %v", pool.closed)
	}

	endpoint.result = herdr.HerdrStartResult{Status: "running"}
	postJSON(t, client, srv.URL+"/api/hosts/hst_ssh/herdr/start", csrf, nil, 200)
	if len(pool.closed) != 1 {
		t.Fatalf("an already running server must keep the shared connection: %v", pool.closed)
	}

	endpoint.err = errors.New("ssh: unexpected EOF")
	failed := postJSON(t, client, srv.URL+"/api/hosts/hst_ssh/herdr/start", csrf, nil, 502)
	if failed["code"] != "herdr_start_failed" {
		t.Fatalf("failed start %v", failed)
	}
}
