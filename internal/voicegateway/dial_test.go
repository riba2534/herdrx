package voicegateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// localFakeGateway is a real WebSocket server that speaks the OpenAI Realtime
// words the relay maps. It exists so the production WSDialer, the production
// wsConn and the production relay all run over a real transport, not a channel
// fake — the same shape as the upstream herdrx will be pointed at.
type localFakeGateway struct {
	server *httptest.Server

	mu        sync.Mutex
	authSeen  string
	querySeen string
	betaSeen  string
	received  []string
}

func newLocalFakeGateway(t *testing.T) *localFakeGateway {
	t.Helper()
	fake := &localFakeGateway{}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

// wsURL rewrites the httptest http URL into a ws URL, the way the gateway's own
// base URL rewriting does.
func (f *localFakeGateway) wsURL() string {
	return "ws" + strings.TrimPrefix(f.server.URL, "http") + "/v1/realtime"
}

func (f *localFakeGateway) serve(writer http.ResponseWriter, request *http.Request) {
	// Capture before the handshake completes, so a dialer returning from Dial
	// always observes a settled record.
	f.mu.Lock()
	f.authSeen = request.Header.Get("Authorization")
	f.querySeen = request.URL.RawQuery
	f.betaSeen = request.Header.Get("OpenAI-Beta")
	f.mu.Unlock()

	conn, err := websocket.Accept(writer, request, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	ctx := request.Context()
	// Announce the session before any client frame, exactly as the upstream does.
	_ = writeJSONFrame(ctx, conn, map[string]any{"type": "session.created", "session": map[string]any{"model": "gpt-realtime"}})

	for {
		typ, payload, err := conn.Read(ctx)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.received = append(f.received, string(payload))
		f.mu.Unlock()

		if typ != websocket.MessageText {
			continue
		}
		var envelope struct {
			Type  string `json:"type"`
			Audio string `json:"audio"`
		}
		if json.Unmarshal(payload, &envelope) != nil {
			continue
		}
		switch envelope.Type {
		case "session.update":
			_ = writeJSONFrame(ctx, conn, map[string]any{"type": "session.updated"})
		case "input_audio_buffer.append":
			decoded, _ := base64.StdEncoding.DecodeString(envelope.Audio)
			_ = writeJSONFrame(ctx, conn, map[string]any{
				"type": "conversation.item.input_audio_transcription.completed",
				"transcript": "bytes:" + itoa(len(decoded)),
			})
		case "response.create":
			_ = writeJSONFrame(ctx, conn, map[string]any{"type": "response.output_text.done", "text": "ok"})
		}
	}
}

func writeJSONFrame(ctx context.Context, conn *websocket.Conn, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, encoded)
}

func itoa(value int) string {
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

// TestWSDialerAgainstLocalFakeGateway is the "local fake gateway, full duplex
// protocol" check: real WebSocket both ways, real base64 uplink, real
// normalized downlink.
func TestWSDialerAgainstLocalFakeGateway(t *testing.T) {
	fake := newLocalFakeGateway(t)
	cfg := testConfig(t)
	cfg.BaseURL = "http://" + strings.TrimPrefix(fake.server.URL, "http://")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	gateway, err := NewWithOptions(Options{
		Config: cfg,
		Dialer: WSDialer{DialTimeout: 5 * time.Second},
		Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	defer gateway.Close()

	ticket, err := gateway.IssueTicket("user-1", "login-1")
	if err != nil {
		t.Fatalf("issue ticket: %v", err)
	}
	session, err := gateway.Open(context.Background(), "user-1", "login-1", ticket.Value)
	if err != nil {
		t.Fatalf("open against the local fake gateway: %v", err)
	}

	fake.mu.Lock()
	if fake.authSeen != "Bearer "+cfg.APIKey {
		fake.mu.Unlock()
		t.Fatal("the upstream must receive the server-owned Bearer credential")
	}
	if !strings.Contains(fake.querySeen, "model=gpt-realtime") {
		fake.mu.Unlock()
		t.Fatalf("the model must travel in the query, saw %q", fake.querySeen)
	}
	if fake.betaSeen != "" {
		fake.mu.Unlock()
		t.Fatalf("the realtime beta header is unsupported and must not be sent, saw %q", fake.betaSeen)
	}
	fake.mu.Unlock()

	browser := newFakeConn()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Relay(ctx, browser) }()

	browser.push(FrameText, []byte(`{"t":"start","output":"text"}`))
	ready := browser.nextJSON(t)
	if ready["t"] != "ready" || ready["model"] != "gpt-realtime" {
		t.Fatalf("unexpected ready frame %v", ready)
	}

	// 960 samples at 24 kHz mono PCM16 = 40 ms.
	pcm := make([]byte, 960*2)
	browser.push(FrameBinary, pcm)
	transcript := browser.nextJSON(t)
	if transcript["t"] != "transcript" || transcript["text"] != "bytes:1920" {
		t.Fatalf("uplink audio did not arrive intact, got %v", transcript)
	}

	browser.push(FrameText, []byte(`{"t":"stop"}`))
	if closed := browser.nextJSON(t); closed["t"] != "closed" {
		t.Fatalf("expected a closed frame, got %v", closed)
	}
	if err := <-done; err != nil {
		t.Fatalf("relay returned an error on a clean stop: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.received) == 0 {
		t.Fatal("the fake gateway received nothing")
	}
	var first struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(fake.received[0]), &first) != nil || first.Type != "session.update" {
		t.Fatalf("the first upstream frame must be session.update, got %q", fake.received[0])
	}
	sawAppend := false
	for _, frame := range fake.received {
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(frame), &envelope) != nil || envelope.Type != "input_audio_buffer.append" {
			continue
		}
		sawAppend = true
		// The uplink must be base64 text; raw NUL bytes would mean the
		// browser's PCM was spliced into JSON unencoded.
		if strings.ContainsRune(frame, '\x00') {
			t.Fatal("the uplink frame carried raw PCM instead of base64")
		}
	}
	if !sawAppend {
		t.Fatal("no uplink audio reached the fake gateway")
	}
}

// TestWSDialerSurfacesATransportFailure proves an unreachable upstream is a
// clean, redacted failure rather than a hang.
func TestWSDialerSurfacesATransportFailure(t *testing.T) {
	cfg := testConfig(t)
	cfg.BaseURL = "http://127.0.0.1:1"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	gateway, err := NewWithOptions(Options{
		Config: cfg,
		Dialer: WSDialer{DialTimeout: 2 * time.Second},
		Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	defer gateway.Close()

	ticket, _ := gateway.IssueTicket("user-1", "login-1")
	_, openErr := gateway.Open(context.Background(), "user-1", "login-1", ticket.Value)
	if openErr == nil {
		t.Fatal("an unreachable upstream must fail")
	}
	if strings.Contains(openErr.Error(), "127.0.0.1:1") || strings.Contains(openErr.Error(), cfg.APIKey) {
		t.Fatalf("the failure must not carry the target or the credential: %v", openErr)
	}
}
