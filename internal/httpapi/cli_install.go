package httpapi

import (
	"fmt"
	"net/http"
)

// cliInstall is public so a remote shell can install without a browser session.
// It only contains public release information from the fixed CLI repository.
func (a *API) cliInstall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	release := a.cliReleases.get(r.Context())
	if release.Status != "available" || !cliReleaseVersion.MatchString(release.Version) {
		http.Error(w, "# CLI release unavailable; retry or visit "+cliRepositoryURL+"/releases\nexit 1", http.StatusServiceUnavailable)
		return
	}
	// Keep the operation inside a complete function so a truncated response
	// cannot execute a partially downloaded installation sequence. The release
	// installer verifies the archive and installs atomically, preserving config.
	fmt.Fprintf(w, `#!/bin/sh
herdrx_install() (
  set -eu
  umask 077
  herdrx_installer=$(mktemp)
  trap 'rm -f "$herdrx_installer"' 0
  trap 'exit 1' HUP INT TERM
  curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 180 '%s/releases/download/%s/install-herdrx.sh' -o "$herdrx_installer"
  sh "$herdrx_installer" --version '%s' "$@"
)
herdrx_install "$@"
`, cliRepositoryURL, release.Version, release.Version)
}
