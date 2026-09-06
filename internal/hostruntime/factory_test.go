package hostruntime

import (
	"context"
	"encoding/json"
	"github.com/riba2534/herdrx/internal/config"
	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/store"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type testEndpoint struct {
	closes  atomic.Int32
	closing chan struct{}
	unblock chan struct{}
}

func (e *testEndpoint) Snapshot(ctx context.Context) (herdr.Snapshot, error) {
	if ctx.Err() != nil {
		return herdr.Snapshot{}, io.EOF
	}
	return herdr.Snapshot{}, nil
}
func (e *testEndpoint) Call(context.Context, string, any) (json.RawMessage, error) { return nil, nil }
func (e *testEndpoint) OpenTerminal(context.Context, herdr.TerminalOpen) (herdr.TerminalProcess, error) {
	return nil, nil
}
func (e *testEndpoint) StageImage(context.Context, string, io.Reader) (string, error) { return "", nil }
func (e *testEndpoint) Close() error {
	e.closes.Add(1)
	if e.closing != nil {
		close(e.closing)
		<-e.unblock
	}
	return nil
}

func TestSharedTransportLifetime(t *testing.T) {
	endpoint := new(testEndpoint)
	var dials atomic.Int32
	f := &Factory{Config: config.Config{HostIdleTimeout: 30 * time.Millisecond}, dial: func(context.Context, store.Host) (herdr.Endpoint, error) { dials.Add(1); return endpoint, nil }}
	defer f.Close()
	host := store.Host{ID: "one", Transport: "ssh"}
	first, err := f.Open(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.Open(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	first.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	second.Snapshot(ctx)
	if endpoint.closes.Load() != 0 {
		t.Fatal("one consumer canceled another consumer's transport")
	}
	second.Close()
	third, err := f.Open(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if endpoint.closes.Load() != 0 || dials.Load() != 1 {
		t.Fatal("idle timer closed an active consumer")
	}
	third.Close()
	deadline := time.Now().Add(time.Second)
	for endpoint.closes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if endpoint.closes.Load() != 1 {
		t.Fatal("unused transport did not close exactly once")
	}
	f.Close()
	if _, err = f.Open(context.Background(), host); err != net.ErrClosed {
		t.Fatalf("closed factory accepted a connection: %v", err)
	}
}

func TestTransportCloseDoesNotHoldFactoryLock(t *testing.T) {
	slow := &testEndpoint{closing: make(chan struct{}), unblock: make(chan struct{})}
	f := &Factory{dial: func(_ context.Context, h store.Host) (herdr.Endpoint, error) {
		if h.ID == "slow" {
			return slow, nil
		}
		return new(testEndpoint), nil
	}}
	defer f.Close()
	handle, err := f.Open(context.Background(), store.Host{ID: "slow", Transport: "ssh"})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	done := make(chan struct{})
	go func() { f.CloseHost("slow"); close(done) }()
	<-slow.closing
	other := make(chan error, 1)
	go func() {
		h, err := f.Open(context.Background(), store.Host{ID: "other", Transport: "ssh"})
		if h != nil {
			h.Close()
		}
		other <- err
	}()
	select {
	case err := <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Error("slow network close blocked other hosts")
	}
	close(slow.unblock)
	<-done
}

func TestChangedCredentialRejectsInFlightTransport(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	endpoint := new(testEndpoint)
	f := &Factory{dial: func(context.Context, store.Host) (herdr.Endpoint, error) {
		close(started)
		<-release
		return endpoint, nil
	}}
	defer f.Close()
	finished := make(chan error, 1)
	go func() {
		h, err := f.Open(context.Background(), store.Host{ID: "changed", Transport: "ssh"})
		if h != nil {
			h.Close()
		}
		finished <- err
	}()
	<-started
	f.CloseHost("changed")
	close(release)
	if err := <-finished; err == nil {
		t.Fatal("transport using obsolete authentication entered cache")
	}
	awaitPool(t, func() bool { return endpoint.closes.Load() == 1 })
}
