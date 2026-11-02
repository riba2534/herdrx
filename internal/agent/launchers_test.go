package agent

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/launchers"
	"github.com/riba2534/herdrx/internal/launchers/launcherstest"
	"golang.org/x/crypto/ssh"
)

func TestLaunchersCommandIsAnExactInternalCommand(t *testing.T) {
	spec, err := ParseCommand(LaunchersCommand)
	if err != nil || spec.Internal != "launchers" || len(spec.Args) != 0 {
		t.Fatalf("spec=%+v err=%v", spec, err)
	}
}

func TestExecutableCommandNeverRunsAnInternalSpec(t *testing.T) {
	for _, spec := range []CommandSpec{
		{},
		{Internal: "future"},
		{Internal: "launchers"},
		// An internal spec with arguments must not run its first argument either.
		{Internal: "stage-image", Args: []string{"png"}},
	} {
		if command, err := ExecutableCommand(spec, "/opt/herdr"); err == nil || command != nil {
			t.Fatalf("spec %+v became a command", spec)
		}
	}
	command, err := ExecutableCommand(CommandSpec{Args: []string{"herdr", "--version"}}, "/opt/herdr")
	if err != nil || command.Path != "/opt/herdr" || strings.Join(command.Args[1:], " ") != "--version" {
		t.Fatalf("herdr command = %+v err=%v", command, err)
	}
}

type execResult struct {
	stdout, stderr string
	status         int
	err            error
}

func runExec(t *testing.T, client *ssh.Client, command string) execResult {
	t.Helper()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	session.Stdout, session.Stderr = &stdout, &stderr
	err = session.Run(command)
	result := execResult{stdout: stdout.String(), stderr: stderr.String()}
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		result.status = exit.ExitStatus()
	} else {
		result.err = err
	}
	return result
}

func TestSSHServerServesLaunchers(t *testing.T) {
	launcherstest.RequireZsh(t)
	home := launcherstest.Isolate(t, launcherstest.Zshrc)
	_, server, clientConfig, _, _ := setupTestSSH(t, true, false)
	client, err := dialLocalTCP(t, server, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	result := runExec(t, client, LaunchersCommand)
	if result.err != nil || result.status != 0 || result.stdout != string(launchers.Format(launcherstest.Expected)) {
		t.Fatalf("result = %+v", result)
	}
	launcherstest.AssertNoSecrets(t, result.stderr)
	launcherstest.AssertNoMarkers(t, home)

	t.Setenv("PATH", t.TempDir())
	if result := runExec(t, client, LaunchersCommand); result.status != launchers.ExitShellUnavailable || result.stdout != "" {
		t.Fatalf("without zsh: %+v", result)
	}
	if result := runExec(t, client, LaunchersCommand+" xcc"); result.err == nil {
		t.Fatalf("daemon accepted an argument: %+v", result)
	}
}

func TestSSHServerRefusesLaunchersBeforePairing(t *testing.T) {
	_, server, clientConfig, _, _ := setupTestSSH(t, false, false)
	client, err := dialLocalTCP(t, server, clientConfig)
	if err != nil {
		t.Skipf("unpaired handshake rejected: %v", err)
	}
	defer client.Close()
	if result := runExec(t, client, LaunchersCommand); result.err == nil || result.stdout != "" {
		t.Fatalf("unpaired agent ran discovery: %+v", result)
	}
}

// A future internal command whose dispatcher case is missing must fail the
// request, not crash the daemon by running Args[0].
func TestSSHServerFailsAnInternalSpecWithoutACase(t *testing.T) {
	_, server, clientConfig, _, _ := setupTestSSH(t, true, false)
	client, err := dialLocalTCP(t, server, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	original := parseCommand
	t.Cleanup(func() { parseCommand = original })
	for _, spec := range []CommandSpec{{Internal: "future"}, {}} {
		parseCommand = func(string) (CommandSpec, error) { return spec, nil }
		if result := runExec(t, client, "anything"); result.err != nil || result.status != 126 || !strings.Contains(result.stderr, "not allowed") {
			t.Fatalf("spec %+v: %+v", spec, result)
		}
	}
	parseCommand = original
	if result := runExec(t, client, "uname -sm"); result.err != nil || result.status != 0 || result.stdout == "" {
		t.Fatalf("daemon stopped serving: %+v", result)
	}
}
