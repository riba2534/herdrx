package agentcli

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type autostartFixture struct {
	starter  *herdrAutostart
	launched []*exec.Cmd
	env      map[string]string
	logs     *bytes.Buffer
	up       bool
	dialErr  error
}

func newAutostartFixture(t *testing.T, goos string) *autostartFixture {
	t.Helper()
	f := &autostartFixture{env: map[string]string{"SHELL": "/usr/bin/zsh", "HOME": t.TempDir()}, logs: &bytes.Buffer{}, dialErr: syscall.ENOENT}
	f.starter = &herdrAutostart{
		logger: slog.New(slog.NewTextHandler(f.logs, nil)),
		socket: "/tmp/herdrx-test/herdr.sock",
		lookPath: func(name string) (string, error) {
			if path, ok := f.env["path:"+name]; ok {
				return path, nil
			}
			return "", exec.ErrNotFound
		},
		getenv: func(name string) string { return f.env[name] },
		probe: func(_ context.Context, args ...string) error {
			if f.env["user-manager"] == "ok" {
				return nil
			}
			return errors.New("no user manager")
		},
		launch: func(cmd *exec.Cmd) error {
			f.launched = append(f.launched, cmd)
			f.up = true
			return nil
		},
		dial: func(string) error {
			if f.up {
				return nil
			}
			return f.dialErr
		},
		goos:       goos,
		readyAfter: time.Second,
	}
	return f
}

func TestHerdrAutostartUsesAnIndependentSystemdScope(t *testing.T) {
	f := newAutostartFixture(t, "linux")
	f.env["path:herdr"] = "/opt/herdr/bin/herdr"
	f.env["path:systemd-run"] = "/usr/bin/systemd-run"
	f.env["path:systemctl"] = "/usr/bin/systemctl"
	f.env["user-manager"] = "ok"
	f.env["INVOCATION_ID"] = "service"
	f.starter.run(t.Context())
	if len(f.launched) != 1 {
		t.Fatalf("launches = %d, logs: %s", len(f.launched), f.logs)
	}
	cmd := f.launched[0]
	args := strings.Join(cmd.Args, " ")
	for _, want := range []string{"/usr/bin/systemd-run --user --scope --quiet --collect --unit=herdrx-herdr-default-", "-- /usr/bin/zsh -lc exec \"$0\" \"$@\" /opt/herdr/bin/herdr server"} {
		if !strings.Contains(args, want) {
			t.Fatalf("launch %q lacks %q", args, want)
		}
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid || cmd.Stdin != nil || cmd.Stdout != nil || cmd.Dir != f.env["HOME"] {
		t.Fatalf("launch is not detached: %+v dir=%q", cmd.SysProcAttr, cmd.Dir)
	}
	if !strings.Contains(f.logs.String(), "herdr started") {
		t.Fatalf("logs: %s", f.logs)
	}
}

func TestHerdrAutostartNeverTouchesARunningOrUnknownServer(t *testing.T) {
	for name, dialErr := range map[string]error{"running": nil, "permission": syscall.EACCES} {
		t.Run(name, func(t *testing.T) {
			f := newAutostartFixture(t, "linux")
			f.env["path:herdr"] = "/opt/herdr/bin/herdr"
			f.dialErr = dialErr
			f.starter.run(t.Context())
			if len(f.launched) != 0 {
				t.Fatalf("launched while the socket reported %v", dialErr)
			}
		})
	}
}

func TestHerdrAutostartRespectsOptOutAndMissingHerdr(t *testing.T) {
	f := newAutostartFixture(t, "linux")
	f.env["path:herdr"] = "/opt/herdr/bin/herdr"
	f.env[herdrAutostartEnv] = "0"
	f.starter.run(t.Context())
	if len(f.launched) != 0 || !strings.Contains(f.logs.String(), "disabled") {
		t.Fatalf("opt-out ignored: %s", f.logs)
	}
	f = newAutostartFixture(t, "linux")
	f.starter.run(t.Context())
	if len(f.launched) != 0 || !strings.Contains(f.logs.String(), "not installed") {
		t.Fatalf("missing herdr: %s", f.logs)
	}
}

func TestHerdrAutostartFindsTheInstallerLocationOutsidePath(t *testing.T) {
	f := newAutostartFixture(t, "darwin")
	bin := filepath.Join(f.env["HOME"], ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.starter.run(t.Context())
	if len(f.launched) != 1 || f.launched[0].Args[len(f.launched[0].Args)-2] != filepath.Join(bin, "herdr") {
		t.Fatalf("launch %v", f.launched)
	}
	if f.launched[0].Args[0] != "/usr/bin/zsh" || !f.launched[0].SysProcAttr.Setsid {
		t.Fatalf("macOS launch must be a new session of the login shell: %v", f.launched[0].Args)
	}
}

func TestHerdrAutostartRefusesToShareTheServiceLifetime(t *testing.T) {
	f := newAutostartFixture(t, "linux")
	f.env["path:herdr"] = "/opt/herdr/bin/herdr"
	f.env["INVOCATION_ID"] = "service"
	f.starter.run(t.Context())
	if len(f.launched) != 0 || !strings.Contains(f.logs.String(), "share this service") {
		t.Fatalf("started inside the service cgroup: %s", f.logs)
	}
	f = newAutostartFixture(t, "linux")
	f.env["path:herdr"] = "/opt/herdr/bin/herdr"
	f.env["SHELL"] = "/usr/bin/fish"
	f.starter.run(t.Context())
	if len(f.launched) != 1 || f.launched[0].Args[0] != "/bin/sh" {
		t.Fatalf("an unknown login shell must fall back to /bin/sh: %v", f.launched)
	}
}

func TestDefaultEnvWiresAutostartButTestEnvironmentsDoNot(t *testing.T) {
	if DefaultEnv().HerdrAutostart == nil {
		t.Fatal("the real environment must autostart Herdr")
	}
	if (Environment{}).HerdrAutostart != nil {
		t.Fatal("a zero environment must never launch Herdr")
	}
}
