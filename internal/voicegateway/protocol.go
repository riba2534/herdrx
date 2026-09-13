package voicegateway

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// Errors returned when a browser control frame does not match the frozen
// vocabulary. They are never surfaced verbatim: the route answers with a fixed
// Chinese message.
var (
	errInvalidControl = errors.New("voice control frame is not valid JSON")
	errUnknownField   = errors.New("voice control frame carries an unsupported field")
)

// Upstream OpenAI Realtime event names the relay understands. Anything not
// listed here is dropped and counted, never forwarded: the browser only sees
// the frozen frame vocabulary, so an upstream that invents a new frame cannot
// reach the client.
const (
	evSessionCreated  = "session.created"
	evSessionUpdated  = "session.updated"
	evTransDelta      = "conversation.item.input_audio_transcription.delta"
	evTransCompleted  = "conversation.item.input_audio_transcription.completed"
	evTransFailed     = "conversation.item.input_audio_transcription.failed"
	evTextDelta       = "response.output_text.delta"
	evTextDone        = "response.output_text.done"
	evAudioTransDelta = "response.output_audio_transcript.delta"
	evAudioTransDone  = "response.output_audio_transcript.done"
	evAudioDelta      = "response.output_audio.delta"
	evError           = "error"
)

// Client control frame types (browser -> herdrx).
const (
	clientStart  = "start"
	clientCommit = "commit"
	clientCancel = "cancel"
	clientStop   = "stop"
)

// Outbound is one frame the relay may forward to the browser. An empty slice
// from a mapping call means "counted and dropped".
type Outbound struct {
	Typ     FrameType
	Payload []byte
}

// ClientControl is the only shape a browser control frame may take. Unknown
// fields are rejected rather than ignored, so a client cannot smuggle a model,
// URL or credential into the session.
type ClientControl struct {
	T      string `json:"t"`
	Output string `json:"output,omitempty"`
}

func textFrame(value any) Outbound {
	encoded, err := json.Marshal(value)
	if err != nil {
		// Every value here is a fixed map or string; a marshal failure is a
		// programming error, and dropping is safer than emitting junk.
		return Outbound{Typ: FrameText, Payload: []byte(`{"t":"error","code":"voice_internal","message":"语音服务内部错误，请稍后重试"}`)}
	}
	return Outbound{Typ: FrameText, Payload: encoded}
}

// ParseClientControl decodes one browser control frame. It rejects unknown
// fields outright: the frozen contract says extra fields are refused, not
// ignored.
func ParseClientControl(payload []byte) (ClientControl, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return ClientControl{}, errInvalidControl
	}
	for key := range raw {
		switch key {
		case "t", "output":
		default:
			return ClientControl{}, errUnknownField
		}
	}
	var control ClientControl
	if err := json.Unmarshal(payload, &control); err != nil {
		return ClientControl{}, errInvalidControl
	}
	return control, nil
}

// SessionUpdate builds the upstream `session.update` for the chosen output
// mode. `session.type` is mandatory; omitting it makes the upstream answer
// missing_required_parameter. No OpenAI-Beta header is used anywhere.
func SessionUpdate(cfg Config, output string) []byte {
	modalities := []string{"text"}
	if output == "audio" && cfg.AudioReply == AudioReplyAllowed {
		modalities = []string{"audio"}
	}
	session := map[string]any{
		"type":              "realtime",
		"output_modalities": modalities,
		"instructions":      ServerInstructions,
		"audio": map[string]any{
			"input": map[string]any{
				"format": map[string]any{"type": "audio/pcm", "rate": cfg.InputHz},
				"transcription": map[string]any{
					"model": TranscriptionModel,
				},
				"turn_detection": map[string]any{
					"type":                "server_vad",
					"create_response":     true,
					"interrupt_response":  true,
					"silence_duration_ms": 500,
				},
			},
			"output": map[string]any{
				"format": map[string]any{"type": "audio/pcm", "rate": cfg.OutputHz},
				"voice":  "alloy",
			},
		},
	}
	encoded, _ := json.Marshal(map[string]any{"type": "session.update", "session": session})
	return encoded
}

// AppendAudio wraps one PCM16LE frame as `input_audio_buffer.append`. The
// base64 encoding lives here, not in the browser: the browser sends raw bytes.
func AppendAudio(pcm []byte) []byte {
	encoded, _ := json.Marshal(map[string]any{
		"type":  "input_audio_buffer.append",
		"audio": base64.StdEncoding.EncodeToString(pcm),
	})
	return encoded
}

func commitRequest() []byte { return []byte(`{"type":"input_audio_buffer.commit"}`) }
func cancelRequest() []byte { return []byte(`{"type":"response.cancel"}`) }
func clearRequest() []byte  { return []byte(`{"type":"input_audio_buffer.clear"}`) }

// upstreamEvent is the union of upstream fields the relay reads. It is
// deliberately narrow: unknown events are dropped, not reflected.
type upstreamEvent struct {
	Type       string `json:"type"`
	Delta      string `json:"delta"`
	Transcript string `json:"transcript"`
	Text       string `json:"text"`
	Audio      string `json:"audio"`
	Session    struct {
		Model string `json:"model"`
		Audio struct {
			Input struct {
				Format struct {
					Rate int `json:"rate"`
				} `json:"format"`
			} `json:"input"`
		} `json:"audio"`
	} `json:"session"`
	Error struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

var safeCode = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)

// relayState is the per-session bookkeeping the mapping needs.
type relayState struct {
	cfg          Config
	output       string
	readyEmitted bool
	model        string
	dropped      int
}

// MapUpstream converts one upstream frame into zero or more browser frames.
// The second result reports whether the frame was recognized at all.
func (s *relayState) MapUpstream(typ FrameType, payload []byte) ([]Outbound, bool) {
	if typ == FrameBinary {
		// The OpenAI Realtime wire is JSON text only; a binary upstream frame
		// is not part of the dialect and must not be forwarded raw.
		s.dropped++
		return nil, false
	}
	var event upstreamEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		s.dropped++
		return nil, false
	}
	switch event.Type {
	case evSessionCreated:
		if event.Session.Model != "" {
			s.model = event.Session.Model
		}
		// session.created alone is not readiness: the session is only
		// configured once the server acknowledges our update.
		return nil, true

	case evSessionUpdated:
		if s.readyEmitted {
			return nil, true
		}
		s.readyEmitted = true
		model := s.model
		if event.Session.Model != "" {
			model = event.Session.Model
		}
		if model == "" {
			model = s.cfg.Model
		}
		return []Outbound{textFrame(map[string]any{
			"t":                "ready",
			"protocol":         ProtocolOpenAIRealtime,
			"model":            model,
			"inputSampleRate":  s.cfg.InputHz,
			"outputSampleRate": s.cfg.OutputHz,
			"audioReply":       s.cfg.AudioReply == AudioReplyAllowed,
		})}, true

	case evTransDelta:
		if event.Delta == "" {
			return nil, true
		}
		return []Outbound{textFrame(map[string]any{"t": "partial", "text": event.Delta})}, true

	case evTransCompleted:
		// The only content that may ever be written to a draft.
		text := event.Transcript
		if text == "" {
			text = event.Text
		}
		return []Outbound{textFrame(map[string]any{"t": "transcript", "text": text, "final": true})}, true

	case evTransFailed:
		s.dropped++
		return []Outbound{textFrame(map[string]any{
			"t": "error", "code": "voice_transcription_failed",
			"message": "上游无法转写这段语音，请重试或改用文字输入",
		})}, true

	case evTextDelta, evAudioTransDelta:
		if event.Delta == "" {
			return nil, true
		}
		return []Outbound{textFrame(map[string]any{"t": "assistantText", "text": event.Delta, "final": false})}, true

	case evTextDone, evAudioTransDone:
		text := event.Text
		if text == "" {
			text = event.Transcript
		}
		return []Outbound{textFrame(map[string]any{"t": "assistantText", "text": text, "final": true})}, true

	case evAudioDelta:
		// OpenAI Realtime carries base64 PCM in delta, not audio.
		if event.Delta == "" {
			return nil, true
		}
		decoded, err := base64.StdEncoding.DecodeString(event.Delta)
		if err != nil || len(decoded) == 0 {
			s.dropped++
			return nil, false
		}
		return []Outbound{{Typ: FrameBinary, Payload: decoded}}, true

	case evError:
		s.dropped++
		return []Outbound{textFrame(normalizeUpstreamError(event.Error.Code, event.Error.Type))}, true
	}
	s.dropped++
	return nil, false
}

// normalizeUpstreamError converts an upstream failure into a fixed Chinese
// message plus a sanitized machine code. The upstream message body is never
// forwarded: it can name the account, the quota plan or the gateway.
func normalizeUpstreamError(code, kind string) map[string]any {
	reason := sanitizeCode(code)
	if reason == "" {
		reason = sanitizeCode(kind)
	}
	message := "语音上游暂时不可用，请稍后重试"
	switch reason {
	case "rate_limit_exceeded":
		message = "语音上游已达频率或配额上限，请稍后重试"
	case "insufficient_quota":
		message = "语音上游额度不足，请联系实例管理员"
	case "invalid_api_key", "authentication_error":
		message = "语音上游凭据无效，请联系实例管理员"
	case "model_not_found":
		message = "语音上游不支持当前配置的模型，请联系实例管理员"
	case "invalid_request_error":
		message = "语音上游拒绝了本次会话配置，请联系实例管理员"
	}
	frame := map[string]any{"t": "error", "code": "voice_upstream_error", "message": message}
	if reason != "" {
		frame["reason"] = reason
	}
	return frame
}

func sanitizeCode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if safeCode.MatchString(value) {
		return value
	}
	return ""
}

// Capabilities is the browser-facing capability payload. It contains no
// upstream address, credential or account identifier.
type Capabilities struct {
	Enabled           bool   `json:"enabled"`
	Protocol          string `json:"protocol"`
	Model             string `json:"model"`
	InputSampleRate   int    `json:"inputSampleRate"`
	OutputSampleRate  int    `json:"outputSampleRate"`
	AudioReply        string `json:"audioReply"`
	MaxSessionSeconds int    `json:"maxSessionSeconds"`
}

// Capabilities is the safe capability subset the browser is allowed to see.
func (c Config) Capabilities() Capabilities {
	return Capabilities{
		Enabled:           c.Available(),
		Protocol:          ProtocolOpenAIRealtime,
		Model:             c.Model,
		InputSampleRate:   c.InputHz,
		OutputSampleRate:  c.OutputHz,
		AudioReply:        c.AudioReply,
		MaxSessionSeconds: c.MaxSessionSeconds,
	}
}
