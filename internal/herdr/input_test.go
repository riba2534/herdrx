package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type inputEndpoint struct {
	Endpoint
	call func(context.Context, string) error
}

func (e inputEndpoint) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if method != "pane.send_text" {
		panic("input must not change ownership or resize the PTY")
	}
	return nil, e.call(ctx, params.(map[string]any)["text"].(string))
}

func TestInputCoalescesInOrderAndPreservesBytes(t *testing.T) {
	started := make(chan string, 8)
	release := make(chan struct{})
	endpoint := inputEndpoint{call: func(ctx context.Context, data string) error {
		started <- data
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	q := NewInputQueue(t.Context(), endpoint, "pane", func(err error) { t.Error(err) })
	defer q.Close()
	if err := q.Enqueue([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if data := <-started; data != "a" {
		t.Fatal(data)
	}
	parts := []string{"中", "文🙂", "\x1b[A", "\x03", "\x1b[200~paste\ntext\x1b[201~", "\r"}
	for _, part := range parts {
		if err := q.Enqueue([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	// A blocked remote request must not block the caller or create one RPC/key.
	close(release)
	select {
	case data := <-started:
		if data != strings.Join(parts, "") {
			t.Fatalf("input reordered: %q", data)
		}
	case <-time.After(time.Second):
		t.Fatal("queued input was not sent")
	}
}

func TestInputFailureAndDetachDiscardPending(t *testing.T) {
	for _, detach := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "detach"}[detach], func(t *testing.T) {
			started, release, finished := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
			failed := make(chan error, 1)
			endpoint := inputEndpoint{call: func(ctx context.Context, data string) error {
				started <- struct{}{}
				defer close(finished)
				select {
				case <-release:
					return errors.New("response lost")
				case <-ctx.Done():
					return ctx.Err()
				}
			}}
			q := NewInputQueue(t.Context(), endpoint, "pane", func(err error) { failed <- err })
			defer q.Close()
			_ = q.Enqueue([]byte("uncertain"))
			<-started
			_ = q.Enqueue([]byte("must not replay"))
			if detach {
				q.Close()
			} else {
				close(release)
				<-failed
			}
			<-finished
			if err := q.Enqueue([]byte("new input")); err == nil {
				t.Fatal("stopped queue accepted input")
			}
			select {
			case <-started:
				t.Fatal("input replayed after failure/detach")
			default:
			}
		})
	}
}

func TestInputFinishDrainsBeforeSwitchingPane(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	endpoint := inputEndpoint{call: func(ctx context.Context, data string) error {
		started <- data
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	q := NewInputQueue(t.Context(), endpoint, "pane", func(err error) { t.Error(err) })
	defer q.Close()
	_ = q.Enqueue([]byte("command"))
	<-started
	_ = q.Enqueue([]byte("\r"))
	done := q.Finish()
	if err := q.Enqueue([]byte("late")); err == nil {
		t.Fatal("closed stream accepted new input")
	}
	select {
	case <-done:
		t.Fatal("pane switch discarded Enter before delivery")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("input drain did not finish")
	}
	if data := <-started; data != "\r" {
		t.Fatalf("queued Enter lost: %q", data)
	}
}

func TestInputPasteBounds(t *testing.T) {
	var received strings.Builder
	done := make(chan struct{})
	paste := strings.Repeat("中文🙂", 17000)
	endpoint := inputEndpoint{call: func(_ context.Context, data string) error {
		if len(data) > 64<<10 || !utf8.ValidString(data) {
			t.Errorf("invalid paste chunk: %d bytes", len(data))
		}
		received.WriteString(data)
		if received.Len() == len(paste) {
			close(done)
		}
		return nil
	}}
	q := NewInputQueue(t.Context(), endpoint, "pane", func(err error) { t.Error(err) })
	defer q.Close()
	if err := q.Enqueue([]byte(paste)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("paste timed out")
	}
	if received.String() != paste {
		t.Fatal("paste changed")
	}
	if err := q.Enqueue(make([]byte, maxQueuedInput+1)); err == nil {
		t.Fatal("unbounded input accepted")
	}
}

// A reproducible WAN-delay comparison, not a claim about a physical network.
func TestInputLatencyWithDelayedRPC(t *testing.T) {
	const count = 24
	const delay = 150 * time.Millisecond
	const interval = 20 * time.Millisecond
	measure := func(batched bool) []time.Duration {
		var mu sync.Mutex
		sent := make([]time.Time, count)
		latencies := make([]time.Duration, 0, count)
		done := make(chan struct{})
		endpoint := inputEndpoint{call: func(ctx context.Context, data string) error {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
			mu.Lock()
			defer mu.Unlock()
			for _, key := range []byte(data) {
				latencies = append(latencies, time.Since(sent[int(key-'a')]))
			}
			if len(latencies) == count {
				close(done)
			}
			return nil
		}}
		var enqueue func([]byte)
		if batched {
			q := NewInputQueue(t.Context(), endpoint, "pane", func(err error) { t.Error(err) })
			defer q.Close()
			enqueue = func(data []byte) {
				if err := q.Enqueue(data); err != nil {
					t.Error(err)
				}
			}
		} else {
			keys := make(chan []byte, count)
			defer close(keys)
			go func() {
				for key := range keys {
					_, _ = endpoint.Call(t.Context(), "pane.send_text", map[string]any{"text": string(key)})
				}
			}()
			enqueue = func(data []byte) { keys <- data }
		}
		for i := range count {
			mu.Lock()
			sent[i] = time.Now()
			mu.Unlock()
			enqueue([]byte{byte('a' + i)})
			time.Sleep(interval)
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("input timed out")
		}
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(latencies)
	}
	before, after := measure(false), measure(true)
	slices.Sort(before)
	slices.Sort(after)
	t.Logf("150ms RPC, 24 keys at 20ms intervals: serial p50=%s p95=%s; coalesced p50=%s p95=%s", before[count/2].Round(time.Millisecond), before[count*95/100].Round(time.Millisecond), after[count/2].Round(time.Millisecond), after[count*95/100].Round(time.Millisecond))
	if after[count*95/100] >= before[count*95/100]/3 {
		t.Fatal("typing still accumulates round trips")
	}
}
