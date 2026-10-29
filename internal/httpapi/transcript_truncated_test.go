package httpapi

import (
	"testing"

	"github.com/riba2534/herdrx/internal/agentlog"
	"github.com/riba2534/herdrx/internal/herdr"
)

// 单行超限被截掉的记录数单独下发，不混进 skipped。
func TestTranscriptResponseCarriesTruncatedCount(t *testing.T) {
	endpoint := &transcriptTestEndpoint{
		snapshot: agentPane(agentlog.AgentClaude, "/srv/proj", ""),
		page: herdr.TranscriptPage{
			Supported: true, Agent: agentlog.AgentClaude, SessionID: "s",
			Binding: herdr.ChatBindingSelected, Messages: []agentlog.Record{}, Truncated: 40,
		},
	}
	fixture := newTranscriptFixture(t, endpoint)
	payload := requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", ""), "", nil, 200)
	if payload["truncated"] != float64(40) || payload["skipped"] != nil {
		t.Fatalf("payload = %v", payload)
	}
}
