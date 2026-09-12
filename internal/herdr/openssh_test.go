package herdr

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/store"
)

func TestOpenSSHArguments(t *testing.T) {
	options := SSHOptions{Host: store.Host{Hostname: "devbox", AuthMethod: "system_ssh"}, Timeout: 15 * time.Second}
	args, err := openSSHArgs(options, "/tmp/fixture/ctl")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"GSSAPIAuthentication=yes", "StrictHostKeyChecking=yes", "BatchMode=yes", "ControlPersist=no", "ForkAfterAuthentication=no", "UpdateHostKeys=no", "ForwardAgent=no", "PermitLocalCommand=no", "ClearAllForwardings=yes", "-- devbox"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s: %v", want, args)
		}
	}
	for _, arg := range args {
		if arg == "-F" || arg == "-p" || arg == "-l" || strings.HasPrefix(arg, "ProxyJump=") || strings.HasPrefix(arg, "ProxyCommand=") {
			t.Fatalf("overrode administrator SSH config: %v", args)
		}
	}
	options.Host.Port, options.Host.Username = 2222, "test-user"
	args, err = openSSHArgs(options, "/tmp/fixture/ctl")
	if err != nil || !strings.Contains(strings.Join(args, " "), "-p 2222 -l test-user -- devbox") {
		t.Fatalf("explicit overrides: %v %v", args, err)
	}
	for _, bad := range []string{"-oProxyCommand=id", "host;id", "$(id)", "user@host", "host\x00", "host%h", "host`id`"} {
		options.Host.Hostname = bad
		if _, err := openSSHArgs(options, "ctl"); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestOpenSSHDisabledAndFailedStart(t *testing.T) {
	if _, err := DialOpenSSHEndpoint(t.Context(), "", SSHOptions{}); err == nil {
		t.Fatal("accepted disabled backend")
	}
	if _, err := DialOpenSSHEndpoint(t.Context(), "/missing/ssh", SSHOptions{Host: store.Host{Hostname: "test"}}); err == nil {
		t.Fatal("accepted missing binary")
	}
}

func TestOpenSSHSessionDoesNotFallback(t *testing.T) {
	binary, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("system OpenSSH unavailable")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	transport := &openSSHTransport{binary: binary, control: filepath.Join(t.TempDir(), "missing"), ctx: ctx}
	session, err := transport.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err = session.Output("printf should-not-run"); err == nil {
		t.Fatal("missing master did not fail closed")
	}
	cancel()
	if _, err := transport.NewSession(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed session: %v", err)
	}
}

func TestOpenSSHSessionCloseReapsProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	session := &openSSHSession{cmd: exec.CommandContext(ctx, "/bin/sh", "-c", "exec cat"), cancel: cancel}
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := session.Start("unused"); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- session.Wait() }()
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("Close did not reap session process")
	}
	if session.cmd.ProcessState == nil {
		t.Fatal("child process was not reaped")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenSSHSessionCloseBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	session := &openSSHSession{cmd: exec.CommandContext(ctx, "/bin/sh"), cancel: cancel}
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	// Check both ends: descriptors held for a child that never starts must
	// also be released, rather than relying on os/exec's Start/Wait cleanup.
	pipes := []io.Closer{stdin, stdout.(io.Closer), stderr.(io.Closer), session.cmd.Stdin.(io.Closer), session.cmd.Stdout.(io.Closer), session.cmd.Stderr.(io.Closer)}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Start("unused"); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed session started: %v", err)
	}
	for _, pipe := range pipes {
		if err := pipe.Close(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("unstarted session retained a pipe: %v", err)
		}
	}
	if _, err := session.StdinPipe(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed session accepted a new pipe: %v", err)
	}
}

func TestOpenSSHSessionStreams(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	session := &openSSHSession{cmd: openSSHCommand(ctx, "/bin/sh", "-c", "cat; printf diagnostic >&2"), cancel: cancel}
	defer session.Close()
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Start("unused"); err != nil {
		t.Fatal(err)
	}
	const input = "terminal input\n中文\x00binary"
	if _, err := io.WriteString(stdin, input); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(stdout)
	if err != nil {
		t.Fatal(err)
	}
	diagnostic, err := io.ReadAll(stderr)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Wait(); err != nil {
		t.Fatal(err)
	}
	if string(output) != input || string(diagnostic) != "diagnostic" {
		t.Fatalf("session streams changed: stdout=%q stderr=%q", output, diagnostic)
	}
}

func TestOpenSSHLiveSnapshot(t *testing.T) {
	host := os.Getenv("HERDRX_TEST_OPENSSH_HOST")
	if host == "" {
		t.Skip("set HERDRX_TEST_OPENSSH_HOST for a read-only live probe")
	}
	binary := os.Getenv("HERDRX_TEST_OPENSSH_BIN")
	if binary == "" {
		binary = "ssh"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	e, err := DialOpenSSHEndpoint(ctx, binary, SSHOptions{Host: store.Host{Transport: "ssh", Hostname: host, AuthMethod: "system_ssh"}})
	if err != nil {
		t.Fatal(err)
	}
	transport := e.client.(*openSSHTransport)
	defer e.Close()
	if _, err := e.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	conn, err := e.OpenTerminalSocket(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(transport.dir); !os.IsNotExist(err) {
		t.Fatal("OpenSSH socket directory leaked")
	}
	if _, err := e.Snapshot(ctx); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed endpoint: %v", err)
	}
	t.Log("OpenSSH authentication, snapshot, native socket forwarding and cleanup succeeded; no pane input sent")
}
