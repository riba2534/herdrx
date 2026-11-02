package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/launchers"
	"github.com/riba2534/herdrx/internal/store"
)

// sessionTestEndpoint answers launcher discovery and the Herdr calls a session
// start makes, and records every call.
type sessionTestEndpoint struct {
	mu           sync.Mutex
	calls        []promptCall
	snapshot     herdr.Snapshot
	snapshotErr  error
	discovered   launchers.Result
	discoveries  atomic.Int32
	discoverGate chan struct{}
	createErr    error
	createRaw    string
	createGate   chan struct{}
	emptyReads   int
	sendErr      error
	// processInfo is the pane.process_info result; processErr fails the call.
	// processSequence, when set, answers the calls in order and then repeats its last entry.
	processInfo     string
	processErr      error
	processSequence []string
	beforeSend      func()
}

// newSessionTestEndpoint answers like a fresh pane whose zsh opened in the
// requested directory "/srv/proj one".
func newSessionTestEndpoint() *sessionTestEndpoint {
	return &sessionTestEndpoint{
		snapshot: herdr.Snapshot{
			Workspaces: []herdr.Workspace{{ID: "w1"}},
			Panes: []herdr.Pane{
				{ID: "w9:p1", WorkspaceID: "w9", TabID: "w9:t1", CWD: "/srv/proj one", ForegroundCWD: "/srv/proj one"},
				{ID: "w1:p7", WorkspaceID: "w1", TabID: "w1:t4", CWD: "/srv/proj one", ForegroundCWD: "/srv/proj one"},
			},
		},
		discovered:  launchers.Found([]launchers.Launcher{{Name: "xcc", Bypass: true, Fullscreen: true, Teammate: true}, {Name: "plain"}}),
		emptyReads:  2,
		processInfo: foregroundInfo(41, "zsh"),
	}
}

// foregroundInfo is a pane.process_info result whose foreground group leader
// has pid group; names are the foreground processes in order, pids from 40.
func foregroundInfo(group int, names ...string) string {
	processes := make([]map[string]any, 0, len(names))
	for index, name := range names {
		processes = append(processes, map[string]any{"pid": 40 + index + 1, "name": name, "argv": []string{name}})
	}
	encoded, _ := json.Marshal(map[string]any{"type": "pane_process_info", "process_info": map[string]any{"pane_id": "w9:p1", "shell_pid": 41, "foreground_process_group_id": group, "foreground_processes": processes}})
	return string(encoded)
}

// setPaneCWD sets the logical (OSC 7) and physical directory of every new pane.
func (e *sessionTestEndpoint) setPaneCWD(logical, physical string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for index := range e.snapshot.Panes {
		e.snapshot.Panes[index].CWD, e.snapshot.Panes[index].ForegroundCWD = logical, physical
	}
}

func (e *sessionTestEndpoint) Launchers(context.Context) (launchers.Result, error) {
	e.discoveries.Add(1)
	if e.discoverGate != nil {
		<-e.discoverGate
	}
	return e.discovered, nil
}

func (e *sessionTestEndpoint) Snapshot(context.Context) (herdr.Snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, promptCall{method: "session.snapshot"})
	return e.snapshot, e.snapshotErr
}

func (e *sessionTestEndpoint) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if (method == "workspace.create" || method == "tab.create") && e.createGate != nil {
		<-e.createGate
	}
	if method == "pane.send_input" && e.beforeSend != nil {
		e.beforeSend()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	values, _ := params.(map[string]any)
	e.calls = append(e.calls, promptCall{method, values})
	switch method {
	case "workspace.create":
		if e.createErr != nil {
			return nil, e.createErr
		}
		if e.createRaw != "" {
			return json.RawMessage(e.createRaw), nil
		}
		return json.RawMessage(`{"type":"workspace_created","workspace":{"workspace_id":"w9"},"tab":{"tab_id":"w9:t1","workspace_id":"w9"},"root_pane":{"pane_id":"w9:p1","workspace_id":"w9","tab_id":"w9:t1"}}`), nil
	case "tab.create":
		if e.createErr != nil {
			return nil, e.createErr
		}
		return json.RawMessage(`{"type":"tab_created","tab":{"tab_id":"w1:t4","workspace_id":"w1"},"root_pane":{"pane_id":"w1:p7","workspace_id":"w1","tab_id":"w1:t4"}}`), nil
	case "pane.read":
		text := "user@host ~/proj $ "
		if e.emptyReads > 0 {
			e.emptyReads--
			text = "\n\n"
		}
		return json.Marshal(map[string]any{"type": "pane_read", "read": map[string]any{"text": text}})
	case "pane.send_input":
		if e.sendErr != nil {
			return nil, e.sendErr
		}
		return json.RawMessage(`{}`), nil
	case "pane.process_info":
		if e.processErr != nil {
			return nil, e.processErr
		}
		if len(e.processSequence) > 0 {
			info := e.processSequence[0]
			if len(e.processSequence) > 1 {
				e.processSequence = e.processSequence[1:]
			}
			return json.RawMessage(info), nil
		}
		return json.RawMessage(e.processInfo), nil
	}
	return nil, errors.New("unexpected method " + method)
}

func (e *sessionTestEndpoint) callsOf(method string) []promptCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []promptCall
	for _, call := range e.calls {
		if call.method == method {
			out = append(out, call)
		}
	}
	return out
}

// assertNothingClosed fails when any call could end a pane, tab or workspace.
func (e *sessionTestEndpoint) assertNothingClosed(t *testing.T) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, call := range e.calls {
		if strings.HasSuffix(call.method, ".close") || strings.Contains(call.method, "kill") || call.method == "pane.send_keys" {
			t.Fatalf("session start called %s", call.method)
		}
	}
}

func (*sessionTestEndpoint) OpenTerminal(context.Context, herdr.TerminalOpen) (herdr.TerminalProcess, error) {
	return nil, errors.New("unexpected terminal")
}
func (*sessionTestEndpoint) StageImage(context.Context, string, io.Reader) (string, error) {
	return "", errors.New("unexpected")
}
func (*sessionTestEndpoint) Close() error { return nil }

type sessionTestPool struct {
	endpoint herdr.Endpoint
	openErr  error
	opened   atomic.Int32
}

func (p *sessionTestPool) Open(context.Context, store.Host) (herdr.Endpoint, error) {
	p.opened.Add(1)
	if p.openErr != nil {
		return nil, p.openErr
	}
	return p.endpoint, nil
}
func (*sessionTestPool) CloseHost(string) {}
func (*sessionTestPool) Close()           {}

type sessionFixture struct {
	api      *API
	db       *store.Store
	server   string
	client   *http.Client
	csrf     string
	owner    string
	endpoint *sessionTestEndpoint
	pool     *sessionTestPool
}

func newSessionFixture(t *testing.T) *sessionFixture {
	t.Helper()
	value, db, server, client, info := authFixture(t)
	value.stopBackground()
	value.hosts.Close()
	owner := info["user"].(map[string]any)["id"].(string)
	if err := db.CreateHost(context.Background(), store.Host{ID: "session-host", OwnerID: owner, Name: "Session", Transport: "ssh", Port: 22}); err != nil {
		t.Fatal(err)
	}
	endpoint := newSessionTestEndpoint()
	pool := &sessionTestPool{endpoint: endpoint}
	value.hosts = pool
	return &sessionFixture{api: value, db: db, server: server.URL, client: client, csrf: info["csrf_token"].(string), owner: owner, endpoint: endpoint, pool: pool}
}

func (f *sessionFixture) url(hostID, suffix string) string {
	return f.server + "/api/hosts/" + hostID + suffix
}

// addMemberHost creates another user's host and returns its id.
func (f *sessionFixture) addMemberHost(t *testing.T) string {
	t.Helper()
	member, err := f.api.createUserRecord(credentialsRequest{Email: "member@example.test", Password: "fixture-member-password", DisplayName: "Member"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.CreateUser(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	if err = f.db.CreateHost(context.Background(), store.Host{ID: "member-host", OwnerID: member.ID, Name: "Member", Transport: "ssh", Port: 22}); err != nil {
		t.Fatal(err)
	}
	return "member-host"
}

func getRaw(t *testing.T, client *http.Client, url, csrf string) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, body
}

func (f *sessionFixture) launchers(t *testing.T, want int) map[string]any {
	t.Helper()
	return requestJSON(t, f.client, "GET", f.url("session-host", "/launchers"), f.csrf, nil, want)
}

// age makes the host's remembered discovery look older.
func (f *sessionFixture) age(by time.Duration) {
	f.api.launcherLists.mu.Lock()
	defer f.api.launcherLists.mu.Unlock()
	if entry := f.api.launcherLists.entries["session-host"]; entry != nil {
		entry.settled = entry.settled.Add(-by)
	}
}

func TestLaunchersReturnsNamesAndFlagsAndCachesThem(t *testing.T) {
	f := newSessionFixture(t)
	response, body := getRaw(t, f.client, f.url("session-host", "/launchers"), f.csrf)
	if response.StatusCode != 200 || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d headers %v", response.StatusCode, response.Header)
	}
	want := `{"launchers":[{"name":"xcc","bypass":true,"fullscreen":true,"teammate":true},{"name":"plain","bypass":false,"fullscreen":false,"teammate":false}],"supported":true}`
	if strings.TrimSpace(string(body)) != want {
		t.Fatalf("body = %s", body)
	}
	f.launchers(t, 200)
	if n := f.endpoint.discoveries.Load(); n != 1 {
		t.Fatalf("discovery ran %d times within the cache window", n)
	}
	// The list is only reused for 60 seconds.
	f.age(launcherListTTL + time.Second)
	f.launchers(t, 200)
	if n := f.endpoint.discoveries.Load(); n != 2 {
		t.Fatalf("a stale list was served: %d discoveries", n)
	}
}

func TestLaunchersRequiresTheCSRFHeader(t *testing.T) {
	f := newSessionFixture(t)
	response, _ := getRaw(t, f.client, f.url("session-host", "/launchers"), "")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d", response.StatusCode)
	}
	if f.pool.opened.Load() != 0 || f.endpoint.discoveries.Load() != 0 {
		t.Fatal("a request without the CSRF header ran discovery")
	}
}

func TestLauncherFailuresAreResultsAndNotReusedByTheList(t *testing.T) {
	f := newSessionFixture(t)
	f.endpoint.discovered = launchers.Unsupported(launchers.ReasonTimeout)
	for range 2 {
		result := f.launchers(t, 200)
		if result["supported"] != false || result["reason"] != "timeout" || len(result["launchers"].([]any)) != 0 {
			t.Fatalf("result = %v", result)
		}
	}
	if n := f.endpoint.discoveries.Load(); n != 2 {
		t.Fatalf("the list reused a failed discovery: %d runs", n)
	}
	// An endpoint without the capability (no handle forwarding it) is an old agent.
	f.pool.endpoint = &promptTestEndpoint{}
	f.api.launcherLists = launcherCache{}
	result := f.launchers(t, 200)
	if result["supported"] != false || result["reason"] != "unsupported_agent" {
		t.Fatalf("result = %v", result)
	}
}

func TestConcurrentLauncherRequestsShareOneDiscovery(t *testing.T) {
	f := newSessionFixture(t)
	f.endpoint.discoverGate = make(chan struct{})
	var wait sync.WaitGroup
	for range 3 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response, _ := getRaw(t, f.client, f.url("session-host", "/launchers"), f.csrf)
			if response.StatusCode != 200 {
				t.Errorf("status %d", response.StatusCode)
			}
		}()
	}
	for f.endpoint.discoveries.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	// Give the other requests time to arrive while the first is still running.
	time.Sleep(50 * time.Millisecond)
	close(f.endpoint.discoverGate)
	wait.Wait()
	if n := f.endpoint.discoveries.Load(); n != 1 {
		t.Fatalf("discovery ran %d times", n)
	}
}

// A panicking discovery must still settle its entry, or every later request
// for the host would wait on it forever.
func TestLauncherCacheSettlesWhenDiscoveryPanics(t *testing.T) {
	var cache launcherCache
	waiter := make(chan launchers.Result, 1)
	started := make(chan struct{})
	func() {
		defer func() { _ = recover() }()
		cache.get(context.Background(), "h", listUsable, func() launchers.Result {
			go func() {
				close(started)
				waiter <- cache.get(context.Background(), "h", listUsable, func() launchers.Result { return launchers.Found(nil) })
			}()
			<-started
			time.Sleep(20 * time.Millisecond)
			panic("discovery bug")
		})
	}()
	select {
	case result := <-waiter:
		if result.Supported || result.Reason != launchers.ReasonReadFailed {
			t.Fatalf("waiter got %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a waiter hung on a panicked discovery")
	}
	if result := cache.get(context.Background(), "h", listUsable, func() launchers.Result { return launchers.Found(nil) }); !result.Supported {
		t.Fatalf("the host stayed broken after a panic: %+v", result)
	}
}

func TestLaunchersAndSessionsAreOwnerScoped(t *testing.T) {
	f := newSessionFixture(t)
	other := f.addMemberHost(t)
	for _, host := range []string{other, "no-such-host"} {
		result := requestJSON(t, f.client, "GET", f.url(host, "/launchers"), f.csrf, nil, 404)
		if result["code"] != "host_not_found" {
			t.Fatalf("result = %v", result)
		}
		result = postJSON(t, f.client, f.url(host, "/sessions"), f.csrf, map[string]any{"request_id": "request-1", "cwd": "/srv", "launcher": "claude", "target": map[string]any{"kind": "workspace"}}, 404)
		if result["code"] != "host_not_found" {
			t.Fatalf("result = %v", result)
		}
	}
	if f.pool.opened.Load() != 0 || f.endpoint.discoveries.Load() != 0 || len(f.endpoint.calls) != 0 {
		t.Fatal("a non-owner request reached the host")
	}
}

func TestLaunchersHostConnectionFailure(t *testing.T) {
	f := newSessionFixture(t)
	f.pool.openErr = errors.New("dial tcp: connection refused")
	if result := f.launchers(t, 502); result["code"] != "host_unreachable" {
		t.Fatalf("result = %v", result)
	}
}
