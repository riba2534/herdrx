package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// eventServer accepts one events.subscribe request on a short Unix socket path
// and lets the test script Herdr's replies.
func eventServer(t *testing.T, serve func(request map[string]any, conn net.Conn)) *LocalEndpoint {
	t.Helper()
	dir, err := os.MkdirTemp("", "hx-ev-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "herdr.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var request map[string]any
		_ = json.Unmarshal(line, &request)
		serve(request, conn)
	}()
	return &LocalEndpoint{SocketPath: path}
}

func TestSubscribeEventsStreamsUntilHerdrCloses(t *testing.T) {
	var got map[string]any
	endpoint := eventServer(t, func(request map[string]any, conn net.Conn) {
		got = request
		_, _ = conn.Write([]byte(`{"id":"herdrx:events","result":{"type":"subscription_started"}}` + "\n"))
		_, _ = conn.Write([]byte(`{"event":"pane_created","data":{"pane_id":"w1:p2"}}` + "\n"))
		_, _ = conn.Write([]byte("not json\n"))
		_, _ = conn.Write([]byte(`{"event":"pane_agent_status_changed","data":{"pane_id":"w1:p2","agent_status":"blocked"}}` + "\n"))
	})
	var events []string
	err := endpoint.SubscribeEvents(t.Context(), []EventSubscription{{"type": "pane.created"}, {"type": "pane.agent_status_changed", "pane_id": "w1:p2"}}, func(event Event) {
		events = append(events, event.Event)
	})
	if err == nil {
		t.Fatal("a closed stream must report why it ended")
	}
	want := []string{EventSubscriptionStarted, "pane_created", "pane_agent_status_changed"}
	if len(events) != len(want) {
		t.Fatalf("events = %v", events)
	}
	for index := range want {
		if events[index] != want[index] {
			t.Fatalf("events = %v", events)
		}
	}
	if got["method"] != "events.subscribe" {
		t.Fatalf("request %v", got)
	}
	subscriptions := got["params"].(map[string]any)["subscriptions"].([]any)
	if len(subscriptions) != 2 || subscriptions[1].(map[string]any)["pane_id"] != "w1:p2" {
		t.Fatalf("subscriptions %v", subscriptions)
	}
}

func TestSubscribeEventsReportsHerdrErrors(t *testing.T) {
	endpoint := eventServer(t, func(_ map[string]any, conn net.Conn) {
		_, _ = conn.Write([]byte(`{"id":"herdrx:events","error":{"code":"invalid_request","message":"unknown variant"}}` + "\n"))
	})
	started := false
	err := endpoint.SubscribeEvents(t.Context(), nil, func(Event) { started = true })
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Code != "invalid_request" || started {
		t.Fatalf("err = %v, started = %v", err, started)
	}

	lost := eventServer(t, func(_ map[string]any, conn net.Conn) {
		_, _ = conn.Write([]byte(`{"id":"herdrx:events","result":{"type":"subscription_started"}}` + "\n"))
		_, _ = conn.Write([]byte(`{"id":"herdrx:events","error":{"code":"events_lost","message":"subscriber fell behind"}}` + "\n"))
		time.Sleep(time.Second)
	})
	err = lost.SubscribeEvents(t.Context(), nil, func(Event) {})
	if !errors.As(err, &apiError) || apiError.Code != "events_lost" {
		t.Fatalf("events_lost err = %v", err)
	}
}

func TestSubscribeEventsEndsWithContext(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	endpoint := eventServer(t, func(_ map[string]any, conn net.Conn) {
		_, _ = conn.Write([]byte(`{"id":"herdrx:events","result":{"type":"subscription_started"}}` + "\n"))
		<-release
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- endpoint.SubscribeEvents(ctx, nil, func(event Event) {
			if event.Event == EventSubscriptionStarted {
				cancel()
			}
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscription did not end with its context")
	}
}
