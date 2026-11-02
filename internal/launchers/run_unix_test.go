//go:build unix

package launchers_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/riba2534/herdrx/internal/launchers"
	"github.com/riba2534/herdrx/internal/launchers/launcherstest"
)

const ttyChildVariable = "HERDRX_LAUNCHERS_TTY_CHILD"

// A website or daemon started in a terminal (foreground herdrx-server, a
// foreground `herdrx serve`) has a controlling terminal. The probe must not
// inherit it: zsh -i would enable job control from a background group and be
// stopped until the timeout. The test runs itself again as the session leader
// of a pseudo-terminal, and that child runs the probe.
func TestRunFromAProcessWithAControllingTerminal(t *testing.T) {
	if os.Getenv(ttyChildVariable) == "1" {
		output, err := launchers.Run(context.Background())
		reason := ""
		if err != nil {
			reason = launchers.ReasonOf(err)
		}
		fmt.Printf("probe reason=%q launchers=%d\n", reason, len(launchers.Parse(output)))
		return
	}
	launcherstest.RequireZsh(t)
	launcherstest.Isolate(t, launcherstest.Zshrc)
	command := exec.Command(os.Args[0], "-test.run=^TestRunFromAProcessWithAControllingTerminal$", "-test.count=1")
	command.Env = append(os.Environ(), ttyChildVariable+"=1")
	started := time.Now()
	terminal, err := pty.Start(command)
	if err != nil {
		t.Skipf("no pseudo-terminal available: %v", err)
	}
	defer terminal.Close()
	output := make(chan []byte, 1)
	go func() {
		// Reading ends with EIO once the child and everything it left are gone.
		data, _ := io.ReadAll(terminal)
		output <- data
	}()
	if err := command.Wait(); err != nil {
		t.Fatalf("child: %v", err)
	}
	elapsed := time.Since(started)
	var data []byte
	select {
	case data = <-output:
	case <-time.After(5 * time.Second):
		t.Fatal("the terminal stayed open after the child exited")
	}
	want := fmt.Sprintf("probe reason=\"\" launchers=%d", len(launcherstest.Expected))
	if !strings.Contains(string(data), want) {
		t.Fatalf("child output %q, want %q", data, want)
	}
	if elapsed > launchers.Timeout-time.Second {
		t.Fatalf("the probe took %s under a terminal", elapsed)
	}
}
