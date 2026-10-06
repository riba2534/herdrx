package herdr

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// HerdrStartResult reports a user-requested start of Herdr on a remote host.
//
// Status is "started", "running" (already up, nothing launched), "missing" (no
// herdr command) or "failed" (launched but not ready in time). Method names how
// the server was detached: "systemd" (a transient scope of the user's systemd
// manager), "setsid" (a new session outside this SSH connection) or "nohup".
// Linger is the remote loginctl value: "yes", "no" or "unknown".
type HerdrStartResult struct {
	Status string `json:"status"`
	Method string `json:"method,omitempty"`
	Linger string `json:"linger,omitempty"`
}

// ErrHerdrStartUnsupported is returned for transports that cannot run a shell
// command on the remote host (Tailcat only accepts a fixed command grammar).
var ErrHerdrStartUnsupported = errors.New("this connection cannot start Herdr on the remote host")

// StartHerdr starts the Herdr server for this host's session when it is not
// running. The server is detached from the SSH connection that launched it, so
// closing the workbench, the SSH session or the website never stops it. A
// running server is never replaced, restarted or upgraded.
func (e *SSHEndpoint) StartHerdr(ctx context.Context) (HerdrStartResult, error) {
	if e.host.Transport != "ssh" {
		return HerdrStartResult{}, ErrHerdrStartUnsupported
	}
	script, err := herdrStartScript(e.host.SessionName)
	if err != nil {
		return HerdrStartResult{}, err
	}
	session, err := e.newSession(ctx)
	if err != nil {
		return HerdrStartResult{}, fmt.Errorf("open SSH session to start Herdr: %w", err)
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	output, err := session.Output("sh -c '" + strings.ReplaceAll(script, "'", `'"'"'`) + "'")
	if err != nil {
		return HerdrStartResult{}, fmt.Errorf("run Herdr start on the remote host: %w", err)
	}
	return parseHerdrStart(string(output))
}

// herdrStartScript is plain POSIX sh so it runs on any remote without herdrx.
//
// Detaching: with linger enabled the server goes into its own systemd --user
// scope, which outlives every login session; otherwise it gets a new session
// via setsid, which is what Herdr's own client does when it spawns a server.
// The server starts through the user's login shell so it sees the same PATH and
// locale as a terminal login; panes inherit that environment.
func herdrStartScript(sessionName string) (string, error) {
	sessionArgs, unitName := "", "default"
	if sessionName != "" {
		if !publicID.MatchString(sessionName) {
			return "", fmt.Errorf("invalid herdr session name")
		}
		sessionArgs, unitName = "--session "+sessionName, sessionName
	}
	return `PATH="${PATH:-/usr/bin:/bin}:$HOME/.local/bin:/usr/local/bin"; export PATH
herdr_bin=$(command -v herdr 2>/dev/null) || { echo "herdrx-start missing"; exit 0; }
running() { "$herdr_bin" ` + sessionArgs + ` status server 2>/dev/null | grep -q '^status: running'; }
if running; then echo "herdrx-start running"; exit 0; fi
linger=unknown
if command -v loginctl >/dev/null 2>&1; then
  case "$(loginctl show-user "$(id -u)" -p Linger 2>/dev/null)" in
    Linger=yes) linger=yes ;;
    Linger=no) linger=no ;;
  esac
fi
login_shell=${SHELL:-/bin/sh}
case "${login_shell##*/}" in bash|zsh|sh|dash|ksh|mksh) ;; *) login_shell=/bin/sh ;; esac
if [ "$linger" = yes ] && command -v systemd-run >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
  method=systemd
  unit="herdrx-herdr-` + unitName + `-$(date +%s)"
  if command -v setsid >/dev/null 2>&1; then
    ( setsid systemd-run --user --scope --quiet --collect --unit="$unit" -- "$login_shell" -lc 'exec "$0" "$@"' "$herdr_bin" ` + sessionArgs + ` server </dev/null >/dev/null 2>&1 & )
  else
    ( nohup systemd-run --user --scope --quiet --collect --unit="$unit" -- "$login_shell" -lc 'exec "$0" "$@"' "$herdr_bin" ` + sessionArgs + ` server </dev/null >/dev/null 2>&1 & )
  fi
elif command -v setsid >/dev/null 2>&1; then
  method=setsid
  ( setsid "$login_shell" -lc 'exec "$0" "$@"' "$herdr_bin" ` + sessionArgs + ` server </dev/null >/dev/null 2>&1 & )
else
  method=nohup
  ( nohup "$login_shell" -lc 'exec "$0" "$@"' "$herdr_bin" ` + sessionArgs + ` server </dev/null >/dev/null 2>&1 & )
fi
tries=0
while [ "$tries" -lt 30 ]; do
  if running; then echo "herdrx-start started $method $linger"; exit 0; fi
  sleep 0.3 2>/dev/null || sleep 1
  tries=$((tries + 1))
done
echo "herdrx-start failed $method $linger"
`, nil
}

func parseHerdrStart(output string) (HerdrStartResult, error) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "herdrx-start" {
			continue
		}
		result := HerdrStartResult{Status: fields[1]}
		switch result.Status {
		case "running", "missing":
			return result, nil
		case "started", "failed":
			if len(fields) != 4 {
				break
			}
			result.Method, result.Linger = fields[2], fields[3]
			return result, nil
		}
	}
	return HerdrStartResult{}, fmt.Errorf("unexpected Herdr start output")
}
