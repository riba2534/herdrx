// Package testprocess shuts down processes created by isolated test fixtures.
// Call these functions with defer, before canceling the process context and
// before testing.T cleanup removes directories or restores environment variables.
package testprocess

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// StopHerdr targets only the named daemon started by the calling fixture.
func StopHerdr(t testing.TB, binary, session string, server *exec.Cmd) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopErr := exec.CommandContext(ctx, binary, "--session", session, "server", "stop").Run()
	wait(t, ctx, server, "Herdr daemon")
	if stopErr != nil && !t.Failed() {
		t.Errorf("stop isolated Herdr: %v", stopErr)
	}
}

// StopNative closes the fixture's control pipe. Its host must reap the native
// terminal child before exiting, independently of inherited signal handling.
func StopNative(t testing.TB, process *exec.Cmd, control io.Closer, diagnosticPath string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = control.Close()
	wait(t, ctx, process, "native terminal fixture")
	logs, err := os.ReadFile(diagnosticPath)
	for _, stage := range []string{"control EOF received", "reaped status=", "master closed; host exiting"} {
		if !strings.Contains(string(logs), stage) {
			t.Errorf("native terminal fixture exited without expected cleanup stage %q (read: %v)", stage, err)
		}
	}
	if t.Failed() {
		if len(logs) > 16384 {
			logs = logs[len(logs)-16384:]
		}
		t.Logf("native terminal fixture PID %d cleanup stages/Python stacks (read: %v):\n%s", process.Process.Pid, err, logs)
	}
}

func wait(t testing.TB, ctx context.Context, process *exec.Cmd, name string) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	select {
	case err := <-done:
		if err != nil && !t.Failed() {
			t.Errorf("isolated %s did not exit cleanly: %v", name, err)
		}
	case <-ctx.Done():
		_ = process.Process.Kill()
		<-done
		if !t.Failed() {
			t.Errorf("isolated %s did not finish graceful shutdown before cleanup", name)
		}
	}
}
