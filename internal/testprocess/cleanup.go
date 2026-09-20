// Package testprocess shuts down processes created by isolated test fixtures.
// Call these functions with defer, before canceling the process context and
// before testing.T cleanup removes directories or restores environment variables.
package testprocess

import (
	"context"
	"os/exec"
	"syscall"
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

// Stop sends the signal installed by our Python native-terminal fixtures.
// SIGINT can be inherited as ignored on macOS runners; SIGTERM is explicit.
func Stop(t testing.TB, process *exec.Cmd) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = process.Process.Signal(syscall.SIGTERM)
	wait(t, ctx, process, "native terminal fixture")
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
