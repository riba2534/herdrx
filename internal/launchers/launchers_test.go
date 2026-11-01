package launchers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/launchers"
	"github.com/riba2534/herdrx/internal/launchers/launcherstest"
)

func TestParseKeepsOnlyWellFormedLines(t *testing.T) {
	output := strings.Join([]string{
		"herdrx-launcher\txcc\t1\t1\t1",
		"herdrx-launcher\tbeta\t0\t0\t0",
		"rc noise herdrx-launcher\tnoise\t1\t1\t1",
		"herdrx-launcher\tshort\t1\t1",
		"herdrx-launcher\tlong\t1\t1\t1\t1",
		"herdrx-launcher\tbad flag\t1\t1\t1",
		"herdrx-launcher\tflag\tyes\t0\t0",
		"herdrx-launcher\tspace\t1\t0\t0 ",
		"herdrx-launcher\t-dash\t0\t0\t0",
		"herdrx-launcher\ta+b\t0\t0\t0",
		"herdrx-launcher\t" + strings.Repeat("n", 65) + "\t0\t0\t0",
		"herdrx-launcher\t" + strings.Repeat("n", 64) + "\t0\t0\t0",
		"herdrx-launcher\tbeta\t1\t1\t1",
		"herdrx-launcher\t_under.dot-1\t0\t1\t0",
		"herdrx-launcher\tcr\t0\t0\t0\r",
		"HERDRX-LAUNCHER\tcase\t0\t0\t0",
		"",
	}, "\n")
	got := launchers.Parse([]byte(output))
	want := []launchers.Launcher{
		{Name: "_under.dot-1", Fullscreen: true},
		{Name: "beta"},
		{Name: strings.Repeat("n", 64)},
		{Name: "xcc", Bypass: true, Fullscreen: true, Teammate: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse = %+v\nwant %+v", got, want)
	}
	if got := launchers.Parse(nil); got == nil || len(got) != 0 {
		t.Fatalf("Parse(nil) = %#v, want an empty list", got)
	}
}

func TestParseSortsAndCapsTheList(t *testing.T) {
	var lines []string
	for index := 300; index > 0; index-- {
		lines = append(lines, fmt.Sprintf("herdrx-launcher\tl%03d\t0\t0\t0", index))
	}
	got := launchers.Parse([]byte(strings.Join(lines, "\n")))
	if len(got) != launchers.MaxLaunchers || got[0].Name != "l001" || got[len(got)-1].Name != "l200" {
		t.Fatalf("got %d launchers, first %+v last %+v", len(got), got[0], got[len(got)-1])
	}
}

func TestFormatRoundTrips(t *testing.T) {
	list := []launchers.Launcher{{Name: "a", Bypass: true}, {Name: "b", Fullscreen: true}, {Name: "c", Teammate: true}}
	formatted := launchers.Format(list)
	if string(formatted) != "herdrx-launcher\ta\t1\t0\t0\nherdrx-launcher\tb\t0\t1\t0\nherdrx-launcher\tc\t0\t0\t1\n" {
		t.Fatalf("Format = %q", formatted)
	}
	if got := launchers.Parse(formatted); !reflect.DeepEqual(got, list) {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestResultJSONMatchesTheFrontendContract(t *testing.T) {
	cases := map[string]launchers.Result{
		`{"launchers":[{"name":"xcc","bypass":true,"fullscreen":false,"teammate":true}],"supported":true}`: launchers.Found([]launchers.Launcher{{Name: "xcc", Bypass: true, Teammate: true}}),
		`{"launchers":[],"supported":true}`:                         launchers.Found(nil),
		`{"launchers":[],"supported":false,"reason":"timeout"}`:     launchers.Unsupported(launchers.ReasonTimeout),
		`{"launchers":[],"supported":false,"reason":"read_failed"}`: launchers.Unsupported(launchers.ReasonReadFailed),
	}
	for want, result := range cases {
		encoded, err := json.Marshal(result)
		if err != nil || string(encoded) != want {
			t.Fatalf("json = %s %v, want %s", encoded, err, want)
		}
	}
}

func TestExitStatusesMapBackToReasons(t *testing.T) {
	for reason, status := range map[string]uint32{
		launchers.ReasonShellUnavailable: 127,
		launchers.ReasonTimeout:          124,
		launchers.ReasonReadFailed:       1,
	} {
		err := &launchers.Error{Reason: reason}
		if got := launchers.ExitStatus(err); got != status {
			t.Fatalf("ExitStatus(%s) = %d, want %d", reason, got, status)
		}
		if got := launchers.ReasonForExit(int(status)); got != reason {
			t.Fatalf("ReasonForExit(%d) = %s, want %s", status, got, reason)
		}
		if strings.Contains(err.Error(), "herdrx-launcher") {
			t.Fatalf("error text carries output: %v", err)
		}
	}
	if launchers.ReasonOf(errors.New("other")) != launchers.ReasonReadFailed || launchers.ReasonForExit(2) != launchers.ReasonReadFailed {
		t.Fatal("unknown failures must be failed reads")
	}
}

// Six quotes are the three single-quoted words around the wrapper, loader and
// script: none of them may contain a quote or a backslash (fish unescapes it).
func TestScriptsSurviveSingleQuoting(t *testing.T) {
	if !strings.HasPrefix(launchers.SSHCommand, "sh -c '") || strings.Count(launchers.SSHCommand, "'") != 6 || strings.Contains(launchers.SSHCommand, `\`) {
		t.Fatalf("SSH command quoting: %q", launchers.SSHCommand)
	}
	if !strings.Contains(launchers.SSHCommand, "(sleep "+strconv.Itoa(int(launchers.Timeout/time.Second))+";") {
		t.Fatal("the SSH watchdog must use the discovery timeout")
	}
}

func TestRunReadsAliasesWithoutLeakingBodies(t *testing.T) {
	launcherstest.RequireZsh(t)
	home := launcherstest.Isolate(t, launcherstest.Zshrc)
	output, err := launchers.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the launcher lines reach descriptor 3: no body, no startup-file noise.
	if string(output) != string(launchers.Format(launcherstest.Expected)) {
		t.Fatalf("output has more than launcher lines: %q", output)
	}
	launcherstest.AssertNoMarkers(t, home)
	if result := launchers.Discover(context.Background()); !result.Supported || !reflect.DeepEqual(result.Launchers, launcherstest.Expected) {
		t.Fatalf("Discover = %+v", result)
	}
}

func TestRunWithoutZshIsShellUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := launchers.Run(context.Background())
	if launchers.ReasonOf(err) != launchers.ReasonShellUnavailable {
		t.Fatalf("err = %v", err)
	}
	if result := launchers.Discover(context.Background()); result.Supported || result.Reason != launchers.ReasonShellUnavailable || result.Launchers == nil {
		t.Fatalf("Discover = %+v", result)
	}
}

func TestRunFailingStartupIsReadFailed(t *testing.T) {
	launcherstest.RequireZsh(t)
	launcherstest.Isolate(t, func(string) string { return "alias xcc=claude\nexit 3\n" })
	if _, err := launchers.Run(context.Background()); launchers.ReasonOf(err) != launchers.ReasonReadFailed {
		t.Fatalf("err = %v", err)
	}
}

func TestRunOverOutputLimitIsReadFailed(t *testing.T) {
	launcherstest.RequireZsh(t)
	launcherstest.Isolate(t, func(string) string {
		return `for i in {1..4000}; do alias "launcher_with_a_rather_long_name_to_fill_the_output_quickly_$i=claude"; done` + "\n"
	})
	if _, err := launchers.Run(context.Background()); launchers.ReasonOf(err) != launchers.ReasonReadFailed {
		t.Fatalf("err = %v", err)
	}
}

// hangingZshrc starts a background child and a foreground child that would
// outlive the probe, records both pids, then hangs.
func hangingZshrc(home string) string {
	return fmt.Sprintf(`alias xcc=claude
sleep 60 &
echo $! > %[1]s/background.pid
sh -c 'echo $$ > %[1]s/foreground.pid; exec sleep 60'
`, home)
}

func assertKilled(t *testing.T, home string) {
	t.Helper()
	for _, name := range []string{"background.pid", "foreground.pid"} {
		data, err := os.ReadFile(filepath.Join(home, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for syscall.Kill(pid, 0) == nil {
			if time.Now().After(deadline) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatalf("%s %d outlived the probe", name, pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestRunTimeoutKillsTheProcessGroup(t *testing.T) {
	launcherstest.RequireZsh(t)
	home := launcherstest.Isolate(t, hangingZshrc)
	started := time.Now()
	_, err := launchers.Run(context.Background())
	if launchers.ReasonOf(err) != launchers.ReasonTimeout {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(started); elapsed < launchers.Timeout || elapsed > launchers.Timeout+2*time.Second {
		t.Fatalf("timeout after %s", elapsed)
	}
	assertKilled(t, home)
}

func TestRunCleansUpWhatStartupLeftBehind(t *testing.T) {
	launcherstest.RequireZsh(t)
	home := launcherstest.Isolate(t, func(home string) string {
		return fmt.Sprintf("alias xcc=claude\nsleep 60 &\necho $! > %[1]s/background.pid\nsh -c 'echo $$ > %[1]s/foreground.pid; exec sleep 60' &\nsleep 0.2\n", home)
	})
	started := time.Now()
	output, err := launchers.Run(context.Background())
	if err != nil || len(launchers.Parse(output)) != 1 {
		t.Fatalf("output %q err %v", output, err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("a background child held the probe for %s", elapsed)
	}
	assertKilled(t, home)
}

// runSSHCommand runs SSHCommand the way sshd does: in a new session, through
// the account's login shell.
func runSSHCommand(t *testing.T, loginShell string, extraEnv ...string) ([]byte, int) {
	t.Helper()
	path, err := exec.LookPath(loginShell)
	if err != nil {
		t.Skipf("%s is not installed", loginShell)
	}
	args := []string{"-c", launchers.SSHCommand}
	if loginShell == "busybox" {
		args = append([]string{"sh"}, args...)
	}
	command := exec.Command(path, args...)
	command.Env = append(os.Environ(), extraEnv...)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	output, err := command.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return output, exit.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return output, 0
}

func TestSSHCommandUnderEachLoginShell(t *testing.T) {
	launcherstest.RequireZsh(t)
	home := launcherstest.Isolate(t, launcherstest.Zshrc)
	for _, shell := range []string{"sh", "bash", "dash", "zsh", "busybox"} {
		t.Run(shell, func(t *testing.T) {
			output, status := runSSHCommand(t, shell)
			if status != 0 {
				t.Fatalf("status %d output %q", status, output)
			}
			if string(output) != string(launchers.Format(launcherstest.Expected)) {
				t.Fatalf("output = %q", output)
			}
			launcherstest.AssertNoMarkers(t, home)
		})
	}
}

func TestSSHCommandWithoutZshExits127(t *testing.T) {
	output, status := runSSHCommand(t, "sh", "PATH="+t.TempDir())
	if status != launchers.ExitShellUnavailable || len(output) != 0 {
		t.Fatalf("status %d output %q", status, output)
	}
	if launchers.ReasonForExit(status) != launchers.ReasonShellUnavailable {
		t.Fatal("exit 127 must read as shell_unavailable")
	}
}

// toolDir links each name to a tool, so a test can hide setsid or choose sh.
func toolDir(t *testing.T, links map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, tool := range links {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is not installed", tool)
		}
		if err := os.Symlink(path, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSSHCommandTimeoutKillsTheGroup(t *testing.T) {
	launcherstest.RequireZsh(t)
	home := launcherstest.Isolate(t, hangingZshrc)
	for name, env := range map[string][]string{
		"setsid": nil,
		// Without setsid(1), job control (bash as sh here, as on macOS)
		// gives zsh its own group.
		"job control": {"PATH=" + toolDir(t, map[string]string{"sh": "bash", "zsh": "zsh", "sleep": "sleep"})},
	} {
		t.Run(name, func(t *testing.T) {
			started := time.Now()
			output, status := runSSHCommand(t, "sh", env...)
			if status != launchers.ExitTimeout || len(output) != 0 {
				t.Fatalf("status %d output %q", status, output)
			}
			if elapsed := time.Since(started); elapsed < launchers.Timeout || elapsed > launchers.Timeout+2*time.Second {
				t.Fatalf("timeout after %s", elapsed)
			}
			assertKilled(t, home)
		})
	}
}
