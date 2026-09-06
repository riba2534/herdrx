package hostruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/config"
	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/store"
)

func awaitPool(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("pool did not reach expected state")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestConcurrentObserversShareDialAndCancelIndependently(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	endpoint := new(testEndpoint)
	f := &Factory{dial: func(ctx context.Context, _ store.Host) (herdr.Endpoint, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return endpoint, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	defer f.Close()
	host := store.Host{ID: "shared", Transport: "ssh"}
	canceled, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := f.Open(canceled, host); first <- err }()
	<-started
	const observers = 64
	results := make(chan herdr.Endpoint, observers)
	errs := make(chan error, observers)
	for range observers {
		go func() { h, err := f.Open(context.Background(), host); results <- h; errs <- err }()
	}
	awaitPool(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.flights[host.ID].waiters == observers+1 })
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	for range observers {
		h := <-results
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		h.Close()
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate dials: %d", calls.Load())
	}
	stats := f.Stats()
	if stats.Idle != 1 || stats.Active != 0 || stats.Pending != 0 {
		t.Fatalf("reference leak: %+v", stats)
	}
}

func TestLastObserverCancellationCancelsDial(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	f := &Factory{dial: func(ctx context.Context, _ store.Host) (herdr.Endpoint, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}}
	defer f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := f.Open(ctx, store.Host{ID: "last", Transport: "ssh"}); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("abandoned dial kept running")
	}
	if stats := f.Stats(); stats.Pending != 0 || stats.Connections != 0 {
		t.Fatalf("abandoned flight retained: %+v", stats)
	}
}

func TestPoolBoundsDialsAndConnections(t *testing.T) {
	var active, peak atomic.Int32
	release := make(chan struct{})
	f := &Factory{Config: config.Config{HostDialConcurrency: 3, MaxHostConnections: 10}, dial: func(ctx context.Context, _ store.Host) (herdr.Endpoint, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); n > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, n) {
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return new(testEndpoint), nil
		}
	}}
	defer f.Close()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var handles []herdr.Endpoint
	var rejected atomic.Int32
	for i := range 100 {
		wg.Go(func() {
			h, err := f.Open(context.Background(), store.Host{ID: fmt.Sprint(i), Transport: "ssh"})
			if err != nil {
				if DescribeError(err).Code != "host_capacity" {
					t.Errorf("unexpected error: %v", err)
				}
				rejected.Add(1)
				return
			}
			mu.Lock()
			handles = append(handles, h)
			mu.Unlock()
		})
	}
	awaitPool(t, func() bool { return f.Stats().Pending == 10 && rejected.Load() == 90 })
	if f.Stats().Dialing > 3 {
		t.Fatal("too many simultaneous dials")
	}
	close(release)
	wg.Wait()
	if len(handles) != 10 || peak.Load() > 3 || rejected.Load() != 90 {
		t.Fatalf("bad limits: accepted=%d rejected=%d peak=%d", len(handles), rejected.Load(), peak.Load())
	}
	handles[0].Close()
	next, err := f.Open(context.Background(), store.Host{ID: "evict-idle", Transport: "ssh"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Stats().Connections != 10 {
		t.Fatal("idle eviction exceeded capacity")
	}
	next.Close()
	for _, h := range handles {
		h.Close()
	}
}

func TestDialDeadlineAndBackoff(t *testing.T) {
	var calls atomic.Int32
	f := &Factory{Config: config.Config{HostDialTimeout: 20 * time.Millisecond}, dial: func(ctx context.Context, _ store.Host) (herdr.Endpoint, error) {
		calls.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	defer f.Close()
	host := store.Host{ID: "slow", Transport: "ssh"}
	start := time.Now()
	if _, err := f.Open(context.Background(), host); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("dial deadline not enforced")
	}
	for range 10 {
		if _, err := f.Open(context.Background(), host); err == nil || DescribeError(err).RetryAfter <= 0 {
			t.Fatal("missing backoff", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("outage caused repeated dials")
	}
	f.CloseHost(host.ID)
	if _, err := f.Open(context.Background(), host); err == nil {
		t.Fatal("unexpected success")
	}
	if calls.Load() != 2 {
		t.Fatal("configuration change did not reset backoff")
	}
}

func TestHostKeyFailureRequiresConfigurationChange(t *testing.T) {
	var calls atomic.Int32
	f := &Factory{dial: func(context.Context, store.Host) (herdr.Endpoint, error) {
		calls.Add(1)
		return nil, &herdr.UnknownHostKeyError{Fingerprint: "test"}
	}}
	defer f.Close()
	host := store.Host{ID: "unknown", Transport: "ssh"}
	for range 3 {
		_, err := f.Open(context.Background(), host)
		var unknown *herdr.UnknownHostKeyError
		if !errors.As(err, &unknown) || !DescribeError(err).Permanent {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("untrusted identity was repeatedly retried")
	}
	f.CloseHost(host.ID)
	_, _ = f.Open(context.Background(), host)
	if calls.Load() != 2 {
		t.Fatal("trust update did not unblock connection")
	}
}
