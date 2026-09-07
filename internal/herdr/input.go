package herdr

import (
	"context"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"
)

// InputQueue keeps one ordered RPC in flight per browser pane. Keystrokes
// arriving during that round trip are sent together next, instead of each
// adding another round trip. The WebSocket reader remains free to accept input
// and output acknowledgements. Nothing is replayed after failure or detach.
type InputQueue struct {
	ctx      context.Context
	cancel   context.CancelFunc
	endpoint Endpoint
	paneID   string
	onError  func(error)
	mu       sync.Mutex
	pending  []byte
	stopped  bool
	sealed   bool
	wake     chan struct{}
	done     chan struct{}
}

const maxQueuedInput = 4 << 20

func NewInputQueue(ctx context.Context, endpoint Endpoint, paneID string, onError func(error)) *InputQueue {
	ctx, cancel := context.WithCancel(ctx)
	q := &InputQueue{ctx: ctx, cancel: cancel, endpoint: endpoint, paneID: paneID, onError: onError, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go q.run()
	return q
}

func (q *InputQueue) Enqueue(data []byte) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped || q.sealed || q.ctx.Err() != nil {
		return context.Canceled
	}
	if len(data)+len(q.pending) > maxQueuedInput {
		q.stopped = true
		q.pending = nil
		q.cancel()
		return fmt.Errorf("输入积压过多，请检查网络并重连终端后再输入")
	}
	q.pending = append(q.pending, data...)
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return nil
}

func (q *InputQueue) Close() {
	q.cancel()
	q.mu.Lock()
	q.stopped = true
	q.pending = nil
	q.mu.Unlock()
}

// Finish preserves input already accepted before switching a browser tab or
// pane. Access cancellation still interrupts immediately; it never waits for
// this drain, and failures never retry the current request.
func (q *InputQueue) Finish() <-chan struct{} {
	q.mu.Lock()
	q.sealed = true
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return q.done
}

func (q *InputQueue) run() {
	defer close(q.done)
	defer q.Close()
	for {
		select {
		case <-q.ctx.Done():
			return
		case <-q.wake:
		}
		for {
			q.mu.Lock()
			if q.stopped || q.ctx.Err() != nil {
				q.mu.Unlock()
				return
			}
			if len(q.pending) == 0 {
				sealed := q.sealed
				q.mu.Unlock()
				if sealed {
					return
				}
				break
			}
			// Bound a paste request without splitting Chinese or emoji bytes.
			n := min(len(q.pending), 64<<10)
			for n < len(q.pending) && n > 0 && !utf8.RuneStart(q.pending[n]) {
				n--
			}
			if n == 0 {
				n = min(len(q.pending), 64<<10)
			}
			data := string(q.pending[:n])
			q.pending = q.pending[n:]
			q.mu.Unlock()
			ctx, cancel := context.WithTimeout(q.ctx, 5*time.Second)
			_, err := q.endpoint.Call(ctx, "pane.send_text", map[string]any{"pane_id": q.paneID, "text": data})
			cancel()
			if err != nil {
				if q.ctx.Err() == nil {
					q.Close()
					q.onError(err)
				}
				return
			}
		}
	}
}
