package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr                string
	DataDir             string
	PublicURL           string
	MasterKey           string
	BootstrapToken      string
	HerdrBinary         string
	CookieSecure        bool
	SessionTTL          time.Duration
	HostIdleTimeout     time.Duration
	HostDialTimeout     time.Duration
	HostDialConcurrency int
	MaxHostConnections  int
	AllowedOrigins      []string
	AllowPrivateHosts   bool
	DERPHost            string
	DERPRegionID        int
	TrustedProxies      []netip.Prefix
	AuthHashConcurrency int
}

func Load() (Config, error) {
	dataDir := env("HERDRX_DATA_DIR", "./data")
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve data directory: %w", err)
	}

	cfg := Config{
		Addr:                env("HERDRX_ADDR", "127.0.0.1:8080"),
		DataDir:             abs,
		PublicURL:           strings.TrimRight(env("HERDRX_PUBLIC_URL", "http://127.0.0.1:8080"), "/"),
		MasterKey:           os.Getenv("HERDRX_MASTER_KEY"),
		BootstrapToken:      os.Getenv("HERDRX_BOOTSTRAP_TOKEN"),
		HerdrBinary:         env("HERDRX_HERDR_BIN", "herdr"),
		CookieSecure:        envBool("HERDRX_COOKIE_SECURE", false),
		SessionTTL:          30 * 24 * time.Hour,
		HostIdleTimeout:     2 * time.Minute,
		HostDialTimeout:     30 * time.Second,
		HostDialConcurrency: 4,
		MaxHostConnections:  20,
		AllowedOrigins:      csv(os.Getenv("HERDRX_ALLOWED_ORIGINS")),
		AllowPrivateHosts:   envBool("HERDRX_ALLOW_PRIVATE_HOSTS", true),
		DERPHost:            strings.TrimSpace(os.Getenv("HERDRX_DERP_HOST")),
		DERPRegionID:        envInt("HERDRX_DERP_REGION_ID", 304),
		AuthHashConcurrency: 2,
	}
	for name, target := range map[string]*int{
		"HERDRX_HOST_DIAL_CONCURRENCY": &cfg.HostDialConcurrency,
		"HERDRX_MAX_HOST_CONNECTIONS":  &cfg.MaxHostConnections,
	} {
		if raw := os.Getenv(name); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				return Config{}, fmt.Errorf("%s must be an integer", name)
			}
			*target = value
			if value <= 0 {
				return Config{}, fmt.Errorf("%s must be positive", name)
			}
		}
	}
	for name, target := range map[string]*time.Duration{
		"HERDRX_HOST_DIAL_TIMEOUT": &cfg.HostDialTimeout,
		"HERDRX_HOST_IDLE_TIMEOUT": &cfg.HostIdleTimeout,
	} {
		if raw := os.Getenv(name); raw != "" {
			value, err := time.ParseDuration(raw)
			if err != nil || value <= 0 {
				return Config{}, fmt.Errorf("%s must be a positive duration", name)
			}
			*target = value
		}
	}
	if raw := os.Getenv("HERDRX_AUTH_HASH_CONCURRENCY"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 16 {
			return Config{}, fmt.Errorf("HERDRX_AUTH_HASH_CONCURRENCY must be between 1 and 16")
		}
		cfg.AuthHashConcurrency = value
	}
	if raw := os.Getenv("HERDRX_SESSION_TTL"); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value <= 0 {
			return Config{}, fmt.Errorf("HERDRX_SESSION_TTL must be a positive duration")
		}
		cfg.SessionTTL = value
	}
	for _, raw := range csv(os.Getenv("HERDRX_TRUSTED_PROXIES")) {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			addr, addrErr := netip.ParseAddr(raw)
			if addrErr != nil {
				return Config{}, fmt.Errorf("HERDRX_TRUSTED_PROXIES contains an invalid IP or CIDR")
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		if prefix.Bits() == 0 {
			return Config{}, fmt.Errorf("HERDRX_TRUSTED_PROXIES must not trust every address")
		}
		cfg.TrustedProxies = append(cfg.TrustedProxies, prefix.Masked())
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg *Config) Validate() error {
	origin, err := NormalizeOrigin(cfg.PublicURL)
	if err != nil {
		return fmt.Errorf("HERDRX_PUBLIC_URL: %w", err)
	}
	cfg.PublicURL = origin
	cfg.AllowedOrigins = append([]string(nil), cfg.AllowedOrigins...)
	for _, prefix := range cfg.TrustedProxies {
		if !prefix.IsValid() || prefix.Bits() == 0 {
			return fmt.Errorf("invalid trusted proxy prefix")
		}
	}
	for i, raw := range cfg.AllowedOrigins {
		origin, err := NormalizeOrigin(raw)
		if err != nil {
			return fmt.Errorf("HERDRX_ALLOWED_ORIGINS: %w", err)
		}
		cfg.AllowedOrigins[i] = origin
	}
	if cfg.AuthHashConcurrency == 0 {
		cfg.AuthHashConcurrency = 2
	}
	if cfg.AuthHashConcurrency < 1 || cfg.AuthHashConcurrency > 16 {
		return fmt.Errorf("invalid password hashing concurrency")
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 30 * 24 * time.Hour
	}
	if cfg.SessionTTL < 0 {
		return fmt.Errorf("session lifetime must be positive")
	}
	if cfg.HostDialConcurrency == 0 {
		cfg.HostDialConcurrency = 4
	}
	if cfg.MaxHostConnections == 0 {
		cfg.MaxHostConnections = 20
	}
	if cfg.HostDialTimeout == 0 {
		cfg.HostDialTimeout = 30 * time.Second
	}
	if cfg.HostIdleTimeout == 0 {
		cfg.HostIdleTimeout = 2 * time.Minute
	}
	if cfg.HostDialConcurrency < 1 || cfg.HostDialConcurrency > 64 || cfg.MaxHostConnections < 1 || cfg.MaxHostConnections > 200 {
		return fmt.Errorf("host dial concurrency must be 1–64 and maximum connections 1–200")
	}
	if cfg.HostDialTimeout < time.Second || cfg.HostDialTimeout > 2*time.Minute || cfg.HostIdleTimeout < time.Second || cfg.HostIdleTimeout > 30*time.Minute {
		return fmt.Errorf("host dial timeout must be 1s–2m and idle timeout 1s–30m")
	}
	return nil
}

// NormalizeOrigin accepts an exact HTTP(S) origin, never a wildcard or URL path.
func NormalizeOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(u.Host, "*") {
		return "", fmt.Errorf("expected an exact http(s) origin without credentials, path, query or wildcard")
	}
	host := strings.ToLower(u.Host)
	if (u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443") {
		host = strings.ToLower(u.Hostname())
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	return u.Scheme + "://" + host, nil
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func csv(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, strings.TrimRight(item, "/"))
		}
	}
	return result
}
