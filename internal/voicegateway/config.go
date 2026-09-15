// Package voicegateway relays the browser microphone stream to an
// OpenAI-Realtime-compatible upstream over a server-owned WebSocket session.
//
// The browser can never hold the upstream credential: a native browser
// WebSocket cannot set an Authorization header, so the only workable topology
// is a same-origin relay that owns the credential server-side. This package is
// that relay's upstream half. It deliberately knows nothing about HTTP routes,
// cookies, panes or terminals — see internal/httpapi/voice*.go for the routes
// and docs/design/chat-media-contract.md for the frozen contract.
package voicegateway

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/riba2534/herdrx/internal/config"
)

// Protocol name reported to the browser. Only the OpenAI Realtime session
// transport is implemented; the "compatible duplex model" requirement means a
// compatible model *name* selected by server ENV, not a second protocol
// adapter. Do not add a second value without re-freezing the contract.
const ProtocolOpenAIRealtime = "openai-realtime"

// AudioReplyAllowed / AudioReplyOff are the only accepted values of
// HERDRX_VOICE_AUDIO_REPLY.
const (
	AudioReplyAllowed = "allowed"
	AudioReplyOff     = "off"
)

// Defaults from the frozen contract. Every one of them keeps the capability
// disabled until an operator explicitly configures an upstream.
const (
	defaultModel          = "gpt-realtime"
	defaultRealtimePath   = "/v1/realtime"
	defaultInputRate      = 24000
	defaultOutputRate     = 24000
	defaultMaxPerUser     = 1
	defaultMaxSessions    = 4
	defaultMaxSeconds     = 600
	defaultIdleSeconds    = 30
	defaultMaxAudioSecond = 120
	defaultMaxUplinkBytes = 8 << 20
	defaultMaxControlSize = 4096
	defaultMaxAudioFrame  = 64 << 10
	defaultCreatePerMin   = 10

	// TicketTTL bounds how long an issued one-time ticket stays usable.
	TicketTTL = 30
	// MaxOutstandingTickets is the per-user cap on unconsumed tickets.
	MaxOutstandingTickets = 3
)

// ServerInstructions is fixed server-side and is never client-supplied. It
// states plainly that this is a dictation/conversation assistant and does not
// represent, drive or observe any terminal agent.
const ServerInstructions = "你是 herdrx 的语音输入助手。你的职责只有两件：把用户说的话转写成文字，以及在需要时与用户对话。" +
	"你不代表任何终端 Agent，你看不到也控制不了任何终端或远程主机，你没有执行过也没有能力执行任何终端命令。" +
	"绝对不要声称自己已经执行、正在执行或将要执行任何终端操作，也不要描述终端输出。" +
	"回答保持简短。"

// TranscriptionModel is the upstream transcription model requested for the
// uplink. It is a constant rather than ENV because the frozen ENV list has no
// slot for it; see .local-notes/voice-gateway-handoff.md.
const TranscriptionModel = "whisper-1"

// Config is the fully resolved server-side voice configuration. Zero value is
// "disabled": nothing dials, nothing is exposed, and the capability route
// answers enabled:false so the UI never prompts for a microphone.
type Config struct {
	Enabled  bool
	BaseURL  string // normalized origin of the upstream gateway, e.g. https://gw.example.com
	APIKey   string // server-only credential; never logged, never sent to a browser
	Model    string
	Path     string // upstream realtime path; fixed, never client input
	InputHz  int
	OutputHz int

	AudioReply string // AudioReplyAllowed or AudioReplyOff

	MaxSessionsPerUser    int
	MaxSessions           int
	MaxSessionSeconds     int
	IdleSeconds           int
	MaxUplinkAudioSeconds int
	MaxUplinkBytes        int64
	MaxControlBytes       int
	MaxAudioFrameBytes    int
	CreatePerMinute       int
}

// LoadConfigFromEnv reads the HERDRX_VOICE_* environment into a Config.
//
// It never fails on absent values: an unconfigured deployment is a valid,
// fully disabled deployment. It *does* fail on a present-but-invalid value so a
// typo cannot silently look like "off".
func LoadConfigFromEnv() (Config, error) {
	cfg := Config{
		Enabled:               envBool("HERDRX_VOICE_ENABLED", false),
		BaseURL:               strings.TrimSpace(os.Getenv("HERDRX_VOICE_BASE_URL")),
		APIKey:                strings.TrimSpace(os.Getenv("HERDRX_VOICE_API_KEY")),
		Model:                 envString("HERDRX_VOICE_MODEL", defaultModel),
		Path:                  envString("HERDRX_VOICE_REALTIME_PATH", defaultRealtimePath),
		AudioReply:            envString("HERDRX_VOICE_AUDIO_REPLY", AudioReplyOff),
		MaxSessionsPerUser:    defaultMaxPerUser,
		MaxSessions:           defaultMaxSessions,
		MaxSessionSeconds:     defaultMaxSeconds,
		IdleSeconds:           defaultIdleSeconds,
		MaxUplinkAudioSeconds: defaultMaxAudioSecond,
		MaxUplinkBytes:        defaultMaxUplinkBytes,
		MaxControlBytes:       defaultMaxControlSize,
		MaxAudioFrameBytes:    defaultMaxAudioFrame,
		CreatePerMinute:       defaultCreatePerMin,
	}
	var err error
	if cfg.InputHz, err = envRate("HERDRX_VOICE_INPUT_SAMPLE_RATE", defaultInputRate); err != nil {
		return Config{}, err
	}
	if cfg.OutputHz, err = envRate("HERDRX_VOICE_OUTPUT_SAMPLE_RATE", defaultOutputRate); err != nil {
		return Config{}, err
	}
	for name, target := range map[string]*int{
		"HERDRX_VOICE_MAX_SESSIONS_PER_USER":     &cfg.MaxSessionsPerUser,
		"HERDRX_VOICE_MAX_SESSIONS":              &cfg.MaxSessions,
		"HERDRX_VOICE_MAX_SESSION_SECONDS":       &cfg.MaxSessionSeconds,
		"HERDRX_VOICE_IDLE_SECONDS":              &cfg.IdleSeconds,
		"HERDRX_VOICE_MAX_UPLINK_AUDIO_SECONDS":  &cfg.MaxUplinkAudioSeconds,
		"HERDRX_VOICE_MAX_CONTROL_BYTES":         &cfg.MaxControlBytes,
		"HERDRX_VOICE_MAX_AUDIO_FRAME_BYTES":     &cfg.MaxAudioFrameBytes,
		"HERDRX_VOICE_CREATE_PER_MINUTE":         &cfg.CreatePerMinute,
	} {
		value, err := envPositive(name)
		if err != nil {
			return Config{}, err
		}
		if value != nil {
			*target = *value
		}
	}
	if raw := strings.TrimSpace(os.Getenv("HERDRX_VOICE_MAX_UPLINK_BYTES")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			return Config{}, fmt.Errorf("HERDRX_VOICE_MAX_UPLINK_BYTES must be a positive integer")
		}
		cfg.MaxUplinkBytes = value
	}
	return cfg, cfg.Validate()
}

// Validate normalizes and range-checks a Config. It mutates the receiver so the
// returned config is always the canonical one.
func (c *Config) Validate() error {
	c.Model = strings.TrimSpace(c.Model)
	c.Path = strings.TrimSpace(c.Path)
	c.AudioReply = strings.TrimSpace(strings.ToLower(c.AudioReply))

	if c.Path != "" && !strings.HasPrefix(c.Path, "/") {
		return fmt.Errorf("HERDRX_VOICE_REALTIME_PATH must start with /")
	}
	if c.Path == "" {
		c.Path = defaultRealtimePath
	}
	if c.Model == "" {
		c.Model = defaultModel
	}
	switch c.AudioReply {
	case AudioReplyAllowed, AudioReplyOff:
	default:
		return fmt.Errorf("HERDRX_VOICE_AUDIO_REPLY must be %q or %q", AudioReplyAllowed, AudioReplyOff)
	}
	if c.InputHz == 0 {
		c.InputHz = defaultInputRate
	}
	if c.OutputHz == 0 {
		c.OutputHz = defaultOutputRate
	}
	if c.InputHz < 8000 || c.InputHz > 48000 || c.OutputHz < 8000 || c.OutputHz > 48000 {
		return fmt.Errorf("HERDRX_VOICE_INPUT_SAMPLE_RATE and HERDRX_VOICE_OUTPUT_SAMPLE_RATE must be 8000–48000")
	}
	applyPositive(&c.MaxSessionsPerUser, defaultMaxPerUser)
	applyPositive(&c.MaxSessions, defaultMaxSessions)
	applyPositive(&c.MaxSessionSeconds, defaultMaxSeconds)
	applyPositive(&c.IdleSeconds, defaultIdleSeconds)
	applyPositive(&c.MaxUplinkAudioSeconds, defaultMaxAudioSecond)
	applyPositive(&c.MaxControlBytes, defaultMaxControlSize)
	applyPositive(&c.MaxAudioFrameBytes, defaultMaxAudioFrame)
	applyPositive(&c.CreatePerMinute, defaultCreatePerMin)
	if c.MaxUplinkBytes <= 0 {
		c.MaxUplinkBytes = defaultMaxUplinkBytes
	}
	if c.MaxSessionsPerUser > c.MaxSessions {
		return fmt.Errorf("HERDRX_VOICE_MAX_SESSIONS_PER_USER must not exceed HERDRX_VOICE_MAX_SESSIONS")
	}
	if c.MaxControlBytes < 512 || c.MaxControlBytes > 1<<20 {
		return fmt.Errorf("HERDRX_VOICE_MAX_CONTROL_BYTES must be 512–1048576")
	}
	if c.MaxAudioFrameBytes < 1024 || c.MaxAudioFrameBytes > 1<<20 {
		return fmt.Errorf("HERDRX_VOICE_MAX_AUDIO_FRAME_BYTES must be 1024–1048576")
	}
	if c.BaseURL != "" {
		origin, err := config.NormalizeOrigin(c.BaseURL)
		if err != nil {
			return fmt.Errorf("HERDRX_VOICE_BASE_URL must be an exact http(s) origin without credentials, path, query or wildcard")
		}
		c.BaseURL = origin
	}
	if c.Enabled {
		// An enabled deployment missing any of the three is a misconfiguration,
		// not a silent "off": the operator asked for voice.
		if c.BaseURL == "" {
			return fmt.Errorf("HERDRX_VOICE_BASE_URL is required when HERDRX_VOICE_ENABLED is true")
		}
		if c.APIKey == "" {
			return fmt.Errorf("HERDRX_VOICE_API_KEY is required when HERDRX_VOICE_ENABLED is true")
		}
	}
	return nil
}

// Available reports whether the gateway can actually serve voice. Any missing
// ingredient disables the capability so the UI hides the microphone instead of
// prompting for a permission it cannot use.
func (c Config) Available() bool {
	return c.Enabled && c.BaseURL != "" && c.APIKey != "" && c.Model != ""
}

// UpstreamURL builds the upstream dial target. The model travels in the query
// because that is how the standalone session transport carries it; nothing
// here comes from the client.
func (c Config) UpstreamURL() string {
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return ""
	}
	switch base.Scheme {
	case "https":
		base.Scheme = "wss"
	default:
		base.Scheme = "ws"
	}
	base.Path = c.Path
	base.RawQuery = url.Values{"model": []string{c.Model}}.Encode()
	base.Fragment = ""
	base.User = nil
	return base.String()
}

// Redacted returns a copy safe to log: the credential is removed entirely.
func (c Config) Redacted() Config {
	c.APIKey = ""
	return c
}

func applyPositive(target *int, fallback int) {
	if *target <= 0 {
		*target = fallback
	}
}

func envString(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
}

func envPositive(name string) (*int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return nil, fmt.Errorf("%s must be a positive integer", name)
	}
	return &value, nil
}

func envRate(name string, fallback int) (int, error) {
	value, err := envPositive(name)
	if err != nil {
		return 0, err
	}
	if value == nil {
		return fallback, nil
	}
	return *value, nil
}
