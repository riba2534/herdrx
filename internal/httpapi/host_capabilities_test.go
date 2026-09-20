package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/riba2534/herdrx/internal/herdr"
)

func TestHostCapabilitiesReadOnlyResponseAndFailure(t *testing.T) {
	endpoint := &transcriptTestEndpoint{snapshot: herdr.Snapshot{Version: "0.9.1", Protocol: 22}}
	f := newTranscriptFixture(t, endpoint)
	url := f.server + "/api/hosts/" + f.host.ID + "/capabilities"
	response, err := f.client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v", response.StatusCode, response.Header)
	}
	var payload struct {
		Capabilities herdr.CapabilityReport `json:"capabilities"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Capabilities.Daemon.Version != "0.9.1" || payload.Capabilities.Features["snapshot"].State != herdr.CapabilityAvailable || payload.Capabilities.Features["observe"].State != herdr.CapabilityUnknown {
		t.Fatalf("%+v", payload)
	}
	endpoint.snapshotErr = io.EOF
	requestJSON(t, f.client, "GET", url, "", nil, 502)
	requestJSON(t, newTestClient(t), "GET", url, "", nil, 401)
}
