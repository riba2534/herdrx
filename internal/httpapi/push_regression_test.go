package httpapi

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/store"
)

// Exercise the actual poller and notification transport, including work already
// in flight when the administrator disables the account. No external provider
// or user-owned Herdr process is involved.
func TestDisableStopsPushCollectionAndQueuedDelivery(t *testing.T) {
	for _, stage := range []string{"snapshot", "notification"} {
		t.Run(stage, func(t *testing.T) {
			a, db, srv, admin, info := authFixture(t)
			ctx := context.Background()
			// This isolated socket fixture uses local Herdr, which requires admin.
			user := store.User{ID: "push-member", Email: "push@example.test", DisplayName: "Push administrator", Role: "admin", PasswordHash: "fixture-only"}
			if err := db.CreateUser(ctx, user); err != nil {
				t.Fatal(err)
			}
			host := store.Host{ID: "push-host", OwnerID: user.ID, Name: "Push fixture", Transport: "local", Port: 22}
			if err := db.CreateHost(ctx, host); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", dir)
			if err := os.MkdirAll(filepath.Join(dir, "herdr"), 0700); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", filepath.Join(dir, "herdr", "herdr.sock"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { listener.Close() })
			started := make(chan struct{}, 4)
			var snapshots, deliveries atomic.Int32
			go func() {
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					go func() {
						defer conn.Close()
						var request map[string]any
						if json.NewDecoder(conn).Decode(&request) != nil {
							return
						}
						snapshots.Add(1)
						if stage == "snapshot" {
							started <- struct{}{}
							_, _ = io.Copy(io.Discard, conn)
							return
						}
						_, _ = io.WriteString(conn, `{"id":"herdrx","result":{"type":"ok","snapshot":{"agents":[{"pane_id":"w1:p1","name":"fixture","agent_status":"done"}]}}}`+"\n")
					}()
				}
			}()
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				deliveries.Add(1)
				started <- struct{}{}
				<-r.Context().Done()
			}))
			t.Cleanup(provider.Close)
			setFixturePushClient(t, a, provider)
			key, err := ecdh.P256().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			auth := make([]byte, 16)
			if _, err := rand.Read(auth); err != nil {
				t.Fatal(err)
			}
			subscriptions := make([]store.PushSubscription, 2)
			for i := range subscriptions {
				subscriptions[i] = store.PushSubscription{ID: "push-" + string(rune('a'+i)), UserID: user.ID, Endpoint: "https://push.example.test/" + string(rune('a'+i)), P256DH: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(auth)}
				if err := db.UpsertPushSubscription(ctx, subscriptions[i]); err != nil {
					t.Fatal(err)
				}
			}
			pollCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				a.pollPushUser(pollCtx, user.ID, subscriptions, map[string]string{user.ID + ":" + host.ID + ":w1:p1": "working"})
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("poller did not reach the in-flight stage")
			}
			csrf := info["csrf_token"].(string)
			requestJSON(t, admin, "PATCH", srv.URL+"/api/admin/users/"+user.ID, csrf, map[string]bool{"disabled": true}, 200)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("disable did not cancel the in-flight poller")
			}
			wantDeliveries := int32(0)
			if stage == "notification" {
				wantDeliveries = 1 // Already dispatched; the second must remain unsent.
			}
			if deliveries.Load() != wantDeliveries || snapshots.Load() != 1 {
				t.Fatalf("unexpected calls: snapshots=%d deliveries=%d", snapshots.Load(), deliveries.Load())
			}
			// A stale subscription list cannot regain access while disabled.
			a.pollPushUser(ctx, user.ID, subscriptions, map[string]string{})
			requestJSON(t, admin, "PATCH", srv.URL+"/api/admin/users/"+user.ID, csrf, map[string]bool{"disabled": false}, 200)
			a.pollPushHosts(ctx, map[string]string{})
			if snapshots.Load() != 1 || deliveries.Load() != wantDeliveries {
				t.Fatal("disable or re-enable revived background collection/delivery")
			}
			remaining, err := db.PushSubscriptions(ctx)
			if err != nil || len(remaining) != 0 {
				t.Fatalf("old subscriptions survived re-enable: %d, %v", len(remaining), err)
			}
		})
	}
}
