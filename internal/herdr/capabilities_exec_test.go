//go:build unix

package herdr

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/store"
)

const capabilitySchemaFixture = `{"protocol":22,"schemas":{"request":{"oneOf":[{"properties":{"method":{"const":"session.snapshot"}}},{"properties":{"method":{"const":"pane.send_text"}}}]}}}`

func TestLocalCLIProbeOnlyExecutesReadOnlyCommands(t *testing.T) {
	dir := t.TempDir()
	binary, log := filepath.Join(dir, "herdr"), filepath.Join(dir, "commands")
	// Any accidental status/start/session invocation fails this fixture. The log
	// is written by the fixture, never by production diagnostics.
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\ncase \"$*\" in\n'--version') printf 'herdr 0.9.1\\n';;\n'api schema --json') printf '%s\\n' '" + capabilitySchemaFixture + "';;\n*) exit 91;;\nesac\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	endpoint := &LocalEndpoint{Binary: binary, SessionName: "must-not-be-started"}
	identity, err := endpoint.HerdrCLIIdentity(t.Context())
	if err != nil || identity.Version != "0.9.1" || identity.Protocol != 22 {
		t.Fatalf("%+v %v", identity, err)
	}
	commands, err := os.ReadFile(log)
	if err != nil || string(commands) != "--version\napi schema --json\n" {
		t.Fatalf("unsafe probe commands: %q %v", commands, err)
	}
}

func TestLocalCLIProbeCancellationIsRetryable(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexec sleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	_, err := (&LocalEndpoint{Binary: binary}).HerdrCLIIdentity(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || IsUnsupportedError(err) {
		t.Fatalf("cancelled probe became permanent: %v", err)
	}
}

func TestLocalCLIProbeBoundsInheritedStdout(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nsleep 2 &\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (&LocalEndpoint{Binary: binary}).HerdrCLIIdentity(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("inherited stdout outlived diagnostic deadline: %v (%s)", err, time.Since(start))
	}
}

type capabilitySSHSession struct {
	transcriptRetrySession
	command string
}

func (s *capabilitySSHSession) Start(command string) error { s.command = command; return nil }

type capabilitySSHTransport struct{ sessions []*capabilitySSHSession }

func (t *capabilitySSHTransport) NewSession() (sshSession, error) {
	s := t.sessions[0]
	t.sessions = t.sessions[1:]
	return s, nil
}
func (*capabilitySSHTransport) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("unexpected socket")
}
func (*capabilitySSHTransport) Close() error { return nil }

func TestSSHCLIProbeUsesFixedTransportCommands(t *testing.T) {
	for _, transport := range []string{"ssh", "tailcat"} {
		t.Run(transport, func(t *testing.T) {
			version := &capabilitySSHSession{transcriptRetrySession: transcriptRetrySession{output: "herdr 0.9.1\n"}}
			schema := &capabilitySSHSession{transcriptRetrySession: transcriptRetrySession{output: capabilitySchemaFixture}}
			endpoint := &SSHEndpoint{host: store.Host{Transport: transport, SessionName: "other-session"}, client: &capabilitySSHTransport{sessions: []*capabilitySSHSession{version, schema}}}
			identity, err := endpoint.HerdrCLIIdentity(t.Context())
			if err != nil || identity.Protocol != 22 {
				t.Fatalf("%+v %v", identity, err)
			}
			if transport == "tailcat" {
				if version.command != "herdr --version" || schema.command != "herdr api schema --json" {
					t.Fatalf("unexpected agent commands %q %q", version.command, schema.command)
				}
			} else if !strings.HasSuffix(version.command, "exec herdr --version'") || !strings.HasSuffix(schema.command, "exec herdr api schema --json'") {
				t.Fatalf("unexpected SSH commands %q %q", version.command, schema.command)
			}
			if !version.closed.Load() || !schema.closed.Load() {
				t.Fatal("diagnostic channels leaked")
			}
		})
	}
}

func TestSSHCLIProbeClosesOversizedResponse(t *testing.T) {
	session := &capabilitySSHSession{transcriptRetrySession: transcriptRetrySession{output: strings.Repeat("x", capabilityOutputLimit+1)}}
	endpoint := &SSHEndpoint{client: &capabilitySSHTransport{sessions: []*capabilitySSHSession{session}}}
	_, err := endpoint.HerdrCLIIdentity(t.Context())
	if err == nil || IsUnsupportedError(err) || !session.closed.Load() {
		t.Fatalf("oversized response=%v closed=%v", err, session.closed.Load())
	}
}

func TestAPIErrorKeepsTypedRejection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "herdr.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		_, _ = conn.Read(buf)
		_, _ = io.WriteString(conn, `{"id":"herdrx","error":{"code":"unknown_method","message":"pane.read"}}`)
	}()
	_, err = (&LocalEndpoint{SocketPath: path}).Call(t.Context(), "pane.read", nil)
	if !IsUnsupportedError(err) {
		t.Fatalf("typed error lost: %v", err)
	}
}
