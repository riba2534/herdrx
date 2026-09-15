package voicegateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"math"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveUpstreamProbe is an opt-in, one-shot capability probe against the real
// upstream gateway. It is skipped unless HERDRX_VOICE_LIVE_PROBE=1.
//
// Rules it follows, per the operator's authorization:
//   - exactly one session, one model, one attempt; no retry loop, no key swap;
//   - a short synthetic tone only — never a real recording;
//   - the credential and the gateway host are read from the environment and are
//     never printed, logged or written to disk;
//   - the report states event *names* and byte counts, never transcript text.
//
// Run: HERDRX_VOICE_LIVE_PROBE=1 go test -run TestLiveUpstreamProbe -v ./internal/voicegateway/
func TestLiveUpstreamProbe(t *testing.T) {
	if os.Getenv("HERDRX_VOICE_LIVE_PROBE") != "1" {
		t.Skip("set HERDRX_VOICE_LIVE_PROBE=1 to run the one-shot upstream probe")
	}
	base := strings.TrimSpace(os.Getenv("HERDRX_VOICE_LIVE_BASE_URL"))
	if base == "" {
		base = strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL"))
	}
	key := strings.TrimSpace(os.Getenv("HERDRX_VOICE_LIVE_API_KEY"))
	if key == "" {
		key = strings.TrimSpace(os.Getenv("ASTERGATE_API_KEY"))
	}
	if base == "" || key == "" {
		t.Skip("no upstream base URL or credential in the environment")
	}
	model := envString("HERDRX_VOICE_LIVE_MODEL", defaultModel)

	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		t.Fatalf("upstream base URL is not a URL")
	}
	scheme := "wss"
	if parsed.Scheme == "http" {
		scheme = "ws"
	}
	target := scheme + "://" + parsed.Host + defaultRealtimePath + "?model=" + url.QueryEscape(model)

	// A single hard budget for the whole probe. No retries anywhere.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	dialer := WSDialer{DialTimeout: 15 * time.Second}
	header := http.Header{"Authorization": []string{"Bearer " + key}}
	conn, err := dialer.Dial(ctx, target, header)
	if err != nil {
		// The error text may embed the host; report only the category.
		t.Fatalf("upstream dial failed: %s", sanitizeDialError(err))
	}
	defer conn.Close(1000, "probe done")
	conn.SetReadLimit(1 << 20)

	cfg := testConfig(t)
	cfg.InputHz, cfg.OutputHz = 24000, 24000
	cfg.AudioReply = AudioReplyAllowed

	events := map[string]int{}
	var firstError string
	transcriptSeen := false
	partialSeen := false
	sessionUpdated := false

	write := func(payload []byte) {
		writeCtx, writeCancel := context.WithTimeout(ctx, 10*time.Second)
		defer writeCancel()
		if err := conn.Write(writeCtx, FrameText, payload); err != nil {
			t.Logf("write failed: %s", sanitizeDialError(err))
		}
	}

	// 1. Configure the session including uplink transcription — the part the
	//    earlier investigation did not spend budget on.
	//
	//    Turn detection defaults to manual for the probe: with server VAD the
	//    server only transcribes a detected *speech* turn, so a synthetic tone
	//    produces no transcription events and the probe cannot tell "the
	//    upstream rejects transcription" from "the tone was not speech". With
	//    turn_detection disabled the explicit commit below forces the uplink
	//    buffer to be transcribed, which is the question being asked.
	update := SessionUpdate(cfg, "text")
	if envString("HERDRX_VOICE_LIVE_TURN_DETECTION", "manual") == "manual" {
		update = probeSessionUpdate(cfg)
	}
	write(update)

	// 2. A short synthetic tone: 0.6 s of 440 Hz mono PCM16 at 24 kHz. This is
	//    generated here, not recorded from anyone.
	pcm := sineTone(cfg.InputHz, 0.6, 440)

	deadline := time.Now().Add(25 * time.Second)
	go func() {
		// Give the server a moment to acknowledge the update, then push audio.
		time.Sleep(2 * time.Second)
		write(AppendAudio(pcm))
		write(commitRequest())
		write([]byte(`{"type":"response.create"}`))
	}()

	for time.Now().Before(deadline) {
		readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
		_, payload, err := conn.Read(readCtx)
		readCancel()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			continue
		}
		var event struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &event) != nil {
			continue
		}
		events[event.Type]++
		switch event.Type {
		case evSessionUpdated:
			sessionUpdated = true
		case evTransDelta:
			partialSeen = true
		case evTransCompleted:
			transcriptSeen = true
		case evError:
			if firstError == "" {
				firstError = sanitizeCode(event.Error.Code)
				if firstError == "" {
					firstError = "unnamed"
				}
			}
		}
		if sessionUpdated && (transcriptSeen || firstError != "") && len(events) > 3 {
			break
		}
	}

	// The report is deliberately value-free: event names, counts and booleans.
	t.Logf("upstream model=%s session_updated=%v transcription_delta=%v transcription_completed=%v first_error=%q",
		model, sessionUpdated, partialSeen, transcriptSeen, firstError)
	names := make([]string, 0, len(events))
	for name, count := range events {
		names = append(names, fmt.Sprintf("%s=%d", name, count))
	}
	t.Logf("observed events: %s", strings.Join(names, " "))

	if !sessionUpdated {
		t.Fatalf("upstream never acknowledged session.update; voice is not usable as configured")
	}
	if transcriptSeen {
		t.Logf("uplink transcription is confirmed for %s: the default transcribe-to-draft flow is supported", model)
		return
	}
	t.Logf("WARNING: uplink transcription produced no completed event for %s; treat transcribe-to-draft as unverified", model)
}

// probeSessionUpdate is SessionUpdate with turn detection disabled, so an
// explicit input_audio_buffer.commit is what drives transcription. It differs
// from production only in that one field and exists purely for the probe.
func probeSessionUpdate(cfg Config) []byte {
	var envelope map[string]any
	if json.Unmarshal(SessionUpdate(cfg, "text"), &envelope) != nil {
		return SessionUpdate(cfg, "text")
	}
	session, ok := envelope["session"].(map[string]any)
	if !ok {
		return SessionUpdate(cfg, "text")
	}
	audio, ok := session["audio"].(map[string]any)
	if !ok {
		return SessionUpdate(cfg, "text")
	}
	input, ok := audio["input"].(map[string]any)
	if !ok {
		return SessionUpdate(cfg, "text")
	}
	input["turn_detection"] = nil
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return SessionUpdate(cfg, "text")
	}
	return encoded
}

// sineTone builds a short synthetic PCM16LE mono tone. It is generated, never
// recorded: the probe must never carry anyone's voice.
func sineTone(rate int, seconds float64, hz float64) []byte {
	samples := int(seconds * float64(rate))
	pcm := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		value := int16(6000 * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
		pcm[i*2] = byte(value)
		pcm[i*2+1] = byte(value >> 8)
	}
	return pcm
}
