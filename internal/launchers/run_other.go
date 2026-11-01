//go:build !unix

package launchers

import "context"

// Run needs process groups to bound the probe; other platforms have no zsh
// launchers to discover.
func Run(context.Context) ([]byte, error) {
	return nil, &Error{Reason: ReasonShellUnavailable}
}
