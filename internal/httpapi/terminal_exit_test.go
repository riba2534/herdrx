package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/store"
)

func TestTerminalProcessExitReachesBrowser(t *testing.T) {
	for _, tc := range []struct{ name, script, reason string }{
		{"clean-eof", "exit 0\n", "终端观察进程已退出"},
		{"command-failure", "printf 'herdr unavailable\\n' >&2\nexit 127\n", "127"},
		{"protocol-close", "printf '%s\\n' '{\"type\":\"terminal.closed\",\"reason\":\"pane unavailable\"}'\n", "pane unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := fakeHerdrSocket(t)
			a, db, srv, client, admin := authFixture(t)
			binary := filepath.Join(t.TempDir(), "observer")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+tc.script), 0700); err != nil {
				t.Fatal(err)
			}
			a.hosts.(*hostruntime.Factory).Config.HerdrBinary = binary
			owner := admin["user"].(map[string]any)["id"].(string)
			if err := db.CreateHost(context.Background(), store.Host{ID: "hst_fixture", OwnerID: owner, Name: "Terminal exit fixture", Transport: "local", Port: 22}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ws := fixtureSocket(t, ctx, srv, client)
			if err := ws.Write(ctx, websocket.MessageText, []byte(`{"t":"terminal.open","id":"observe","pane_id":"p_fixture","mode":"observe","cols":80,"rows":24}`)); err != nil {
				t.Fatal(err)
			}
			var opened float64
			for {
				_, raw, err := ws.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var message map[string]any
				if err := json.Unmarshal(raw, &message); err != nil {
					t.Fatal(err)
				}
				switch message["t"] {
				case "terminal.opened":
					opened = message["stream_id"].(float64)
				case "terminal.closed":
					if opened == 0 || message["stream_id"] != opened || !strings.Contains(message["reason"].(string), tc.reason) {
						t.Fatalf("unexpected closure: %s", raw)
					}
					if err := ws.Write(ctx, websocket.MessageText, []byte(`{"t":"ping"}`)); err != nil {
						t.Fatalf("terminal exit closed the workbench: %v", err)
					}
					for {
						_, reply, err := ws.Read(ctx)
						if err != nil {
							t.Fatalf("terminal exit stopped the workbench: %v", err)
						}
						var response struct {
							Type string `json:"t"`
						}
						if err := json.Unmarshal(reply, &response); err != nil {
							t.Fatal(err)
						}
						if response.Type == "terminal.closed" {
							t.Fatal("duplicate terminal closure")
						}
						if response.Type == "pong" {
							break
						}
					}
					if calls.Load() != 0 {
						t.Fatal("terminal exit called a Herdr mutation")
					}
					return
				case "error":
					t.Fatalf("unexpected open error: %s", raw)
				}
			}
		})
	}
}
