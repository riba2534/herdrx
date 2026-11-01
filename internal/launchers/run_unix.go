//go:build unix

package launchers

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// drainGrace bounds how long Run keeps reading after zsh exited. A process the
// startup files moved out of zsh's group may still hold descriptor 3.
const drainGrace = 200 * time.Millisecond

// Run executes Script in an interactive zsh on this machine and returns what
// it printed on descriptor 3. stdin is /dev/null, the startup files' stdout and
// stderr are discarded, and TERM is dumb. zsh leads a new process group; that
// group is killed on timeout and once zsh exits, so nothing the probe started
// outlives it there.
func Run(ctx context.Context) ([]byte, error) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		return nil, &Error{Reason: ReasonShellUnavailable}
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, &Error{Reason: ReasonReadFailed}
	}
	defer reader.Close()
	command := exec.Command(zsh, "-ic", Loader)
	command.Env = probeEnvironment(os.Environ())
	command.ExtraFiles = []*os.File{writer}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = command.Start()
	_ = writer.Close()
	if err != nil {
		return nil, &Error{Reason: ReasonShellUnavailable}
	}
	group := command.Process.Pid
	killGroup := func() { _ = syscall.Kill(-group, syscall.SIGKILL) }

	type readResult struct {
		data []byte
		err  error
	}
	reads := make(chan readResult, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(reader, MaxOutput+1))
		if len(data) > MaxOutput {
			// Stop a writer that would otherwise block on the full pipe.
			killGroup()
		}
		reads <- readResult{data, err}
	}()
	exits := make(chan error, 1)
	go func() { exits <- command.Wait() }()

	var waitErr error
	select {
	case waitErr = <-exits:
	case <-ctx.Done():
		killGroup()
		<-exits
		return nil, &Error{Reason: ReasonTimeout}
	}
	killGroup()
	var read readResult
	select {
	case read = <-reads:
	case <-time.After(drainGrace):
		// Something outside the group still holds the pipe. zsh is gone and
		// the reader drained what it wrote long before this grace.
		_ = reader.Close()
		read = <-reads
		read.err = nil
	}
	if waitErr != nil || read.err != nil || len(read.data) > MaxOutput {
		return nil, &Error{Reason: ReasonReadFailed}
	}
	return read.data, nil
}

func probeEnvironment(environment []string) []string {
	out := make([]string, 0, len(environment)+2)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, "TERM=") && !strings.HasPrefix(entry, ScriptVariable+"=") {
			out = append(out, entry)
		}
	}
	return append(out, "TERM=dumb", ScriptVariable+"="+Script)
}
