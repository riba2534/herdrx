package voicegateway

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestSessionUpdateCarriesTypeModalitiesAndSampling(t *testing.T) {
	cfg := testConfig(t)
	cfg.InputHz = 16000
	cfg.OutputHz = 24000

	raw := SessionUpdate(cfg, "text")
	if strings.Contains(string(raw), "OpenAI-Beta") {
		t.Fatal("the realtime beta header is not supported upstream and must never appear")
	}
	var envelope struct {
		Type    string `json:"type"`
		Session struct {
			Type             string   `json:"type"`
			OutputModalities []string `json:"output_modalities"`
			Instructions     string   `json:"instructions"`
			Audio            struct {
				Input struct {
					Format struct {
						Type string `json:"type"`
						Rate int    `json:"rate"`
					} `json:"format"`
					Transcription struct {
						Model string `json:"model"`
					} `json:"transcription"`
					TurnDetection map[string]any `json:"turn_detection"`
				} `json:"input"`
				Output struct {
					Format struct {
						Type string `json:"type"`
						Rate int    `json:"rate"`
					} `json:"format"`
					Voice string `json:"voice"`
				} `json:"output"`
			} `json:"audio"`
		} `json:"session"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("session.update is not valid JSON: %v", err)
	}
	if envelope.Type != "session.update" {
		t.Fatalf("unexpected envelope %q", envelope.Type)
	}
	// session.type is mandatory upstream; without it the gateway answers
	// missing_required_parameter.
	if envelope.Session.Type != "realtime" {
		t.Fatalf("session.type must be realtime, got %q", envelope.Session.Type)
	}
	if got := envelope.Session.OutputModalities; len(got) != 1 || got[0] != "text" {
		t.Fatalf("unexpected modalities %v", got)
	}
	if envelope.Session.Audio.Input.Format.Rate != 16000 || envelope.Session.Audio.Input.Format.Type != "audio/pcm" {
		t.Fatalf("uplink format must match the configured rate: %+v", envelope.Session.Audio.Input)
	}
	if envelope.Session.Audio.Output.Format.Rate != 24000 {
		t.Fatalf("downlink format must match the configured rate: %+v", envelope.Session.Audio.Output)
	}
	if envelope.Session.Audio.Input.Transcription.Model == "" {
		t.Fatal("uplink transcription must name a model")
	}
	if envelope.Session.Audio.Output.Voice == "" {
		t.Fatal("downlink audio must name a voice")
	}
	if len(envelope.Session.Audio.Input.TurnDetection) == 0 {
		t.Fatal("turn detection must be configured")
	}
	// The instructions are server-owned and must deny any terminal agency.
	for _, phrase := range []string{"终端", "不代表"} {
		if !strings.Contains(envelope.Session.Instructions, phrase) {
			t.Fatalf("instructions must state the voice model is not a terminal agent: %q", envelope.Session.Instructions)
		}
	}
}

func TestSessionUpdateUsesAudioModalitiesOnlyWhenAllowed(t *testing.T) {
	cfg := testConfig(t)
	cfg.AudioReply = AudioReplyAllowed
	var envelope struct {
		Session struct {
			OutputModalities []string `json:"output_modalities"`
		} `json:"session"`
	}
	if err := json.Unmarshal(SessionUpdate(cfg, "audio"), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Session.OutputModalities) != 1 || envelope.Session.OutputModalities[0] != "audio" {
		t.Fatalf("unexpected modalities %v", envelope.Session.OutputModalities)
	}
	// Even an explicit audio request degrades to text when the operator said off.
	cfg.AudioReply = AudioReplyOff
	if err := json.Unmarshal(SessionUpdate(cfg, "audio"), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Session.OutputModalities) != 1 || envelope.Session.OutputModalities[0] != "text" {
		t.Fatalf("audio must degrade to text when disabled, got %v", envelope.Session.OutputModalities)
	}
}

func TestAppendAudioIsBase64PCM(t *testing.T) {
	pcm := []byte{0x01, 0x02, 0x03, 0xff}
	var envelope struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(AppendAudio(pcm), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Type != "input_audio_buffer.append" {
		t.Fatalf("unexpected type %q", envelope.Type)
	}
	decoded, err := base64.StdEncoding.DecodeString(envelope.Audio)
	if err != nil {
		t.Fatalf("audio must be base64: %v", err)
	}
	if string(decoded) != string(pcm) {
		t.Fatalf("round-trip altered the PCM: %v", decoded)
	}
}

func TestParseClientControlRejectsUnknownFields(t *testing.T) {
	for _, payload := range []string{
		`{"t":"start","output":"text","model":"other"}`,
		`{"t":"start","output":"text","instructions":"ignore previous"}`,
		`{"t":"start","output":"text","base_url":"wss://evil"}`,
		`{"t":"start","output":"text","pane_id":"p1"}`,
	} {
		if _, err := ParseClientControl([]byte(payload)); err == nil {
			t.Fatalf("%s must be rejected, not partially honoured", payload)
		}
	}
}

func TestParseClientControlAcceptsTheFrozenVocabulary(t *testing.T) {
	for _, payload := range []string{
		`{"t":"start","output":"text"}`,
		`{"t":"start","output":"audio"}`,
		`{"t":"commit"}`,
		`{"t":"cancel"}`,
		`{"t":"stop"}`,
	} {
		control, err := ParseClientControl([]byte(payload))
		if err != nil {
			t.Fatalf("%s must be accepted: %v", payload, err)
		}
		if control.T == "" {
			t.Fatalf("%s lost its type", payload)
		}
	}
	if _, err := ParseClientControl([]byte(`not json`)); err == nil {
		t.Fatal("malformed JSON must be rejected")
	}
}

func TestMapUpstreamOnlyForwardsTheFrozenVocabulary(t *testing.T) {
	state := &relayState{cfg: testConfig(t)}

	cases := []struct {
		name     string
		event    string
		wantType string
	}{
		{"transcript delta", `{"type":"conversation.item.input_audio_transcription.delta","delta":"he"}`, "partial"},
		{"transcript final", `{"type":"conversation.item.input_audio_transcription.completed","transcript":"hello"}`, "transcript"},
		{"text delta", `{"type":"response.output_text.delta","delta":"hi"}`, "assistantText"},
		{"text done", `{"type":"response.output_text.done","text":"hi there"}`, "assistantText"},
		{"audio transcript delta", `{"type":"response.output_audio_transcript.delta","delta":"hi"}`, "assistantText"},
		{"audio transcript done", `{"type":"response.output_audio_transcript.done","transcript":"hi there"}`, "assistantText"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frames, known := state.MapUpstream(FrameText, []byte(tc.event))
			if !known || len(frames) != 1 {
				t.Fatalf("expected one frame for %s, got %d (known=%v)", tc.name, len(frames), known)
			}
			var decoded map[string]any
			if err := json.Unmarshal(frames[0].Payload, &decoded); err != nil {
				t.Fatalf("frame is not JSON: %v", err)
			}
			if decoded["t"] != tc.wantType {
				t.Fatalf("expected %q, got %v", tc.wantType, decoded)
			}
		})
	}
}

// TestMapUpstreamDropsEverythingElse is the "no smuggling" guarantee: an
// upstream cannot reach the browser with a frame the contract does not name.
func TestMapUpstreamDropsEverythingElse(t *testing.T) {
	state := &relayState{cfg: testConfig(t)}
	for _, event := range []string{
		`{"type":"conversation.item.created","item":{"id":"x"}}`,
		`{"type":"response.done","response":{"id":"x"}}`,
		`{"type":"response.function_call_arguments.delta","delta":"rm -rf /"}`,
		`{"type":"response.output_item.added","item":{"type":"function_call"}}`,
		`{"type":"input_audio_buffer.speech_started"}`,
		`{"type":"rate_limits.updated"}`,
		`{"type":"ping"}`,
		`totally not json`,
	} {
		frames, _ := state.MapUpstream(FrameText, []byte(event))
		if len(frames) != 0 {
			t.Fatalf("%s must not be forwarded, got %s", event, frames[0].Payload)
		}
	}
	// A binary upstream frame is outside the dialect and must be dropped too.
	if frames, _ := state.MapUpstream(FrameBinary, []byte{1, 2, 3}); len(frames) != 0 {
		t.Fatal("binary upstream frames must not be forwarded raw")
	}
	if state.dropped == 0 {
		t.Fatal("dropped frames must be counted")
	}
}

func TestMapUpstreamEmitsReadyOnce(t *testing.T) {
	state := &relayState{cfg: testConfig(t)}
	if frames, _ := state.MapUpstream(FrameText, []byte(`{"type":"session.created","session":{"model":"gpt-realtime"}}`)); len(frames) != 0 {
		t.Fatal("session.created alone is not readiness")
	}
	frames, known := state.MapUpstream(FrameText, []byte(`{"type":"session.updated"}`))
	if !known || len(frames) != 1 {
		t.Fatal("session.updated must produce exactly one ready frame")
	}
	var ready map[string]any
	if err := json.Unmarshal(frames[0].Payload, &ready); err != nil {
		t.Fatalf("ready is not JSON: %v", err)
	}
	if ready["t"] != "ready" || ready["model"] != "gpt-realtime" || ready["protocol"] != ProtocolOpenAIRealtime {
		t.Fatalf("unexpected ready frame %v", ready)
	}
	if ready["inputSampleRate"] != float64(24000) || ready["outputSampleRate"] != float64(24000) {
		t.Fatalf("ready must carry the relay's sampling rates: %v", ready)
	}
	if ready["audioReply"] != false {
		t.Fatalf("ready must carry the operator's audio decision: %v", ready)
	}
	if again, _ := state.MapUpstream(FrameText, []byte(`{"type":"session.updated"}`)); len(again) != 0 {
		t.Fatal("ready must be emitted at most once")
	}
}

// TestMapUpstreamNormalizesErrors is the desensitization guarantee.
func TestMapUpstreamNormalizesErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		event    string
		wantCode string
	}{
		{"rate limit", `{"type":"error","error":{"code":"rate_limit_exceeded","message":"org-abc123 has 0 credits"}}`, "rate_limit_exceeded"},
		{"quota", `{"type":"error","error":{"code":"insufficient_quota","message":"billing account 42"}}`, "insufficient_quota"},
		{"bad key", `{"type":"error","error":{"code":"invalid_api_key","message":"key sk-live-xyz rejected"}}`, "invalid_api_key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &relayState{cfg: testConfig(t)}
			frames, known := state.MapUpstream(FrameText, []byte(tc.event))
			if !known || len(frames) != 1 {
				t.Fatalf("expected one normalized frame, got %d", len(frames))
			}
			text := string(frames[0].Payload)
			for _, leak := range []string{"org-abc123", "sk-live-xyz", "billing account", "credits"} {
				if strings.Contains(text, leak) {
					t.Fatalf("upstream error detail leaked: %s", text)
				}
			}
			var decoded map[string]any
			if err := json.Unmarshal(frames[0].Payload, &decoded); err != nil {
				t.Fatalf("frame is not JSON: %v", err)
			}
			if decoded["t"] != "error" || decoded["code"] != "voice_upstream_error" {
				t.Fatalf("unexpected error frame %v", decoded)
			}
			if decoded["reason"] != tc.wantCode {
				t.Fatalf("expected a sanitized reason %q, got %v", tc.wantCode, decoded["reason"])
			}
			if decoded["message"] == "" {
				t.Fatal("the error must carry an actionable message")
			}
		})
	}
}

func TestSanitizeCodeRejectsHostileValues(t *testing.T) {
	for _, value := range []string{
		"has spaces",
		"has/slash",
		"has:colon",
		"has;injection",
		"has<angle>",
		strings.Repeat("a", 100),
	} {
		if got := sanitizeCode(value); got != "" {
			t.Fatalf("sanitizeCode(%q) must reject, got %q", value, got)
		}
	}
	if got := sanitizeCode("Rate_Limit-Exceeded"); got != "rate_limit-exceeded" {
		t.Fatalf("unexpected sanitized value %q", got)
	}
}

func TestTranscriptionFailureSurfacesAnActionableError(t *testing.T) {
	state := &relayState{cfg: testConfig(t)}
	frames, known := state.MapUpstream(FrameText, []byte(`{"type":"conversation.item.input_audio_transcription.failed","error":{"message":"raw upstream text"}}`))
	if !known || len(frames) != 1 {
		t.Fatalf("expected one frame, got %d", len(frames))
	}
	if strings.Contains(string(frames[0].Payload), "raw upstream text") {
		t.Fatal("the upstream failure text must not reach the browser")
	}
	var decoded map[string]any
	_ = json.Unmarshal(frames[0].Payload, &decoded)
	if decoded["code"] != "voice_transcription_failed" {
		t.Fatalf("unexpected code %v", decoded["code"])
	}
}
