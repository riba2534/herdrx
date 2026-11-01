// Package launchers discovers the Claude launchers a user defined as zsh
// aliases on a remote host.
//
// Alias bodies carry gateway addresses and keys, so they are parsed on the
// host that owns them: one fixed zsh script walks the aliases there and prints
// only a name and three flags per launcher. The workbench never receives,
// stores or logs a body. Local, basic SSH and Tailcat hosts all run this same
// script; Tailcat runs it inside the access daemon under one whitelisted
// command name.
package launchers

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Bounds of one discovery run.
const (
	// Timeout covers the user's interactive startup files plus the script.
	Timeout = 5 * time.Second
	// MaxOutput caps what the workbench reads. 200 launchers fit in a small
	// fraction of it; more output than this is treated as a failed read.
	MaxOutput = 256 << 10
	// MaxLaunchers caps the list after sorting by name.
	MaxLaunchers = 200
)

// Machine-readable reasons, matching LaunchersResult.reason in web/src/types.ts.
const (
	ReasonUnsupportedAgent = "unsupported_agent"
	ReasonShellUnavailable = "shell_unavailable"
	ReasonTimeout          = "timeout"
	ReasonReadFailed       = "read_failed"
)

// Exit statuses shared by the Tailcat daemon command and the SSH wrapper.
// 127 is what a shell returns for a missing command, 124 is what timeout(1)
// returns, anything else non-zero is a failed read.
const (
	ExitShellUnavailable = 127
	ExitTimeout          = 124
	ExitReadFailed       = 1
)

// AgentCommand is the exact exec name the access daemon accepts. Like
// herdrx-transcript it takes no arguments and runs no shell from the client.
const AgentCommand = "herdrx-launchers"

// linePrefix starts every line the script prints.
const linePrefix = "herdrx-launcher\t"

// Launcher is one alias whose first command word is claude. The JSON shape is
// Launcher in web/src/types.ts.
type Launcher struct {
	Name string `json:"name"`
	// Bypass: --dangerously-skip-permissions, or --permission-mode bypassPermissions.
	Bypass bool `json:"bypass"`
	// Fullscreen: CLAUDE_CODE_NO_FLICKER=1 among the leading assignments.
	Fullscreen bool `json:"fullscreen"`
	// Teammate: --teammate-mode.
	Teammate bool `json:"teammate"`
}

// Result is LaunchersResult in web/src/types.ts. Every failure is a result
// with Supported false and a Reason, never an HTTP error.
type Result struct {
	Launchers []Launcher `json:"launchers"`
	Supported bool       `json:"supported"`
	Reason    string     `json:"reason,omitempty"`
}

// Unsupported returns the empty result for reason.
func Unsupported(reason string) Result {
	return Result{Launchers: []Launcher{}, Supported: false, Reason: reason}
}

// Found wraps a parsed list.
func Found(list []Launcher) Result {
	if list == nil {
		list = []Launcher{}
	}
	return Result{Launchers: list, Supported: true}
}

// Discover runs the probe on this machine and parses what it printed.
func Discover(ctx context.Context) Result {
	output, err := Run(ctx)
	if err != nil {
		return Unsupported(ReasonOf(err))
	}
	return Found(Parse(output))
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)

// ValidName reports whether name can be a launcher. Such a name has no shell
// syntax, so typing it into a pane runs exactly that alias.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Parse keeps only well-formed launcher lines and drops everything else, such
// as noise printed by startup files. The list is sorted by name, without
// duplicates, and holds at most MaxLaunchers entries.
func Parse(output []byte) []Launcher {
	seen := make(map[string]bool)
	list := []Launcher{}
	for _, line := range strings.Split(string(output), "\n") {
		rest, ok := strings.CutPrefix(line, linePrefix)
		if !ok {
			continue
		}
		fields := strings.Split(rest, "\t")
		if len(fields) != 4 || !ValidName(fields[0]) || seen[fields[0]] {
			continue
		}
		var flags [3]bool
		valid := true
		for index, field := range fields[1:] {
			switch field {
			case "0":
			case "1":
				flags[index] = true
			default:
				valid = false
			}
		}
		if !valid {
			continue
		}
		seen[fields[0]] = true
		list = append(list, Launcher{Name: fields[0], Bypass: flags[0], Fullscreen: flags[1], Teammate: flags[2]})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	if len(list) > MaxLaunchers {
		list = list[:MaxLaunchers]
	}
	return list
}

// Format writes list in the script's own line format. The Tailcat daemon
// re-emits what it parsed, so even startup-file noise never leaves the host.
func Format(list []Launcher) []byte {
	var builder strings.Builder
	for _, launcher := range list {
		builder.WriteString(linePrefix + launcher.Name)
		for _, flag := range []bool{launcher.Bypass, launcher.Fullscreen, launcher.Teammate} {
			if flag {
				builder.WriteString("\t1")
			} else {
				builder.WriteString("\t0")
			}
		}
		builder.WriteByte('\n')
	}
	return []byte(builder.String())
}

// Error is a classified discovery failure. It never carries script output.
type Error struct{ Reason string }

func (e *Error) Error() string { return "launcher discovery failed: " + e.Reason }

// ReasonOf returns the reason of a discovery error; unknown errors are failed reads.
func ReasonOf(err error) string {
	var classified *Error
	if errors.As(err, &classified) {
		return classified.Reason
	}
	return ReasonReadFailed
}

// ExitStatus is the exit status that reports err across an exec channel.
func ExitStatus(err error) uint32 {
	switch ReasonOf(err) {
	case ReasonShellUnavailable:
		return ExitShellUnavailable
	case ReasonTimeout:
		return ExitTimeout
	default:
		return ExitReadFailed
	}
}

// ReasonForExit maps a non-zero exit status back to its reason.
func ReasonForExit(status int) string {
	switch status {
	case ExitShellUnavailable:
		return ReasonShellUnavailable
	case ExitTimeout:
		return ReasonTimeout
	default:
		return ReasonReadFailed
	}
}

// Script walks the regular aliases of an interactive zsh and prints, on file
// descriptor 3 only, one line per Claude launcher:
//
//	herdrx-launcher<TAB>name<TAB>bypass<TAB>fullscreen<TAB>teammate
//
// It never prints, expands or runs an alias body: ${(z)} splits the body into
// shell words and ${(Q)} only removes their quoting. Leading NAME=value words
// and env/command prefixes are skipped; the alias is a launcher when the next
// word is claude or ends in /claude. Flag words stop at the first command
// separator. The function resets options a startup file may have changed
// (xtrace, ksharrays, ...), and ${(#):-9} is a TAB.
//
// zsh parses a -c string as a whole, with the user's aliases active, so Script
// is not passed there: Loader turns alias expansion off and then evaluates
// Script from the environment variable ScriptVariable.
//
// Neither text has a single quote or a backslash, so every POSIX shell and fish
// pass them through one pair of single quotes unchanged (see SSHCommand).
const Script = `() {
builtin emulate -LR zsh
builtin setopt extended_glob
builtin zmodload -i zsh/parameter
local name raw word program tab=${(#):-9}
local -a words
local -i index bypass fullscreen teammate
for name in ${(ko)aliases}; do
  [[ $name == [A-Za-z0-9_][A-Za-z0-9_.-](#c0,63) ]] || continue
  words=( ${(z)aliases[$name]} )
  bypass=0 fullscreen=0 teammate=0 program=
  for (( index = 1; index <= $#words; index++ )); do
    word=${(Q)words[index]}
    if [[ $word == [A-Za-z_][A-Za-z0-9_]#=* ]]; then
      [[ $word == CLAUDE_CODE_NO_FLICKER=1 ]] && fullscreen=1
    elif [[ $word != (env|command) ]]; then
      program=$word
      break
    fi
  done
  [[ $program == claude || $program == */claude ]] || continue
  for (( index++; index <= $#words; index++ )); do
    raw=$words[index]
    [[ $raw == (";"|"&"|"|"|"&&"|"||"|"|&"|"&|"|"&!"|";|"|";&") ]] && break
    case ${(Q)raw} in
      (--dangerously-skip-permissions) bypass=1 ;;
      (--permission-mode) [[ ${(Q)words[index+1]} == bypassPermissions ]] && bypass=1 ;;
      (--teammate-mode) teammate=1 ;;
    esac
  done
  builtin print -r -u 3 -- "herdrx-launcher$tab$name$tab$bypass$tab$fullscreen$tab$teammate"
done
return 0
}
`

// ScriptVariable carries Script into the zsh that runs Loader.
const ScriptVariable = "HERDRX_LAUNCHERS_SCRIPT"

// Loader is the zsh -ic command. builtin keeps its own two words away from
// aliases; Script is parsed after alias expansion is off.
const Loader = `builtin setopt no_aliases; builtin eval "$` + ScriptVariable + `"`

// sshWrapper runs on a basic SSH host under POSIX sh, with Loader as $1 and
// Script as $2. The startup files' own stdout and stderr go to /dev/null; only
// descriptor 3 reaches the SSH channel. zsh leads its own process group: a new
// session through setsid(1) where it exists, otherwise a job-control group (sh
// without either falls back to zsh alone). A watchdog kills that group after
// five seconds and the wrapper exits 124; when zsh finishes first, whatever is
// left in its group is killed too.
const sshWrapper = `PATH="${PATH:-/usr/bin:/bin}:$HOME/.local/bin:/usr/local/bin"; export PATH
command -v zsh >/dev/null 2>&1 || exit 127
TERM=dumb; export TERM
` + ScriptVariable + `=$2; export ` + ScriptVariable + `
if command -v setsid >/dev/null 2>&1; then
  setsid zsh -ic "$1" 3>&1 </dev/null >/dev/null 2>&1 &
else
  set -m 2>/dev/null
  zsh -ic "$1" 3>&1 </dev/null >/dev/null 2>&1 &
fi
child=$!
set +m 2>/dev/null
(sleep 5; kill -s KILL -- -"$child" 2>/dev/null || kill -s KILL "$child" 2>/dev/null) </dev/null >/dev/null 2>&1 &
watchdog=$!
wait "$child"
code=$?
kill -s KILL "$watchdog" 2>/dev/null
kill -s KILL -- -"$child" 2>/dev/null
if [ "$code" -eq 137 ]; then code=124; fi
exit "$code"
`

// SSHCommand is the exec command for a basic SSH host. The login shell there
// may be bash, zsh, dash or fish; each receives four single-quoted words.
var SSHCommand = "sh -c '" + sshWrapper + "' herdrx-launchers '" + Loader + "' '" + Script + "'"

func init() {
	if strings.ContainsAny(Script+Loader+sshWrapper, `'\`) {
		panic("launcher scripts must not contain single quotes or backslashes")
	}
}
