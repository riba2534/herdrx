package voicegateway

import (
	"strings"
	"testing"
)

func clearVoiceEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"HERDRX_VOICE_ENABLED", "HERDRX_VOICE_BASE_URL", "HERDRX_VOICE_API_KEY",
		"HERDRX_VOICE_MODEL", "HERDRX_VOICE_REALTIME_PATH", "HERDRX_VOICE_INPUT_SAMPLE_RATE",
		"HERDRX_VOICE_OUTPUT_SAMPLE_RATE", "HERDRX_VOICE_AUDIO_REPLY",
		"HERDRX_VOICE_MAX_SESSIONS_PER_USER", "HERDRX_VOICE_MAX_SESSIONS",
		"HERDRX_VOICE_MAX_SESSION_SECONDS", "HERDRX_VOICE_IDLE_SECONDS",
		"HERDRX_VOICE_MAX_UPLINK_AUDIO_SECONDS", "HERDRX_VOICE_MAX_UPLINK_BYTES",
		"HERDRX_VOICE_MAX_CONTROL_BYTES", "HERDRX_VOICE_MAX_AUDIO_FRAME_BYTES",
		"HERDRX_VOICE_CREATE_PER_MINUTE",
	} {
		t.Setenv(name, "")
	}
}

func TestLoadConfigFromEnvDefaultsToDisabled(t *testing.T) {
	clearVoiceEnv(t)
	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("an unconfigured deployment must be valid: %v", err)
	}
	if cfg.Available() {
		t.Fatal("an unconfigured deployment must not advertise voice")
	}
	caps := cfg.Capabilities()
	if caps.Enabled {
		t.Fatal("capabilities must report enabled:false so the UI never prompts for a microphone")
	}
	if caps.Protocol != ProtocolOpenAIRealtime {
		t.Fatalf("unexpected protocol %q", caps.Protocol)
	}
	if caps.AudioReply != AudioReplyOff {
		t.Fatalf("audio reply must default to off, got %q", caps.AudioReply)
	}
}

func TestLoadConfigFromEnvDisabledWhenAnyIngredientMissing(t *testing.T) {
	for _, missing := range []string{"HERDRX_VOICE_BASE_URL", "HERDRX_VOICE_API_KEY", "HERDRX_VOICE_MODEL"} {
		t.Run(missing, func(t *testing.T) {
			clearVoiceEnv(t)
			t.Setenv("HERDRX_VOICE_ENABLED", "true")
			t.Setenv("HERDRX_VOICE_BASE_URL", "https://voice.example.com")
			t.Setenv("HERDRX_VOICE_API_KEY", "placeholder-key")
			t.Setenv("HERDRX_VOICE_MODEL", "gpt-realtime")
			t.Setenv(missing, "")
			if missing == "HERDRX_VOICE_ENABLED" {
				t.Setenv("HERDRX_VOICE_ENABLED", "false")
			}
			// An enabled deployment missing an ingredient is a hard error, not
			// a silent "off".
			cfg, err := LoadConfigFromEnv()
			if missing == "HERDRX_VOICE_MODEL" {
				// Model falls back to the default, so it stays available.
				if err != nil || !cfg.Available() {
					t.Fatalf("model must fall back to a usable default: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s missing must be a configuration error", missing)
			}
		})
	}
}

func TestLoadConfigFromEnvRejectsInvalidValues(t *testing.T) {
	cases := map[string]string{
		"HERDRX_VOICE_INPUT_SAMPLE_RATE":     "not-a-number",
		"HERDRX_VOICE_MAX_SESSIONS":          "0",
		"HERDRX_VOICE_MAX_SESSIONS_PER_USER": "-3",
		"HERDRX_VOICE_MAX_UPLINK_BYTES":      "abc",
		"HERDRX_VOICE_MAX_CONTROL_BYTES":     "8",
		"HERDRX_VOICE_MAX_AUDIO_FRAME_BYTES": "99999999",
		"HERDRX_VOICE_AUDIO_REPLY":           "maybe",
		"HERDRX_VOICE_REALTIME_PATH":         "v1/realtime",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			clearVoiceEnv(t)
			t.Setenv(name, value)
			if _, err := LoadConfigFromEnv(); err == nil {
				t.Fatalf("%s=%q must be rejected instead of silently falling back", name, value)
			}
		})
	}
}

func TestLoadConfigFromEnvRejectsNonOriginBaseURL(t *testing.T) {
	for _, value := range []string{
		"https://voice.example.com/path",
		"https://voice.example.com?x=1",
		"https://user:pass@voice.example.com",
		"https://*.example.com",
		"not-a-url",
	} {
		clearVoiceEnv(t)
		t.Setenv("HERDRX_VOICE_ENABLED", "true")
		t.Setenv("HERDRX_VOICE_BASE_URL", value)
		t.Setenv("HERDRX_VOICE_API_KEY", "placeholder-key")
		if _, err := LoadConfigFromEnv(); err == nil {
			t.Fatalf("BASE_URL %q must be rejected", value)
		}
	}
}

func TestLoadConfigFromEnvRejectsPerUserAboveGlobal(t *testing.T) {
	clearVoiceEnv(t)
	t.Setenv("HERDRX_VOICE_MAX_SESSIONS_PER_USER", "8")
	t.Setenv("HERDRX_VOICE_MAX_SESSIONS", "2")
	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("a per-user cap above the global cap is nonsense and must be rejected")
	}
}

func TestUpstreamURLUsesWSSAndCarriesTheModelInTheQuery(t *testing.T) {
	cfg := testConfig(t)
	got := cfg.UpstreamURL()
	if !strings.HasPrefix(got, "wss://voice.example.com/v1/realtime?") {
		t.Fatalf("unexpected upstream URL %q", got)
	}
	if !strings.Contains(got, "model=gpt-realtime") {
		t.Fatalf("the model must travel in the query: %q", got)
	}
	// A plain-http base is only used in tests against a local fake.
	plain := cfg
	plain.BaseURL = "http://127.0.0.1:9"
	if !strings.HasPrefix(plain.UpstreamURL(), "ws://127.0.0.1:9/v1/realtime?") {
		t.Fatalf("an http base must map to ws://, got %q", plain.UpstreamURL())
	}
}

func TestConfigRedactedDropsTheCredential(t *testing.T) {
	cfg := testConfig(t)
	redacted := cfg.Redacted()
	if redacted.APIKey != "" {
		t.Fatal("the redacted config must not carry the credential")
	}
	if cfg.APIKey == "" {
		t.Fatal("the test config must actually have a credential to remove")
	}
}

func TestCapabilitiesNeverCarryTheGatewayOrCredential(t *testing.T) {
	cfg := testConfig(t)
	encoded := mustJSON(cfg.Capabilities())
	text := string(encoded)
	if strings.Contains(text, cfg.APIKey) || strings.Contains(text, "voice.example.com") {
		t.Fatalf("capabilities leaked server-side configuration: %s", text)
	}
	if !strings.Contains(text, `"enabled":true`) {
		t.Fatalf("a configured gateway must advertise enabled:true: %s", text)
	}
	if !strings.Contains(text, `"maxSessionSeconds"`) {
		t.Fatalf("the browser needs the session cap to show a countdown: %s", text)
	}
}
