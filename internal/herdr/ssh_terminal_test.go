package herdr

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHTerminalNonInteractivePath(t *testing.T) {
	// Exercise the shell used by OpenSSH, including a HOME needing shell quoting.
	home := filepath.Join(t.TempDir(), "user's home $literal")
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"herdr", "--session", "test-session", "terminal", "session", "observe", "w2:p1", "--cols", "80", "--rows", "24"}
	command := terminalCommand("ssh", args)
	run := func(path string) string {
		t.Helper()
		cmd := exec.Command("/bin/sh", "-c", command)
		cmd.Env = []string{"HOME=" + home, "PATH=" + path}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("non-interactive command: %v: %s", err, out)
		}
		return string(out)
	}
	if got, want := run("/usr/bin:/bin"), strings.Join(args[1:], "\n")+"\n"; got != want {
		t.Fatalf("terminal arguments: got %q, want %q", got, want)
	}
	preferred := filepath.Join(t.TempDir(), "preferred")
	if err := os.Mkdir(preferred, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(preferred, "herdr"), []byte("#!/bin/sh\nprintf 'preferred\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := run(preferred + ":/usr/bin:/bin"); got != "preferred\n" {
		t.Fatalf("existing PATH was overridden: %q", got)
	}
	if got := terminalCommand("tailcat", args); got != strings.Join(args, " ") {
		t.Fatalf("Tailcat command must retain the daemon's restricted grammar: %q", got)
	}
}
