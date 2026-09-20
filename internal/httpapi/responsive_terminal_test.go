package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	ws := fixtureSocketHello(t, ctx, srv, client, `{"t":"hello","protocol":1,"browser_instance_id":"fixture-browser-0001","capabilities":{"terminal_control":1,"terminal_resize_v2":1}}`)
	write := func(typ websocket.MessageType, data []byte) {
		t.Helper()
		if err := ws.Write(ctx, typ, data); err != nil {
			t.Fatal(err)
		}
	}
	receivedFrames := make(map[uint32]bool)
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
				receivedFrames[frame.StreamID] = true
				write(websocket.MessageBinary, terminalwire.Encode(terminalwire.Frame{Opcode: terminalwire.OpcodeAck, StreamID: frame.StreamID, Seq: frame.Seq}))
				continue
			}
			if typ == websocket.MessageBinary {
				continue
			}
			var msg map[string]any
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatal(err)
			}
			if msg["t"] == "error" && want != "error" {
				t.Fatalf("unexpected API error: %s", data)
			}
			if msg["t"] == want {
				return msg
			}
		}
	}
	epochs := make(map[uint32]string)
	generations := make(map[uint32]string)
	sendJSON := func(m map[string]any) { t.Helper(); data, _ := json.Marshal(m); write(websocket.MessageText, data) }
	acquire := func(id uint32, cols, rows int, transfer bool, want string) map[string]any {
		t.Helper()
		sendJSON(map[string]any{"t": "terminal.control.acquire", "id": "acquire", "stream_id": id, "stream_epoch": epochs[id], "cols": cols, "rows": rows, "transfer": transfer})
		reply := read(want)
		if gen, ok := reply["control_generation"].(string); ok {
			generations[id] = gen
		}
		return reply
	}
	open := func(responsive bool, cols, rows int) uint32 {
		t.Helper()
		sendJSON(map[string]any{"t": "terminal.open", "id": "open", "pane_id": created.RootPane.ID, "mode": "observe", "cols": cols, "rows": rows})
		reply := read("terminal.opened")
		id := uint32(reply["stream_id"].(float64))
		epochs[id] = reply["stream_epoch"].(string)
		if responsive {
			acquire(id, cols, rows, false, "terminal.control.acquired")
		}
		return id
	}
	resize := func(id uint32, seq, cols, rows int) {
		sendJSON(map[string]any{"t": "terminal.resize_v2", "stream_id": id, "stream_epoch": epochs[id], "control_generation": generations[id], "resize_seq": seq, "cols": cols, "rows": rows})
	}

	before := state()
	desktop := open(false, 295, 40)
	write(websocket.MessageText, []byte(fmt.Sprintf(`{"t":"terminal.open","id":"legacy","pane_id":%q,"mode":"control","responsive":true,"takeover":true,"cols":142,"rows":81}`, created.RootPane.ID)))
	legacy := uint32(read("terminal.opened")["stream_id"].(float64))
	until(func() bool {
		write(websocket.MessageText, []byte(`{"t":"ping"}`))
		read("pong")
		return receivedFrames[desktop] && receivedFrames[legacy]
	})
	if got := state(); got != before {
		t.Fatalf("opening default or legacy page changed the task: %+v -> %+v", before, got)
	}
	write(websocket.MessageBinary, terminalwire.Encode(terminalwire.Frame{Opcode: terminalwire.OpcodeResize, StreamID: legacy, Payload: terminalwire.TerminalPayload(142, 81, nil)}))
	// An ordinary desktop resize remains local; its wire message cannot change PTY geometry.
	write(websocket.MessageBinary, terminalwire.Encode(terminalwire.Frame{Opcode: terminalwire.OpcodeResize, StreamID: desktop, Payload: terminalwire.TerminalPayload(100, 30, nil)}))
	write(websocket.MessageText, []byte(`{"t":"ping"}`))
	read("pong")
	if got := state(); got.Cols != before.Cols || got.Rows != before.Rows {
		t.Fatalf("observer resized PTY: %+v -> %+v", before, got)
	}
	mobile := open(true, 56, 30)
	until(func() bool { value := state(); return value.Cols == 56 && value.Rows == 30 })
	resize(mobile, 1, 40, 18)
	until(func() bool { value := state(); return value.Cols == 40 && value.Rows == 18 })
	write(websocket.MessageText, []byte(fmt.Sprintf(`{"t":"call","id":"wheel","method":"terminal.scroll","params":{"stream_id":%d,"lines":-9,"column":10,"row":5}}`, mobile)))
	read("res")
	until(func() bool { return state().Position == -3 })
	if got := state(); got.Cols != 40 || got.Rows != 18 || got.PID != pid {
		t.Fatalf("wheel changed geometry/process: %+v", got)
	}
	// A second observation cannot acquire without explicit site-local transfer.
	second := open(false, 70, 25)
	conflict := acquire(second, 70, 25, false, "error")
	if conflict["code"] != "control_conflict" {
		t.Fatal(conflict)
	}
	if got := state(); got.Cols != 40 || got.Rows != 18 {
		t.Fatalf("second view stole dimensions: %+v", got)
	}
	// A wheel from another observer must use the existing controller.
	sendJSON(map[string]any{"t": "call", "id": "wheel-other", "method": "terminal.scroll", "params": map[string]any{"stream_id": second, "lines": 3, "column": 10, "row": 5}})
	read("res")
	until(func() bool { return state().Position == -2 })
	acquire(second, 70, 25, true, "terminal.control.acquired")
	until(func() bool { return state().Cols == 70 && state().Rows == 25 })
	resize(mobile, 2, 22, 10)
	if stale := read("error"); stale["code"] != "control_expired" {
		t.Fatal(stale)
	}
	resize(second, 2, 64, 24)
	until(func() bool { return state().Cols == 64 && state().Rows == 24 })
	resize(second, 1, 22, 10) // out-of-order requests cannot restore an older target.
	sendJSON(map[string]any{"t": "ping"})
	read("pong")
	if state().Cols != 64 {
		t.Fatal("stale resize changed grid")
	}
	sendJSON(map[string]any{"t": "terminal.control.release", "id": "release", "stream_id": second, "stream_epoch": epochs[second], "control_generation": generations[second]})
	read("terminal.control.released")

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

// Exercise the Web contract independently of the native viewport wire tests.
func TestResponsiveTerminalCommands(t *testing.T) {
	_, endpoint, srv, client, ctx := geometryFixture(t)
	desktop := newGeometryBrowser(t, ctx, srv, client)
	mobile := newGeometryBrowser(t, ctx, srv, client)
	mobile.acquire(false)
	for _, b := range []*geometryBrowser{desktop, mobile} {
		if err := b.ws.Write(ctx, websocket.MessageBinary, terminalwire.Encode(terminalwire.Frame{Opcode: terminalwire.OpcodeResize, StreamID: b.stream, Payload: terminalwire.TerminalPayload(22, 10, nil)})); err != nil {
			t.Fatal(err)
		}
	}
	mobile.command("terminal.resize_v2", map[string]any{"resize_seq": 1, "cols": 40, "rows": 18})
	mobile.read("terminal.resize.status")
	desktop.send(map[string]any{"t": "call", "id": "wheel", "method": "terminal.scroll", "params": map[string]any{"stream_id": desktop.stream, "lines": -7, "column": 10, "row": 5}})
	desktop.read("res")
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	if len(endpoint.commands) != 4 {
		t.Fatal(endpoint.commands)
	}
	total := 0
	for i, m := range endpoint.commands {
		if i == 0 {
			if m["type"] != "terminal.resize" || m["cols"] != float64(40) || m["rows"] != float64(18) {
				t.Fatal(m)
			}
			continue
		}
		if m["type"] != "terminal.scroll" || m["direction"] != "up" || m["source"] != "wheel" || m["column"] != float64(10) || m["row"] != float64(5) || m["lines"].(float64) > 3 {
			t.Fatal(m)
		}
		total += int(m["lines"].(float64))
	}
	if total != 7 || len(endpoint.processes) != 3 {
		t.Fatalf("scroll=%d processes=%d", total, len(endpoint.processes))
	}
}
