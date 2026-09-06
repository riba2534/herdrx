package config

import "testing"

func TestAuthConfigurationValidation(t *testing.T) {
	for _, pair := range [][2]string{
		{"HERDRX_PUBLIC_URL", "https://example.test/app"}, {"HERDRX_PUBLIC_URL", "https://user@example.test"},
		{"HERDRX_ALLOWED_ORIGINS", "*"}, {"HERDRX_ALLOWED_ORIGINS", "https://*.example.test"},
		{"HERDRX_TRUSTED_PROXIES", "0.0.0.0/0"}, {"HERDRX_TRUSTED_PROXIES", "::/0"}, {"HERDRX_TRUSTED_PROXIES", "proxy.example.test"},
		{"HERDRX_AUTH_HASH_CONCURRENCY", "0"}, {"HERDRX_AUTH_HASH_CONCURRENCY", "17"},
		{"HERDRX_SESSION_TTL", "-1s"},
		{"HERDRX_HOST_DIAL_CONCURRENCY", "0"}, {"HERDRX_HOST_DIAL_CONCURRENCY", "65"},
		{"HERDRX_MAX_HOST_CONNECTIONS", "201"}, {"HERDRX_MAX_HOST_CONNECTIONS", "invalid"},
		{"HERDRX_HOST_DIAL_TIMEOUT", "0s"}, {"HERDRX_HOST_DIAL_TIMEOUT", "3m"},
		{"HERDRX_HOST_IDLE_TIMEOUT", "31m"},
	} {
		t.Run(pair[0]+pair[1], func(t *testing.T) {
			t.Setenv(pair[0], pair[1])
			if _, err := Load(); err == nil {
				t.Fatal("invalid security configuration accepted")
			}
		})
	}
}

func TestCanonicalOriginsAndExplicitProxy(t *testing.T) {
	t.Setenv("HERDRX_PUBLIC_URL", "https://EXAMPLE.test:443/")
	t.Setenv("HERDRX_ALLOWED_ORIGINS", "http://localhost:5173,https://[::1]:443")
	t.Setenv("HERDRX_TRUSTED_PROXIES", "192.0.2.1,2001:db8::/64")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://example.test" || cfg.AllowedOrigins[1] != "https://[::1]" || cfg.TrustedProxies[0].String() != "192.0.2.1/32" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}
