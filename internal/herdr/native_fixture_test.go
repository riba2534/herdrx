package herdr

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// An inherited ignored signal must not keep the host alive or leave its child
// behind when the test releases the control pipe (as on the macOS CI runners).
func TestNativeTerminalFixtureReapsChildAfterControlEOF(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("native terminal fixture requires python3")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "native-child.py")
	script := `#!/usr/bin/env python3
import os,signal,time
signal.signal(signal.SIGTERM,signal.SIG_IGN)
signal.signal(signal.SIGINT,signal.SIG_IGN)
with open(os.path.join(os.path.dirname(__file__),'child.pid'),'w') as f: f.write(str(os.getpid()))
while True: time.sleep(1)
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var pid int
	func() {
		stop := startNativeTerminalFixture(t, ctx, dir, binary, "isolated-cleanup")
		defer stop()
		for pid == 0 {
			contents, _ := os.ReadFile(filepath.Join(dir, "child.pid"))
			pid, _ = strconv.Atoi(string(contents))
			if pid != 0 {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("native child did not start")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("native child PID %d still exists after host cleanup: %v", pid, err)
	}
	logs, err := os.ReadFile(filepath.Join(dir, "native.log"))
	if err != nil || !strings.Contains(string(logs), "after graceful timeout") {
		t.Fatalf("fixture did not exercise the ignored-signal child cleanup: %v\n%s", err, logs)
	}
}
