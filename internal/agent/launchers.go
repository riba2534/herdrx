package agent

import (
	"context"
	"io"

	"github.com/riba2534/herdrx/internal/launchers"
)

// LaunchersCommand is the only exec name the access daemon accepts for
// launcher discovery. Like TranscriptCommand it takes no arguments: the daemon
// runs the fixed script from internal/launchers itself.
const LaunchersCommand = launchers.AgentCommand

// ServeLaunchers discovers this host's Claude launchers and writes them in the
// script's line format. It re-emits only what it parsed, so startup-file
// output and alias bodies stay on this host. The result is the exit status:
// 0, or 127/124/1 for no zsh, a timeout and a failed read.
func ServeLaunchers(ctx context.Context, w io.Writer) uint32 {
	output, err := launchers.Run(ctx)
	if err != nil {
		return launchers.ExitStatus(err)
	}
	if _, err := w.Write(launchers.Format(launchers.Parse(output))); err != nil {
		return launchers.ExitReadFailed
	}
	return 0
}
