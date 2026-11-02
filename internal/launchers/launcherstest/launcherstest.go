// Package launcherstest gives tests an isolated interactive zsh whose startup
// file defines launcher aliases with fake secrets in their bodies.
package launcherstest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/launchers"
)

// Secrets appear only in alias bodies and startup-file noise. No discovery
// output, response, error or log may contain any of them.
var Secrets = []string{"sk-herdrx-FAKE-0000", "gateway.example.test", "proxy.example.test"}

// Zshrc defines launchers in the forms users write, aliases that must not be
// launchers, and startup-file behavior that must not leak or break discovery:
// noise on stdout and stderr (including every alias body), aliases and global
// aliases over words the script uses, and options such as xtrace. Bodies with
// command substitutions would create the marker files if anything ran them.
func Zshrc(home string) string {
	return fmt.Sprintf(`alias xcc="ANTHROPIC_BASE_URL=https://gateway.example.test ANTHROPIC_AUTH_TOKEN=sk-herdrx-FAKE-0000 CLAUDE_CODE_NO_FLICKER=1 claude --teammate-mode in-process --dangerously-skip-permissions"
alias plain=claude
alias pm="env HTTPS_PROXY=http://proxy.example.test /opt/tools/bin/claude --permission-mode bypassPermissions"
alias eq="claude --permission-mode=bypassPermissions --teammate-mode=in-process --model opus"
alias quoted='TOKEN="sk-herdrx-FAKE-0000 two words" command ~/.local/bin/claude --model opus'
alias nf="CLAUDE_CODE_NO_FLICKER=0 claude --permission-mode default"
alias chain="claude --model opus; echo --dangerously-skip-permissions --teammate-mode"
alias sub='A=$(touch %[1]s/ran-assignment) claude'
alias notclaude="claudex --dangerously-skip-permissions"
alias first='$(touch %[1]s/ran-command) claude'
alias ll="ls -l"
alias a+b=claude
alias -g G=claude
alias print="echo HIJACKED"
alias local="echo HIJACKED"
alias builtin="echo HIJACKED"
alias -g word="| cat"
alias -g raw="| cat"
echo "rc noise sk-herdrx-FAKE-0000"
echo "rc noise sk-herdrx-FAKE-0000" >&2
alias
setopt xtrace ksh_arrays sh_word_split no_unset err_exit
`, home)
}

// Expected is what discovery returns for Zshrc.
var Expected = []launchers.Launcher{
	{Name: "chain"},
	{Name: "eq", Bypass: true, Teammate: true},
	{Name: "nf"},
	{Name: "plain"},
	{Name: "pm", Bypass: true},
	{Name: "quoted"},
	{Name: "sub"},
	{Name: "xcc", Bypass: true, Fullscreen: true, Teammate: true},
}

// Markers are the files a body would create if discovery ran it.
var Markers = []string{"ran-assignment", "ran-command"}

// RequireZsh skips a test that needs a real zsh on a machine without one.
func RequireZsh(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh is not installed; launcher discovery runs the user's interactive zsh")
	}
}

// Isolate points HOME and ZDOTDIR at a new directory whose .zshrc is
// rc(directory) and returns the directory.
func Isolate(t testing.TB, rc func(home string) string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(rc(home)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	return home
}

// AssertNoSecrets fails when text carries anything from an alias body.
func AssertNoSecrets(t testing.TB, text string) {
	t.Helper()
	for _, secret := range Secrets {
		if strings.Contains(text, secret) {
			t.Fatalf("output leaked %q: %q", secret, text)
		}
	}
}

// AssertNoMarkers fails when an alias body ran during discovery.
func AssertNoMarkers(t testing.TB, home string) {
	t.Helper()
	for _, marker := range Markers {
		if _, err := os.Stat(filepath.Join(home, marker)); err == nil {
			t.Fatalf("discovery ran an alias body: %s exists", marker)
		}
	}
}
