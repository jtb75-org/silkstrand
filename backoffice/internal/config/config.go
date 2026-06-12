package config

import (
	"encoding/hex"
	"fmt"
	"os"
)

type Config struct {
	Port                   string
	DatabaseURL            string
	JWTSecret              string
	EncryptionKey          []byte // 32 bytes for AES-256-GCM
	BootstrapAdminEmail    string // If set with password, create admin on first startup
	BootstrapAdminPassword string

	// Tenant auth (in-house, replacing Clerk).
	TenantJWTSecret string // HS256 secret shared with all DC APIs
	SMTPRelayAddr   string // host:port of the in-cluster SMTP relay (-> SES). Empty → noop mailer (logs to stdout)
	FromEmail       string // e.g. "SilkStrand <noreply@silkstrand.io>"
	TenantWebURL    string // Base URL of the tenant frontend for building invite / reset links
}

func Load() (*Config, error) {
	var encKey []byte
	if encKeyHex := getEnv("ENCRYPTION_KEY", ""); encKeyHex != "" {
		var err error
		encKey, err = hex.DecodeString(encKeyHex)
		if err != nil {
			return nil, fmt.Errorf("decoding ENCRYPTION_KEY: %w", err)
		}
		if len(encKey) != 32 {
			return nil, fmt.Errorf("ENCRYPTION_KEY must be 32 bytes (64 hex chars), got %d bytes", len(encKey))
		}
	}

	cfg := &Config{
		Port:                   getEnv("PORT", "8081"),
		DatabaseURL:            getEnv("DATABASE_URL", "postgres://silkstrand:localdev@localhost:15433/silkstrand_backoffice?sslmode=disable"),
		JWTSecret:              getEnv("JWT_SECRET", "dev-secret-change-in-production"),
		EncryptionKey:          encKey,
		BootstrapAdminEmail:    getEnv("BOOTSTRAP_ADMIN_EMAIL", ""),
		BootstrapAdminPassword: getEnv("BOOTSTRAP_ADMIN_PASSWORD", ""),
		TenantJWTSecret:        getEnv("TENANT_JWT_SECRET", "dev-secret-change-in-production"),
		SMTPRelayAddr:          getEnv("SMTP_RELAY_ADDR", ""),
		FromEmail:              getEnv("FROM_EMAIL", "SilkStrand <noreply@silkstrand.io>"),
		TenantWebURL:           getEnv("TENANT_WEB_URL", "http://localhost:5173"),
	}

	if getEnv("ENV", "dev") == "production" {
		if cfg.JWTSecret == "dev-secret-change-in-production" {
			return nil, fmt.Errorf("JWT_SECRET must be set in production")
		}
		if len(cfg.EncryptionKey) == 0 {
			return nil, fmt.Errorf("ENCRYPTION_KEY must be set in production")
		}
		if cfg.TenantJWTSecret == "dev-secret-change-in-production" {
			return nil, fmt.Errorf("TENANT_JWT_SECRET must be set in production")
		}
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
