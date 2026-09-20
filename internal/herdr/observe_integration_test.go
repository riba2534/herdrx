package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/testprocess"
)

// A viewer canvas is independent of the actual PTY. Verify the private observer
// wire against every pinned release, including v0.8 protocol 20. No user session,
// executable or task is used; all children belong to this temporary fixture.
func TestNativeObserveViewportWithRealHerdr(t *testing.T) {
	binary := os.Getenv("HERDRX_TEST_HERDR")
	if binary == "" {
		t.Skip("HERDRX_TEST_HERDR is not set")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Fatal("the isolated observer fixture requires python3")
	}
	for _, scenario := range []string{"no-controller", "external-control", "native-tui"} {
		t.Run(scenario, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "hxo-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			t.Setenv("XDG_CONFIG_HOME", dir)
			t.Setenv("HERDR_CONFIG_PATH", filepath.Join(dir, "config.toml"))
			t.Setenv("TERM", "xterm-ghostty")
			t.Setenv("TERM_PROGRAM", "ghostty")
			for _, key := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY", "TMUX", "STY", "HERDR_WORKSPACE_ID", "HERDR_TAB_ID", "HERDR_PANE_ID"} {
				t.Setenv(key, "")
				_ = os.Unsetenv(key)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("onboarding = false\n[terminal]\ndefault_shell = \"/bin/sh\"\nshell_mode = \"non_login\"\n[experimental]\nkitty_graphics = true\n"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			const session = "observer-integration"
			logPath := filepath.Join(dir, "daemon.log")
			logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer logFile.Close()
			daemon := exec.CommandContext(ctx, binary, "--session", session, "server")
			daemon.Stdout, daemon.Stderr = logFile, logFile
			if err := daemon.Start(); err != nil {
				t.Fatal(err)
			}
			defer testprocess.StopHerdr(t, binary, session, daemon)
			endpoint, err := NewLocalEndpoint(binary, session)
			if err != nil {
				t.Fatal(err)
			}
			failFixture := func(stage string) {
				t.Helper()
				state, _ := os.ReadFile(filepath.Join(dir, "state.json"))
				appError, _ := os.ReadFile(filepath.Join(dir, "app-error.log"))
				logs, _ := os.ReadFile(logPath)
				if len(logs) > 8192 {
					logs = logs[len(logs)-8192:]
				}
				t.Fatalf("%s: %v; last app state/PID=%s; app stderr=%s; daemon stdout/stderr=%s", stage, ctx.Err(), state, appError, logs)
			}
			wait := func(stage string, check func() bool) {
				t.Helper()
				for !check() {
					select {
					case <-ctx.Done():
						failFixture(stage)
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			var snapshot Snapshot
			wait("daemon ready", func() bool { snapshot, err = endpoint.Snapshot(ctx); return err == nil })
			result, err := endpoint.Call(ctx, "workspace.create", map[string]any{"cwd": dir, "focus": true, "label": "Isolated observer fixture"})
			if err != nil {
				t.Fatal(err)
			}
			var created struct {
				RootPane Pane `json:"root_pane"`
			}
			if err := json.Unmarshal(result, &created); err != nil || created.RootPane.ID == "" {
				t.Fatalf("isolated pane: %s %v", result, err)
			}
			pane := created.RootPane.ID
			if scenario == "native-tui" {
				if err := os.WriteFile(filepath.Join(dir, "native.py"), []byte(flickerNativeHost), 0600); err != nil {
					t.Fatal(err)
				}
				native := exec.CommandContext(ctx, "python3", filepath.Join(dir, "native.py"), binary, session)
				if err := native.Start(); err != nil {
					t.Fatal(err)
				}
				defer testprocess.Stop(t, native)
				wait("native TUI layout", func() bool {
					s, err := endpoint.Snapshot(ctx)
					if err != nil {
						return false
					}
					for _, layout := range s.Layouts {
						for _, p := range layout.Panes {
							if p.PaneID == pane && p.Rect.Width > 120 {
								return true
							}
						}
					}
					return false
				})
			}
			if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte(observerApplication), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := endpoint.Call(ctx, "pane.send_text", map[string]any{"pane_id": pane, "text": "python3 " + strconv.Quote(filepath.Join(dir, "app.py")) + "\n"}); err != nil {
				t.Fatal(err)
			}
			type sourceState struct {
				PID, Winches int
				Size         [4]uint16
			}
			readState := func() (sourceState, bool) {
				var s sourceState
				data, err := os.ReadFile(filepath.Join(dir, "state.json"))
				ok := err == nil && json.Unmarshal(data, &s) == nil && s.PID > 0
				return s, ok
			}
			wait("application ready", func() bool { _, ok := readState(); return ok })
			if scenario == "external-control" {
				controller, err := endpoint.OpenTerminal(ctx, TerminalOpen{PaneID: pane, Mode: "control", Cols: 90, Rows: 30})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = controller.Close(); _ = controller.Wait() }()
				ready := make(chan error, 1)
				go func() {
					scanner := bufio.NewScanner(controller.Stdout())
					scanner.Buffer(make([]byte, 65536), 16<<20)
					confirmed := false
					for scanner.Scan() {
						var frame struct {
							Type          string
							Full          bool
							Width, Height uint16
						}
						if !confirmed && json.Unmarshal(scanner.Bytes(), &frame) == nil && frame.Type == "terminal.frame" && frame.Full && frame.Width == 90 && frame.Height == 30 {
							confirmed = true
							ready <- nil
						}
					}
					if !confirmed {
						ready <- fmt.Errorf("controller ended before its first full frame: %v", scanner.Err())
					}
				}()
				select {
				case err := <-ready:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					failFixture("controller first frame")
				}
				// Finish the attach's zero-pixel resize before testing a second,
				// pixel-bearing resize. This also proves the app consumed SIGWINCH.
				wait("controller initial PTY", func() bool { s, ok := readState(); return ok && s.Size == [4]uint16{30, 90, 0, 0} })
				command := map[string]any{"type": "terminal.resize", "cols": 90, "rows": 30, "cell_width_px": 9, "cell_height_px": 18}
				if err := json.NewEncoder(controller.Stdin()).Encode(command); err != nil {
					t.Fatal(err)
				}
				wait("controller pixel PTY", func() bool { s, ok := readState(); return ok && s.Size == [4]uint16{30, 90, 810, 540} })
			}
			var baseline sourceState
			stable := time.Now()
			wait("stable application state", func() bool {
				s, ok := readState()
				if !ok {
					return false
				}
				if s != baseline {
					baseline, stable = s, time.Now()
				}
				return time.Since(stable) > 250*time.Millisecond
			})
			if scenario != "no-controller" && (baseline.Size[2] == 0 || baseline.Size[3] == 0) {
				t.Fatalf("pixel geometry missing: %+v", baseline)
			}
			observer, err := OpenViewportObserver(ctx, endpoint, snapshot.Protocol, pane, TerminalGeometry{Cols: 70, Rows: 23})
			if err != nil {
				t.Fatal(err)
			}
			closed := false
			defer func() {
				if !closed {
					_ = observer.Close()
					_ = observer.Wait()
				}
			}()
			viewport, ok := observer.(ViewportObserver)
			if !ok {
				t.Fatal("observer does not expose viewport resizing")
			}
			type outputFrame struct {
				Type          string
				Width, Height uint16
				Full          bool
				Seq           uint64
			}
			frames := make(chan outputFrame, 32)
			go func() {
				defer close(frames)
				scanner := bufio.NewScanner(observer.Stdout())
				scanner.Buffer(make([]byte, 65536), 16<<20)
				for scanner.Scan() {
					var f outputFrame
					if json.Unmarshal(scanner.Bytes(), &f) == nil && f.Type == "terminal.frame" {
						select {
						case frames <- f:
						case <-ctx.Done():
							return
						}
					}
				}
			}()
			var previous uint64
			wantFrame := func(cols, rows uint16) {
				t.Helper()
				for {
					select {
					case f, ok := <-frames:
						if !ok {
							t.Fatal("observer ended before viewport frame")
						}
						if f.Width == cols && f.Height == rows {
							if !f.Full || f.Seq <= previous {
								t.Fatalf("resize must yield a fresh full baseline: %+v (prior %d)", f, previous)
							}
							previous = f.Seq
							return
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
			}
			assertUnchanged := func() {
				t.Helper()
				time.Sleep(100 * time.Millisecond)
				if got, ok := readState(); !ok || got != baseline {
					t.Fatalf("viewer mutated task PID, SIGWINCH or PTY rows/cols/pixels: %+v -> %+v", baseline, got)
				}
			}
			wantFrame(70, 23)
			assertUnchanged()
			for _, size := range [][2]uint16{{50, 17}, {130, 45}, {90, 30}} {
				if err := viewport.ResizeViewport(ctx, size[0], size[1]); err != nil {
					t.Fatal(err)
				}
				wantFrame(size[0], size[1])
				assertUnchanged()
			}
			if _, err := observer.Stdin().Write([]byte("{\"type\":\"terminal.input\",\"text\":\"forbidden\"}\n")); err == nil {
				t.Fatal("read-only observer accepted task input")
			}
			_ = observer.Close()
			_ = observer.Wait()
			closed = true
			assertUnchanged()
			t.Logf("protocol %d: 4 full viewer canvases; unchanged PID=%d SIGWINCH=%d PTY=%v, including observer close", snapshot.Protocol, baseline.PID, baseline.Winches, baseline.Size)
		})
	}
}

const observerApplication = `import os,tty,json,signal,fcntl,termios,struct,select,traceback
tty.setraw(0)
winches=0
dirty=True
def record():
    size=struct.unpack('HHHH',fcntl.ioctl(0,termios.TIOCGWINSZ,bytes(8)))
    with open('state.tmp','w') as f: json.dump(dict(pid=os.getpid(),winches=winches,size=size),f)
    os.replace('state.tmp','state.json')
def resize(*args):
    global winches,dirty
    winches+=1
    dirty=True
signal.signal(signal.SIGWINCH,resize)
# Signal callbacks never write files: consecutive SIGWINCH deliveries may nest
# during Python I/O. One main loop serializes state.tmp/rename and PTY output.
try:
    while True:
        if dirty:
            dirty=False
            record()
            os.write(1,b'\x1b[HObserver fixture')
        if not select.select([0],[],[],.02)[0]: continue
        data=os.read(0,4096)
        if not data: break
        os.write(1,data)
except BaseException:
    with open('app-error.log','w') as output: traceback.print_exc(file=output)
    raise
`
