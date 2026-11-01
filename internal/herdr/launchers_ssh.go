package herdr

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/riba2534/herdrx/internal/launchers"
)

// launcherRemoteSlack is the round trip on top of the remote timeout, so the
// remote watchdog reports a timeout before this side stops waiting.
const launcherRemoteSlack = 3 * time.Second

// Launchers runs discovery on the remote host. Basic SSH sends the fixed
// wrapper and script (launchers.SSHCommand) to the login shell; Tailcat sends
// the daemon's one whitelisted name. Neither carries user input, and the reply
// holds launcher lines only. Nothing here ever reads, logs or returns stderr.
func (e *SSHEndpoint) Launchers(ctx context.Context) (launchers.Result, error) {
	tailcat := e.host.Transport == "tailcat"
	command := launchers.SSHCommand
	if tailcat {
		command = launchers.AgentCommand
	} else if e.host.Transport != "" && e.host.Transport != "ssh" {
		return launchers.Unsupported(launchers.ReasonUnsupportedAgent), nil
	}
	runCtx, cancel := context.WithTimeout(ctx, launchers.Timeout+launcherRemoteSlack)
	defer cancel()
	timedOut := func() bool { return ctx.Err() == nil && runCtx.Err() != nil }
	session, err := e.newSession(runCtx)
	if err != nil {
		if timedOut() {
			return launchers.Unsupported(launchers.ReasonTimeout), nil
		}
		return launchers.Unsupported(launchers.ReasonReadFailed), fmt.Errorf("open SSH session for launcher discovery: %w", err)
	}
	defer session.Close()
	stop := context.AfterFunc(runCtx, func() { _ = session.Close() })
	defer stop()
	stdout, err := session.StdoutPipe()
	if err != nil {
		return launchers.Unsupported(launchers.ReasonReadFailed), nil
	}
	if err := session.Start(command); err != nil {
		if tailcat && !timedOut() {
			// A daemon from before discovery refuses the unknown name. Not
			// cached: once the CLI is updated the next request works.
			return launchers.Unsupported(launchers.ReasonUnsupportedAgent), nil
		}
		return launchers.Unsupported(launchers.ReasonReadFailed), nil
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, launchers.MaxOutput+1))
	if len(data) > launchers.MaxOutput {
		_ = session.Close()
	}
	waitErr := session.Wait()
	switch {
	case timedOut():
		return launchers.Unsupported(launchers.ReasonTimeout), nil
	case ctx.Err() != nil:
		return launchers.Unsupported(launchers.ReasonReadFailed), ctx.Err()
	case len(data) > launchers.MaxOutput || readErr != nil:
		return launchers.Unsupported(launchers.ReasonReadFailed), nil
	case waitErr != nil:
		return launchers.Unsupported(launchers.ReasonForExit(remoteExitStatus(waitErr))), nil
	}
	return launchers.Found(launchers.Parse(data)), nil
}
