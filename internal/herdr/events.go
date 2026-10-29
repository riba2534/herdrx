package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// EventSubscription is one events.subscribe entry: {"type":"pane.created"},
// or per pane {"type":"pane.agent_status_changed","pane_id":"w1:p2"}.
type EventSubscription map[string]string

// Event is one pushed Herdr event. Herdr names kinds with underscores
// ("pane_agent_status_changed") although subscriptions use dots.
type Event struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// EventSubscriptionStarted is reported first, once Herdr accepted the request.
const EventSubscriptionStarted = "subscription_started"

// ErrEventsUnsupported reports a transport that cannot hold an API stream.
var ErrEventsUnsupported = errors.New("herdr event subscription is not available on this transport")

// EventEndpoint keeps one Herdr API connection open for pushed events. It is a
// read-only access stream: ending it never affects Herdr or its panes.
type EventEndpoint interface {
	SubscribeEvents(context.Context, []EventSubscription, func(Event)) error
}

const (
	eventsHandshakeTimeout = 10 * time.Second
	eventsMaxLine          = 4 << 20
)

// SubscribeEvents streams events until ctx ends or Herdr closes the stream.
func (e *LocalEndpoint) SubscribeEvents(ctx context.Context, subscriptions []EventSubscription, onEvent func(Event)) error {
	return subscribeEvents(ctx, func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", e.SocketPath)
	}, subscriptions, onEvent)
}

// SubscribeEvents forwards the remote API socket like Call does. Tailcat's
// access daemon only copies bytes on this channel, so all SSH paths share it.
func (e *SSHEndpoint) SubscribeEvents(ctx context.Context, subscriptions []EventSubscription, onEvent func(Event)) error {
	return subscribeEvents(ctx, func(ctx context.Context) (net.Conn, error) {
		return e.client.DialContext(ctx, "unix", e.socketPath)
	}, subscriptions, onEvent)
}

// subscribeEvents makes one events.subscribe call per connection; Herdr
// ignores a second call, so a changed subscription set needs a new stream.
func subscribeEvents(ctx context.Context, dial func(context.Context) (net.Conn, error), subscriptions []EventSubscription, onEvent func(Event)) error {
	dialCtx, cancel := context.WithTimeout(ctx, eventsHandshakeTimeout)
	conn, err := dial(dialCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("connect herdr event stream: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	// SSH channels do not support deadlines; bound the handshake by closing.
	handshake := time.AfterFunc(eventsHandshakeTimeout, func() { _ = conn.Close() })
	defer handshake.Stop()
	request := map[string]any{"id": "herdrx:events", "method": "events.subscribe", "params": map[string]any{"subscriptions": subscriptions}}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return fmt.Errorf("write herdr events.subscribe: %w", err)
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64<<10), eventsMaxLine)
	if !scanner.Scan() {
		return streamEnd(ctx, scanner.Err(), "read herdr events.subscribe")
	}
	var response Response
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
		return fmt.Errorf("decode herdr events.subscribe: %w", err)
	}
	if response.Error != nil {
		return fmt.Errorf("herdr events.subscribe: %w", response.Error)
	}
	if !handshake.Stop() {
		return fmt.Errorf("herdr events.subscribe: %w", context.DeadlineExceeded)
	}
	onEvent(Event{Event: EventSubscriptionStarted})
	for scanner.Scan() {
		var line struct {
			Event
			Error *APIError `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		if line.Error != nil {
			// events_lost: Herdr dropped a subscriber that fell behind.
			return fmt.Errorf("herdr events: %w", line.Error)
		}
		if line.Event.Event != "" {
			onEvent(line.Event)
		}
	}
	return streamEnd(ctx, scanner.Err(), "herdr events")
}

func streamEnd(ctx context.Context, err error, operation string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err == nil {
		err = errors.New("stream closed")
	}
	return fmt.Errorf("%s: %w", operation, err)
}
