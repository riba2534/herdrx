package herdr

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseHerdrStart(t *testing.T) {
	cases := map[string]HerdrStartResult{
		"herdrx-start running\n":                    {Status: "running"},
		"noise from a profile\nherdrx-start missing": {Status: "missing"},
		"herdrx-start started systemd yes\n":         {Status: "started", Method: "systemd", Linger: "yes"},
		"herdrx-start failed setsid no\n":            {Status: "failed", Method: "setsid", Linger: "no"},
	}
	for output, want := range cases {
		got, err := parseHerdrStart(output)
		if err != nil || got != want {
			t.Fatalf("parse %q = %+v, %v; want %+v", output, got, err, want)
		}
	}
	for _, output := range []string{"", "herdrx-start started", "herdrx-start maybe", "status: running"} {
		if _, err := parseHerdrStart(output); err == nil {
			t.Fatalf("parse %q accepted", output)
		}
	}
}

func TestHerdrStartScriptRejectsUnsafeSessionNames(t *testing.T) {
	for _, name := range []string{"a b", "$(id)", "a'b", "-x", strings.Repeat("a", 129)} {
		if _, err := herdrStartScript(name); err == nil {
			t.Fatalf("session %q accepted", name)
		}
	}
	script, err := herdrStartScript("work.1")
	if err != nil || !strings.Contains(script, `"$herdr_bin" --session work.1 status server`) || !strings.Contains(script, "herdrx-herdr-work.1-") {
		t.Fatalf("named session script: %v\n%s", err, script)
	}
}

func TestStartHerdrRefusesTransportsWithoutAShell(t *testing.T) {
	endpoint := &SSHEndpoint{}
	endpoint.host.Transport = "tailcat"
	if _, err := endpoint.StartHerdr(t.Context()); err != ErrHerdrStartUnsupported {
		t.Fatalf("tailcat start error = %v", err)
	}
}

// runStartScript executes the script the way an SSH exec would, against a fake
// herdr and loginctl so no real Herdr server or systemd unit is touched.
func runStartScript(t *testing.T, home, fakeBin, state, session string) HerdrStartResult {
	t.Helper()
	script, err := herdrStartScript(session)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", "-c", script)
	command.Env = []string{"HOME=" + home, "SHELL=/bin/sh", "PATH=" + fakeBin + ":/usr/bin:/bin", "FAKE_HERDR_STATE=" + state}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("start script: %v\n%s", err, output)
	}
	result, err := parseHerdrStart(string(output))
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	return result
}

func TestHerdrStartScriptDetachesOnceAndNeverReplacesARunningServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell only")
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	root := t.TempDir()
	home, fakeBin, state := filepath.Join(root, "home"), filepath.Join(root, "bin"), filepath.Join(root, "state")
	for _, dir := range []string{home, fakeBin, state} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if result := runStartScript(t, home, fakeBin, state, ""); result.Status != "missing" {
		t.Fatalf("without herdr: %+v", result)
	}
	fakeHerdr := `#!/bin/sh
case " $* " in
  *" status server "*) if [ -f "$FAKE_HERDR_STATE/running" ]; then echo "status: running"; else echo "status: not running"; fi ;;
  *" server "*) echo "$*" >> "$FAKE_HERDR_STATE/launches"; ps -o sid= -p $$ > "$FAKE_HERDR_STATE/sid"; touch "$FAKE_HERDR_STATE/running"; sleep 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(fakeBin, "herdr"), []byte(fakeHerdr), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "loginctl"), []byte("#!/bin/sh\necho Linger=no\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	result := runStartScript(t, home, fakeBin, state, "work")
	if result != (HerdrStartResult{Status: "started", Method: "setsid", Linger: "no"}) {
		t.Fatalf("first start: %+v", result)
	}
	if time.Since(started) > 1500*time.Millisecond {
		t.Fatalf("the script waited for the detached server to exit: %v", time.Since(started))
	}
	launches, _ := os.ReadFile(filepath.Join(state, "launches"))
	if strings.TrimSpace(string(launches)) != "--session work server" {
		t.Fatalf("launch arguments %q", launches)
	}
	if runtime.GOOS == "linux" {
		sid, _ := os.ReadFile(filepath.Join(state, "sid"))
		if strings.TrimSpace(string(sid)) == "" || strings.TrimSpace(string(sid)) == strings.TrimSpace(currentSID(t)) {
			t.Fatalf("server did not get its own session: %q", sid)
		}
	}
	if again := runStartScript(t, home, fakeBin, state, "work"); again.Status != "running" {
		t.Fatalf("second start must not launch again: %+v", again)
	}
	launches, _ = os.ReadFile(filepath.Join(state, "launches"))
	if strings.Count(string(launches), "server") != 1 {
		t.Fatalf("a running server was launched again: %q", launches)
	}
}

func currentSID(t *testing.T) string {
	t.Helper()
	output, err := exec.Command("ps", "-o", "sid=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}
