package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port                    string
	DatabaseURL             string
	RedisURL                string
	JWTSecret               string
	InternalAPIKey          string
	CredentialEncryptionKey []byte   // 32 bytes for AES-256-GCM
	AllowedOrigins          []string // Allowed WebSocket origins (empty = allow all in dev)
	AgentReleasesURL        string   // Public S3/MinIO base URL for agent binaries + install.sh
	BundleStoragePath       string   // Local filesystem path for uploaded bundle tarballs (v1)
	BundleControlsDir       string   // Path to individual controls/ directory for server-side bundle assembly
	BundleGCSBucket         string   // GCS bucket for bundle tarballs (empty = local-only dev mode)
	BundlePublicBaseURL     string   // Public base URL for published bundle tarballs (homelab MinIO, e.g. https://downloads.silkstrand.io/bundles). Sets bundles.gcs_path when GCS is unset.
	PoliciesDir             string   // Path to builtin policies/ directory for copy-from-builtin
	PolicyDir               string   // Directory containing Rego policy files (ADR 011 D10)
	AuditEventsEnabled      bool     // ADR 005: enable audit event persistence (default true)
	DiscoveryChunkIPs       int      // IPs per discovery chunk; 0 = scheduler default (1024). DISCOVERY_CHUNK_IPS
}

func Load() (*Config, error) {
	var credKey []byte
	if credKeyHex := getEnv("CREDENTIAL_ENCRYPTION_KEY", ""); credKeyHex != "" {
		var err error
		credKey, err = hex.DecodeString(credKeyHex)
		if err != nil {
			return nil, fmt.Errorf("decoding CREDENTIAL_ENCRYPTION_KEY: %w", err)
		}
		if len(credKey) != 32 {
			return nil, fmt.Errorf("CREDENTIAL_ENCRYPTION_KEY must be 32 bytes (64 hex chars), got %d bytes", len(credKey))
		}
	}

	auditEnabled := getEnv("AUDIT_EVENTS_ENABLED", "true") != "false"

	cfg := &Config{
		Port:                    getEnv("PORT", "8080"),
		DatabaseURL:             getEnv("DATABASE_URL", "postgres://silkstrand:localdev@localhost:5432/silkstrand?sslmode=disable"),
		RedisURL:                getEnv("REDIS_URL", "redis://localhost:6379"),
		JWTSecret:               getEnv("JWT_SECRET", "dev-secret-change-in-production"),
		InternalAPIKey:          getEnv("INTERNAL_API_KEY", ""),
		CredentialEncryptionKey: credKey,
		AllowedOrigins:          parseOrigins(getEnv("ALLOWED_ORIGINS", "")),
		AgentReleasesURL:        getEnv("AGENT_RELEASES_URL", "https://downloads.silkstrand.io/agent"),
		BundleStoragePath:       getEnv("BUNDLE_STORAGE_PATH", ""),
		BundleControlsDir:       getEnv("BUNDLE_CONTROLS_DIR", "./controls"),
		BundleGCSBucket:         getEnv("BUNDLE_GCS_BUCKET", ""),
		BundlePublicBaseURL:     getEnv("BUNDLE_PUBLIC_BASE_URL", ""),
		PoliciesDir:             getEnv("POLICIES_DIR", "./policies"),
		PolicyDir:               getEnv("POLICY_DIR", "./policies"),
		AuditEventsEnabled:      auditEnabled,
		DiscoveryChunkIPs:       getEnvInt("DISCOVERY_CHUNK_IPS", 0),
	}

	if getEnv("ENV", "dev") == "production" {
		if cfg.JWTSecret == "dev-secret-change-in-production" {
			return nil, fmt.Errorf("JWT_SECRET must be set in production")
		}
		if len(cfg.CredentialEncryptionKey) == 0 {
			return nil, fmt.Errorf("CREDENTIAL_ENCRYPTION_KEY must be set in production")
		}
		if cfg.InternalAPIKey == "" {
			return nil, fmt.Errorf("INTERNAL_API_KEY must be set in production")
		}
	}

	return cfg, nil
}

func parseOrigins(s string) []string {
	if s == "" {
		return nil
	}
	origins := strings.Split(s, ",")
	for i := range origins {
		origins[i] = strings.TrimSpace(origins[i])
	}
	return origins
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
