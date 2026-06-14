package handler

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jtb75/silkstrand/api/internal/audit"
	"github.com/jtb75/silkstrand/api/internal/awssm"
	"github.com/jtb75/silkstrand/api/internal/crypto"
	"github.com/jtb75/silkstrand/api/internal/middleware"
	"github.com/jtb75/silkstrand/api/internal/model"
	"github.com/jtb75/silkstrand/api/internal/pubsub"
	"github.com/jtb75/silkstrand/api/internal/store"
	"github.com/jtb75/silkstrand/api/internal/vault"
	"github.com/jtb75/silkstrand/api/internal/websocket"
)

var base64Enc = base64.StdEncoding

// credentialSourceTypeAllowed mirrors the DB CHECK constraint added in
// migration 017. Kept here so the API rejects invalid types with a clean
// 400 before the INSERT round-trips.
var credentialSourceTypeAllowed = map[string]bool{
	model.CredentialSourceTypeStatic:            true,
	model.CredentialSourceTypeSlack:             true,
	model.CredentialSourceTypeWebhook:           true,
	model.CredentialSourceTypeEmail:             true,
	model.CredentialSourceTypePagerDuty:         true,
	model.CredentialSourceTypeAWSSecretsManager: true,
	model.CredentialSourceTypeHashiCorpVault:    true,
	model.CredentialSourceTypeCyberArk:          true,
}

// Vault resolver types (aws_secrets_manager / hashicorp_vault / cyberark)
// persist config JSONB in this consolidated surface; the actual secret
// fetch path returns 501 until ADR 004 C1+ lands in a later PR.

// CredentialsHandler serves the tenant-facing credential API for targets.
// Credentials are stored encrypted at rest (AES-256-GCM); the API never
// returns plaintext after creation — only a presence indicator + type.
type CredentialsHandler struct {
	store  store.Store
	encKey []byte // optional in dev; required in prod via CREDENTIAL_ENCRYPTION_KEY
	audit  audit.Writer
	ps     *pubsub.PubSub // for agent-side credential tests
}

func NewCredentialsHandler(s store.Store, encKey []byte, aw audit.Writer, ps *pubsub.PubSub) *CredentialsHandler {
	return &CredentialsHandler{store: s, encKey: encKey, audit: aw, ps: ps}
}

// GET /api/v1/targets/{id}/credential
// Returns 200 {set: true, type: "database"} or 200 {set: false}.
// Never returns the ciphertext or plaintext.
func (h *CredentialsHandler) Get(w http.ResponseWriter, r *http.Request) {
	targetID := r.PathValue("id")
	// Verify the target belongs to this tenant before answering.
	target, err := h.store.GetTarget(r.Context(), targetID)
	if err != nil || target == nil {
		writeError(w, http.StatusNotFound, "target not found")
		return
	}
	has, credType, err := h.store.HasCredentialForTarget(r.Context(), targetID)
	if err != nil {
		slog.Error("checking credential", "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	resp := map[string]any{"set": has}
	if has {
		resp["type"] = credType
	}
	writeJSON(w, http.StatusOK, resp)
}

// PUT /api/v1/targets/{id}/credential
// Body: {type: "database", data: {<secret fields>}}
// Upserts the credential — replaces any existing one for this target.
func (h *CredentialsHandler) Put(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	targetID := r.PathValue("id")
	target, err := h.store.GetTarget(r.Context(), targetID)
	if err != nil || target == nil {
		writeError(w, http.StatusNotFound, "target not found")
		return
	}

	var req struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Type == "" || len(req.Data) == 0 {
		writeError(w, http.StatusBadRequest, "type and data are required")
		return
	}

	var stored []byte
	if len(h.encKey) > 0 {
		stored, err = crypto.Encrypt(req.Data, h.encKey)
		if err != nil {
			slog.Error("encrypting credential", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to encrypt credential")
			return
		}
	} else {
		// Dev only: store plaintext if no encryption key configured.
		stored = req.Data
	}

	if err := h.store.UpsertStaticCredentialSource(r.Context(), claims.TenantID, targetID, req.Type, stored); err != nil {
		slog.Error("upserting credential source", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save credential")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ====================================================================
// Credential sources surface (ADR 004 C0 + ADR 006 P6 consolidation).
// Static sources hold encrypted DB / host creds. Channel sources
// (slack/webhook/email/pagerduty) store integration config + secrets.
// Vault sources (aws_secrets_manager/hashicorp_vault/cyberark) persist
// config JSONB; actual secret fetch returns 501 until ADR 004 C1+.
//
// For all non-static types we preserve secrets on update the same way
// NotificationChannels did before this consolidation: if a known secret
// key is left blank on update, keep the existing value. Secrets are
// returned as the sentinel "(set)" on read so plaintext never leaves
// the server post-creation.
// ====================================================================

// secretKeysByType enumerates the config keys that are considered
// secrets. On read we scrub them with "(set)"; on update, blank values
// are preserved from the existing row.
var secretKeysByType = map[string][]string{
	model.CredentialSourceTypeWebhook:           {"secret"},
	model.CredentialSourceTypeSlack:             {"webhook_url"},
	model.CredentialSourceTypeEmail:             {"smtp_password", "api_key"},
	model.CredentialSourceTypePagerDuty:         {"routing_key", "api_token"},
	model.CredentialSourceTypeAWSSecretsManager: {"aws_secret_access_key"},
	model.CredentialSourceTypeHashiCorpVault:    {"token", "role_id", "secret_id"},
	model.CredentialSourceTypeCyberArk:          {"api_key", "password"},
}

type credentialSourceView struct {
	ID        string          `json:"id"`
	TenantID  string          `json:"tenant_id"`
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	Config    json.RawMessage `json:"config"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

func scrubConfig(t string, cfg json.RawMessage) json.RawMessage {
	keys, ok := secretKeysByType[t]
	if !ok || len(cfg) == 0 {
		return cfg
	}
	var m map[string]any
	if err := json.Unmarshal(cfg, &m); err != nil {
		return cfg
	}
	for _, k := range keys {
		if _, exists := m[k]; exists {
			m[k] = "(set)"
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return cfg
	}
	return out
}

func toView(cs model.CredentialSource) credentialSourceView {
	// Static sources carry encrypted_data; scrub it but expose the
	// username so the list view can display it.
	cfg := cs.Config
	if cs.Type == model.CredentialSourceTypeStatic {
		var m map[string]any
		if json.Unmarshal(cfg, &m) == nil {
			// Replace encrypted_data with the sentinel; never expose ciphertext.
			if _, ok := m["encrypted_data"]; ok {
				m["encrypted_data"] = "(set)"
			}
			// Always show password as masked (the UI relies on this sentinel).
			m["password"] = "(set)"
			if out, err := json.Marshal(m); err == nil {
				cfg = out
			}
		}
	} else {
		cfg = scrubConfig(cs.Type, cfg)
	}
	return credentialSourceView{
		ID:        cs.ID,
		TenantID:  cs.TenantID,
		Name:      cs.Name,
		Type:      cs.Type,
		Config:    cfg,
		CreatedAt: cs.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt: cs.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// ListSources — GET /api/v1/credential-sources[?type=static|slack|...]
func (h *CredentialsHandler) ListSources(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	items, err := h.store.ListCredentialSources(r.Context(), claims.TenantID)
	if err != nil {
		slog.Error("listing credential sources", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list credential sources")
		return
	}
	typeFilter := r.URL.Query().Get("type")
	out := make([]credentialSourceView, 0, len(items))
	for _, cs := range items {
		if typeFilter != "" && cs.Type != typeFilter {
			continue
		}
		out = append(out, toView(cs))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetSource — GET /api/v1/credential-sources/{id}
func (h *CredentialsHandler) GetSource(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.PathValue("id")
	cs, err := h.store.GetCredentialSource(r.Context(), id)
	if err != nil {
		slog.Error("getting credential source", "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	if cs == nil || cs.TenantID != claims.TenantID {
		writeError(w, http.StatusNotFound, "credential source not found")
		return
	}
	writeJSON(w, http.StatusOK, toView(*cs))
}

// CreateSource — POST /api/v1/credential-sources
// Body: {name: "...", type: "static"|"slack"|..., config: {...}}
// For type=static, config should contain {username, password}. The
// handler encrypts these before storing.
func (h *CredentialsHandler) CreateSource(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Name   string          `json:"name"`
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !credentialSourceTypeAllowed[req.Type] {
		writeError(w, http.StatusBadRequest, "invalid credential source type")
		return
	}
	if len(req.Config) == 0 {
		req.Config = json.RawMessage(`{}`)
	}
	// Static sources: encrypt username+password into the config JSONB so
	// plaintext credentials are never at rest.
	if req.Type == model.CredentialSourceTypeStatic {
		cfg, err := h.encryptStaticConfig(req.Config)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Config = cfg
	}
	// AWS Secrets Manager: validate required config fields.
	if req.Type == model.CredentialSourceTypeAWSSecretsManager {
		if err := validateAWSSecretsManagerConfig(req.Config); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	// HashiCorp Vault: validate required config fields.
	if req.Type == model.CredentialSourceTypeHashiCorpVault {
		if err := validateHashiCorpVaultConfig(req.Config); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	id, err := h.store.CreateCredentialSource(r.Context(), claims.TenantID, req.Name, req.Type, req.Config)
	if err != nil {
		slog.Error("creating credential source", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create credential source")
		return
	}
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: claims.TenantID, EventType: audit.EventCredentialCreated,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "credential_source", ResourceID: id,
		Payload: map[string]any{"type": req.Type, "name": req.Name, "resource_label": req.Name},
	})
	cs, err := h.store.GetCredentialSource(r.Context(), id)
	if err != nil || cs == nil {
		writeError(w, http.StatusInternalServerError, "created but read-back failed")
		return
	}
	writeJSON(w, http.StatusCreated, toView(*cs))
}

// encryptStaticConfig takes the raw config containing {username,
// password} and returns a config JSONB with {type, encrypted_data}
// where encrypted_data is AES-256-GCM encrypted base64. The
// username is stored as the "type" field for display purposes.
func (h *CredentialsHandler) encryptStaticConfig(raw json.RawMessage) (json.RawMessage, error) {
	var incoming map[string]any
	if err := json.Unmarshal(raw, &incoming); err != nil {
		return nil, errors.New("config must be a JSON object")
	}
	username, _ := incoming["username"].(string)
	password, _ := incoming["password"].(string)
	if username == "" || password == "" {
		return nil, errors.New("username and password are required for static credentials")
	}
	// The agent needs {username, password} as plaintext in the decrypted
	// payload — encrypt the entire credential JSON blob.
	credJSON, _ := json.Marshal(map[string]string{
		"username": username,
		"password": password,
	})
	var stored []byte
	if len(h.encKey) > 0 {
		var err error
		stored, err = crypto.Encrypt(credJSON, h.encKey)
		if err != nil {
			return nil, errors.New("failed to encrypt credential")
		}
	} else {
		stored = credJSON
	}
	cfg, err := json.Marshal(map[string]any{
		"type":           "database",
		"username":       username,
		"encrypted_data": encodeBase64(stored),
	})
	if err != nil {
		return nil, errors.New("failed to build credential config")
	}
	return cfg, nil
}

func encodeBase64(data []byte) string {
	return base64Enc.EncodeToString(data)
}

// UpdateSource — PUT /api/v1/credential-sources/{id}
// Preserves secret fields when they are blank in the incoming config.
// For static type: blank password means "keep existing".
func (h *CredentialsHandler) UpdateSource(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.PathValue("id")
	existing, err := h.store.GetCredentialSource(r.Context(), id)
	if err != nil {
		slog.Error("getting credential source", "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	if existing == nil || existing.TenantID != claims.TenantID {
		writeError(w, http.StatusNotFound, "credential source not found")
		return
	}
	var req struct {
		Name   *string         `json:"name"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := existing.Name
	if req.Name != nil {
		name = *req.Name
	}

	var merged json.RawMessage
	if existing.Type == model.CredentialSourceTypeStatic {
		// Static update: if password is blank/missing, keep the existing
		// encrypted config. If password is provided, re-encrypt.
		var incoming map[string]any
		if err := json.Unmarshal(req.Config, &incoming); err != nil {
			writeError(w, http.StatusBadRequest, "config must be a JSON object")
			return
		}
		password, _ := incoming["password"].(string)
		if password == "" || password == "(set)" {
			// Keep existing encrypted config as-is.
			merged = existing.Config
		} else {
			username, _ := incoming["username"].(string)
			if username == "" {
				writeError(w, http.StatusBadRequest, "username is required")
				return
			}
			cfg, err := h.encryptStaticConfig(req.Config)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			merged = cfg
		}
	} else {
		// Non-static: merge preserving blank secret keys.
		var incoming, current map[string]any
		if err := json.Unmarshal(req.Config, &incoming); err != nil {
			writeError(w, http.StatusBadRequest, "config must be a JSON object")
			return
		}
		_ = json.Unmarshal(existing.Config, &current)
		for _, k := range secretKeysByType[existing.Type] {
			if v, ok := incoming[k]; !ok || v == "" || v == "(set)" {
				if cur, ok := current[k]; ok {
					incoming[k] = cur
				} else {
					delete(incoming, k)
				}
			}
		}
		var mergeErr error
		merged, mergeErr = json.Marshal(incoming)
		if mergeErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to serialize config")
			return
		}
	}

	if err := h.store.UpdateCredentialSource(r.Context(), id, name, merged); err != nil {
		slog.Error("updating credential source", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update credential source")
		return
	}
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: claims.TenantID, EventType: audit.EventCredentialUpdated,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "credential_source", ResourceID: id,
		Payload: map[string]any{"resource_label": name},
	})
	cs, _ := h.store.GetCredentialSource(r.Context(), id)
	if cs == nil {
		writeError(w, http.StatusNotFound, "credential source not found")
		return
	}
	writeJSON(w, http.StatusOK, toView(*cs))
}

// DeleteSource — DELETE /api/v1/credential-sources/{id}
func (h *CredentialsHandler) DeleteSource(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.PathValue("id")
	existing, err := h.store.GetCredentialSource(r.Context(), id)
	if err != nil {
		slog.Error("getting credential source", "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	if existing == nil || existing.TenantID != claims.TenantID {
		writeError(w, http.StatusNotFound, "credential source not found")
		return
	}
	if err := h.store.DeleteCredentialSource(r.Context(), id); err != nil {
		slog.Error("deleting credential source", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete credential source")
		return
	}
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: claims.TenantID, EventType: audit.EventCredentialDeleted,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "credential_source", ResourceID: id,
		Payload: map[string]any{"resource_label": existing.Name},
	})
	w.WriteHeader(http.StatusNoContent)
}

// validateAWSSecretsManagerConfig checks that the required fields are
// present in the config for an aws_secrets_manager credential source.
// We don't validate connectivity at create time — that's what the
// test endpoint is for.
func validateAWSSecretsManagerConfig(raw json.RawMessage) error {
	var cfg struct {
		Region           string `json:"region"`
		SecretARN        string `json:"secret_arn"`
		SecretKeyUsername string `json:"secret_key_username"`
		SecretKeyPassword string `json:"secret_key_password"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return errors.New("config must be a JSON object")
	}
	if cfg.Region == "" {
		return errors.New("region is required for aws_secrets_manager")
	}
	if cfg.SecretARN == "" {
		return errors.New("secret_arn is required for aws_secrets_manager")
	}
	if cfg.SecretKeyUsername == "" {
		return errors.New("secret_key_username is required for aws_secrets_manager")
	}
	if cfg.SecretKeyPassword == "" {
		return errors.New("secret_key_password is required for aws_secrets_manager")
	}
	return nil
}

const credentialTestTimeout = 15 * time.Second

// TestSource — POST /api/v1/credential-sources/{id}/test
// Attempts to resolve the credential source and returns success/failure.
// For aws_secrets_manager: calls AWS to fetch the secret and extract
// username + password. Returns the username (never the password) on success.
//
// When ?agent_id=xxx is provided, the test is forwarded to the agent via
// Redis pub/sub (same pattern as probes). This is required for on-prem
// vaults that are unreachable from Cloud Run.
func (h *CredentialsHandler) TestSource(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.PathValue("id")
	cs, err := h.store.GetCredentialSource(r.Context(), id)
	if err != nil {
		slog.Error("testing credential source", "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	if cs == nil || cs.TenantID != claims.TenantID {
		writeError(w, http.StatusNotFound, "credential source not found")
		return
	}
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: claims.TenantID, EventType: audit.EventCredentialTest,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "credential_source", ResourceID: id,
		Payload: map[string]any{"type": cs.Type, "resource_label": cs.Name},
	})

	// Agent-side test: forward to agent via Redis pub/sub.
	agentID := r.URL.Query().Get("agent_id")
	if agentID != "" {
		h.testViaAgent(w, r, cs, agentID)
		return
	}

	switch cs.Type {
	case model.CredentialSourceTypeAWSSecretsManager:
		var cfg awssm.ResolveConfig
		if err := json.Unmarshal(cs.Config, &cfg); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"error":   "invalid config: " + err.Error(),
			})
			return
		}
		cred, err := awssm.Resolve(r.Context(), cfg)
		if err != nil {
			slog.Warn("credential_source.test",
				"source_id", cs.ID, "type", cs.Type, "error", err)
			writeJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"error":   err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":  true,
			"username": cred.Username,
		})

	case model.CredentialSourceTypeHashiCorpVault:
		var cfg vault.ResolveConfig
		if err := json.Unmarshal(cs.Config, &cfg); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"error":   "invalid config: " + err.Error(),
			})
			return
		}
		cred, err := vault.Resolve(r.Context(), cfg)
		if err != nil {
			slog.Warn("credential_source.test",
				"source_id", cs.ID, "type", cs.Type, "error", err)
			// On-prem vaults are typically unreachable from Cloud Run.
			// Return a helpful message explaining agent-side resolution.
			writeJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"error":   err.Error(),
				"hint":    "If this Vault is on-prem, the agent will resolve this credential at scan time using its local network. Server-side test connectivity is expected to fail for on-prem Vault instances.",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":  true,
			"username": cred.Username,
		})

	case model.CredentialSourceTypeStatic:
		// For static sources, verify we can decrypt the stored credential.
		var cfg model.StaticCredentialConfig
		if err := json.Unmarshal(cs.Config, &cfg); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"error":   "invalid config",
			})
			return
		}
		data, decErr := base64Enc.DecodeString(cfg.EncryptedData)
		if decErr != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"error":   "failed to decode credential data",
			})
			return
		}
		if len(h.encKey) > 0 {
			decrypted, decErr := crypto.Decrypt(data, h.encKey)
			if decErr != nil {
				writeJSON(w, http.StatusOK, map[string]any{
					"success": false,
					"error":   "failed to decrypt credential",
				})
				return
			}
			var creds map[string]string
			if err := json.Unmarshal(decrypted, &creds); err == nil {
				writeJSON(w, http.StatusOK, map[string]any{
					"success":  true,
					"username": creds["username"],
				})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
		})

	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"error":   "test not supported for type: " + cs.Type,
		})
	}
}

// testViaAgent forwards a credential test to a connected agent via Redis
// pub/sub, following the same subscribe-before-publish pattern as probes.
func (h *CredentialsHandler) testViaAgent(w http.ResponseWriter, r *http.Request, cs *model.CredentialSource, agentID string) {
	if h.ps == nil {
		writeError(w, http.StatusServiceUnavailable, "pub/sub not available")
		return
	}

	// Verify the agent belongs to this tenant.
	agent, err := h.store.GetAgentByID(r.Context(), agentID)
	if err != nil {
		slog.Error("credential test: looking up agent", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to look up agent")
		return
	}
	claims := middleware.GetClaims(r.Context())
	if agent == nil || agent.TenantID != claims.TenantID {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	testID := hex.EncodeToString(buf)

	testPayload, _ := json.Marshal(websocket.CredentialTestPayload{
		TestID:       testID,
		ResolverType: cs.Type,
		Config:       cs.Config,
	})

	resultBytes, err := h.ps.AwaitCredentialTestResult(r.Context(), testID, credentialTestTimeout, func() error {
		return h.ps.PublishCredentialTest(r.Context(), agentID, testPayload)
	})
	if err != nil {
		if errors.Is(err, r.Context().Err()) {
			writeError(w, http.StatusServiceUnavailable, "request cancelled")
			return
		}
		if err.Error() == "agent did not reply in time" {
			writeJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"error":   "agent did not reply in time",
			})
			return
		}
		slog.Error("credential test: pub/sub failure", "error", err, "test_id", testID)
		writeError(w, http.StatusInternalServerError, "credential test failed: "+err.Error())
		return
	}

	var result websocket.CredentialTestResultPayload
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		slog.Error("credential test: invalid result payload", "error", err, "test_id", testID)
		writeError(w, http.StatusInternalServerError, "invalid agent reply")
		return
	}

	resp := map[string]any{
		"success":     result.Success,
		"duration_ms": result.DurationMs,
	}
	if result.Username != "" {
		resp["username"] = result.Username
	}
	if result.Error != "" {
		resp["error"] = result.Error
	}
	writeJSON(w, http.StatusOK, resp)
}

// DELETE /api/v1/targets/{id}/credential
func (h *CredentialsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	targetID := r.PathValue("id")
	if err := h.store.DeleteCredentialForTarget(r.Context(), claims.TenantID, targetID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "no credential set for this target")
			return
		}
		slog.Error("deleting credential", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete credential")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateHashiCorpVaultConfig checks that the required fields are
// present in the config for a hashicorp_vault credential source.
func validateHashiCorpVaultConfig(raw json.RawMessage) error {
	var cfg struct {
		VaultURL         string `json:"vault_url"`
		AuthMethod       string `json:"auth_method"`
		Token            string `json:"token"`
		SecretPath       string `json:"secret_path"`
		SecretKeyUsername string `json:"secret_key_username"`
		SecretKeyPassword string `json:"secret_key_password"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return errors.New("config must be a JSON object")
	}
	if cfg.VaultURL == "" {
		return errors.New("vault_url is required for hashicorp_vault")
	}
	if cfg.SecretPath == "" {
		return errors.New("secret_path is required for hashicorp_vault")
	}
	if cfg.SecretKeyUsername == "" {
		return errors.New("secret_key_username is required for hashicorp_vault")
	}
	if cfg.SecretKeyPassword == "" {
		return errors.New("secret_key_password is required for hashicorp_vault")
	}
	method := cfg.AuthMethod
	if method == "" {
		method = "token"
	}
	if method == "token" && cfg.Token == "" {
		return errors.New("token is required for hashicorp_vault with auth_method=token")
	}
	return nil
}
