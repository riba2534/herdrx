package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/riba2534/herdrx/internal/terminalwire"
)

// Exercise browser wire -> API -> real Herdr -> SIGWINCH application, including
// simultaneous observation, native mouse scrolling, ownership and detachment.
// Every process and pane belongs to an isolated temporary Herdr session.
func TestResponsiveTerminalWithRealHerdr(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	server := exec.CommandContext(ctx, binary, "--session", "responsive-test", "server")
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command(binary, "--session", "responsive-test", "server", "stop").Run()
		_ = server.Process.Kill()
		_ = server.Wait()
	})
	endpoint, err := herdr.NewLocalEndpoint(binary, "responsive-test")
	if err != nil {
		t.Fatal(err)
	}
	until := func(check func() bool) {
		t.Helper()
		for !check() {
			select {
			case <-ctx.Done():
				t.Fatal("responsive fixture timed out")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	until(func() bool { _, err := endpoint.Snapshot(ctx); return err == nil })
	raw, err := endpoint.Call(ctx, "workspace.create", map[string]any{"cwd": dir, "label": "responsive-test", "focus": false})
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		RootPane herdr.Pane `json:"root_pane"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.RootPane.ID == "" {
		t.Fatalf("create fixture: %s %v", raw, err)
	}
	program := `import os,tty,re,json,signal
import threading
tty.setraw(0)
position=0
received=0
lock=threading.RLock()
def draw(*args):
    with lock:
        size=os.get_terminal_size(0)
        with open("state.tmp","w") as f: json.dump({"pid":os.getpid(),"cols":size.columns,"rows":size.lines,"position":position,"received":received},f)
        os.replace("state.tmp","state.json")
        text="这是需要按手机宽度自动换行的终端内容。"*8
        columns=max(1,(size.columns-1)//2)
        lines=[text[i:i+columns] for i in range(0,len(text),columns)]
        frame="\x1b[2J\x1b[H"+"\r\n".join(lines[:size.lines-1])+"\x1b[%d;%dHEND"%(size.lines,size.columns-2)
        os.write(1,frame.encode())
signal.signal(signal.SIGWINCH,draw)
os.write(1,b"\x1b[?1049h\x1b[?1000h\x1b[?1006h")
draw()
data=b""
while True:
    chunk=os.read(0,4096)
    received+=len(chunk)
    data+=chunk
    while True:
        match=re.search(rb"\x1b\[<(64|65);\d+;\d+M",data)
        if not match: break
        position+=-1 if match[1]==b"64" else 1
        data=data[match.end():]
    draw()
`
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.Call(ctx, "pane.send_text", map[string]any{"pane_id": created.RootPane.ID, "text": "python3 app.py\r"}); err != nil {
		t.Fatal(err)
	}
	type appState struct{ PID, Cols, Rows, Position, Received int }
	state := func() appState {
		var value appState
		data, _ := os.ReadFile(filepath.Join(dir, "state.json"))
		_ = json.Unmarshal(data, &value)
		return value
	}
	until(func() bool { return state().PID > 0 })
	pid := state().PID
	a, db, srv, client, admin := authFixture(t)
	a.hosts.(*hostruntime.Factory).Config.HerdrBinary = binary
	owner := admin["user"].(map[string]any)["id"].(string)
	if err := db.CreateHost(ctx, store.Host{ID: "hst_fixture", OwnerID: owner, Name: "Responsive fixture", Transport: "local", SessionName: "responsive-test", Port: 22}); err != nil {
		t.Fatal(err)
	}
	ws := fixtureSocket(t, ctx, srv, client)
	write := func(typ websocket.MessageType, data []byte) {
		t.Helper()
		if err := ws.Write(ctx, typ, data); err != nil {
			t.Fatal(err)
		}
	}
	read := func(want string) map[string]any {
		t.Helper()
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if typ == websocket.MessageBinary {
				frame, err := terminalwire.Decode(data)
				if err != nil {
					t.Fatal(err)
				}
				write(websocket.MessageBinary, terminalwire.Encode(terminalwire.Frame{Opcode: terminalwire.OpcodeAck, StreamID: frame.StreamID, Seq: frame.Seq}))
				continue
			}
			var msg map[string]any
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatal(err)
			}
			if msg["t"] == "error" {
				t.Fatalf("unexpected API error: %s", data)
			}
			if msg["t"] == want {
				return msg
			}
		}
	}
	open := func(responsive bool, cols, rows int) uint32 {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"t": "terminal.open", "id": "open", "pane_id": created.RootPane.ID, "mode": "observe", "responsive": responsive, "takeover": true, "cols": cols, "rows": rows})
		write(websocket.MessageText, data)
		return uint32(read("terminal.opened")["stream_id"].(float64))
	}
	desktop := open(false, 295, 40)
	before := state()
	// An ordinary desktop resize remains local; its wire message cannot change PTY geometry.
	write(websocket.MessageBinary, terminalwire.Encode(terminalwire.Frame{Opcode: terminalwire.OpcodeResize, StreamID: desktop, Payload: terminalwire.TerminalPayload(100, 30, nil)}))
	write(websocket.MessageText, []byte(`{"t":"ping"}`))
	read("pong")
	if got := state(); got.Cols != before.Cols || got.Rows != before.Rows {
		t.Fatalf("observer resized PTY: %+v -> %+v", before, got)
	}
	mobile := open(true, 56, 30)
	until(func() bool { value := state(); return value.Cols == 56 && value.Rows == 30 })
	write(websocket.MessageBinary, terminalwire.Encode(terminalwire.Frame{Opcode: terminalwire.OpcodeResize, StreamID: mobile, Payload: terminalwire.TerminalPayload(40, 18, nil)}))
	until(func() bool { value := state(); return value.Cols == 40 && value.Rows == 18 })
	write(websocket.MessageText, []byte(fmt.Sprintf(`{"t":"call","id":"wheel","method":"terminal.scroll","params":{"stream_id":%d,"lines":-9,"column":10,"row":5}}`, mobile)))
	read("res")
	until(func() bool { return state().Position == -3 })
	if got := state(); got.Cols != 40 || got.Rows != 18 || got.PID != pid {
		t.Fatalf("wheel changed geometry/process: %+v", got)
	}
	// A second responsive window must never steal the first one's attachment,
	// even if an untrusted browser requests takeover.
	second := open(true, 70, 25)
	closed := read("terminal.closed")
	if uint32(closed["stream_id"].(float64)) != second || !strings.Contains(closed["reason"].(string), "already has an attached client") {
		t.Fatal(closed)
	}
	if got := state(); got.Cols != 40 || got.Rows != 18 {
		t.Fatalf("second view stole dimensions: %+v", got)
	}
	// Returning from history and changing display mode close then reopen the
	// same pane; the previous controller must release its attachment in time.
	for i := 0; i < 4; i++ {
		write(websocket.MessageText, []byte(fmt.Sprintf(`{"t":"terminal.close","stream_id":%d}`, mobile)))
		mobile = open(true, 48+i, 20)
		until(func() bool { value := state(); return value.Cols == 48+i && value.Rows == 20 })
	}
	before = state()
	ws.CloseNow()
	if _, err := endpoint.Call(ctx, "pane.send_text", map[string]any{"pane_id": created.RootPane.ID, "text": "after browser close"}); err != nil {
		t.Fatal(err)
	}
	until(func() bool { return state().Received > before.Received })
	if got := state(); got.PID != pid {
		t.Fatalf("browser close replaced task: %+v", got)
	}
	snapshot, err := endpoint.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, pane := range snapshot.Panes {
		if pane.ID == created.RootPane.ID {
			return
		}
	}
	t.Fatal("browser detachment removed remote pane")
}

// Keep wire-mode and controller reuse coverage in CI without requiring Herdr.
func TestResponsiveTerminalCommands(t *testing.T) {
	calls := fakeHerdrSocket(t)
	a, db, srv, client, admin := authFixture(t)
	dir := t.TempDir()
	t.Setenv("HERDRX_RESPONSIVE_TEST_ARGS", filepath.Join(dir, "args"))
	t.Setenv("HERDRX_RESPONSIVE_TEST_COMMANDS", filepath.Join(dir, "commands"))
	binary := filepath.Join(dir, "terminal")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HERDRX_RESPONSIVE_TEST_ARGS\"\ncat >> \"$HERDRX_RESPONSIVE_TEST_COMMANDS\"\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a.hosts.(*hostruntime.Factory).Config.HerdrBinary = binary
	owner := admin["user"].(map[string]any)["id"].(string)
	if err := db.CreateHost(t.Context(), store.Host{ID: "hst_fixture", OwnerID: owner, Name: "Responsive commands", Transport: "local", Port: 22}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws := fixtureSocket(t, ctx, srv, client)
	write := func(typ websocket.MessageType, data []byte) {
		t.Helper()
		if err := ws.Write(ctx, typ, data); err != nil {
			t.Fatal(err)
		}
	}
	read := func(want string) map[string]any {
		t.Helper()
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var msg map[string]any
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatal(err)
			}
			if msg["t"] == "error" {
				t.Fatalf("unexpected error: %s", data)
			}
			if msg["t"] == want {
				return msg
			}
		}
	}
	open := func(responsive bool) uint32 {
		t.Helper()
		write(websocket.MessageText, []byte(fmt.Sprintf(`{"t":"terminal.open","id":"open","pane_id":"p_fixture","mode":"observe","responsive":%t,"takeover":true,"cols":56,"rows":30}`, responsive)))
		return uint32(read("terminal.opened")["stream_id"].(float64))
	}
	desktop := open(false)
	mobile := open(true)
	for _, id := range []uint32{desktop, mobile} {
		write(websocket.MessageBinary, terminalwire.Encode(terminalwire.Frame{Opcode: terminalwire.OpcodeResize, StreamID: id, Payload: terminalwire.TerminalPayload(40, 18, nil)}))
	}
	write(websocket.MessageText, []byte(fmt.Sprintf(`{"t":"call","id":"wheel","method":"terminal.scroll","params":{"stream_id":%d,"lines":-7,"column":10,"row":5}}`, mobile)))
	read("res")
	var commands, args string
	for {
		raw, _ := os.ReadFile(filepath.Join(dir, "commands"))
		commands = string(raw)
		raw, _ = os.ReadFile(filepath.Join(dir, "args"))
		args = string(raw)
		if strings.Count(commands, "\n") == 4 && strings.Count(args, "\n") == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("incomplete controller capture: %q %q", args, commands)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if strings.Count(args, "session control") != 1 || strings.Count(args, "session observe") != 1 || strings.Contains(args, "--takeover") {
		t.Fatalf("unexpected attachment modes: %s", args)
	}
	var total int
	for index, line := range strings.Split(strings.TrimSpace(commands), "\n") {
		var command struct {
			Type, Direction, Source        string
			Cols, Rows, Lines, Column, Row int
		}
		if err := json.Unmarshal([]byte(line), &command); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			if command.Type != "terminal.resize" || command.Cols != 40 || command.Rows != 18 {
				t.Fatal(command)
			}
			continue
		}
		if command.Type != "terminal.scroll" || command.Direction != "up" || command.Source != "wheel" || command.Column != 10 || command.Row != 5 || command.Lines > 3 {
			t.Fatal(command)
		}
		total += command.Lines
	}
	if total != 7 || calls.Load() != 0 {
		t.Fatalf("scroll total=%d, unexpected Herdr mutations=%d", total, calls.Load())
	}
}
