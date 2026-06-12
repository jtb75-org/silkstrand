package config

import (
	"fmt"
	"log/slog"
	"os"
)

// Config holds the agent configuration loaded from environment variables.
type Config struct {
	APIURL       string // env: SILKSTRAND_API_URL, default "ws://localhost:8080"
	AgentID      string // env: SILKSTRAND_AGENT_ID (post-bootstrap)
	AgentKey     string // env: SILKSTRAND_AGENT_KEY (post-bootstrap)
	BundleDir    string // env: SILKSTRAND_BUNDLE_DIR, default "./bundles"
	PublicKeyPath string // env: SILKSTRAND_PUBLIC_KEY, optional Ed25519 public key for bundle verification
	LogLevel     string // env: SILKSTRAND_LOG_LEVEL, default "info"

	// Self-bootstrap (container / k8s flow): when AgentID/AgentKey aren't set
	// but InstallToken is, the agent exchanges the token for credentials on
	// startup and persists them to CredsPath.
	InstallToken string // env: SILKSTRAND_INSTALL_TOKEN
	Name         string // env: SILKSTRAND_AGENT_NAME (optional; default = hostname)
	CredsPath    string // env: SILKSTRAND_CREDS_PATH, default "/var/lib/silkstrand/agent.creds"

	// CACertPath is an optional PEM file (path only, never inlined) appended to
	// the system trust store for outbound TLS — for TLS-inspecting corporate
	// proxies that re-sign with a private root CA (ADR 013 D3). Egress proxy is
	// taken from the standard HTTPS_PROXY/NO_PROXY env, not a config field.
	CACertPath string // env: SILKSTRAND_CA_CERT_PATH
}

// Load reads configuration from environment variables and validates required fields.
// When AgentID/AgentKey are missing, an InstallToken may take their place and the
// caller is expected to call BootstrapIfNeeded before using the config.
func Load() (*Config, error) {
	cfg := &Config{
		APIURL:        envOrDefault("SILKSTRAND_API_URL", "ws://localhost:8080"),
		AgentID:       os.Getenv("SILKSTRAND_AGENT_ID"),
		AgentKey:      os.Getenv("SILKSTRAND_AGENT_KEY"),
		BundleDir:     envOrDefault("SILKSTRAND_BUNDLE_DIR", "./bundles"),
		PublicKeyPath: os.Getenv("SILKSTRAND_PUBLIC_KEY"),
		LogLevel:      envOrDefault("SILKSTRAND_LOG_LEVEL", "info"),
		InstallToken:  os.Getenv("SILKSTRAND_INSTALL_TOKEN"),
		Name:          os.Getenv("SILKSTRAND_AGENT_NAME"),
		CredsPath:     envOrDefault("SILKSTRAND_CREDS_PATH", "/var/lib/silkstrand/agent.creds"),
		CACertPath:    os.Getenv("SILKSTRAND_CA_CERT_PATH"),
	}

	// ID and KEY must either both be set (pre-bootstrapped) or both empty
	// (bootstrap-or-load path). A half-filled pair is always an error.
	if (cfg.AgentID == "") != (cfg.AgentKey == "") {
		return nil, fmt.Errorf("SILKSTRAND_AGENT_ID and SILKSTRAND_AGENT_KEY must be set together")
	}
	// If neither credential is set, we need an install token for the
	// bootstrap flow (or a pre-existing creds file at CredsPath, but we
	// can't test for that here without racing).
	if cfg.AgentID == "" && cfg.InstallToken == "" {
		return nil, fmt.Errorf("one of SILKSTRAND_AGENT_ID+KEY or SILKSTRAND_INSTALL_TOKEN is required")
	}

	return cfg, nil
}

// RequireCreds asserts that AgentID + AgentKey are populated. Call after
// BootstrapIfNeeded.
func (c *Config) RequireCreds() error {
	if c.AgentID == "" {
		return fmt.Errorf("SILKSTRAND_AGENT_ID missing after bootstrap")
	}
	if c.AgentKey == "" {
		return fmt.Errorf("SILKSTRAND_AGENT_KEY missing after bootstrap")
	}
	return nil
}

// ParseLogLevel converts the string log level to a slog.Level.
func ParseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
