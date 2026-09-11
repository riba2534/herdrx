package httpapi

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/riba2534/herdrx/internal/terminalwire"
	"github.com/riba2534/herdrx/internal/testpaths"
)

func TestSlowInputDoesNotBlockWebSocketAndFailureStopsPendingInput(t *testing.T) {
	dir := testpaths.ShortTempDir(t)
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "herdr"), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "herdr", "herdr.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	started := make(chan string, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var request struct {
					Method string
					Params struct{ Text string }
				}
				if json.NewDecoder(conn).Decode(&request) != nil {
					return
				}
				if request.Method == "pane.send_text" {
					started <- request.Params.Text
					<-release
					_ = json.NewEncoder(conn).Encode(map[string]any{"error": map[string]string{"code": "unavailable", "message": "input response failed"}})
					return
				}
				_ = json.NewEncoder(conn).Encode(map[string]any{"result": map[string]any{"snapshot": map[string]any{}}})
			}()
		}
	}()
	a, db, srv, client, admin := authFixture(t)
	binary := filepath.Join(dir, "observer")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexec cat\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a.hosts.(*hostruntime.Factory).Config.HerdrBinary = binary
	owner := admin["user"].(map[string]any)["id"].(string)
	if err := db.CreateHost(t.Context(), store.Host{ID: "hst_fixture", OwnerID: owner, Name: "Input fixture", Transport: "local", Port: 22}); err != nil {
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
			_, raw, err := ws.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var message map[string]any
			if err := json.Unmarshal(raw, &message); err != nil {
				t.Fatal(err)
			}
			if message["t"] == want {
				return message
			}
		}
	}
	write(websocket.MessageText, []byte(`{"t":"terminal.open","id":"input","pane_id":"p_fixture","mode":"observe","cols":80,"rows":24}`))
	streamID := uint32(read("terminal.opened")["stream_id"].(float64))
	input := func(data string) {
		write(websocket.MessageBinary, terminalwire.Encode(terminalwire.Frame{Opcode: terminalwire.OpcodeInput, StreamID: streamID, Payload: []byte(data)}))
	}
	input("first")
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	input("queued 中文")
	write(websocket.MessageText, []byte(`{"t":"ping"}`))
	read("pong") // The remote RPC is still blocked here.
	releaseOnce.Do(func() { close(release) })
	closed := read("terminal.closed")
	if !strings.Contains(closed["reason"].(string), "输入未能确认送达") {
		t.Fatal(closed)
	}
	input("must stay stopped")
	write(websocket.MessageText, []byte(`{"t":"ping"}`))
	read("pong")
	select {
	case data := <-started:
		t.Fatalf("input sent after failure: %q", data)
	default:
	}
}
