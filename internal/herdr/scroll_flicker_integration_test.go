package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

// This is intentionally a native-client fixture: a headless terminal with zero
// cell pixels cannot reproduce the resize/clear/repaint defect. Every process,
// socket and pane belongs to a temporary named session, never a user session.
func TestScrollFlickerWithRealHerdr(t *testing.T) {
	binary := os.Getenv("HERDRX_TEST_HERDR")
	if binary == "" || runtime.GOOS != "linux" {
		t.Skip("requires Linux and HERDRX_TEST_HERDR")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("the isolated native-terminal fixture requires python3")
	}
	for _, split := range []bool{false, true} {
		name := "native-pixels"
		if split {
			name = "native-split-and-pixels"
		}
		t.Run(name, func(t *testing.T) {
			// Keep both Herdr socket paths within Unix's 108-byte path limit.
			dir, err := os.MkdirTemp("", "hxf-")
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
			config := "onboarding = false\n[terminal]\ndefault_shell = \"/bin/sh\"\nshell_mode = \"non_login\"\n[experimental]\nkitty_graphics = true\n"
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			const session = "flicker-integration"
			daemon := exec.CommandContext(ctx, binary, "--session", session, "server")
			if err := daemon.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				stop, done := context.WithTimeout(context.Background(), 2*time.Second)
				defer done()
				_ = exec.CommandContext(stop, binary, "--session", session, "server", "stop").Run()
				_ = daemon.Process.Kill()
				_ = daemon.Wait()
			})
			endpoint, err := NewLocalEndpoint(binary, session)
			if err != nil {
				t.Fatal(err)
			}
			wait := func(condition func() bool) {
				t.Helper()
				for !condition() {
					if ctx.Err() != nil {
						t.Fatal(ctx.Err())
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			wait(func() bool { _, err := endpoint.Snapshot(ctx); return err == nil })
			result, err := endpoint.Call(ctx, "workspace.create", map[string]any{"cwd": dir, "label": "Isolated flicker fixture", "focus": true})
			if err != nil {
				t.Fatal(err)
			}
			var created struct {
				RootPane Pane `json:"root_pane"`
			}
			if err := json.Unmarshal(result, &created); err != nil || created.RootPane.ID == "" {
				t.Fatalf("create isolated pane: %s, %v", result, err)
			}
			pane := created.RootPane.ID
			// Emulate an actual Ghostty host with a PTY, consuming Herdr's TUI
			// output and responding to host-geometry/terminal-identity queries.
			if err := os.WriteFile(filepath.Join(dir, "native.py"), []byte(flickerNativeHost), 0600); err != nil {
				t.Fatal(err)
			}
			native := exec.CommandContext(ctx, "python3", filepath.Join(dir, "native.py"), binary, session)
			if err := native.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = native.Process.Signal(os.Interrupt)
				_ = native.Wait()
			}()
			var rect Rect
			getRect := func() bool {
				snapshot, err := endpoint.Snapshot(ctx)
				if err != nil {
					return false
				}
				for _, layout := range snapshot.Layouts {
					for _, item := range layout.Panes {
						if item.PaneID == pane {
							rect = item.Rect
							return true
						}
					}
				}
				return false
			}
			wait(func() bool { return getRect() && rect.Width > 120 })
			if split {
				if _, err := endpoint.Call(ctx, "pane.split", map[string]any{"pane_id": pane, "direction": "right", "cwd": dir, "focus": false}); err != nil {
					t.Fatal(err)
				}
				wait(func() bool { return getRect() && rect.Width < 100 })
			}
			if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte(flickerApplication), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := endpoint.Call(ctx, "pane.send_text", map[string]any{"pane_id": pane, "text": "python3 " + strconv.Quote(filepath.Join(dir, "app.py")) + "\n"}); err != nil {
				t.Fatal(err)
			}
			type sourceState struct {
				PID, Position, Winches int
				Size                   [4]int
			}
			readState := func() (sourceState, bool) {
				var state sourceState
				data, err := os.ReadFile(filepath.Join(dir, "state.json"))
				if err != nil || json.Unmarshal(data, &state) != nil {
					return state, false
				}
				return state, true
			}
			// Entering the app's alternate screen may itself remove the native
			// scrollbar. Wait for that legitimate layout transition to settle.
			var before sourceState
			stable := time.Now()
			wait(func() bool {
				value, ok := readState()
				if !ok || value.PID == 0 {
					return false
				}
				if value != before {
					before, stable = value, time.Now()
				}
				return time.Since(stable) > 150*time.Millisecond
			})
			if before.Size[2] == 0 || before.Size[3] == 0 {
				t.Fatalf("fixture did not establish native pixel geometry: %+v", before)
			}
			if split && rect.Width == before.Size[1] && rect.Height == before.Size[0] {
				t.Fatal("fixture must expose the difference between outer layout and PTY geometry")
			}
			observer, err := endpoint.OpenTerminal(ctx, TerminalOpen{PaneID: pane, Mode: "observe", Cols: uint16(rect.Width), Rows: uint16(rect.Height)})
			if err != nil {
				t.Fatal(err)
			}
			var frameMu sync.Mutex
			frames, fullFrames := 0, 0
			observed := make(chan struct{})
			go func() {
				defer close(observed)
				scanner := bufio.NewScanner(observer.Stdout())
				scanner.Buffer(make([]byte, 65536), 16<<20)
				for scanner.Scan() {
					var frame struct {
						Type string `json:"type"`
						Full bool   `json:"full"`
					}
					if json.Unmarshal(scanner.Bytes(), &frame) == nil && frame.Type == "terminal.frame" {
						frameMu.Lock()
						frames++
						if frame.Full {
							fullFrames++
						}
						frameMu.Unlock()
					}
				}
			}()
			defer func() { _ = observer.Close(); _ = observer.Wait(); <-observed }()
			wait(func() bool { frameMu.Lock(); defer frameMu.Unlock(); return frames > 0 })
			// A newly opened observer also asks the native shell to recompute its
			// current layout. Measure wheel gestures after this access-only setup,
			// just as a browser has an observer before its first wheel event.
			stable = time.Now()
			wait(func() bool {
				value, ok := readState()
				if !ok {
					return false
				}
				if value != before {
					before, stable = value, time.Now()
				}
				return time.Since(stable) > 150*time.Millisecond
			})
			scroll := NewScrollController(ctx, endpoint, pane)
			defer scroll.Close()
			for gesture := 0; gesture < 3; gesture++ {
				if err := scroll.Send(ctx, -3, 10, 5); err != nil {
					t.Fatal(err)
				}
				wait(func() bool { value, ok := readState(); return ok && value.Position == -gesture-1 })
				wait(func() bool { scroll.mu.Lock(); defer scroll.mu.Unlock(); return scroll.connection == nil })
				// The app records SIGWINCH immediately; allow the native shell's
				// restore render to complete before checking the final dimensions.
				time.Sleep(70 * time.Millisecond)
				after, ok := readState()
				if !ok || after.PID != before.PID || after.Winches != before.Winches || after.Size != before.Size {
					t.Fatalf("gesture %d resized or replaced the native app: %+v -> %+v", gesture, before, after)
				}
			}
			frameMu.Lock()
			t.Logf("3 separate wheel bursts: unchanged PID, zero extra SIGWINCH; PTY %v, API outer rectangle %dx%d; observer %d frames (%d full)", before.Size, rect.Width, rect.Height, frames, fullFrames)
			frameMu.Unlock()
		})
	}
}

const flickerNativeHost = `import os,sys,pty,fcntl,termios,struct,subprocess,select,signal
master,slave=pty.openpty()
fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',50,160,1280,800))
def prepare():
    os.setsid()
    fcntl.ioctl(0,termios.TIOCSCTTY,0)
child=subprocess.Popen([sys.argv[1],'--session',sys.argv[2]],stdin=slave,stdout=slave,stderr=slave,preexec_fn=prepare)
os.close(slave)
def stop(*args): raise KeyboardInterrupt()
signal.signal(signal.SIGTERM,stop)
try:
    while child.poll() is None:
        if not select.select([master],[],[],.2)[0]: continue
        data=os.read(master,65536)
        if not data: break
        for query,response in [(b'\x1b[16t',b'\x1b[6;16;8t'),(b'\x1b[14t',b'\x1b[4;800;1280t'),(b'\x1b[18t',b'\x1b[8;50;160t'),(b'\x1b[6n',b'\x1b[1;1R'),(b'\x1b[c',b'\x1b[?1;2c'),(b'\x1b[>c',b'\x1b[>1;4000;0c')]:
            if query in data: os.write(master,response)
except (KeyboardInterrupt,OSError): pass
finally:
    child.terminate()
    try: child.wait(timeout=2)
    except subprocess.TimeoutExpired: child.kill();child.wait()
    os.close(master)
`

const flickerApplication = `import os,tty,re,json,signal,time,fcntl,termios,struct
tty.setraw(0)
position=0
winches=0
def record():
    size=struct.unpack('HHHH',fcntl.ioctl(0,termios.TIOCGWINSZ,b'\0'*8))
    with open('state.tmp','w') as f: json.dump({'pid':os.getpid(),'position':position,'winches':winches,'size':size},f)
    os.replace('state.tmp','state.json')
def draw():
    os.write(1,('\x1b[H'+'\r\n'.join(('Wheel view %d row %02d '+('X'*60))%(position,y) for y in range(20))).encode())
def winch(signum,frame):
    global winches
    winches+=1
    record()
    os.write(1,b'\x1b[2J\x1b[H')
    time.sleep(.04)
    draw()
signal.signal(signal.SIGWINCH,winch)
os.write(1,b'\x1b[?1049h\x1b[?1000h\x1b[?1006h')
record();draw()
data=b''
while True:
    data+=os.read(0,4096)
    while True:
        match=re.search(rb'\x1b\[<(64|65);\d+;\d+M',data)
        if not match: break
        position+=-1 if match[1]==b'64' else 1
        data=data[match.end():]
        record();draw()
`
