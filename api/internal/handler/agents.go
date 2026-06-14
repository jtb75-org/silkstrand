package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jtb75/silkstrand/api/internal/allowlist"
	"github.com/jtb75/silkstrand/api/internal/audit"
	"github.com/jtb75/silkstrand/api/internal/crypto"
	"github.com/jtb75/silkstrand/api/internal/events"
	"github.com/jtb75/silkstrand/api/internal/middleware"
	"github.com/jtb75/silkstrand/api/internal/model"
	"github.com/jtb75/silkstrand/api/internal/pubsub"
	"github.com/jtb75/silkstrand/api/internal/store"
	"github.com/jtb75/silkstrand/api/internal/websocket"
)

const installTokenTTL = time.Hour

// AgentsHandler serves the tenant-facing agent CRUD API. Agents registered
// here get a one-time API key shown in the response; the hash is stored.
// See api/internal/handler/agent.go for the WebSocket connect handler.
type AgentsHandler struct {
	store       store.Store
	hub         *websocket.Hub
	ps          *pubsub.PubSub
	bus         events.Bus
	audit       audit.Writer
	releasesURL string // base URL for agent binaries/installer, e.g. GCS bucket
}

func NewAgentsHandler(s store.Store, hub *websocket.Hub, ps *pubsub.PubSub, bus events.Bus, aw audit.Writer, releasesURL string) *AgentsHandler {
	return &AgentsHandler{store: s, hub: hub, ps: ps, bus: bus, audit: aw, releasesURL: releasesURL}
}

// GET /api/v1/agents
func (h *AgentsHandler) List(w http.ResponseWriter, r *http.Request) {
	agents, err := h.store.ListAgents(r.Context())
	if err != nil {
		slog.Error("listing agents", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list agents")
		return
	}
	if agents == nil {
		agents = []model.Agent{}
	}
	writeJSON(w, http.StatusOK, agents)
}

// GET /api/v1/agents/{id}
func (h *AgentsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent, err := h.store.GetAgent(r.Context(), id)
	if err != nil {
		slog.Error("getting agent", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get agent")
		return
	}
	if agent == nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

// GET /api/v1/agents/{id}/allowlist
// Returns the customer-owned scan allowlist snapshot the agent most
// recently reported over WSS (ADR 003 D11). The server has zero
// authority to edit it — this endpoint is purely a viewer so admins
// can see what the agent is willing to scan. 404 when the agent has
// never reported a snapshot (new agent, or running an older binary).
func (h *AgentsHandler) GetAllowlist(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Tenant-scoped GetAgent enforces that the caller owns this agent.
	agent, err := h.store.GetAgent(r.Context(), id)
	if err != nil {
		slog.Error("getting agent for allowlist", "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	if agent == nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	snap, err := h.store.GetAgentAllowlist(r.Context(), id)
	if err != nil {
		slog.Error("loading agent allowlist", "agent_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load allowlist")
		return
	}
	if snap == nil {
		writeError(w, http.StatusNotFound, "agent has not reported an allowlist yet")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// POST /api/v1/agents
// Body: {name, version?}
// Response includes the plaintext api_key — shown ONCE; the hash is stored.
func (h *AgentsHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Name    string `json:"name"`
		Version string `json:"version,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	agent, rawKey, err := h.store.CreateAgent(r.Context(), model.CreateAgentRequest{
		TenantID: claims.TenantID,
		Name:     req.Name,
		Version:  req.Version,
	})
	if err != nil {
		slog.Error("creating agent", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create agent")
		return
	}
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: claims.TenantID, EventType: audit.EventAgentCreated,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "agent", ResourceID: agent.ID,
		Payload: map[string]any{"name": req.Name, "resource_label": req.Name},
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"agent":   agent,
		"api_key": rawKey, // shown once; store securely
	})
}

// GET /api/v1/agents/{id}/logs
// Query params: since, until (RFC3339), level, scan_id, limit (max 1000), order (asc|desc)
func (h *AgentsHandler) Logs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent, err := h.store.GetAgent(r.Context(), id)
	if err != nil {
		slog.Error("getting agent for logs", "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	if agent == nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	q := r.URL.Query()
	var f store.AgentLogFilter

	if s := q.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since parameter")
			return
		}
		f.Since = &t
	} else {
		// Default: 24 hours ago
		t := time.Now().Add(-24 * time.Hour)
		f.Since = &t
	}

	if s := q.Get("until"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid until parameter")
			return
		}
		f.Until = &t
	}

	f.Level = q.Get("level")
	f.ScanID = q.Get("scan_id")
	f.Order = q.Get("order")
	if f.Order == "" {
		f.Order = "desc"
	}

	if s := q.Get("limit"); s != "" {
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
			f.Limit = n
		}
	}
	if f.Limit <= 0 {
		f.Limit = 200
	}

	items, total, err := h.store.ListAgentLogEvents(r.Context(), id, f)
	if err != nil {
		slog.Error("listing agent logs", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list logs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": total,
	})
}

// POST /api/v1/agents/{id}/rotate-key
// Response includes new plaintext key (old one stays valid until the agent
// reconnects; that's how dual-key rotation works).
func (h *AgentsHandler) RotateKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Verify tenant ownership before rotating.
	agent, err := h.store.GetAgent(r.Context(), id)
	if err != nil || agent == nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	rawKey, err := h.store.RotateAgentKey(r.Context(), id)
	if err != nil {
		slog.Error("rotating agent key", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to rotate key")
		return
	}
	claims := middleware.GetClaims(r.Context())
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: agent.TenantID, EventType: audit.EventAgentKeyRotated,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "agent", ResourceID: id,
		Payload: map[string]any{"resource_label": agent.Name},
	})
	writeJSON(w, http.StatusOK, map[string]string{"api_key": rawKey})
}

// DownloadInfo describes where agent binaries and the installer script live.
// The public S3/MinIO base URL is configured at the API level (AGENT_RELEASES_URL)
// and surfaced here so the tenant frontend doesn't need to hardcode it.
type DownloadInfo struct {
	Version       string            `json:"version"`
	InstallScript string            `json:"install_script"`
	InstallCmd    string            `json:"install_cmd"`
	Binaries      map[string]string `json:"binaries"`
}

// GET /api/v1/agents/downloads
func (h *AgentsHandler) Downloads(w http.ResponseWriter, r *http.Request) {
	base := h.releasesURL
	if base == "" {
		base = "https://downloads.silkstrand.io/agent"
	}
	info := DownloadInfo{
		Version:       "latest",
		InstallScript: base + "/install.sh",
		InstallCmd:    "curl -sSL " + base + "/install.sh | sh",
		Binaries: map[string]string{
			"linux-amd64":       base + "/latest/silkstrand-agent-linux-amd64",
			"linux-arm64":       base + "/latest/silkstrand-agent-linux-arm64",
			"darwin-amd64":      base + "/latest/silkstrand-agent-darwin-amd64",
			"darwin-arm64":      base + "/latest/silkstrand-agent-darwin-arm64",
			"windows-amd64.exe": base + "/latest/silkstrand-agent-windows-amd64.exe",
		},
	}
	writeJSON(w, http.StatusOK, info)
}

// DELETE /api/v1/agents/{id}
func (h *AgentsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.DeleteAgent(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		slog.Error("deleting agent", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete agent")
		return
	}
	claims := middleware.GetClaims(r.Context())
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: claims.TenantID, EventType: audit.EventAgentDeleted,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "agent", ResourceID: id,
	})
	w.WriteHeader(http.StatusNoContent)
}

// discoverScheduleToCron maps the panel's recurrence choice (ADR 013 D5) to a
// cron string for the auto-created discovery definition. nil = on-connect only.
func discoverScheduleToCron(sched string) (*string, error) {
	switch strings.ToLower(strings.TrimSpace(sched)) {
	case "", "off":
		return nil, nil
	case "daily":
		c := "0 3 * * *"
		return &c, nil
	case "weekly":
		c := "0 3 * * 1"
		return &c, nil
	}
	return nil, fmt.Errorf("discover_schedule must be off, daily, or weekly")
}

// normalizeZone turns a free-text site label into a bounded slug for ADR 013
// D10: lowercase, runs of non-alphanumeric collapsed to single hyphens, edges
// trimmed, capped at 63 chars. Empty-after-normalization returns nil (unset),
// which the overlap heuristic treats conservatively (always warn).
func normalizeZone(s string) *string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 63 {
		slug = strings.Trim(slug[:63], "-")
	}
	if slug == "" {
		return nil
	}
	return &slug
}

// POST /api/v1/agents/install-tokens (authenticated, tenant-scoped)
// Body: {} (no fields yet)
// Returns a one-time install token (1h, single-use) bound to this tenant.
// Used by install.sh to call /api/v1/agents/bootstrap and self-register.
func (h *AgentsHandler) CreateInstallToken(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Optional auto-discover intent (ADR 013 D5). Body may be empty.
	var req struct {
		AutoDiscover     bool   `json:"auto_discover"`
		DiscoverSchedule string `json:"discover_schedule"` // "" | off | daily | weekly
		Zone             string `json:"zone"`              // ADR 013 D10: optional site label
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var discoverCron *string
	if req.AutoDiscover {
		c, err := discoverScheduleToCron(req.DiscoverSchedule)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		discoverCron = c
	}
	zone := normalizeZone(req.Zone) // nil if empty-after-trim

	plaintext, tokenHash, err := crypto.NewInstallToken()
	if err != nil {
		slog.Error("generating install token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	expiresAt := time.Now().Add(installTokenTTL)
	createdBy := ""
	if claims.Sub != "" {
		createdBy = claims.Sub
	} else if claims.UserID != "" {
		createdBy = claims.UserID
	}
	if err := h.store.CreateInstallToken(r.Context(), store.CreateInstallTokenInput{
		TenantID:     claims.TenantID,
		TokenHash:    tokenHash,
		ExpiresAt:    expiresAt,
		CreatedBy:    createdBy,
		AutoDiscover: req.AutoDiscover,
		DiscoverCron: discoverCron,
		Zone:         zone,
	}); err != nil {
		slog.Error("storing install token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"install_token": plaintext,
		"expires_at":    expiresAt.UTC().Format(time.RFC3339),
	})
}

// overlapSource is one discovering range to check operator input against.
type overlapSource struct {
	kind     string // "agent" | "scan_definition"
	ownerID  string
	name     string
	zone     *string
	iv       allowlist.Interval
	rangeStr string
}

// POST /api/v1/agents/allowlist-preview (authenticated, tenant-scoped)
// Body: {cidrs: ["10.0.0.0/24", ...], zone?: "office-east"}
// ADR 013 D6: warn (never block) when the ranges an operator is about to seed
// overlap with another agent that actually discovers — double-scanning the
// customer's network wastes load and muddies attribution. Returns
// {overlaps, redundant}; the panel shows a confirmation modal but always lets
// the operator proceed. This is UI guidance, never an authorization boundary.
func (h *AgentsHandler) AllowlistPreview(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		CIDRs []string `json:"cidrs"`
		Zone  string   `json:"zone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	inputZone := normalizeZone(req.Zone)

	// Only address ranges participate; hostnames are skipped (no range to
	// intersect). CIDR, bare IP, and "a-b" range all reduce to an interval.
	type parsedRange struct {
		raw string
		iv  allowlist.Interval
	}
	var inputs []parsedRange
	for _, c := range req.CIDRs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if iv, ok := allowlist.ParseInterval(c); ok {
			inputs = append(inputs, parsedRange{raw: c, iv: iv})
		}
	}

	// (1) Intra-input redundancy: a typed range fully inside another typed one.
	redundant := []string{}
	for i := 0; i < len(inputs); i++ {
		for j := i + 1; j < len(inputs); j++ {
			a, b := inputs[i], inputs[j]
			switch {
			case a.iv.Contains(b.iv) && b.iv.Contains(a.iv):
				redundant = append(redundant, fmt.Sprintf("%s duplicates %s", b.raw, a.raw))
			case a.iv.Contains(b.iv):
				redundant = append(redundant, fmt.Sprintf("%s is contained in %s (also entered)", b.raw, a.raw))
			case b.iv.Contains(a.iv):
				redundant = append(redundant, fmt.Sprintf("%s is contained in %s (also entered)", a.raw, b.raw))
			}
		}
	}

	// (2) Overlap with other agents that actually discover.
	sources := h.discoverySources(r.Context())
	overlaps := []map[string]any{}
	seen := map[string]bool{}
	for _, in := range inputs {
		for _, src := range sources {
			if !in.iv.Overlaps(src.iv) {
				continue
			}
			// Zone-aware suppression (D10): only when both sides carry a zone,
			// the zones differ, and *both* ranges are wholly private — so the
			// shared addresses are private too. Public overlap (either side not
			// wholly private), or any unset zone, always warns (conservative).
			if inputZone != nil && src.zone != nil && *inputZone != *src.zone &&
				in.iv.Private() && src.iv.Private() {
				continue
			}
			key := in.raw + "|" + src.rangeStr + "|" + src.ownerID
			if seen[key] {
				continue
			}
			seen[key] = true
			overlaps = append(overlaps, map[string]any{
				"cidr": in.raw,
				"conflicts_with": map[string]any{
					"kind":              src.kind,
					"name":              src.name,
					"id":                src.ownerID,
					"range":             src.rangeStr,
					"zone":              src.zone,
					"discovery_enabled": true,
				},
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"overlaps":  overlaps,
		"redundant": redundant,
	})
}

// discoverySources collects every range in this tenant that is actively
// discovered: an enabled discovery scan_definition scoped to cidr or
// agent_allowlist. Compliance definitions (endpoint-scoped) are skipped — they
// target endpoints, not ranges, so they never double-scan a CIDR.
func (h *AgentsHandler) discoverySources(ctx context.Context) []overlapSource {
	defs, err := h.store.ListScanDefinitions(ctx)
	if err != nil {
		slog.Error("allowlist-preview: listing scan definitions", "error", err)
		return nil
	}
	agents, err := h.store.ListAgents(ctx)
	if err != nil {
		slog.Error("allowlist-preview: listing agents", "error", err)
		// continue with empty agent map — owner labels degrade, overlap math holds
	}
	agentByID := make(map[string]model.Agent, len(agents))
	for _, a := range agents {
		agentByID[a.ID] = a
	}

	var sources []overlapSource
	snapCache := map[string]*store.AgentAllowlistSnapshot{}
	add := func(agentID *string, rangeStr string) {
		iv, ok := allowlist.ParseInterval(rangeStr)
		if !ok {
			return
		}
		src := overlapSource{kind: "scan_definition", iv: iv, rangeStr: rangeStr}
		if agentID != nil {
			if a, ok := agentByID[*agentID]; ok {
				src.kind = "agent"
				src.ownerID = a.ID
				src.name = a.Name
				src.zone = a.Zone
			} else {
				src.ownerID = *agentID
			}
		}
		sources = append(sources, src)
	}

	for _, d := range defs {
		if !d.Enabled || d.Kind != model.ScanDefinitionKindDiscovery {
			continue
		}
		switch d.ScopeKind {
		case model.ScanDefinitionScopeCIDR:
			if d.CIDR != nil {
				add(d.AgentID, strings.TrimSpace(*d.CIDR))
			}
		case model.ScanDefinitionScopeAgentAllowlist:
			if d.AgentID == nil {
				continue
			}
			snap, ok := snapCache[*d.AgentID]
			if !ok {
				snap, _ = h.store.GetAgentAllowlist(ctx, *d.AgentID)
				snapCache[*d.AgentID] = snap
			}
			if snap == nil {
				continue
			}
			for _, entry := range snap.Allow {
				add(d.AgentID, strings.TrimSpace(entry))
			}
		}
	}
	return sources
}

// POST /api/v1/agents/bootstrap (public, rate-limited)
// Body: {install_token, name, version?}
// Consumes the token (single-use) and creates an agent for the token's
// tenant. Returns long-lived agent_id + api_key. Tenant is derived from
// the token on the server — never trusted from the client.
func (h *AgentsHandler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstallToken string `json:"install_token"`
		Name         string `json:"name"`
		Version      string `json:"version,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.InstallToken == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "install_token and name are required")
		return
	}

	// Create the agent BEFORE consuming the token so we have the agent_id to
	// audit on the token row. Small race window (agent exists briefly with
	// no token link) — acceptable; agents table isn't user-visible yet.
	// Caveat: we need the tenant_id first to create the agent. So we look it
	// up with a non-consuming read, then consume atomically after creation.

	// Check token validity (read-only) first to give a proper error up-front.
	// We can piggy-back on the UPDATE…RETURNING by doing a two-step: create
	// agent after a valid peek, then consume for real.
	// Simpler: consume first, roll back the agent if consume failed. Since we
	// don't have tx plumbing here, go with peek-then-create-then-consume.

	hash := crypto.HashInstallToken(req.InstallToken)

	// Consume (and get tenant_id) — use a placeholder agent_id since the
	// audit field is optional. Then create the agent. We set the audit
	// field with a follow-up UPDATE once we have the real id. If the agent
	// creation fails, the token is already used — that's a UX regression
	// but not a security issue (admin just generates a new token).
	tok, err := h.store.ConsumeInstallToken(r.Context(), hash, "00000000-0000-0000-0000-000000000000")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "install token invalid, expired, or already used")
			return
		}
		slog.Error("consuming install token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}

	agent, rawKey, err := h.store.CreateAgent(r.Context(), model.CreateAgentRequest{
		TenantID: tok.TenantID,
		Name:     req.Name,
		Version:  req.Version,
		// ADR 013 D5: carry the auto-discover intent onto the agent; it fires
		// once on the agent's first allowlist snapshot.
		AutoDiscoverPending: tok.AutoDiscover,
		DiscoverCron:        tok.DiscoverCron,
		// ADR 013 D10: copy the zone label from the token onto the agent.
		Zone: tok.Zone,
	})
	if err != nil {
		slog.Error("bootstrap: creating agent", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create agent")
		return
	}

	h.audit.Emit(r.Context(), audit.Event{
		TenantID: tok.TenantID, EventType: audit.EventAgentCreated,
		ActorType:    audit.ActorSystem,
		ResourceType: "agent", ResourceID: agent.ID,
		Payload: map[string]any{"name": req.Name, "via": "bootstrap", "resource_label": req.Name},
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"agent_id": agent.ID,
		"api_key":  rawKey,
	})
}

// UpgradeRequest is the body of POST /api/v1/agents/{id}/upgrade.
// Version defaults to "latest" when empty. sha256_by_platform is optional;
// if omitted the agent will download without checksum verification.
type UpgradeRequest struct {
	Version          string            `json:"version"`
	SHA256ByPlatform map[string]string `json:"sha256_by_platform,omitempty"`
}

// POST /api/v1/agents/{id}/upgrade (tenant-authed)
// Body: {version?, sha256_by_platform?}
// Sends an upgrade directive to a connected agent.
func (h *AgentsHandler) Upgrade(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Tenant-scoped GetAgent ensures the agent belongs to the caller's tenant.
	agent, err := h.store.GetAgent(r.Context(), id)
	if err != nil || agent == nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	var req UpgradeRequest
	// Body is optional — tolerate empty / missing.
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Version == "" {
		req.Version = "latest"
	}

	baseURL := h.releasesURL
	if baseURL == "" {
		baseURL = "https://downloads.silkstrand.io/agent"
	}

	payload, _ := json.Marshal(websocket.UpgradePayload{
		Version:          req.Version,
		BaseURL:          baseURL,
		SHA256ByPlatform: req.SHA256ByPlatform,
	})
	if h.ps == nil {
		writeError(w, http.StatusServiceUnavailable, "upgrade not available (no pubsub)")
		return
	}
	// Route through Redis pub/sub — the API instance handling this HTTP
	// request rarely owns the agent's WSS connection. Whichever instance
	// has the connection picks up the published message and delivers it.
	if err := h.ps.PublishUpgrade(r.Context(), id, payload); err != nil {
		slog.Error("publishing upgrade directive", "agent_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to dispatch upgrade")
		return
	}
	// Emit agent_status event so the UI reflects the upgrading state.
	if h.bus != nil {
		claims := middleware.GetClaims(r.Context())
		tenantID := ""
		if claims != nil {
			tenantID = claims.TenantID
		}
		if tenantID != "" {
			statusPayload, _ := json.Marshal(map[string]string{"status": "upgrading"})
			if pubErr := h.bus.Publish(r.Context(), events.Event{
				Kind:         "agent_status",
				ResourceType: "agent",
				ResourceID:   id,
				TenantID:     tenantID,
				OccurredAt:   time.Now(),
				Payload:      statusPayload,
			}); pubErr != nil {
				slog.Error("publishing agent_status event", "agent_id", id, "error", pubErr)
			}
		}
	}
	claims := middleware.GetClaims(r.Context())
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: agent.TenantID, EventType: audit.EventAgentUpgraded,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "agent", ResourceID: id,
		Payload: map[string]any{"version": req.Version, "resource_label": agent.Name},
	})
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "requested",
		"version": req.Version,
	})
}
