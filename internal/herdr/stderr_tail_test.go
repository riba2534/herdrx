package herdr

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/store"
)

func TestStderrTailKeepsTheLastReadableLine(t *testing.T) {
	for _, tc := range []struct{ name, stderr, want string }{
		{"empty", " \n\n", ""},
		{"last-line", "Warning: Permanently added 'example.test'\nuser@example.test: Permission denied (publickey).\n", "user@example.test: Permission denied (publickey)."},
		{"escapes", "\x1b[31mherdrx-mux: command not found\x1b[0m\r\n", "herdrx-mux: command not found"},
		{"osc-and-controls", "\x1b]0;title\x07bad\x00\tline\x7f\n", "bad line"},
		{"bounded", strings.Repeat("界", 300), strings.Repeat("界", 240) + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := StderrTail(tc.stderr); got != tc.want {
				t.Fatalf("StderrTail = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWithStderrTailWrapsOnlyWithContent(t *testing.T) {
	base := errors.New("discover remote config path: exit status 127")
	if got := WithStderrTail(base, "远程主机输出", "\n"); got != base {
		t.Fatalf("empty stderr changed the error: %v", got)
	}
	got := WithStderrTail(base, "远程主机输出", "sh: 1: herdrx-mux: not found\n")
	var remote *RemoteStderrError
	if !errors.As(got, &remote) || !errors.Is(got, base) || got.Error() != base.Error()+"（远程主机输出：sh: 1: herdrx-mux: not found）" {
		t.Fatalf("wrapped = %v", got)
	}
}

type stderrSession struct {
	transcriptRetrySession
	stderr string
}

func (s *stderrSession) setStderr(w io.Writer) { _, _ = io.WriteString(w, s.stderr) }

func TestSocketDiscoveryReportsRemoteStderr(t *testing.T) {
	session := &stderrSession{transcriptRetrySession: transcriptRetrySession{waitErr: errors.New("exit status 126")}, stderr: "command is not allowed by the access agent\n"}
	endpoint := &SSHEndpoint{host: store.Host{Transport: "tailcat"}, client: sessionTransport{session}}
	_, err := endpoint.locateSocket(context.Background())
	if err == nil || !strings.Contains(err.Error(), "command is not allowed by the access agent") {
		t.Fatalf("socket discovery error lost the remote reason: %v", err)
	}
}

type sessionTransport struct{ session sshSession }

func (t sessionTransport) NewSession() (sshSession, error) { return t.session, nil }
func (sessionTransport) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("unused")
}
func (sessionTransport) Close() error { return nil }
