package herdr

import (
	"context"

	"github.com/riba2534/herdrx/internal/launchers"
)

// LauncherEndpoint is an optional capability, like NativeScrollEndpoint: the
// endpoint can list the Claude launchers defined on its host. The script runs
// on that host and only names and flags come back (see internal/launchers).
//
// Every classified failure (no zsh, a timeout, an older daemon) is a Result
// with Supported false. The error is set only when the transport itself
// failed, so a shared connection can be dropped and redialed.
type LauncherEndpoint interface {
	Launchers(context.Context) (launchers.Result, error)
}

// Launchers runs discovery directly: the local Herdr shares this machine and
// user with the website.
func (e *LocalEndpoint) Launchers(ctx context.Context) (launchers.Result, error) {
	return launchers.Discover(ctx), nil
}
