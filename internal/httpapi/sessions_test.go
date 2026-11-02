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
	// After the prompt and before typing: the directory, then the foreground process.
	var order []string
	for _, call := range f.endpoint.calls {
		if call.method != "pane.read" {
			order = append(order, call.method)
		}
	}
	if strings.Join(order, ",") != "workspace.create,pane.process_info,session.snapshot,pane.send_input" {
		t.Fatalf("order = %v", order)
	}
	if info := f.endpoint.callsOf("pane.process_info"); info[0].params["pane_id"] != "w9:p1" {
		t.Fatalf("process_info = %+v", info)
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
	f.endpoint.setPaneCWD(body["cwd"].(string), body["cwd"].(string))
	f.start(t, body, 200)
	postJSON(t, f.client, f.url("session-host", "/sessions"), "", sessionBody("request-0005", "xcc", newWorkspace), 403)
	if len(f.endpoint.callsOf("workspace.create")) != 1 {
		t.Fatal("an invalid request created something")
	}
}

func TestStartSessionLauncherRules(t *testing.T) {
	f := newSessionFixture(t)
	// A usable, non-empty list: the name must be in it.
	result := f.start(t, sessionBody("request-0006", "yolo", newWorkspace), 409)
	if result["code"] != "launcher_unknown" || len(f.endpoint.callsOf("workspace.create")) != 0 {
		t.Fatalf("result = %v", result)
	}
	// Discovery unavailable or empty: a hand-typed valid name is allowed.
	for name, discovered := range map[string]launchers.Result{
		"unsupported": launchers.Unsupported(launchers.ReasonShellUnavailable),
		"empty":       launchers.Found(nil),
	} {
		f.endpoint.discovered = discovered
		f.api.launcherLists = launcherCache{}
		if result := f.start(t, sessionBody("request-"+name, "yolo", newWorkspace), 200); result["launcher"] != "yolo" {
			t.Fatalf("%s: result = %v", name, result)
		}
	}
	// A rejected request created nothing, so the same id runs once the alias exists.
	f.endpoint.discovered = launchers.Found([]launchers.Launcher{{Name: "yolo"}})
	f.api.launcherLists = launcherCache{}
	if result := f.start(t, sessionBody("request-0006", "yolo", newWorkspace), 200); result["launcher"] != "yolo" {
		t.Fatalf("retry = %v", result)
	}
}

func TestStartSessionReusesDiscoveryForTenMinutes(t *testing.T) {
	f := newSessionFixture(t)
	f.launchers(t, 200)
	f.start(t, sessionBody("request-0009", "xcc", newWorkspace), 200)
	f.age(5 * time.Minute)
	f.start(t, sessionBody("request-0010", "plain", newWorkspace), 200)
	if n := f.endpoint.discoveries.Load(); n != 1 {
		t.Fatalf("discovery ran %d times within ten minutes", n)
	}
	// A failed discovery is reused for validation too (and lets a typed name through).
	f.api.launcherLists = launcherCache{}
	f.endpoint.discovered = launchers.Unsupported(launchers.ReasonTimeout)
	f.start(t, sessionBody("request-0019", "typed", newWorkspace), 200)
	f.start(t, sessionBody("request-0020", "typed", newWorkspace), 200)
	if n := f.endpoint.discoveries.Load(); n != 2 {
		t.Fatalf("a failed discovery was not reused for validation: %d", n)
	}
	f.age(launcherValidationTTL + time.Second)
	f.start(t, sessionBody("request-0021", "typed", newWorkspace), 200)
	if n := f.endpoint.discoveries.Load(); n != 3 {
		t.Fatalf("discovery older than ten minutes was reused: %d", n)
	}
}

func TestStartSessionRepeatsTheFirstResultForTheSameRequestID(t *testing.T) {
	f := newSessionFixture(t)
	first := f.start(t, sessionBody("request-0011", "xcc", newWorkspace), 200)
	again := f.start(t, sessionBody("request-0011", "xcc", newWorkspace), 200)
	if again["session_id"] != first["session_id"] {
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

func TestStartSessionRejectsADifferentBodyForTheSameRequestID(t *testing.T) {
	f := newSessionFixture(t)
	f.start(t, sessionBody("request-0022", "xcc", newWorkspace), 200)
	edits := []func(map[string]any){
		func(b map[string]any) { b["cwd"] = "/srv/other" },
		func(b map[string]any) { b["launcher"] = "plain" },
		func(b map[string]any) { b["target"] = map[string]any{"kind": "tab", "workspace_id": "w1"} },
		func(b map[string]any) { b["label"] = "renamed" },
	}
	for _, edit := range edits {
		body := sessionBody("request-0022", "xcc", newWorkspace)
		edit(body)
		if result := f.start(t, body, 409); result["code"] != "request_mismatch" {
			t.Fatalf("body %v: result %v", body, result)
		}
	}
	// Also while the first request is still running: no waiting, no second create.
	f.endpoint.createGate = make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.start(t, sessionBody("request-0023", "xcc", newWorkspace), 200)
	}()
	time.Sleep(100 * time.Millisecond)
	body := sessionBody("request-0023", "plain", newWorkspace)
	if result := f.start(t, body, 409); result["code"] != "request_mismatch" {
		t.Fatalf("in flight: %v", result)
	}
	close(f.endpoint.createGate)
	<-done
	if n := len(f.endpoint.callsOf("workspace.create")); n != 2 {
		t.Fatalf("created %d workspaces", n)
	}
}

// pane409 checks a failure that left the new pane open and typed nothing.
func pane409(t *testing.T, f *sessionFixture, result map[string]any, code, key, value string) {
	t.Helper()
	details, _ := result["details"].(map[string]any)
	if result["code"] != code || details["workspace_id"] != "w9" || details["tab_id"] != "w9:t1" || details["pane_id"] != "w9:p1" || details[key] != value {
		t.Fatalf("result = %v", result)
	}
	if len(f.endpoint.callsOf("pane.send_input")) != 0 {
		t.Fatal("the launcher line was typed")
	}
	f.endpoint.assertNothingClosed(t)
}

func TestStartSessionRefusesAPaneOutsideTheRequestedDirectory(t *testing.T) {
	f := newSessionFixture(t)
	// Herdr opened the missing directory's pane in the home directory instead.
	f.endpoint.setPaneCWD("/home/user", "/home/user")
	result := f.start(t, sessionBody("request-0024", "xcc", newWorkspace), 409)
	pane409(t, f, result, "cwd_mismatch", "actual_cwd", "/home/user")
	if !strings.Contains(result["error"].(string), "/home/user") {
		t.Fatalf("error = %v", result["error"])
	}
	// Recorded: the pane exists, so the same id never opens another one.
	again := f.start(t, sessionBody("request-0024", "xcc", newWorkspace), 409)
	if again["code"] != "cwd_mismatch" || len(f.endpoint.callsOf("workspace.create")) != 1 {
		t.Fatalf("again = %v", again)
	}
	entries, err := f.db.ListAudit(context.Background(), store.AuditFilter{Action: "session.start"})
	if err != nil || len(entries) != 1 || !strings.Contains(string(entries[0].Details), `"result":"cwd_mismatch"`) {
		t.Fatalf("audit = %+v %v", entries, err)
	}
}

func TestStartSessionReportsTheLogicalDirectoryWhenBothDiffer(t *testing.T) {
	f := newSessionFixture(t)
	f.endpoint.setPaneCWD("/home/user", "/private/home/user")
	result := f.start(t, sessionBody("request-0032", "xcc", newWorkspace), 409)
	pane409(t, f, result, "cwd_mismatch", "actual_cwd", "/home/user")
	// Without a logical directory the physical one is reported.
	f = newSessionFixture(t)
	f.endpoint.setPaneCWD("", "/private/home/user")
	result = f.start(t, sessionBody("request-0033", "xcc", newWorkspace), 409)
	pane409(t, f, result, "cwd_mismatch", "actual_cwd", "/private/home/user")
}

// Either directory matching is enough: the logical one the shell reports
// (OSC 7) or the physical one (macOS /tmp is /private/tmp).
func TestStartSessionAcceptsTheLogicalOrThePhysicalDirectory(t *testing.T) {
	cases := []struct{ name, request, logical, physical string }{
		{"logical matches", "/tmp/proj", "/tmp/proj", "/private/tmp/proj"},
		{"physical matches", "/private/tmp/proj", "/tmp/proj", "/private/tmp/proj"},
		{"stale logical, physical matches", "/srv/proj one", "/home/user", "/srv/proj one"},
		{"only logical known", "/srv/proj one", "/srv/proj one", ""},
		{"only physical known", "/srv/proj one", "", "/srv/proj one"},
		{"trailing slash", "/srv/proj one/", "/srv/proj one", "/srv/proj one"},
		{"root", "/", "/", "/"},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSessionFixture(t)
			f.endpoint.setPaneCWD(tc.logical, tc.physical)
			body := sessionBody("request-cwd-"+strconv.Itoa(index), "xcc", newWorkspace)
			body["cwd"] = tc.request
			f.start(t, body, 200)
		})
	}
}

// shortForegroundWait shortens the wait for the foreground shell in tests
// that expect it to run out.
func shortForegroundWait(t *testing.T) {
	t.Helper()
	wait, poll := sessionForegroundWait, sessionForegroundPoll
	sessionForegroundWait, sessionForegroundPoll = 300*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { sessionForegroundWait, sessionForegroundPoll = wait, poll })
}

func TestStartSessionWaitsForStartupCommandsToLeaveTheForeground(t *testing.T) {
	f := newSessionFixture(t)
	// .zshrc runs fastfetch and ssh-add before the shell takes the foreground back.
	f.endpoint.processSequence = []string{foregroundInfo(42, "zsh", "fastfetch"), foregroundInfo(42, "zsh", "sleep"), foregroundInfo(42, "zsh", "ssh-add"), foregroundInfo(41, "zsh")}
	f.start(t, sessionBody("request-0034", "xcc", newWorkspace), 200)
	if n := len(f.endpoint.callsOf("pane.process_info")); n != 4 {
		t.Fatalf("process_info read %d times", n)
	}
	if len(f.endpoint.callsOf("pane.send_input")) != 1 {
		t.Fatal("the launcher line was not typed once the shell had the foreground")
	}
}

func TestStartSessionRefusesAForegroundThatCannotRunTheLauncher(t *testing.T) {
	shortForegroundWait(t)
	cases := []struct {
		name, launcher string
		infos          []string
		want           string
	}{
		{"tmux from .zshrc", "claude", []string{foregroundInfo(42, "zsh", "tmux: client")}, "tmux: client"},
		{"bash for a zsh alias", "xcc", []string{foregroundInfo(41, "bash")}, "bash"},
		{"the last process read is reported", "claude", []string{foregroundInfo(42, "zsh", "sleep"), foregroundInfo(42, "zsh", "fastfetch")}, "fastfetch"},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSessionFixture(t)
			f.endpoint.processSequence = tc.infos
			result := f.start(t, sessionBody("request-shell-"+strconv.Itoa(index), tc.launcher, newWorkspace), 409)
			pane409(t, f, result, "shell_mismatch", "foreground", tc.want)
		})
	}
	t.Run("claude runs in any shell", func(t *testing.T) {
		f := newSessionFixture(t)
		f.endpoint.processInfo = foregroundInfo(41, "bash")
		f.start(t, sessionBody("request-0028", "claude", newWorkspace), 200)
	})
	t.Run("a login zsh is zsh", func(t *testing.T) {
		f := newSessionFixture(t)
		f.endpoint.processInfo = foregroundInfo(41, "-zsh")
		f.start(t, sessionBody("request-0029", "xcc", newWorkspace), 200)
	})
}

func TestStartSessionTypesNothingWhenThePaneCannotBeChecked(t *testing.T) {
	shortForegroundWait(t)
	cases := map[string]func(*sessionTestEndpoint){
		"snapshot fails":       func(e *sessionTestEndpoint) { e.snapshotErr = errors.New("read remote herdr response: EOF") },
		"pane not in snapshot": func(e *sessionTestEndpoint) { e.snapshot.Panes = nil },
		"no directory known":   func(e *sessionTestEndpoint) { e.setPaneCWD("", "") },
		"process info fails":   func(e *sessionTestEndpoint) { e.processErr = errors.New("unknown method") },
		"no foreground":        func(e *sessionTestEndpoint) { e.processInfo = foregroundInfo(41) },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSessionFixture(t)
			breakIt(f.endpoint)
			result := f.start(t, sessionBody("request-0030", "xcc", newWorkspace), 502)
			pane409(t, f, result, "start_failed", "pane_id", "w9:p1")
		})
	}
}

// Revoking access mid-start cancels the start's context, but the pane may
// already exist: its audit entry must still be written.
func TestStartSessionAuditSurvivesRevokedAccess(t *testing.T) {
	f := newSessionFixture(t)
	f.endpoint.beforeSend = func() {
		if err := f.api.access.change(f.owner, "", true, func() error { return nil }); err != nil {
			t.Error(err)
		}
	}
	f.start(t, sessionBody("request-0035", "xcc", newWorkspace), 200)
	entries, err := f.db.ListAudit(context.Background(), store.AuditFilter{Action: "session.start"})
	if err != nil || len(entries) != 1 || !strings.Contains(string(entries[0].Details), `"pane_id":"w9:p1"`) {
		t.Fatalf("audit = %+v %v", entries, err)
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
	// Herdr does not check cwd (a missing directory falls back to home, see the
	// cwd_mismatch test); this is any other explicit refusal.
	t.Run("Herdr refused: nothing exists, the id can retry", func(t *testing.T) {
		f := newSessionFixture(t)
		f.endpoint.createErr = &herdr.APIError{Code: "spawn_failed", Message: "failed to spawn shell"}
		result := f.start(t, sessionBody("request-0015", "xcc", newWorkspace), 502)
		if result["code"] != "start_failed" || !strings.Contains(result["error"].(string), "failed to spawn shell") || len(f.endpoint.callsOf("pane.send_input")) != 0 {
			t.Fatalf("result = %v", result)
		}
		f.endpoint.createErr = nil
		f.start(t, sessionBody("request-0015", "xcc", newWorkspace), 200)
	})
	assertAudited := func(t *testing.T, f *sessionFixture, outcome string) {
		t.Helper()
		entries, err := f.db.ListAudit(context.Background(), store.AuditFilter{Action: "session.start"})
		if err != nil || len(entries) != 1 || !strings.Contains(string(entries[0].Details), `"result":"`+outcome+`"`) || !strings.Contains(string(entries[0].Details), `"launcher":"xcc"`) {
			t.Fatalf("audit = %+v %v", entries, err)
		}
	}
	t.Run("uncertain: never retried, audited", func(t *testing.T) {
		f := newSessionFixture(t)
		f.endpoint.createErr = errors.New("read remote herdr response: EOF")
		f.start(t, sessionBody("request-0016", "xcc", newWorkspace), 502)
		f.endpoint.createErr = nil
		f.start(t, sessionBody("request-0016", "xcc", newWorkspace), 502)
		if n := len(f.endpoint.callsOf("workspace.create")); n != 1 {
			t.Fatalf("an uncertain create was retried: %d", n)
		}
		assertAudited(t, f, "create_unknown")
	})
	t.Run("no pane in the reply: never retried, audited", func(t *testing.T) {
		f := newSessionFixture(t)
		f.endpoint.createRaw = `{"type":"workspace_created","workspace":{"workspace_id":"w9"}}`
		f.start(t, sessionBody("request-0031", "xcc", newWorkspace), 502)
		f.start(t, sessionBody("request-0031", "xcc", newWorkspace), 502)
		if n := len(f.endpoint.callsOf("workspace.create")); n != 1 || len(f.endpoint.callsOf("pane.send_input")) != 0 {
			t.Fatalf("creates %d", n)
		}
		assertAudited(t, f, "pane_missing")
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
	running, _, _ := starts.begin(key("bob", 0), "", now)
	for index := range sessionStartUserCapacity + 10 {
		entry, fresh, err := starts.begin(key("alice", index), "", now.Add(time.Duration(index)*time.Millisecond))
		if err != nil || !fresh {
			t.Fatalf("entry %d fresh=%v err=%v", index, fresh, err)
		}
		starts.finish(key("alice", index), entry, &sessionReply{status: 200})
	}
	if n := len(starts.users["alice"]); n > sessionStartUserCapacity {
		t.Fatalf("alice grew to %d", n)
	}
	if entry, fresh, _ := starts.begin(key("bob", 0), "", now); fresh || entry != running {
		t.Fatal("another user's running start was evicted")
	}
	for index := range sessionStartUserCapacity {
		starts.begin(key("carol", index), "", now)
	}
	if _, _, err := starts.begin(key("carol", 999), "", now); !errors.Is(err, errSessionStartBusy) {
		t.Fatalf("a full table of running starts accepted more: %v", err)
	}
	// Settled entries expire after the window.
	if _, fresh, _ := starts.begin(key("alice", sessionStartUserCapacity+9), "", now.Add(sessionStartTTL+time.Minute)); !fresh {
		t.Fatal("an expired start still deduplicated")
	}
}
