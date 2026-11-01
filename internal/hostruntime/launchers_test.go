package hostruntime

import (
	"context"
	"io"
	"reflect"
	"testing"

	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/launchers"
	"github.com/riba2534/herdrx/internal/store"
)

type launcherTestEndpoint struct {
	testEndpoint
	result launchers.Result
	err    error
}

func (e *launcherTestEndpoint) Launchers(context.Context) (launchers.Result, error) {
	return e.result, e.err
}

func openShared(t *testing.T, endpoint herdr.Endpoint) (*Factory, herdr.Endpoint) {
	t.Helper()
	f := &Factory{dial: func(context.Context, store.Host) (herdr.Endpoint, error) { return endpoint, nil }}
	t.Cleanup(f.Close)
	handle, err := f.Open(context.Background(), store.Host{ID: "one", Transport: "ssh"})
	if err != nil {
		t.Fatal(err)
	}
	return f, handle
}

func TestSharedHandleForwardsLaunchers(t *testing.T) {
	want := launchers.Found([]launchers.Launcher{{Name: "xcc", Bypass: true}})
	endpoint := &launcherTestEndpoint{result: want}
	_, handle := openShared(t, endpoint)
	discover, ok := handle.(herdr.LauncherEndpoint)
	if !ok {
		t.Fatal("shared handle hides launcher discovery")
	}
	got, err := discover.Launchers(context.Background())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v err %v", got, err)
	}
	// A classified failure keeps the shared transport.
	endpoint.result = launchers.Unsupported(launchers.ReasonTimeout)
	if got, _ := discover.Launchers(context.Background()); got.Reason != launchers.ReasonTimeout || endpoint.closes.Load() != 0 {
		t.Fatalf("got %+v closes %d", got, endpoint.closes.Load())
	}
	// A broken transport is dropped so the next open redials.
	endpoint.err = io.EOF
	if _, err := discover.Launchers(context.Background()); err != io.EOF || endpoint.closes.Load() != 1 {
		t.Fatalf("err %v closes %d", err, endpoint.closes.Load())
	}
}

func TestSharedHandleWithoutDiscoveryIsUnsupported(t *testing.T) {
	_, handle := openShared(t, new(testEndpoint))
	got, err := handle.(herdr.LauncherEndpoint).Launchers(context.Background())
	if err != nil || got.Supported || got.Reason != launchers.ReasonUnsupportedAgent || got.Launchers == nil {
		t.Fatalf("got %+v err %v", got, err)
	}
}
