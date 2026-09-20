package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type capabilityFixture struct {
	mu               sync.Mutex
	snapshot         Snapshot
	cli              CLIIdentity
	cliErr           error
	cliCalls         int
	callErrors       map[string]error
	calls            []string
	ping             json.RawMessage
	started, release chan struct{}
}

func newCapabilityFixture() *capabilityFixture {
	return &capabilityFixture{
		snapshot:   Snapshot{Version: "0.9.1", Protocol: 22, Panes: []Pane{{ID: "pane_fixture"}}},
		cli:        CLIIdentity{Version: "0.9.1", Protocol: 22, Methods: []string{"pane.read", "pane.send_input", "session.snapshot"}},
		callErrors: make(map[string]error),
	}
}

func (f *capabilityFixture) Snapshot(context.Context) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot, nil
}
func (f *capabilityFixture) Call(_ context.Context, method string, _ any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method)
	if err := f.callErrors[method]; err != nil {
		return nil, err
	}
	switch method {
	case "ping":
		if f.ping != nil {
			return f.ping, nil
		}
		return json.Marshal(map[string]any{"type": "pong", "version": f.snapshot.Version, "protocol": f.snapshot.Protocol})
	case "pane.read":
		return json.RawMessage(`{"read":{"text":"fixture"}}`), nil
	case "pane.process_info":
		return json.RawMessage(`{"process_info":{"pane_id":"pane_fixture","shell_pid":42}}`), nil
	default:
		panic("capability probe attempted a non-read-only method: " + method)
	}
}
func (f *capabilityFixture) HerdrCLIIdentity(ctx context.Context) (CLIIdentity, error) {
	f.mu.Lock()
	f.cliCalls++
	cli, err, started, release := f.cli, f.cliErr, f.started, f.release
	f.started, f.release = nil, nil
	f.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return CLIIdentity{}, ctx.Err()
		}
	}
	return cli, err
}
func (*capabilityFixture) OpenTerminal(context.Context, TerminalOpen) (TerminalProcess, error) {
	panic("diagnostics opened terminal")
}
func (*capabilityFixture) StageImage(context.Context, string, io.Reader) (string, error) {
	panic("diagnostics wrote file")
}
func (*capabilityFixture) Close() error { return nil }
func (f *capabilityFixture) TerminalGeometry(ctx context.Context, pane string) (TerminalGeometry, error) {
	_, err := f.Call(ctx, "pane.process_info", map[string]any{"pane_id": pane})
	return TerminalGeometry{Cols: 80, Rows: 24}, err
}
func (*capabilityFixture) OpenTerminalSocket(context.Context) (net.Conn, error) {
	panic("diagnostics attached terminal")
}

func TestCapabilityVersionAndProtocolBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		cliVersion              string
		cliProtocol             int
		daemonVersion           string
		daemonProtocol          int
		terminal, input, scroll CapabilityState
		coverage                string
	}{
		{"baseline20", "0.8.2", 20, "0.8.2", 20, CapabilityAvailable, CapabilityAvailable, CapabilityAvailable, "tested"},
		{"same-protocol-different-release", "0.9.0", 22, "0.9.1", 22, CapabilityAvailable, CapabilityAvailable, CapabilityAvailable, "tested"},
		{"cli-daemon-mismatch", "0.8.2", 20, "0.9.1", 22, CapabilityUnavailable, CapabilityAvailable, CapabilityAvailable, "tested"},
		{"future-product-reviewed-protocol", "0.12.0", 22, "0.12.0", 22, CapabilityAvailable, CapabilityAvailable, CapabilityAvailable, "untested"},
		{"unknown-protocol", "0.12.0", 99, "0.12.0", 99, CapabilityUnavailable, CapabilityUnknown, CapabilityUnavailable, "untested"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCapabilityFixture()
			f.cli.Version, f.cli.Protocol = tc.cliVersion, tc.cliProtocol
			f.snapshot.Version, f.snapshot.Protocol = tc.daemonVersion, tc.daemonProtocol
			report, err := ProbeCapabilities(t.Context(), f)
			if err != nil {
				t.Fatal(err)
			}
			for name, want := range map[string]CapabilityState{"snapshot": CapabilityAvailable, "history": CapabilityAvailable, "observe": tc.scroll, "resize": tc.terminal, "input": tc.input, "preserve_scroll": tc.scroll} {
				if report.Features[name].State != want {
					t.Errorf("%s: %+v want %s", name, report.Features[name], want)
				}
			}
			if report.Coverage != tc.coverage {
				t.Fatalf("coverage=%s", report.Coverage)
			}
			if report.Daemon.Capabilities != nil {
				t.Fatal("CLI schema manufactured live capabilities")
			}
		})
	}
}

func TestCapabilityOptionalMethodAndStaleProbe(t *testing.T) {
	f := newCapabilityFixture()
	f.callErrors["pane.read"] = &APIError{Code: "method_not_found", Message: "pane.read"}
	report, err := ProbeCapabilities(t.Context(), f)
	if err != nil || report.Features["history"].State != CapabilityUnavailable || report.Features["observe"].State != CapabilityAvailable || report.Features["input"].State != CapabilityAvailable {
		t.Fatalf("optional failure disabled unrelated features: %+v %v", report, err)
	}
	delete(f.callErrors, "pane.read")
	f.cli.Methods = []string{"session.snapshot"}
	f.ping = json.RawMessage(`{"version":"0.9.1","protocol":22,"capabilities":{"endpoint_protocol_generation":1}}`)
	report, _ = ProbeCapabilities(t.Context(), f)
	if report.Features["history"].State != CapabilityAvailable || string(report.Daemon.Capabilities["endpoint_protocol_generation"]) != "1" {
		t.Fatal("daemon evidence was replaced by installed schema")
	}
	f.ping = json.RawMessage(`{"version":"0.10.0","protocol":23}`)
	report, _ = ProbeCapabilities(t.Context(), f)
	if report.Features["observe"].State != CapabilityUnknown || report.Features["resize"].State != CapabilityUnknown {
		t.Fatal("mixed daemon identities accepted")
	}
}

func TestCapabilityTransientFailureIsRetried(t *testing.T) {
	for _, name := range []string{"cli", "pane.read", "pane.process_info"} {
		t.Run(name, func(t *testing.T) {
			f := newCapabilityFixture()
			if name == "cli" {
				f.cliErr = context.DeadlineExceeded
			} else {
				f.callErrors[name] = context.DeadlineExceeded
			}
			var cache CapabilityCache
			first, err := cache.Report(t.Context(), f)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := cache.Cached(); ok {
				t.Fatal("transient failure was cached")
			}
			if first.Status != "limited" {
				t.Fatalf("report=%+v", first)
			}
			f.cliErr = nil
			delete(f.callErrors, name)
			second, err := cache.Report(t.Context(), f)
			if err != nil || second.Status != "available" || f.cliCalls != 2 {
				t.Fatalf("retry failed: %+v %v", second, err)
			}
		})
	}
}

func TestCapabilityGeometryFailureKeepsReadOnlyCLIFallback(t *testing.T) {
	f := newCapabilityFixture()
	f.callErrors["pane.process_info"] = &APIError{Code: "unknown_method", Message: "pane.process_info"}
	report, err := ProbeCapabilities(t.Context(), f)
	if err != nil || report.Features["observe"].State != CapabilityAvailable || report.Features["resize"].State != CapabilityUnavailable || report.Features["preserve_scroll"].State != CapabilityUnavailable {
		t.Fatalf("geometry rejection lost fallback or claimed control: %+v %v", report, err)
	}
	delete(f.callErrors, "pane.process_info")
	var cache CapabilityCache
	_, _ = cache.Report(t.Context(), f)
	cache.RecordFailure("pane.process_info", &APIError{Code: "method_not_found"})
	cached, ok := cache.Cached()
	if !ok || cached.Features["resize"].State != CapabilityUnavailable || cached.Features["observe"].State != CapabilityAvailable {
		t.Fatalf("geometry rejection scope incorrect: %+v", cached)
	}
}

func TestCapabilityCacheTracksLiveGenerationAndMethodRejection(t *testing.T) {
	f := newCapabilityFixture()
	var cache CapabilityCache
	first, err := cache.Report(t.Context(), f)
	if err != nil {
		t.Fatal(err)
	}
	cache.RecordFailure("pane.send_input", context.DeadlineExceeded)
	second, _ := cache.Report(t.Context(), f)
	if f.cliCalls != 1 || second.Features["input"].State != CapabilityAvailable {
		t.Fatal("timeout poisoned cached input")
	}
	cache.RecordFailure("pane.send_input", fmt.Errorf("request: %w", &APIError{Code: "unknown_method"}))
	second, ok := cache.Cached()
	if !ok || second.Features["input"].State != CapabilityUnavailable || second.Features["observe"].State != CapabilityAvailable {
		t.Fatal("method rejection scope wrong")
	}
	second.CLI.Methods[0] = "mutated"
	second.Features["observe"] = FeatureCapability{}
	copy, _ := cache.Cached()
	if copy.CLI.Methods[0] == "mutated" || copy.Features["observe"].State != CapabilityAvailable {
		t.Fatal("mutable report escaped cache")
	}
	f.snapshot.Version = "0.9.2"
	cache.ObserveSnapshot(f.snapshot)
	if cache.RuntimeGeneration() == first.Generation {
		t.Fatal("live version change retained geometry generation")
	}
	if _, ok := cache.Cached(); ok {
		t.Fatal("live version change retained capability cache")
	}
	cache.RecordFailureAt(first.Generation, "pane.read", &APIError{Code: "unsupported_method"})
	third, _ := cache.Report(t.Context(), f)
	if third.Features["input"].State != CapabilityAvailable || third.Features["history"].State != CapabilityAvailable || f.cliCalls != 2 {
		t.Fatal("old daemon method rejection poisoned new generation")
	}
}

func TestCapabilityInFlightProbeDoesNotPublishOldDaemon(t *testing.T) {
	f := newCapabilityFixture()
	f.started, f.release = make(chan struct{}), make(chan struct{})
	started, release := f.started, f.release
	var cache CapabilityCache
	done := make(chan CapabilityReport, 1)
	go func() { r, _ := cache.Report(t.Context(), f); done <- r }()
	<-started
	f.mu.Lock()
	f.snapshot.Version = "0.10.0"
	snapshot := f.snapshot
	f.mu.Unlock()
	cache.ObserveSnapshot(snapshot)
	close(release)
	select {
	case report := <-done:
		if report.Daemon.Version != "0.10.0" || report.Generation != cache.RuntimeGeneration() {
			t.Fatalf("stale probe published: %+v", report)
		}
	case <-time.After(time.Second):
		t.Fatal("probe did not retry after generation changed")
	}
}

func TestParseCLIIdentityUsesDeclaredProtocol(t *testing.T) {
	schema := []byte(`{"protocol":22,"schemas":{"request":{"oneOf":[{"properties":{"method":{"const":"session.snapshot"}}},{"properties":{"method":{"const":"pane.read"}}}]}}}`)
	for _, version := range []string{"herdr 0.9.1", "herdr 0.9.2-preview.123"} {
		identity, err := ParseCLIIdentity([]byte(version), schema)
		if err != nil || identity.Protocol != 22 || len(identity.Methods) != 2 {
			t.Fatalf("%+v %v", identity, err)
		}
	}
	for _, input := range []struct{ version, schema string }{
		{"not-herdr 0.9.1", string(schema)}, {"herdr 0.9.1", `{"schemas":{}}`}, {"herdr 0.9.1", `null`},
	} {
		if _, err := ParseCLIIdentity([]byte(input.version), []byte(input.schema)); err == nil {
			t.Fatalf("accepted incomplete identity: %+v", input)
		}
	}
}

func TestUnsupportedMethodErrorsExcludeTransientAndTargetFailures(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, io.EOF, errors.New("unsupported method"), &APIError{Code: "not_found"}, &APIError{Code: "invalid_request", Message: "missing field pane_id"}, &APIError{Code: "invalid_request", Message: "unknown variant `paste`, expected text or key"}, &APIError{Code: "unsupported", Message: "unsupported image format"}} {
		if IsUnsupportedError(err) {
			t.Fatalf("unsafe permanent downgrade: %v", err)
		}
	}
	if !IsUnsupportedError(&APIError{Code: "invalid_request", Message: "unknown variant `pane.read`"}) {
		t.Fatal("explicit parser method rejection ignored")
	}
}
