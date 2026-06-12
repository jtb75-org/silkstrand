// Package bootstrap exchanges a one-time install token for long-lived
// agent credentials (agent_id + api_key) via the DC's public bootstrap
// endpoint. Credentials are persisted to disk so a restart doesn't need
// the install token again.
package bootstrap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jtb75/silkstrand/agent/internal/config"
	"github.com/jtb75/silkstrand/agent/internal/nettls"
)

type storedCreds struct {
	AgentID  string `json:"agent_id"`
	AgentKey string `json:"agent_key"`
}

// EnsureCreds hydrates cfg.AgentID + cfg.AgentKey. Preference order:
//   1. Explicit env vars (SILKSTRAND_AGENT_ID + _KEY) — already on cfg.
//   2. Previously-persisted credentials at cfg.CredsPath.
//   3. Exchange cfg.InstallToken for fresh credentials; persist to disk.
//
// version is the agent binary's version string; passed through to the
// bootstrap request so the DC records it from the start (before the first
// heartbeat).
func EnsureCreds(cfg *config.Config, version string) error {
	if cfg.AgentID != "" && cfg.AgentKey != "" {
		return nil
	}
	if creds, err := readCreds(cfg.CredsPath); err == nil && creds.AgentID != "" {
		cfg.AgentID = creds.AgentID
		cfg.AgentKey = creds.AgentKey
		slog.Info("loaded persisted agent credentials", "path", cfg.CredsPath)
		return nil
	}
	if cfg.InstallToken == "" {
		return fmt.Errorf("no credentials and no install token available")
	}
	return bootstrapViaToken(cfg, version)
}

func bootstrapViaToken(cfg *config.Config, version string) error {
	name := cfg.Name
	if name == "" {
		name, _ = os.Hostname()
		if name == "" {
			name = "silkstrand-agent"
		}
	}

	httpBase := wsToHTTP(cfg.APIURL)
	url := strings.TrimRight(httpBase, "/") + "/api/v1/agents/bootstrap"
	body, _ := json.Marshal(map[string]string{
		"install_token": cfg.InstallToken,
		"name":          name,
		"version":       version,
	})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building bootstrap request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := nettls.Client(20 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("bootstrap request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("bootstrap returned %d: %s", resp.StatusCode, string(b))
	}

	var out struct {
		AgentID string `json:"agent_id"`
		APIKey  string `json:"api_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("decoding bootstrap response: %w", err)
	}
	if out.AgentID == "" || out.APIKey == "" {
		return fmt.Errorf("bootstrap response missing agent_id or api_key")
	}

	cfg.AgentID = out.AgentID
	cfg.AgentKey = out.APIKey

	if err := writeCreds(cfg.CredsPath, storedCreds{AgentID: out.AgentID, AgentKey: out.APIKey}); err != nil {
		// Non-fatal — we have credentials in memory. Subsequent restarts
		// without the install token will fail; log so the operator knows.
		slog.Warn("persisting agent credentials failed", "path", cfg.CredsPath, "error", err)
	} else {
		slog.Info("bootstrapped agent and persisted credentials", "path", cfg.CredsPath, "agent_id", out.AgentID)
	}
	return nil
}

func readCreds(path string) (storedCreds, error) {
	var c storedCreds
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, nil
}

func writeCreds(path string, c storedCreds) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	// Write with 0600 and replace atomically.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func wsToHTTP(u string) string {
	if strings.HasPrefix(u, "wss://") {
		return "https://" + u[len("wss://"):]
	}
	if strings.HasPrefix(u, "ws://") {
		return "http://" + u[len("ws://"):]
	}
	return u
}
