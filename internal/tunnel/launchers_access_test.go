package tunnel

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/agent"
	"github.com/riba2534/herdrx/internal/launchers"
	"github.com/riba2534/herdrx/internal/launchers/launcherstest"
	"golang.org/x/crypto/ssh"
)

func runPermanentExec(t *testing.T, client *ssh.Client, command string) (string, string, int, error) {
	t.Helper()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	session.Stdout, session.Stderr = &stdout, &stderr
	err = session.Run(command)
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		return stdout.String(), stderr.String(), exit.ExitStatus(), nil
	}
	return stdout.String(), stderr.String(), 0, err
}

func TestPermanentLaunchersCommandIsWhitelisted(t *testing.T) {
	launcherstest.RequireZsh(t)
	home := launcherstest.Isolate(t, launcherstest.Zshrc)

	_, _, dial := permanentSSHFixture(t, false)
	prepared, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if stdout, _, _, err := runPermanentExec(t, prepared, agent.LaunchersCommand); err == nil || stdout != "" {
		t.Fatal("prepared connection ran launcher discovery")
	}

	_, _, dial = permanentSSHFixture(t, true)
	active, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	for _, command := range []string{agent.LaunchersCommand + " xcc", "sh -c " + agent.LaunchersCommand, "zsh -ic alias"} {
		if _, _, _, err := runPermanentExec(t, active, command); err == nil {
			t.Fatalf("active daemon accepted %q", command)
		}
	}
	stdout, stderr, status, err := runPermanentExec(t, active, agent.LaunchersCommand)
	if err != nil || status != 0 || stdout != string(launchers.Format(launcherstest.Expected)) {
		t.Fatalf("stdout=%q stderr=%q status=%d err=%v", stdout, stderr, status, err)
	}
	for _, secret := range launcherstest.Secrets {
		if strings.Contains(stdout+stderr, secret) {
			t.Fatalf("daemon output leaked %q", secret)
		}
	}
	launcherstest.AssertNoMarkers(t, home)

	t.Setenv("PATH", t.TempDir())
	if stdout, _, status, err := runPermanentExec(t, active, agent.LaunchersCommand); err != nil || status != launchers.ExitShellUnavailable || stdout != "" {
		t.Fatalf("without zsh: stdout=%q status=%d err=%v", stdout, status, err)
	}
}

// A future internal command whose dispatcher case is missing must fail the
// request, not crash the daemon by running Args[0].
func TestPermanentDaemonFailsAnInternalSpecWithoutACase(t *testing.T) {
	_, _, dial := permanentSSHFixture(t, true)
	active, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	original := parseAgentCommand
	t.Cleanup(func() { parseAgentCommand = original })
	for _, spec := range []agent.CommandSpec{{Internal: "future"}, {}} {
		parseAgentCommand = func(string) (agent.CommandSpec, error) { return spec, nil }
		if _, stderr, status, err := runPermanentExec(t, active, "anything"); err != nil || status != 126 || !strings.Contains(stderr, "not allowed") {
			t.Fatalf("spec %+v: stderr=%q status=%d err=%v", spec, stderr, status, err)
		}
	}
	parseAgentCommand = original
	if stdout, _, status, err := runPermanentExec(t, active, "uname -sm"); err != nil || status != 0 || stdout == "" {
		t.Fatalf("daemon stopped serving: stdout=%q status=%d err=%v", stdout, status, err)
	}
}
