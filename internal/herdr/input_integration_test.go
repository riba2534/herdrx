package herdr

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Uses a separate Herdr server and raw PTY under t.TempDir. It never writes
// into the user's existing panes. Opt in with HERDRX_TEST_HERDR.
func TestInputWithRealHerdr(t *testing.T) {
	binary := os.Getenv("HERDRX_TEST_HERDR")
	if binary == "" {
		t.Skip("HERDRX_TEST_HERDR is not set")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HERDR_CONFIG_PATH", filepath.Join(dir, "config.toml"))
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("onboarding = false\n[terminal]\ndefault_shell = \"/bin/sh\"\nshell_mode = \"non_login\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	server := exec.CommandContext(ctx, binary, "--session", "input-test", "server")
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command(binary, "--session", "input-test", "server", "stop").Run()
		_ = server.Process.Kill()
		_ = server.Wait()
	})
	endpoint, err := NewLocalEndpoint(binary, "input-test")
	if err != nil {
		t.Fatal(err)
	}
	until := func(check func() bool) {
		t.Helper()
		for !check() {
			select {
			case <-ctx.Done():
				t.Fatal("isolated PTY timed out")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	until(func() bool { _, err := endpoint.Snapshot(ctx); return err == nil })
	created, err := endpoint.Call(ctx, "workspace.create", map[string]any{"cwd": dir, "label": "input-test", "focus": false})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		RootPane Pane `json:"root_pane"`
	}
	if err := json.Unmarshal(created, &result); err != nil || result.RootPane.ID == "" {
		t.Fatalf("create fixture: %s, %v", created, err)
	}
	program := "import os,tty\ntty.setraw(0)\nopen('ready','w').close()\nwhile True:\n data=os.read(0,4096)\n with open('received','ab') as f: f.write(data)\n os.write(1,data)\n"
	if err := os.WriteFile(filepath.Join(dir, "echo.py"), []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.Call(ctx, "pane.send_text", map[string]any{"pane_id": result.RootPane.ID, "text": "python3 echo.py\r"}); err != nil {
		t.Fatal(err)
	}
	until(func() bool { _, err := os.Stat(filepath.Join(dir, "ready")); return err == nil })
	before, err := endpoint.TerminalGeometry(ctx, result.RootPane.ID)
	if err != nil {
		t.Fatal(err)
	}
	q := NewInputQueue(ctx, endpoint, result.RootPane.ID, func(err error) { t.Error(err) })
	parts := []string{"abc", "中文🙂", "\x1b[A", "\x1b[13;2u", "\x03", "\x1b[200~multi\nline\x1b[201~", "\r"}
	for _, part := range parts {
		if err := q.Enqueue([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	expected := strings.Join(parts, "")
	until(func() bool { data, _ := os.ReadFile(filepath.Join(dir, "received")); return string(data) == expected })
	q.Close()
	after, err := endpoint.TerminalGeometry(ctx, result.RootPane.ID)
	if err != nil || before != after {
		t.Fatalf("input changed PTY geometry: %+v -> %+v (%v)", before, after, err)
	}
	snapshot, err := endpoint.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, pane := range snapshot.Panes {
		if pane.ID == result.RootPane.ID {
			return
		}
	}
	t.Fatal("detaching input removed the remote pane")
}
