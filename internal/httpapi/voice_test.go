package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/riba2534/herdrx/internal/config"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"github.com/riba2534/herdrx/internal/voicegateway"
)

const voiceTestOrigin = "http://example.test"

// ───────────────────────────── fake upstream gateway ─────────────────────────

// fakeRealtimeUpstream speaks the OpenAI Realtime words the relay maps, over a
// real WebSocket, so the HTTP layer is tested through the production transport.
type fakeRealtimeUpstream struct {
	server *httptest.Server

	mu       sync.Mutex
	auth     string
	query    string
	received []string
}

func newFakeRealtimeUpstream(t *testing.T) *fakeRealtimeUpstream {
	t.Helper()
	fake := &fakeRealtimeUpstream{}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeRealtimeUpstream) serve(writer http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	f.auth = request.Header.Get("Authorization")
	f.query = request.URL.RawQuery
	f.mu.Unlock()

	conn, err := websocket.Accept(writer, request, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := context.Background()

	_ = voiceWriteJSON(ctx, conn, map[string]any{"type": "session.created", "session": map[string]any{"model": "gpt-realtime"}})

	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.received = append(f.received, string(payload))
		f.mu.Unlock()

		var envelope struct {
			Type  string `json:"type"`
			Audio string `json:"audio"`
		}
		if json.Unmarshal(payload, &envelope) != nil {
			continue
		}
		switch envelope.Type {
		case "session.update":
			_ = voiceWriteJSON(ctx, conn, map[string]any{"type": "session.updated"})
		case "input_audio_buffer.append":
			decoded, _ := base64.StdEncoding.DecodeString(envelope.Audio)
			_ = voiceWriteJSON(ctx, conn, map[string]any{
				"type":       "conversation.item.input_audio_transcription.completed",
				"transcript": "relayed:" + itoaTest(len(decoded)),
			})
		}
	}
}

func voiceWriteJSON(ctx context.Context, conn *websocket.Conn, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, encoded)
}

func itoaTest(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// ───────────────────────────── herdrx test harness ──────────────────────────

type voiceHarness struct {
	api      *API
	gateway  *voicegateway.Gateway
	server   *httptest.Server
	upstream *fakeRealtimeUpstream
	client   *http.Client
	csrf     string
}

// newVoiceHarness boots a real API instance plus a voice subrouter mounted the
// way the contract specifies, pointed at an in-process fake upstream.
func newVoiceHarness(t *testing.T, configure func(*voicegateway.Config)) *voiceHarness {
	t.Helper()
	upstream := newFakeRealtimeUpstream(t)

	dataDir := t.TempDir()
	dataStore, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	vault, err := secure.OpenVault(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Addr: "127.0.0.1:0", DataDir: dataDir, PublicURL: voiceTestOrigin, BootstrapToken: "bootstrap-test-token",
		SessionTTL: time.Hour, HerdrBinary: "herdr", AllowPrivateHosts: true, DERPRegionID: 304,
	}
	api, err := New(cfg, dataStore, vault, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Close)

	voiceConfig := voicegateway.Config{
		Enabled: true, BaseURL: upstream.server.URL, APIKey: "server-only-test-credential",
		Model: "gpt-realtime", Path: "/v1/realtime", InputHz: 24000, OutputHz: 24000,
		AudioReply: voicegateway.AudioReplyOff,
	}
	if configure != nil {
		configure(&voiceConfig)
	}
	gateway, err := voicegateway.NewWithOptions(voicegateway.Options{
		Config:  voiceConfig,
		Limiter: requestLimiterAdapter{limiter: &api.limiter},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("voice gateway: %v", err)
	}
	t.Cleanup(gateway.Close)

	// Exercise the actual application router, not a mirrored route fixture.
	if api.voice != nil {
		api.voice.Close()
	}
	api.voice = gateway
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)

	harness := &voiceHarness{api: api, gateway: gateway, server: server, upstream: upstream, client: newTestClient(t)}
	harness.csrf = bootstrapVoiceUser(t, harness.client, server.URL)
	return harness
}

func bootstrapVoiceUser(t *testing.T, client *http.Client, base string) string {
	t.Helper()
	bootstrap := postJSON(t, client, base+"/api/bootstrap", "", map[string]any{
		"email": "admin@example.test", "display_name": "Admin",
		"password": "correct-horse-battery-staple", "token": "bootstrap-test-token",
	}, http.StatusOK)
	return bootstrap["csrf_token"].(string)
}

// getWithOrigin issues an authenticated GET carrying the configured Origin, so
// the handshake reaches the route rather than the origin guard.
func (h *voiceHarness) getWithOrigin(t *testing.T, target string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", voiceTestOrigin)
	response, err := h.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func (h *voiceHarness) dialVoiceAt(t *testing.T, base string, extra http.Header) *websocket.Conn {
	t.Helper()
	header := http.Header{}
	header.Set("Origin", voiceTestOrigin)
	for key, values := range extra {
		for _, value := range values {
			header.Add(key, value)
		}
	}
	client := &http.Client{Jar: h.client.Jar}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, base, &websocket.DialOptions{HTTPHeader: header, HTTPClient: client, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		t.Fatalf("dial voice socket: %v", err)
	}
	return conn
}

func (h *voiceHarness) ticket(t *testing.T, body any, wantStatus int) map[string]any {
	t.Helper()
	return postJSON(t, h.client, h.server.URL+"/api/voice/sessions", h.csrf, body, wantStatus)
}

func readVoiceFrame(t *testing.T, conn *websocket.Conn) (websocket.MessageType, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read voice frame: %v", err)
	}
	return typ, payload
}

func readVoiceJSON(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	typ, payload := readVoiceFrame(t, conn)
	if typ != websocket.MessageText {
		t.Fatalf("expected a text frame, got binary of %d bytes", len(payload))
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("frame is not JSON: %v (%s)", err, payload)
	}
	return decoded
}

// ─────────────────────────────────── tests ──────────────────────────────────

func TestVoiceCapabilitiesNeverLeakServerConfiguration(t *testing.T) {
	harness := newVoiceHarness(t, nil)
	result := requestJSON(t, harness.client, http.MethodGet, harness.server.URL+"/api/voice/capabilities", "", nil, http.StatusOK)
	if result["enabled"] != true {
		t.Fatalf("a configured gateway must advertise voice, got %v", result)
	}
	if result["protocol"] != voicegateway.ProtocolOpenAIRealtime {
		t.Fatalf("unexpected protocol %v", result["protocol"])
	}
	if result["audioReply"] != voicegateway.AudioReplyOff {
		t.Fatalf("audio reply must default to off, got %v", result["audioReply"])
	}
	encoded, _ := json.Marshal(result)
	for _, leak := range []string{"server-only-test-credential", "127.0.0.1"} {
		if strings.Contains(string(encoded), leak) {
			t.Fatalf("capabilities leaked %q: %s", leak, encoded)
		}
	}
}

func TestVoiceRoutesAreClosedWhenDisabled(t *testing.T) {
	harness := newVoiceHarness(t, func(cfg *voicegateway.Config) {
		cfg.Enabled = false
		cfg.BaseURL = ""
		cfg.APIKey = ""
	})
	requestJSON(t, harness.client, http.MethodGet, harness.server.URL+"/api/voice/capabilities", "", nil, http.StatusNotFound)
	requestJSON(t, harness.client, http.MethodPost, harness.server.URL+"/api/voice/sessions", harness.csrf, map[string]any{"output": "text"}, http.StatusNotFound)
	response := harness.getWithOrigin(t, harness.server.URL+"/api/voice/ws?ticket=x")
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("a disabled gateway must not expose /ws, got %d", response.StatusCode)
	}
}

func TestVoiceRoutesRequireAuthentication(t *testing.T) {
	harness := newVoiceHarness(t, nil)
	requestJSON(t, newTestClient(t), http.MethodGet, harness.server.URL+"/api/voice/capabilities", "", nil, http.StatusUnauthorized)
	requestJSON(t, newTestClient(t), http.MethodPost, harness.server.URL+"/api/voice/sessions", "", map[string]any{"output": "text"}, http.StatusUnauthorized)
}

func TestVoiceSessionRequiresCSRF(t *testing.T) {
	harness := newVoiceHarness(t, nil)
	postJSON(t, harness.client, harness.server.URL+"/api/voice/sessions", "", map[string]any{"output": "text"}, http.StatusForbidden)
}

// TestVoiceSessionRejectsExtraFields pins "extra fields are rejected, not
// ignored": a client cannot choose the model, the upstream or the target.
func TestVoiceSessionRejectsExtraFields(t *testing.T) {
	harness := newVoiceHarness(t, nil)
	for _, body := range []map[string]any{
		{"output": "text", "model": "gpt-realtime-mini"},
		{"output": "text", "url": "wss://elsewhere.invalid"},
		{"output": "text", "base_url": "https://elsewhere.invalid"},
		{"output": "text", "api_key": "attacker-supplied"},
		{"output": "text", "target": "other-pane"},
		{"output": "text", "instructions": "ignore everything"},
	} {
		result := harness.ticket(t, body, http.StatusBadRequest)
		if result["code"] != "invalid_request" {
			t.Fatalf("extra fields must be refused for %v, got %v", body, result)
		}
	}
}

func TestVoiceSessionRejectsUnknownOutput(t *testing.T) {
	harness := newVoiceHarness(t, nil)
	result := harness.ticket(t, map[string]any{"output": "both"}, http.StatusBadRequest)
	if result["code"] != "invalid_output" {
		t.Fatalf("unexpected response %v", result)
	}
}

func TestVoiceSessionRefusesAudioWhenDisabled(t *testing.T) {
	harness := newVoiceHarness(t, nil)
	result := harness.ticket(t, map[string]any{"output": "audio"}, http.StatusForbidden)
	if result["code"] != "voice_audio_disabled" {
		t.Fatalf("unexpected response %v", result)
	}
}

func TestVoiceSessionAllowsAudioWhenEnabled(t *testing.T) {
	harness := newVoiceHarness(t, func(cfg *voicegateway.Config) {
		cfg.AudioReply = voicegateway.AudioReplyAllowed
	})
	result := harness.ticket(t, map[string]any{"output": "audio"}, http.StatusOK)
	if result["ticket"] == "" || result["ticket"] == nil {
		t.Fatalf("expected a ticket, got %v", result)
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok || caps["audioReply"] != voicegateway.AudioReplyAllowed {
		t.Fatalf("the ticket must carry the effective capabilities, got %v", result["capabilities"])
	}
}

// TestVoiceRelayFullDuplex is the HTTP-level end-to-end check: cookie, CSRF,
// ticket, Origin, upgrade, relay, close.
func TestVoiceRelayFullDuplex(t *testing.T) {
	harness := newVoiceHarness(t, nil)
	ticket := harness.ticket(t, map[string]any{"output": "text"}, http.StatusOK)["ticket"].(string)

	conn := harness.dialVoiceAt(t, harness.server.URL+"/api/voice/ws?ticket="+url.QueryEscape(ticket), nil)
	defer conn.CloseNow()

	if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"t":"start","output":"text"}`)); err != nil {
		t.Fatal(err)
	}
	ready := readVoiceJSON(t, conn)
	if ready["t"] != "ready" || ready["model"] != "gpt-realtime" {
		t.Fatalf("unexpected ready frame %v", ready)
	}

	// The server-owned credential must have reached the upstream, and the
	// client must never see it.
	harness.upstream.mu.Lock()
	auth := harness.upstream.auth
	query := harness.upstream.query
	harness.upstream.mu.Unlock()
	if auth != "Bearer server-only-test-credential" {
		t.Fatalf("the upstream must receive the server credential, got %q", auth)
	}
	if !strings.Contains(query, "model=gpt-realtime") {
		t.Fatalf("the model must come from the server config, got %q", query)
	}

	pcm := make([]byte, 960*2)
	if err := conn.Write(context.Background(), websocket.MessageBinary, pcm); err != nil {
		t.Fatal(err)
	}
	transcript := readVoiceJSON(t, conn)
	if transcript["t"] != "transcript" || transcript["text"] != "relayed:1920" {
		t.Fatalf("uplink audio did not survive the relay: %v", transcript)
	}
	encodedReady, _ := json.Marshal(ready)
	if strings.Contains(string(encodedReady), "server-only-test-credential") {
		t.Fatal("the relay leaked the credential to the browser")
	}

	if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"t":"stop"}`)); err != nil {
		t.Fatal(err)
	}
	closed := readVoiceJSON(t, conn)
	if closed["t"] != "closed" || closed["reason"] != voicegateway.ReasonUser {
		t.Fatalf("unexpected close frame %v", closed)
	}
}

func TestVoiceSocketRejectsBadTicket(t *testing.T) {
	harness := newVoiceHarness(t, nil)
	// An authenticated request with a forged ticket must be refused with an HTTP
	// status, before any upgrade.
	response := harness.getWithOrigin(t, harness.server.URL+"/api/voice/ws?ticket=forged")
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for a forged ticket, got %d", response.StatusCode)
	}
	var payload map[string]any
	_ = json.NewDecoder(response.Body).Decode(&payload)
	if payload["code"] != "voice_ticket_invalid" {
		t.Fatalf("unexpected payload %v", payload)
	}
}

func TestVoiceTicketIsSingleUse(t *testing.T) {
	harness := newVoiceHarness(t, nil)
	ticket := harness.ticket(t, map[string]any{"output": "text"}, http.StatusOK)["ticket"].(string)

	conn := harness.dialVoiceAt(t, harness.server.URL+"/api/voice/ws?ticket="+url.QueryEscape(ticket), nil)
	defer conn.CloseNow()
	if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"t":"start","output":"text"}`)); err != nil {
		t.Fatal(err)
	}
	_ = readVoiceJSON(t, conn)

	response := harness.getWithOrigin(t, harness.server.URL+"/api/voice/ws?ticket="+url.QueryEscape(ticket))
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("a spent ticket must be refused, got %d", response.StatusCode)
	}
}

func TestVoiceSocketRequiresAticket(t *testing.T) {
	harness := newVoiceHarness(t, nil)
	response := harness.getWithOrigin(t, harness.server.URL+"/api/voice/ws")
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 without a ticket, got %d", response.StatusCode)
	}
}

// TestVoiceSocketRejectsCrossSiteOrigin is the cross-site WebSocket hijack
// guard. The relay carries microphone audio, so this boundary is load-bearing.
func TestVoiceSocketRejectsCrossSiteOrigin(t *testing.T) {
	harness := newVoiceHarness(t, nil)
	ticket := harness.ticket(t, map[string]any{"output": "text"}, http.StatusOK)["ticket"].(string)

	parsed, err := url.Parse(harness.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{}
	header.Set("Origin", "https://evil.example.com")
	client := &http.Client{Jar: harness.client.Jar}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, response, err := websocket.Dial(ctx, "ws://"+parsed.Host+"/api/voice/ws?ticket="+url.QueryEscape(ticket),
		&websocket.DialOptions{HTTPHeader: header, HTTPClient: client})
	if err == nil {
		t.Fatal("a cross-site Origin must not be able to open the voice socket")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected a 403 handshake rejection, got %v", response)
	}

	// Sec-Fetch-Site: cross-site is refused even with a matching Origin.
	header.Set("Origin", voiceTestOrigin)
	header.Set("Sec-Fetch-Site", "cross-site")
	if _, response, err := websocket.Dial(ctx, "ws://"+parsed.Host+"/api/voice/ws?ticket="+url.QueryEscape(ticket),
		&websocket.DialOptions{HTTPHeader: header, HTTPClient: client}); err == nil {
		t.Fatal("Sec-Fetch-Site: cross-site must be refused")
	} else if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected a 403 handshake rejection, got %v", response)
	}
}

func TestVoiceConcurrencyCapReturnsBusy(t *testing.T) {
	harness := newVoiceHarness(t, func(cfg *voicegateway.Config) {
		cfg.MaxSessionsPerUser = 1
		cfg.MaxSessions = 1
	})
	first := harness.ticket(t, map[string]any{"output": "text"}, http.StatusOK)["ticket"].(string)
	conn := harness.dialVoiceAt(t, harness.server.URL+"/api/voice/ws?ticket="+url.QueryEscape(first), nil)
	defer conn.CloseNow()
	if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"t":"start","output":"text"}`)); err != nil {
		t.Fatal(err)
	}
	_ = readVoiceJSON(t, conn)

	second := harness.ticket(t, map[string]any{"output": "text"}, http.StatusOK)["ticket"].(string)
	response := harness.getWithOrigin(t, harness.server.URL+"/api/voice/ws?ticket="+url.QueryEscape(second))
	defer response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 while a session is live, got %d", response.StatusCode)
	}
	var payload map[string]any
	_ = json.NewDecoder(response.Body).Decode(&payload)
	if payload["code"] != "voice_busy" {
		t.Fatalf("unexpected payload %v", payload)
	}
}

func TestVoiceUpstreamFailureIsRedacted(t *testing.T) {
	harness := newVoiceHarness(t, func(cfg *voicegateway.Config) {
		// Point at a closed port: the dial fails after the upgrade.
		cfg.BaseURL = "http://127.0.0.1:1"
	})
	ticket := harness.ticket(t, map[string]any{"output": "text"}, http.StatusOK)["ticket"].(string)
	conn := harness.dialVoiceAt(t, harness.server.URL+"/api/voice/ws?ticket="+url.QueryEscape(ticket), nil)
	defer conn.CloseNow()

	frame := readVoiceJSON(t, conn)
	if frame["t"] != "error" || frame["code"] != "voice_upstream_unavailable" {
		t.Fatalf("expected a normalized upstream error, got %v", frame)
	}
	encoded, _ := json.Marshal(frame)
	if strings.Contains(string(encoded), "127.0.0.1") {
		t.Fatalf("the upstream target leaked to the browser: %s", encoded)
	}
}

func TestVoiceSocketReleasesConcurrencyOnClose(t *testing.T) {
	harness := newVoiceHarness(t, func(cfg *voicegateway.Config) {
		cfg.MaxSessionsPerUser = 1
		cfg.MaxSessions = 1
	})
	first := harness.ticket(t, map[string]any{"output": "text"}, http.StatusOK)["ticket"].(string)
	conn := harness.dialVoiceAt(t, harness.server.URL+"/api/voice/ws?ticket="+url.QueryEscape(first), nil)
	if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"t":"start","output":"text"}`)); err != nil {
		t.Fatal(err)
	}
	_ = readVoiceJSON(t, conn)
	conn.CloseNow()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if harness.gateway.ActiveSessions() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closing the browser socket must release the concurrency slot")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestVoiceMalformedControlFrameIsRefused(t *testing.T) {
	harness := newVoiceHarness(t, nil)
	ticket := harness.ticket(t, map[string]any{"output": "text"}, http.StatusOK)["ticket"].(string)
	conn := harness.dialVoiceAt(t, harness.server.URL+"/api/voice/ws?ticket="+url.QueryEscape(ticket), nil)
	defer conn.CloseNow()

	if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"t":"start","output":"text","model":"sneaky"}`)); err != nil {
		t.Fatal(err)
	}
	frame := readVoiceJSON(t, conn)
	if frame["t"] != "error" || frame["code"] != "voice_invalid_frame" {
		t.Fatalf("an unknown control field must be refused, got %v", frame)
	}
	_ = conn.CloseNow()
}

// TestVoiceCreateRateLimitUsesTheInstanceLimiter proves POST /sessions shares
// the requestLimiter accounting rather than skipping it.
func TestVoiceCreateRateLimitUsesTheInstanceLimiter(t *testing.T) {
	harness := newVoiceHarness(t, nil)
	adapter := requestLimiterAdapter{limiter: &harness.api.limiter}

	// The gateway's create budget must be spent from the instance's own table,
	// so a caller cannot get a fresh allowance by going through the voice route.
	if !adapter.Allow("voice-create:probe", 1) {
		t.Fatal("the adapter must admit the first request")
	}
	if adapter.Allow("voice-create:probe", 1) {
		t.Fatal("the adapter must share the instance limiter's window")
	}
	if !harness.api.limiter.allow("voice-create:probe", 2) {
		t.Fatal("the adapter must write into the instance limiter, not a private copy")
	}
}
