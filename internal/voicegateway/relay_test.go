package voicegateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRelayFullDuplex runs the frozen browser<->upstream vocabulary end to end
// against the in-process fake gateway.
func TestRelayFullDuplex(t *testing.T) {
	upstream := newScriptedUpstream()
	_, session, browser := openSession(t, testConfig(t), upstream)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go upstream.serve(ctx)

	relayDone := make(chan error, 1)
	go func() { relayDone <- session.Relay(ctx, browser) }()

	browser.push(FrameText, []byte(`{"t":"start","output":"text"}`))

	// 1. The upstream must receive a session.update that names session.type.
	update := upstream.conn.next(t)
	var decoded struct {
		Type    string `json:"type"`
		Session struct {
			Type             string   `json:"type"`
			OutputModalities []string `json:"output_modalities"`
		} `json:"session"`
	}
	if err := json.Unmarshal(update.payload, &decoded); err != nil {
		t.Fatalf("session.update is not JSON: %v", err)
	}
	if decoded.Type != "session.update" || decoded.Session.Type != "realtime" {
		t.Fatalf("session.update must carry session.type=realtime, got %s", update.payload)
	}
	if len(decoded.Session.OutputModalities) != 1 || decoded.Session.OutputModalities[0] != "text" {
		t.Fatalf("text mode must request output_modalities=[text], got %v", decoded.Session.OutputModalities)
	}

	// 2. The browser must be told the session is ready.
	ready := browser.nextJSON(t)
	if ready["t"] != "ready" || ready["model"] != "gpt-realtime" {
		t.Fatalf("expected a ready frame, got %v", ready)
	}
	if ready["audioReply"] != false {
		t.Fatalf("audioReply must reflect the server config, got %v", ready["audioReply"])
	}

	// 3. Uplink audio is base64-encoded for the upstream, never by the browser.
	pcm := make([]byte, 960*2)
	for i := range pcm {
		pcm[i] = byte(i % 251)
	}
	browser.push(FrameBinary, pcm)
	appendFrame := upstream.conn.next(t)
	var appended struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(appendFrame.payload, &appended); err != nil {
		t.Fatalf("append frame is not JSON: %v", err)
	}
	if appended.Type != "input_audio_buffer.append" {
		t.Fatalf("expected input_audio_buffer.append, got %q", appended.Type)
	}
	decodedAudio, err := base64.StdEncoding.DecodeString(appended.Audio)
	if err != nil || len(decodedAudio) != len(pcm) {
		t.Fatalf("uplink audio did not survive base64 round-trip: %v (%d bytes)", err, len(decodedAudio))
	}

	// 4. Only the user's own final transcript may be written to a draft.
	partial := browser.nextJSON(t)
	if partial["t"] != "partial" || partial["text"] != "hello" {
		t.Fatalf("expected a partial frame, got %v", partial)
	}
	final := browser.nextJSON(t)
	if final["t"] != "transcript" || final["text"] != "hello world" || final["final"] != true {
		t.Fatalf("expected a final transcript frame, got %v", final)
	}

	// 5. Stop ends the session, tells the browser why, and closes both legs.
	browser.push(FrameText, []byte(`{"t":"stop"}`))
	closed := browser.nextJSON(t)
	if closed["t"] != "closed" || closed["reason"] != ReasonUser {
		t.Fatalf("expected a user-reason closed frame, got %v", closed)
	}
	if code, _ := browser.awaitClose(t); code != 1000 {
		t.Fatalf("clean stop must close with 1000, got %d", code)
	}
	select {
	case err := <-relayDone:
		if err != nil {
			t.Fatalf("a user stop is not a failure: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not return after stop")
	}
	if _, ok := upstream.conn.awaitClosed(); !ok {
		t.Fatal("the upstream leg must be closed when the relay ends")
	}
}

// TestRelayRejectsFramesBeforeStart pins the ordered handshake.
func TestRelayRejectsFramesBeforeStart(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame fakeFrame
	}{
		{"audio", fakeFrame{typ: FrameBinary, payload: []byte{1, 2, 3, 4}}},
		{"commit", fakeFrame{typ: FrameText, payload: []byte(`{"t":"commit"}`)}},
		{"cancel", fakeFrame{typ: FrameText, payload: []byte(`{"t":"cancel"}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newScriptedUpstream()
			_, session, browser := openSession(t, testConfig(t), upstream)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- session.Relay(ctx, browser) }()

			browser.push(tc.frame.typ, tc.frame.payload)
			frame := browser.nextJSON(t)
			if frame["t"] != "error" || frame["code"] != "voice_start_required" {
				t.Fatalf("a pre-start frame must be refused, got %v", frame)
			}
			if code, _ := browser.awaitClose(t); code != 1000 {
				t.Fatalf("expected a normal close, got %d", code)
			}
			<-done
		})
	}
}

// TestRelayRejectsUnknownControlFields pins "extra fields are rejected, not
// ignored": a client cannot smuggle a model or a target into the session.
func TestRelayRejectsUnknownControlFields(t *testing.T) {
	for _, payload := range []string{
		`{"t":"start","output":"text","model":"gpt-realtime"}`,
		`{"t":"start","output":"text","url":"wss://elsewhere.invalid"}`,
		`{"t":"start","output":"text","api_key":"leak"}`,
		`{"t":"start","output":"text","target":"other-pane"}`,
	} {
		upstream := newScriptedUpstream()
		_, session, browser := openSession(t, testConfig(t), upstream)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- session.Relay(ctx, browser) }()
		browser.push(FrameText, []byte(payload))
		frame := browser.nextJSON(t)
		if frame["t"] != "error" || frame["code"] != "voice_invalid_frame" {
			t.Fatalf("extra fields must be refused for %s, got %v", payload, frame)
		}
		if _, ok := upstream.conn.awaitClosed(); !ok {
			t.Fatal("no upstream session may be configured for a rejected frame")
		}
		cancel()
		<-done
	}
}

// TestRelayRefusesAudioWithoutOperatorOptIn keeps audio output server-owned.
func TestRelayRefusesAudioWithoutOperatorOptIn(t *testing.T) {
	upstream := newScriptedUpstream()
	cfg := testConfig(t)
	cfg.AudioReply = AudioReplyOff
	_, session, browser := openSession(t, cfg, upstream)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Relay(ctx, browser) }()
	browser.push(FrameText, []byte(`{"t":"start","output":"audio"}`))
	frame := browser.nextJSON(t)
	if frame["t"] != "error" || frame["code"] != "voice_audio_disabled" {
		t.Fatalf("audio must stay off unless the operator allows it, got %v", frame)
	}
	<-done
}

// TestRelayAudioModeRequestsModalitiesWhenAllowed is the positive counterpart.
func TestRelayAudioModeRequestsModalitiesWhenAllowed(t *testing.T) {
	upstream := newScriptedUpstream()
	cfg := testConfig(t)
	cfg.AudioReply = AudioReplyAllowed
	_, session, browser := openSession(t, cfg, upstream)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go upstream.serve(ctx)
	done := make(chan error, 1)
	go func() { done <- session.Relay(ctx, browser) }()

	browser.push(FrameText, []byte(`{"t":"start","output":"audio"}`))
	update := upstream.conn.next(t)
	var decoded struct {
		Session struct {
			OutputModalities []string `json:"output_modalities"`
		} `json:"session"`
	}
	if err := json.Unmarshal(update.payload, &decoded); err != nil {
		t.Fatalf("session.update is not JSON: %v", err)
	}
	if len(decoded.Session.OutputModalities) != 1 || decoded.Session.OutputModalities[0] != "audio" {
		t.Fatalf("audio mode must request output_modalities=[audio], got %v", decoded.Session.OutputModalities)
	}
	ready := browser.nextJSON(t)
	if ready["t"] != "ready" || ready["audioReply"] != true {
		t.Fatalf("ready must advertise audio, got %v", ready)
	}
	browser.push(FrameText, []byte(`{"t":"stop"}`))
	<-done
}

// TestRelayUpstreamAudioBecomesBrowserBinary keeps the downstream path free of
// base64: the browser receives raw PCM.
func TestRelayUpstreamAudioBecomesBrowserBinary(t *testing.T) {
	upstream := newScriptedUpstream()
	cfg := testConfig(t)
	cfg.AudioReply = AudioReplyAllowed
	_, session, browser := openSession(t, cfg, upstream)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go upstream.serve(ctx)
	done := make(chan error, 1)
	go func() { done <- session.Relay(ctx, browser) }()
	browser.push(FrameText, []byte(`{"t":"start","output":"audio"}`))
	_ = upstream.conn.next(t)
	_ = browser.nextJSON(t)

	pcm := []byte{9, 8, 7, 6}
	upstream.conn.push(FrameText, []byte(`{"type":"response.output_audio.delta","delta":"`+base64.StdEncoding.EncodeToString(pcm)+`"}`))
	audio := browser.next(t)
	if audio.typ != FrameBinary {
		t.Fatalf("downstream audio must be a binary frame, got text %s", audio.payload)
	}
	if string(audio.payload) != string(pcm) {
		t.Fatalf("downstream PCM was altered: %v", audio.payload)
	}

	// The assistant's own words are a separate channel and never a transcript.
	upstream.conn.push(FrameText, []byte(`{"type":"response.output_text.delta","delta":"hi"}`))
	assistant := browser.nextJSON(t)
	if assistant["t"] != "assistantText" || assistant["final"] != false {
		t.Fatalf("assistant output must be its own channel, got %v", assistant)
	}
	browser.push(FrameText, []byte(`{"t":"stop"}`))
	<-done
}

// TestRelayNeverPassesThroughUnknownUpstreamFrames keeps an upstream that
// invents a frame from reaching the browser.
func TestRelayNeverPassesThroughUnknownUpstreamFrames(t *testing.T) {
	upstream := newScriptedUpstream()
	_, session, browser := openSession(t, testConfig(t), upstream)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go upstream.serve(ctx)
	done := make(chan error, 1)
	go func() { done <- session.Relay(ctx, browser) }()
	browser.push(FrameText, []byte(`{"t":"start","output":"text"}`))
	_ = upstream.conn.next(t)
	_ = browser.nextJSON(t)

	upstream.conn.push(FrameText, []byte(`{"type":"conversation.item.created","item":{"id":"secret-item"}}`))
	upstream.conn.push(FrameBinary, []byte{1, 2, 3})

	// The next thing the browser sees must be a deliberate control frame, not
	// the passthrough of either unknown frame.
	upstream.conn.push(FrameText, []byte(`{"type":"conversation.item.input_audio_transcription.completed","transcript":"visible"}`))
	frame := browser.nextJSON(t)
	if frame["t"] != "transcript" || frame["text"] != "visible" {
		t.Fatalf("unknown upstream frames leaked through: %v", frame)
	}
	browser.push(FrameText, []byte(`{"t":"stop"}`))
	<-done
}

// TestRelayNormalizesUpstreamErrors proves the upstream error body never
// reaches the browser.
func TestRelayNormalizesUpstreamErrors(t *testing.T) {
	upstream := newScriptedUpstream()
	upstream.reject = true
	_, session, browser := openSession(t, testConfig(t), upstream)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go upstream.serve(ctx)
	done := make(chan error, 1)
	go func() { done <- session.Relay(ctx, browser) }()
	browser.push(FrameText, []byte(`{"t":"start","output":"text"}`))

	frame := browser.nextJSON(t)
	if frame["t"] != "error" || frame["code"] != "voice_upstream_error" {
		t.Fatalf("expected a normalized upstream error, got %v", frame)
	}
	raw, _ := json.Marshal(frame)
	if strings.Contains(string(raw), "secret-account-detail-42") {
		t.Fatalf("upstream error text leaked to the browser: %s", raw)
	}
	browser.push(FrameText, []byte(`{"t":"stop"}`))
	<-done
}

// TestRelayEnforcesFrameCaps covers both per-frame size limits.
func TestRelayEnforcesFrameCaps(t *testing.T) {
	t.Run("control", func(t *testing.T) {
		upstream := newScriptedUpstream()
		cfg := testConfig(t)
		cfg.MaxControlBytes = 512
		_, session, browser := openSession(t, cfg, upstream)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- session.Relay(ctx, browser) }()
		oversize := make([]byte, 513)
		for i := range oversize {
			oversize[i] = 'a'
		}
		browser.push(FrameText, oversize)
		frame := browser.nextJSON(t)
		if frame["code"] != "voice_control_too_large" {
			t.Fatalf("expected a control cap error, got %v", frame)
		}
		<-done
	})

	t.Run("audio", func(t *testing.T) {
		upstream := newScriptedUpstream()
		cfg := testConfig(t)
		cfg.MaxAudioFrameBytes = 1024
		_, session, browser := openSession(t, cfg, upstream)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- session.Relay(ctx, browser) }()
		browser.push(FrameText, []byte(`{"t":"start","output":"text"}`))
		browser.push(FrameBinary, make([]byte, 1025))
		frame := browser.nextJSON(t)
		if frame["code"] != "voice_audio_frame_too_large" {
			t.Fatalf("expected an audio cap error, got %v", frame)
		}
		<-done
	})
}

// TestRelayEnforcesUplinkBudget covers the cumulative audio-second budget.
func TestRelayEnforcesUplinkBudget(t *testing.T) {
	upstream := newScriptedUpstream()
	cfg := testConfig(t)
	cfg.MaxUplinkAudioSeconds = 1
	_, session, browser := openSession(t, cfg, upstream)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Relay(ctx, browser) }()
	browser.push(FrameText, []byte(`{"t":"start","output":"text"}`))
	// 1 second at 24 kHz mono PCM16 is 48000 bytes; push 60000 to exceed it.
	browser.push(FrameBinary, make([]byte, 60000))
	frame := browser.nextJSON(t)
	if frame["code"] != "voice_uplink_exhausted" {
		t.Fatalf("expected an uplink budget error, got %v", frame)
	}
	<-done
}

// TestRelayEnforcesSessionDeadline covers the hard session duration cap.
func TestRelayEnforcesSessionDeadline(t *testing.T) {
	upstream := newScriptedUpstream()
	cfg := testConfig(t)
	cfg.MaxSessionSeconds = 1
	_, session, browser := openSession(t, cfg, upstream)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Relay(ctx, browser) }()
	browser.push(FrameText, []byte(`{"t":"start","output":"text"}`))
	frame := browser.nextJSON(t)
	if frame["t"] != "closed" || frame["reason"] != ReasonTimeout {
		t.Fatalf("expected a timeout close, got %v", frame)
	}
	if err := <-done; err != nil {
		t.Fatalf("a deadline is a normal end: %v", err)
	}
}

// TestRelayEnforcesIdleTimeout covers the two-way idle cap.
func TestRelayEnforcesIdleTimeout(t *testing.T) {
	upstream := newScriptedUpstream()
	cfg := testConfig(t)
	cfg.IdleSeconds = 1
	_, session, browser := openSession(t, cfg, upstream)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Relay(ctx, browser) }()
	browser.push(FrameText, []byte(`{"t":"start","output":"text"}`))
	frame := browser.nextJSON(t)
	if frame["t"] != "closed" || frame["reason"] != ReasonIdle {
		t.Fatalf("expected an idle close, got %v", frame)
	}
	<-done
}

// TestRelayStopIsIdempotentAndClean proves a repeated stop cannot panic or
// double-close.
func TestRelayStopIsIdempotentAndClean(t *testing.T) {
	upstream := newScriptedUpstream()
	gateway, session, browser := openSession(t, testConfig(t), upstream)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go upstream.serve(ctx)
	done := make(chan error, 1)
	go func() { done <- session.Relay(ctx, browser) }()
	browser.push(FrameText, []byte(`{"t":"start","output":"text"}`))
	_ = upstream.conn.next(t)
	_ = browser.nextJSON(t)
	browser.push(FrameText, []byte(`{"t":"stop"}`))
	<-done

	session.shutdown()
	session.shutdown()
	gateway.Close()

	if active := gateway.ActiveSessions(); active != 0 {
		t.Fatalf("concurrency must be released, %d still active", active)
	}
}

// TestRelayClassifierMapsAccessRevocation proves the HTTP layer can reuse the
// workbench vocabulary for login expiry.
func TestRelayClassifierMapsAccessRevocation(t *testing.T) {
	revoked := errors.New("Web login ended")
	upstream := newScriptedUpstream()
	_, session, browser := openSession(t, testConfig(t), upstream)
	session.SetClassifier(func(cause error) (int, string, bool) {
		if errors.Is(cause, revoked) {
			return 4401, "auth", true
		}
		return 0, "", false
	})

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	go upstream.serve(ctx)
	done := make(chan error, 1)
	go func() { done <- session.Relay(ctx, browser) }()
	browser.push(FrameText, []byte(`{"t":"start","output":"text"}`))
	_ = browser.nextJSON(t)
	cancel(revoked)

	frame := browser.nextJSON(t)
	if frame["t"] != "closed" || frame["reason"] != "auth" {
		t.Fatalf("expected an auth close, got %v", frame)
	}
	if code, _ := browser.awaitClose(t); code != 4401 {
		t.Fatalf("expected close code 4401, got %d", code)
	}
	<-done
}

// awaitClosed is the upstream-side counterpart of awaitClose without a
// testing.T, used where the assertion is the boolean itself.
func (c *fakeConn) awaitClosed() (int, bool) {
	select {
	case <-c.closed:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.closeCode, true
	case <-time.After(2 * time.Second):
		return 0, false
	}
}
