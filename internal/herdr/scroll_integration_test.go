package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

// Opt in with HERDRX_TEST_HERDR=/path/to/herdr. All processes, sockets, panes
// and configuration belong to t.TempDir; never attach to an existing session.
func TestScrollWithRealHerdr(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	server := exec.CommandContext(ctx, binary, "--session", "wheel-test", "server")
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command(binary, "--session", "wheel-test", "server", "stop").Run()
		_ = server.Process.Kill()
		_ = server.Wait()
	})
	endpoint, err := NewLocalEndpoint(binary, "wheel-test")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := endpoint.Snapshot(ctx); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(20 * time.Millisecond)
	}
	result, err := endpoint.Call(ctx, "workspace.create", map[string]any{"cwd": dir, "label": "wheel-test", "focus": false})
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		RootPane Pane `json:"root_pane"`
	}
	if err := json.Unmarshal(result, &created); err != nil || created.RootPane.ID == "" {
		t.Fatalf("create isolated pane: %s %v", result, err)
	}
	t.Cleanup(func() {
		closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_, _ = endpoint.Call(closeCtx, "pane.close", map[string]any{"pane_id": created.RootPane.ID})
	})
	// A headless fixture starts at the default 120x40 PTY size until a native
	// view claims its layout. Establish that view before launching the app, as
	// on an already-open Herdr workspace, then measure across wheel gestures.
	snapshot, err := endpoint.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, layout := range snapshot.Layouts {
		for _, pane := range layout.Panes {
			if pane.PaneID != created.RootPane.ID {
				continue
			}
			view, err := endpoint.OpenTerminal(ctx, TerminalOpen{PaneID: pane.PaneID, Mode: "control", Cols: uint16(pane.Rect.Width), Rows: uint16(pane.Rect.Height)})
			if err != nil {
				t.Fatal(err)
			}
			if !bufio.NewScanner(view.Stdout()).Scan() {
				t.Fatal("fixture native view did not open")
			}
			_ = view.Close()
			_ = view.Wait()
		}
	}
	program := `import os,tty,re,json,signal,time
tty.setraw(0)
position=0
def record():
    size=os.get_terminal_size(0)
    with open("state.tmp","w") as f: json.dump({"pid":os.getpid(),"position":position,"cols":size.columns,"rows":size.lines,"received":time.time_ns()},f)
    os.replace("state.tmp","state.json")
    os.write(1,("\x1b[HWheel position: %d\x1b[K"%position).encode())
os.write(1,b"\x1b[?1049h\x1b[?1000h\x1b[?1006h")
record()
data=b""
while True:
    data+=os.read(0,4096)
    while True:
        match=re.search(rb"\x1b\[<(64|65);\d+;\d+M",data)
        if not match: break
        position+=-1 if match[1]==b"64" else 1
        data=data[match.end():]
        record()
`
	if err := os.WriteFile(filepath.Join(dir, "wheel.py"), []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.Call(ctx, "pane.send_text", map[string]any{"pane_id": created.RootPane.ID, "text": "python3 " + strconv.Quote(filepath.Join(dir, "wheel.py")) + "\n"}); err != nil {
		t.Fatal(err)
	}
	type state struct {
		PID, Position, Cols, Rows int
		Received                  int64
	}
	readState := func() (state, bool) {
		var value state
		data, err := os.ReadFile(filepath.Join(dir, "state.json"))
		if err != nil {
			return value, false
		}
		return value, json.Unmarshal(data, &value) == nil
	}
	waitPosition := func(position int) state {
		for {
			value, ok := readState()
			if ok && value.Position == position {
				return value
			}
			if ctx.Err() != nil {
				t.Fatalf("expected position %d, got %+v", position, value)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	before := waitPosition(0)
	scroll := NewScrollController(ctx, endpoint, created.RootPane.ID)
	defer scroll.Close()
	if err := scroll.Send(ctx, -9, 10, 5); err != nil {
		t.Fatal(err)
	}
	after := waitPosition(-3)
	if err := scroll.Send(ctx, 6, 10, 5); err != nil {
		t.Fatal(err)
	}
	final := waitPosition(-1)
	if before.PID != after.PID || before.PID != final.PID || before.Cols != final.Cols || before.Rows != final.Rows {
		t.Fatalf("scroll changed task or dimensions: %+v -> %+v -> %+v", before, after, final)
	}
	// Idle expiry must give native clients access again without requiring the
	// Web observer to disconnect. Reading under mu also waits for release EOF.
	for {
		scroll.mu.Lock()
		idle := scroll.connection == nil
		scroll.mu.Unlock()
		if idle {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("scroll controller did not expire")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if os.Getenv("HERDRX_SCROLL_BENCH") != "" {
		for _, mode := range []string{"per-gesture", "continuous"} {
			send := func(lines int) error { return sendScrollPerGesture(ctx, endpoint, created.RootPane.ID, lines, 10, 5) }
			controller := NewScrollController(ctx, endpoint, created.RootPane.ID)
			if mode == "continuous" {
				send = func(lines int) error { return controller.Send(ctx, lines, 10, 5) }
			}
			var sent, received []float64
			for i := 0; i < 30; i++ {
				start := time.Now()
				if err := send(3); err != nil {
					t.Fatal(err)
				}
				sent = append(sent, float64(time.Since(start).Microseconds())/1000)
				value := waitPosition(i)
				received = append(received, float64(value.Received-start.UnixNano())/1e6)
			}
			slices.Sort(sent)
			slices.Sort(received)
			t.Logf("%s, 30 gestures: Send return median %.2f ms / p95 %.2f ms; app input median %.2f ms / p95 %.2f ms", mode, sent[15], sent[28], received[15], received[28])
			if err := send(-90); err != nil {
				t.Fatal(err)
			}
			waitPosition(-1)
			controller.Close()
		}
	}
	owner, err := endpoint.OpenTerminal(ctx, TerminalOpen{PaneID: created.RootPane.ID, Mode: "control", Cols: uint16(final.Cols), Rows: uint16(final.Rows)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(); _ = owner.Wait() }()
	if !bufio.NewScanner(owner.Stdout()).Scan() {
		t.Fatal("fixture controller did not open")
	}
	if err := scroll.Send(ctx, -3, 10, 5); err == nil {
		t.Fatal("scroll took over an existing controller")
	}
	// The original owner can still send input, proving rejection did not detach it.
	if err := json.NewEncoder(owner.Stdin()).Encode(map[string]any{"type": "terminal.scroll", "direction": "down", "lines": 3, "source": "wheel"}); err != nil {
		t.Fatal(err)
	}
	retained := waitPosition(0)
	if retained.PID != before.PID {
		t.Fatal("controller rejection affected the task")
	}
	t.Logf("native SGR wheel up/down delivered; task PID and %dx%d grid survived controller releases", final.Cols, final.Rows)
}
