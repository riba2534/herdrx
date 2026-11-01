package herdr

import (
	"context"
	"errors"
	"io"
	"net"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/agent"
	"github.com/riba2534/herdrx/internal/launchers"
	"github.com/riba2534/herdrx/internal/store"
)

type launcherSession struct {
	output   string
	startErr error
	waitErr  error
	command  string
}

func (*launcherSession) Output(string) ([]byte, error) { return nil, errors.New("unused") }
func (s *launcherSession) Start(command string) error {
	s.command = command
	return s.startErr
}
func (s *launcherSession) Wait() error                      { return s.waitErr }
func (*launcherSession) Close() error                       { return nil }
func (*launcherSession) StdinPipe() (io.WriteCloser, error) { return transcriptDiscard{}, nil }
func (s *launcherSession) StdoutPipe() (io.Reader, error)   { return strings.NewReader(s.output), nil }
func (*launcherSession) StderrPipe() (io.Reader, error)     { return strings.NewReader(""), nil }
func (*launcherSession) setStderr(io.Writer)                {}
func (*launcherTransport) Close() error                     { return nil }
func (*launcherTransport) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("unused")
}

type launcherTransport struct{ session *launcherSession }

func (t *launcherTransport) NewSession() (sshSession, error) { return t.session, nil }

// exitError is a real process exit with status code, as system OpenSSH reports it.
func exitError(t *testing.T, code string) error {
	t.Helper()
	err := exec.Command("/bin/sh", "-c", "exit "+code).Run()
	if err == nil {
		t.Fatal("expected an exit error")
	}
	return err
}

func launcherEndpoint(transport string, session *launcherSession) *SSHEndpoint {
	return &SSHEndpoint{host: store.Host{Transport: transport}, client: &launcherTransport{session: session}}
}

func TestLauncherCommandMatchesTheDaemonAllowlist(t *testing.T) {
	if launchers.AgentCommand != agent.LaunchersCommand {
		t.Fatalf("client %q != daemon %q", launchers.AgentCommand, agent.LaunchersCommand)
	}
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
		session := &launcherSession{output: output}
		result, err := launcherEndpoint(transport, session).Launchers(context.Background())
		if err != nil || session.command != want {
			t.Fatalf("%q: command %.40q err %v", transport, session.command, err)
		}
		if !reflect.DeepEqual(result, launchers.Found([]launchers.Launcher{{Name: "xcc", Bypass: true, Fullscreen: true, Teammate: true}})) {
			t.Fatalf("%q: result %+v", transport, result)
		}
	}
	result, err := launcherEndpoint("future", &launcherSession{}).Launchers(context.Background())
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
		{"old daemon refuses the name", "tailcat", &launcherSession{startErr: errors.New("ssh: command herdrx-launchers failed")}, launchers.ReasonUnsupportedAgent},
		{"old daemon exits 126", "tailcat", &launcherSession{waitErr: exitError(t, "126")}, launchers.ReasonUnsupportedAgent},
		{"daemon without zsh", "tailcat", &launcherSession{waitErr: exitError(t, "127")}, launchers.ReasonShellUnavailable},
		{"daemon timeout", "tailcat", &launcherSession{waitErr: exitError(t, "124")}, launchers.ReasonTimeout},
		{"daemon read failure", "tailcat", &launcherSession{waitErr: exitError(t, "1")}, launchers.ReasonReadFailed},
		{"ssh without zsh", "ssh", &launcherSession{waitErr: exitError(t, "127")}, launchers.ReasonShellUnavailable},
		{"ssh watchdog", "ssh", &launcherSession{waitErr: exitError(t, "124")}, launchers.ReasonTimeout},
		{"ssh refused exec", "ssh", &launcherSession{startErr: errors.New("refused")}, launchers.ReasonReadFailed},
		{"ssh 126 is not an old daemon", "ssh", &launcherSession{waitErr: exitError(t, "126")}, launchers.ReasonReadFailed},
		{"oversized reply", "ssh", &launcherSession{output: strings.Repeat("herdrx-launcher\tx\t0\t0\t0\n", launchers.MaxOutput/24+1)}, launchers.ReasonReadFailed},
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

type failingTransport struct{ launcherTransport }

func (failingTransport) NewSession() (sshSession, error) { return nil, io.EOF }

func TestSSHLaunchersReportsABrokenTransport(t *testing.T) {
	endpoint := &SSHEndpoint{host: store.Host{Transport: "ssh"}, client: &failingTransport{}}
	result, err := endpoint.Launchers(context.Background())
	if !errors.Is(err, io.EOF) || result.Supported || result.Reason != launchers.ReasonReadFailed {
		t.Fatalf("result %+v err %v", result, err)
	}
}
