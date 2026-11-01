package herdr

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/agent"
	"github.com/riba2534/herdrx/internal/launchers"
	"github.com/riba2534/herdrx/internal/store"
)

// launcherSession is a scripted SSH session whose Start can also fail.
type launcherSession struct {
	transcriptRetrySession
	startErr error
}

func (s *launcherSession) Start(command string) error {
	s.command = command
	return s.startErr
}

func replied(output string) *launcherSession {
	return &launcherSession{transcriptRetrySession: transcriptRetrySession{output: output}}
}

func refused(err error) *launcherSession { return &launcherSession{startErr: err} }

// exited ends with a real process exit status, as system OpenSSH reports it.
func exited(t *testing.T, code string) *launcherSession {
	t.Helper()
	err := exec.Command("/bin/sh", "-c", "exit "+code).Run()
	if err == nil {
		t.Fatal("expected an exit error")
	}
	return &launcherSession{transcriptRetrySession: transcriptRetrySession{waitErr: err}}
}

func launcherEndpoint(transport string, session *launcherSession) *SSHEndpoint {
	return &SSHEndpoint{host: store.Host{Transport: transport}, client: sessionTransport{session}}
}

func TestLauncherCommandMatchesTheDaemonAllowlist(t *testing.T) {
	spec, err := agent.ParseCommand(launchers.AgentCommand)
	if err != nil || spec.Internal != "launchers" || len(spec.Args) != 0 {
		t.Fatalf("allowlist: %+v %v", spec, err)
	}
	// The basic SSH command must never be accepted by the daemon.
	if _, err := agent.ParseCommand(launchers.SSHCommand); err == nil {
		t.Fatal("daemon accepted the shell command")
	}
}

func TestSSHLaunchersPicksTheCommandForTheTransport(t *testing.T) {
	output := "noise sk-herdrx-FAKE-0000\nherdrx-launcher\txcc\t1\t1\t1\n"
	for transport, want := range map[string]string{"ssh": launchers.SSHCommand, "": launchers.SSHCommand, "tailcat": launchers.AgentCommand} {
		session := replied(output)
		result, err := launcherEndpoint(transport, session).Launchers(context.Background())
		if err != nil || session.command != want {
			t.Fatalf("%q: command %.40q err %v", transport, session.command, err)
		}
		if !reflect.DeepEqual(result, launchers.Found([]launchers.Launcher{{Name: "xcc", Bypass: true, Fullscreen: true, Teammate: true}})) {
			t.Fatalf("%q: result %+v", transport, result)
		}
	}
	result, err := launcherEndpoint("future", replied("")).Launchers(context.Background())
	if err != nil || result.Supported || result.Reason != launchers.ReasonUnsupportedAgent {
		t.Fatalf("unknown transport: %+v %v", result, err)
	}
}

func TestSSHLaunchersClassifiesFailures(t *testing.T) {
	cases := []struct {
		name      string
		transport string
		session   *launcherSession
		reason    string
	}{
		{"old daemon refuses the name", "tailcat", refused(errors.New("ssh: command herdrx-launchers failed")), launchers.ReasonUnsupportedAgent},
		{"daemon without zsh", "tailcat", exited(t, "127"), launchers.ReasonShellUnavailable},
		{"daemon timeout", "tailcat", exited(t, "124"), launchers.ReasonTimeout},
		{"daemon read failure", "tailcat", exited(t, "1"), launchers.ReasonReadFailed},
		{"ssh without zsh", "ssh", exited(t, "127"), launchers.ReasonShellUnavailable},
		{"ssh watchdog", "ssh", exited(t, "124"), launchers.ReasonTimeout},
		{"ssh refused exec", "ssh", refused(errors.New("refused")), launchers.ReasonReadFailed},
		{"other exit status", "ssh", exited(t, "126"), launchers.ReasonReadFailed},
		{"oversized reply", "ssh", replied(strings.Repeat("herdrx-launcher\tx\t0\t0\t0\n", launchers.MaxOutput/24+1)), launchers.ReasonReadFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := launcherEndpoint(tc.transport, tc.session).Launchers(context.Background())
			if err != nil {
				t.Fatalf("classified failure returned a transport error: %v", err)
			}
			if result.Supported || result.Reason != tc.reason || result.Launchers == nil || len(result.Launchers) != 0 {
				t.Fatalf("result %+v, want reason %s", result, tc.reason)
			}
		})
	}
}

type failingTransport struct{ sessionTransport }

func (failingTransport) NewSession() (sshSession, error) { return nil, io.EOF }

func TestSSHLaunchersReportsABrokenTransport(t *testing.T) {
	endpoint := &SSHEndpoint{host: store.Host{Transport: "ssh"}, client: failingTransport{}}
	result, err := endpoint.Launchers(context.Background())
	if !errors.Is(err, io.EOF) || result.Supported || result.Reason != launchers.ReasonReadFailed {
		t.Fatalf("result %+v err %v", result, err)
	}
}
