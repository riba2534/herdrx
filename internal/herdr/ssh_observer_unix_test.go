//go:build unix

package herdr

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This child keeps stdout completely quiet and never reads stdin, like Herdr
// observe. A separate socket proves that it exits without relying on PID reuse.
func TestSSHQuietObserverProcess(t *testing.T) {
	path := os.Getenv("HERDRX_QUIET_OBSERVER_PROBE")
	if path == "" {
		return
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		os.Exit(2)
	}
	_, _ = fmt.Fprintln(conn, "ready")
	_, _ = io.Copy(io.Discard, conn)
	os.Exit(0)
}

func TestSSHObserverStopsOnChannelEOF(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "hx-observer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "probe")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	wrapper := "#!/bin/sh\nexec " + quote(binary) + " -test.run='^TestSSHQuietObserverProcess$'\n"
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"herdr", "--session", "fixture", "terminal", "session", "observe", "w1:p1", "--cols", "80", "--rows", "24"}
	// More cycles than OpenSSH's usual session limit; none may wait for output.
	for i := 0; i < 12; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", terminalCommand("ssh", args))
		cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin", "HERDRX_QUIET_OBSERVER_PROBE="+socket)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		_ = listener.SetDeadline(time.Now().Add(2 * time.Second))
		probe, err := listener.AcceptUnix()
		if err != nil {
			stdin.Close()
			cancel()
			cmd.Wait()
			t.Fatal(err)
		}
		_ = probe.SetReadDeadline(time.Now().Add(2 * time.Second))
		if line, err := bufio.NewReader(probe).ReadString('\n'); err != nil || line != "ready\n" {
			probe.Close()
			stdin.Close()
			cancel()
			cmd.Wait()
			t.Fatalf("observer readiness: %q %v", line, err)
		}
		if _, err = stdin.Write([]byte("ignored input\n")); err != nil {
			t.Fatal(err)
		}
		_ = stdin.Close()
		_, readErr := probe.Read(make([]byte, 1))
		probe.Close()
		waitErr := cmd.Wait()
		timedOut := ctx.Err()
		cancel()
		if !errors.Is(readErr, io.EOF) {
			t.Fatalf("cycle %d: quiet observer survived EOF: %v", i, readErr)
		}
		if timedOut != nil {
			t.Fatalf("cycle %d: supervisor did not exit: %v", i, waitErr)
		}
	}
}

func TestSSHObserverPropagatesEarlyExit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte("#!/bin/sh\nprintf 'observer failed\\n' >&2\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", terminalCommand("ssh", []string{"herdr", "terminal", "session", "observe", "w1:p1", "--cols", "80", "--rows", "24"}))
	cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 || !strings.Contains(string(out), "observer failed") || ctx.Err() != nil {
		t.Fatalf("early exit: %s %v, context=%v", out, err, ctx.Err())
	}
}
