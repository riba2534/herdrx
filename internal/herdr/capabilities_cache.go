package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var runtimeGenerationSequence atomic.Uint64

// CapabilityCache belongs to one access transport, not a host database row.
// No network or child-process work runs under its mutex. A changed live version
// invalidates both successful probes and explicit unsupported-method evidence.
type CapabilityCache struct {
	mu           sync.Mutex
	base         uint64
	revision     uint64
	liveVersion  string
	liveProtocol int
	liveKnown    bool
	cliVersion   string
	cliProtocol  int
	report       *CapabilityReport
	flight       chan struct{}
	unsupported  map[string]bool
}

func (c *CapabilityCache) generationLocked() string {
	if c.base == 0 {
		c.base = runtimeGenerationSequence.Add(1)
	}
	return fmt.Sprintf("%x.%x", c.base, c.revision)
}

func (c *CapabilityCache) RuntimeGeneration() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generationLocked()
}

func (c *CapabilityCache) Cached() (CapabilityReport, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.report == nil || time.Since(c.report.CheckedAt) >= time.Minute {
		return CapabilityReport{}, false
	}
	return cloneCapabilityReport(*c.report), true
}

func (c *CapabilityCache) ObserveSnapshot(snapshot Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.liveKnown && (c.liveVersion != snapshot.Version || c.liveProtocol != snapshot.Protocol) {
		c.revision++
		c.report = nil
		c.unsupported = nil
	}
	c.liveVersion, c.liveProtocol, c.liveKnown = snapshot.Version, snapshot.Protocol, true
}

func (c *CapabilityCache) RecordFailure(method string, err error) {
	c.RecordFailureAt("", method, err)
}

// A request that started before a daemon change cannot poison the new cache.
func (c *CapabilityCache) RecordFailureAt(generation, method string, err error) {
	if !IsUnsupportedError(err) {
		return
	}
	name := methodFeature(method)
	if name == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != "" && generation != c.generationLocked() {
		return
	}
	if c.unsupported == nil {
		c.unsupported = make(map[string]bool)
	}
	c.unsupported[name] = true
	if method == "pane.process_info" {
		// Explicit sizing also requires a dynamically synchronized observer.
		c.unsupported["resize"] = true
	}
	if c.report != nil {
		c.applyFailuresLocked(c.report)
	}
}

func methodFeature(method string) string {
	switch method {
	case "session.snapshot":
		return "snapshot"
	case "pane.send_input", "pane.send_text", "pane.send_keys":
		return "input"
	case "pane.read":
		return "history"
	case "pane.process_info":
		return "preserve_scroll"
	}
	return ""
}

func (c *CapabilityCache) applyFailuresLocked(report *CapabilityReport) {
	for name := range c.unsupported {
		report.Features[name] = feature(CapabilityUnavailable, "当前 Herdr 服务明确拒绝此接口；其他功能仍可使用", "daemon:explicit unsupported method")
	}
	report.refreshStatus()
}

func (c *CapabilityCache) Report(ctx context.Context, endpoint Endpoint) (CapabilityReport, error) {
	for {
		if err := ctx.Err(); err != nil {
			return CapabilityReport{}, err
		}
		// The cheap live snapshot is refreshed even when the executable/schema
		// result is cached. This detects an independently upgraded daemon.
		snapshot, err := endpoint.Snapshot(ctx)
		if err != nil {
			return CapabilityReport{}, err
		}
		c.ObserveSnapshot(snapshot)
		c.mu.Lock()
		if c.report != nil && time.Since(c.report.CheckedAt) < time.Minute {
			result := cloneCapabilityReport(*c.report)
			c.mu.Unlock()
			return result, nil
		}
		if c.flight != nil {
			done := c.flight
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return CapabilityReport{}, ctx.Err()
			case <-done:
				continue
			}
		}
		flight := make(chan struct{})
		c.flight = flight
		revision := c.revision
		c.mu.Unlock()

		report := probeCapabilities(ctx, endpoint, snapshot)
		c.mu.Lock()
		c.flight = nil
		close(flight)
		if revision != c.revision {
			c.mu.Unlock()
			continue
		}
		if report.CLI.Protocol > 0 && c.cliProtocol > 0 && (c.cliVersion != report.CLI.Version || c.cliProtocol != report.CLI.Protocol) {
			c.revision++
			c.unsupported = nil
		}
		if report.CLI.Protocol > 0 {
			c.cliVersion, c.cliProtocol = report.CLI.Version, report.CLI.Protocol
		}
		report.Generation = c.generationLocked()
		c.applyFailuresLocked(&report)
		cacheable := ctx.Err() == nil
		for _, item := range report.Features {
			if item.State == CapabilityUnknown {
				cacheable = false
			}
		}
		if cacheable {
			stored := cloneCapabilityReport(report)
			c.report = &stored
		}
		c.mu.Unlock()
		return report, nil
	}
}

func cloneCapabilityReport(report CapabilityReport) CapabilityReport {
	report.CLI.Methods = append([]string{}, report.CLI.Methods...)
	features := make(map[string]FeatureCapability, len(report.Features))
	for name, value := range report.Features {
		value.Evidence = append([]string{}, value.Evidence...)
		features[name] = value
	}
	report.Features = features
	if report.Daemon.Capabilities != nil {
		capabilities := make(map[string]json.RawMessage, len(report.Daemon.Capabilities))
		for name, value := range report.Daemon.Capabilities {
			capabilities[name] = append(json.RawMessage{}, value...)
		}
		report.Daemon.Capabilities = capabilities
	}
	return report
}
