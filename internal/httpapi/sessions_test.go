package httpapi

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/launchers"
	"github.com/riba2534/herdrx/internal/store"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func sessionBody(requestID, launcher string, target map[string]any) map[string]any {
	return map[string]any{"request_id": requestID, "cwd": "/srv/proj one", "launcher": launcher, "target": target}
}

var newWorkspace = map[string]any{"kind": "workspace"}

func (f *sessionFixture) start(t *testing.T, body map[string]any, want int) map[string]any {
	t.Helper()
	return postJSON(t, f.client, f.url("session-host", "/sessions"), f.csrf, body, want)
}

func TestStartSessionOpensAWorkspaceAndTypesOneLine(t *testing.T) {
	f := newSessionFixture(t)
	body := sessionBody("request-0001", "xcc", newWorkspace)
	body["label"] = "Demo 演示"
	result := f.start(t, body, 200)
	sessionID, _ := result["session_id"].(string)
	if result["workspace_id"] != "w9" || result["tab_id"] != "w9:t1" || result["pane_id"] != "w9:p1" || result["launcher"] != "xcc" || !uuidV4.MatchString(sessionID) {
		t.Fatalf("result = %v", result)
	}
	creates := f.endpoint.callsOf("workspace.create")
	if len(creates) != 1 || creates[0].params["cwd"] != "/srv/proj one" || creates[0].params["focus"] != false || creates[0].params["label"] != "Demo 演示" {
		t.Fatalf("creates = %+v", creates)
	}
	// The shell prompt is awaited: two empty reads, then the prompt.
	reads := f.endpoint.callsOf("pane.read")
	if len(reads) != 3 || reads[0].params["pane_id"] != "w9:p1" || reads[0].params["source"] != "visible" {
		t.Fatalf("reads = %+v", reads)
	}
	sends := f.endpoint.callsOf("pane.send_input")
	if len(sends) != 1 || sends[0].params["pane_id"] != "w9:p1" || sends[0].params["text"] != "xcc --session-id "+sessionID {
		t.Fatalf("sends = %+v", sends)
	}
	if keys, _ := sends[0].params["keys"].([]string); len(keys) != 1 || keys[0] != "Enter" {
		t.Fatalf("keys = %#v", sends[0].params["keys"])
	}
	f.endpoint.assertNothingClosed(t)

	entries, err := f.db.ListAudit(context.Background(), store.AuditFilter{Action: "session.start"})
	if err != nil || len(entries) != 1 || entries[0].TargetID != "session-host" || entries[0].UserID != f.owner {
		t.Fatalf("audit = %+v %v", entries, err)
	}
	details := string(entries[0].Details)
	for _, expected := range []string{`"pane_id":"w9:p1"`, `"launcher":"xcc"`, `"cwd":"/srv/proj one"`, `"result":"started"`} {
		if !strings.Contains(details, expected) {
			t.Fatalf("audit lost %s: %s", expected, details)
		}
	}
}

func TestStartSessionInAnExistingWorkspaceOpensATab(t *testing.T) {
	f := newSessionFixture(t)
	result := f.start(t, sessionBody("request-0002", "claude", map[string]any{"kind": "tab", "workspace_id": "w1"}), 200)
	if result["workspace_id"] != "w1" || result["tab_id"] != "w1:t4" || result["pane_id"] != "w1:p7" || result["launcher"] != "claude" {
		t.Fatalf("result = %v", result)
	}
	creates := f.endpoint.callsOf("tab.create")
	if len(creates) != 1 || creates[0].params["workspace_id"] != "w1" || creates[0].params["cwd"] != "/srv/proj one" || creates[0].params["focus"] != false {
		t.Fatalf("creates = %+v", creates)
	}
	if _, ok := creates[0].params["label"]; ok {
		t.Fatal("an empty label was sent")
	}
	// claude needs no discovery.
	if f.endpoint.discoveries.Load() != 0 {
		t.Fatal("claude was checked against the discovered list")
	}

	missing := f.start(t, sessionBody("request-0003", "claude", map[string]any{"kind": "tab", "workspace_id": "w404"}), 404)
	if missing["code"] != "workspace_not_found" || len(f.endpoint.callsOf("tab.create")) != 1 {
		t.Fatalf("missing workspace: %v", missing)
	}
}

func TestStartSessionValidatesEveryField(t *testing.T) {
	f := newSessionFixture(t)
	cases := []struct {
		code string
		edit func(map[string]any)
	}{
		{"invalid_request_id", func(b map[string]any) { b["request_id"] = "short" }},
		{"invalid_request_id", func(b map[string]any) { b["request_id"] = "has space 1234" }},
		{"invalid_request_id", func(b map[string]any) { b["request_id"] = strings.Repeat("a", 65) }},
		{"invalid_cwd", func(b map[string]any) { b["cwd"] = "relative/dir" }},
		{"invalid_cwd", func(b map[string]any) { b["cwd"] = "" }},
		{"invalid_cwd", func(b map[string]any) { b["cwd"] = "/srv\n; rm -rf ~" }},
		{"invalid_cwd", func(b map[string]any) { b["cwd"] = "/srv\x1b[2J" }},
		{"invalid_cwd", func(b map[string]any) { b["cwd"] = "/" + strings.Repeat("a", 4096) }},
		{"invalid_launcher", func(b map[string]any) { b["launcher"] = "xcc; id" }},
		{"invalid_launcher", func(b map[string]any) { b["launcher"] = "xcc --dangerously-skip-permissions" }},
		{"invalid_launcher", func(b map[string]any) { b["launcher"] = "$(id)" }},
		{"invalid_launcher", func(b map[string]any) { b["launcher"] = "-x" }},
		{"invalid_launcher", func(b map[string]any) { b["launcher"] = "" }},
		{"invalid_launcher", func(b map[string]any) { b["launcher"] = "xcc\n" }},
		{"invalid_label", func(b map[string]any) { b["label"] = strings.Repeat("名", 65) }},
		{"invalid_label", func(b map[string]any) { b["label"] = "two\nlines" }},
		{"invalid_target", func(b map[string]any) { b["target"] = map[string]any{"kind": "pane"} }},
		{"invalid_target", func(b map[string]any) { b["target"] = map[string]any{"kind": "tab"} }},
		{"invalid_target", func(b map[string]any) { b["target"] = map[string]any{"kind": "tab", "workspace_id": "../w1"} }},
		{"invalid_target", func(b map[string]any) { b["target"] = map[string]any{"kind": "workspace", "workspace_id": "w1"} }},
		{"invalid_target", func(b map[string]any) { delete(b, "target") }},
		{"invalid_request", func(b map[string]any) { b["command"] = "id" }},
	}
	for _, tc := range cases {
		body := sessionBody("request-0004", "xcc", newWorkspace)
		tc.edit(body)
		if result := f.start(t, body, 400); result["code"] != tc.code {
			t.Fatalf("body %v: result %v, want %s", body, result, tc.code)
		}
	}
	body := sessionBody("request-0004", "xcc", newWorkspace)
	body["label"] = strings.Repeat("名", 64)
	body["cwd"] = "/" + strings.Repeat("a", 4095)
	f.start(t, body, 200)
	postJSON(t, f.client, f.url("session-host", "/sessions"), "", sessionBody("request-0005", "xcc", newWorkspace), 403)
	if len(f.endpoint.callsOf("workspace.create")) != 1 {
		t.Fatal("an invalid request created something")
	}
}

func TestStartSessionRejectsAnUndiscoveredLauncher(t *testing.T) {
	f := newSessionFixture(t)
	result := f.start(t, sessionBody("request-0006", "yolo", newWorkspace), 409)
	if result["code"] != "launcher_unknown" || len(f.endpoint.callsOf("workspace.create")) != 0 {
		t.Fatalf("result = %v", result)
	}
	// Discovery is unavailable: only claude can start.
	f.endpoint.discovered = launchers.Unsupported(launchers.ReasonShellUnavailable)
	f.api.launcherLists = launcherCache{}
	f.start(t, sessionBody("request-0007", "xcc", newWorkspace), 409)
	f.start(t, sessionBody("request-0008", "claude", newWorkspace), 200)
	// A rejected request created nothing, so the same id runs once the alias exists.
	f.endpoint.discovered = launchers.Found([]launchers.Launcher{{Name: "yolo"}})
	f.api.launcherLists = launcherCache{}
	if result := f.start(t, sessionBody("request-0006", "yolo", newWorkspace), 200); result["launcher"] != "yolo" {
		t.Fatalf("retry = %v", result)
	}
}

func TestStartSessionReusesTheCachedLauncherList(t *testing.T) {
	f := newSessionFixture(t)
	requestJSON(t, f.client, "GET", f.url("session-host", "/launchers"), "", nil, 200)
	f.start(t, sessionBody("request-0009", "xcc", newWorkspace), 200)
	f.start(t, sessionBody("request-0010", "plain", newWorkspace), 200)
	if n := f.endpoint.discoveries.Load(); n != 1 {
		t.Fatalf("discovery ran %d times", n)
	}
}

func TestStartSessionRepeatsTheFirstResultForTheSameRequestID(t *testing.T) {
	f := newSessionFixture(t)
	first := f.start(t, sessionBody("request-0011", "xcc", newWorkspace), 200)
	again := f.start(t, sessionBody("request-0011", "plain", newWorkspace), 200)
	if again["session_id"] != first["session_id"] || again["launcher"] != "xcc" {
		t.Fatalf("first %v, again %v", first, again)
	}
	if n := len(f.endpoint.callsOf("workspace.create")); n != 1 {
		t.Fatalf("created %d workspaces", n)
	}
	// Another id starts another session.
	if other := f.start(t, sessionBody("request-0012", "xcc", newWorkspace), 200); other["session_id"] == first["session_id"] {
		t.Fatal("a new request reused a session id")
	}
}

func TestConcurrentDuplicateStartsWaitForTheFirst(t *testing.T) {
	f := newSessionFixture(t)
	f.endpoint.createGate = make(chan struct{})
	results := make([]map[string]any, 3)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[index] = f.start(t, sessionBody("request-0013", "xcc", newWorkspace), 200)
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(f.endpoint.createGate)
	wait.Wait()
	for _, result := range results[1:] {
		if result["session_id"] != results[0]["session_id"] {
			t.Fatalf("results differ: %v", results)
		}
	}
	if n := len(f.endpoint.callsOf("workspace.create")); n != 1 {
		t.Fatalf("created %d workspaces", n)
	}
}

func TestStartSessionInputFailureKeepsThePaneAndReportsIt(t *testing.T) {
	f := newSessionFixture(t)
	f.endpoint.sendErr = errors.New("read remote herdr response: EOF")
	result := f.start(t, sessionBody("request-0014", "xcc", newWorkspace), 502)
	details, _ := result["details"].(map[string]any)
	if result["code"] != "start_failed" || details["workspace_id"] != "w9" || details["tab_id"] != "w9:t1" || details["pane_id"] != "w9:p1" {
		t.Fatalf("result = %v", result)
	}
	f.endpoint.assertNothingClosed(t)
	// The pane exists: repeating the id must not open another one or retype.
	f.endpoint.sendErr = nil
	again := f.start(t, sessionBody("request-0014", "xcc", newWorkspace), 502)
	if again["code"] != "start_failed" || len(f.endpoint.callsOf("workspace.create")) != 1 || len(f.endpoint.callsOf("pane.send_input")) != 1 {
		t.Fatalf("again = %v", again)
	}
	entries, err := f.db.ListAudit(context.Background(), store.AuditFilter{Action: "session.start"})
	if err != nil || len(entries) != 1 || !strings.Contains(string(entries[0].Details), `"result":"input_failed"`) {
		t.Fatalf("audit = %+v %v", entries, err)
	}
}

func TestStartSessionCreateFailures(t *testing.T) {
	t.Run("Herdr refused: nothing exists, the id can retry", func(t *testing.T) {
		f := newSessionFixture(t)
		f.endpoint.createErr = &herdr.APIError{Code: "invalid_cwd", Message: "cwd does not exist"}
		result := f.start(t, sessionBody("request-0015", "xcc", newWorkspace), 502)
		if result["code"] != "start_failed" || !strings.Contains(result["error"].(string), "cwd does not exist") || len(f.endpoint.callsOf("pane.send_input")) != 0 {
			t.Fatalf("result = %v", result)
		}
		f.endpoint.createErr = nil
		f.start(t, sessionBody("request-0015", "xcc", newWorkspace), 200)
	})
	t.Run("uncertain: never retried", func(t *testing.T) {
		f := newSessionFixture(t)
		f.endpoint.createErr = errors.New("read remote herdr response: EOF")
		f.start(t, sessionBody("request-0016", "xcc", newWorkspace), 502)
		f.endpoint.createErr = nil
		f.start(t, sessionBody("request-0016", "xcc", newWorkspace), 502)
		if n := len(f.endpoint.callsOf("workspace.create")); n != 1 {
			t.Fatalf("an uncertain create was retried: %d", n)
		}
	})
	t.Run("host unreachable: nothing exists, the id can retry", func(t *testing.T) {
		f := newSessionFixture(t)
		f.pool.openErr = errors.New("dial tcp: connection refused")
		if result := f.start(t, sessionBody("request-0017", "xcc", newWorkspace), 502); result["code"] != "herdr_unavailable" {
			t.Fatalf("result = %v", result)
		}
		f.pool.openErr = nil
		f.start(t, sessionBody("request-0017", "xcc", newWorkspace), 200)
	})
}

func TestStartSessionStopsWaitingForThePrompt(t *testing.T) {
	f := newSessionFixture(t)
	f.endpoint.emptyReads = 1 << 30
	started := time.Now()
	f.start(t, sessionBody("request-0018", "xcc", newWorkspace), 200)
	if elapsed := time.Since(started); elapsed < sessionShellWait || elapsed > sessionShellWait+2*time.Second {
		t.Fatalf("waited %s for the prompt", elapsed)
	}
	if len(f.endpoint.callsOf("pane.send_input")) != 1 {
		t.Fatal("the launcher line was not typed after the wait")
	}
}

func TestSessionStartsAreBoundedPerUserAndExpire(t *testing.T) {
	starts := &sessionStarts{}
	now := time.Now()
	key := func(user string, index int) sessionStartKey {
		return sessionStartKey{user: user, host: "h", id: strconv.Itoa(index)}
	}
	running, _, _ := starts.begin(key("bob", 0), now)
	for index := range sessionStartUserCapacity + 10 {
		entry, fresh, err := starts.begin(key("alice", index), now.Add(time.Duration(index)*time.Millisecond))
		if err != nil || !fresh {
			t.Fatalf("entry %d fresh=%v err=%v", index, fresh, err)
		}
		starts.finish(key("alice", index), entry, &sessionReply{status: 200})
	}
	if n := len(starts.users["alice"]); n > sessionStartUserCapacity {
		t.Fatalf("alice grew to %d", n)
	}
	if entry, fresh, _ := starts.begin(key("bob", 0), now); fresh || entry != running {
		t.Fatal("another user's running start was evicted")
	}
	for index := range sessionStartUserCapacity {
		starts.begin(key("carol", index), now)
	}
	if _, _, err := starts.begin(key("carol", 999), now); !errors.Is(err, errSessionStartBusy) {
		t.Fatalf("a full table of running starts accepted more: %v", err)
	}
	// Settled entries expire after the window.
	if _, fresh, _ := starts.begin(key("alice", sessionStartUserCapacity+9), now.Add(sessionStartTTL+time.Minute)); !fresh {
		t.Fatal("an expired start still deduplicated")
	}
}
