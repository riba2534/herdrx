package hostruntime

import (
	"context"

	"github.com/riba2534/herdrx/internal/herdr"
)

func (h *sharedHandle) RuntimeGeneration() string {
	return h.entry.capabilities.RuntimeGeneration()
}

func (h *sharedHandle) HerdrCapabilities(ctx context.Context) (herdr.CapabilityReport, error) {
	return h.entry.capabilities.Report(ctx, h.entry.endpoint)
}

func (h *sharedHandle) CachedHerdrCapabilities() (herdr.CapabilityReport, bool) {
	return h.entry.capabilities.Cached()
}
