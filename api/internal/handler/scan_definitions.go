package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jtb75/silkstrand/api/internal/audit"
	"github.com/jtb75/silkstrand/api/internal/middleware"
	"github.com/jtb75/silkstrand/api/internal/model"
	"github.com/jtb75/silkstrand/api/internal/scheduler"
	"github.com/jtb75/silkstrand/api/internal/store"
)

// ScanDefinitionsHandler serves the ADR 007 D3/D5/D7 CRUD + execute
// surface. Execute and the scheduler tick share
// scheduler.Dispatcher.Execute so manual and timer-driven runs go
// through the same code path.
type ScanDefinitionsHandler struct {
	store store.Store
	disp  scheduler.Dispatcher
	audit audit.Writer
}

func NewScanDefinitionsHandler(s store.Store, d scheduler.Dispatcher, aw audit.Writer) *ScanDefinitionsHandler {
	return &ScanDefinitionsHandler{store: s, disp: d, audit: aw}
}

type scanDefRequest struct {
	Name            string  `json:"name"`
	Kind            string  `json:"kind"`
	BundleID        *string `json:"bundle_id"`
	ScopeKind       string  `json:"scope_kind"`
	AssetEndpointID *string `json:"asset_endpoint_id"`
	CollectionID    *string `json:"collection_id"`
	CIDR            *string `json:"cidr"`
	AgentID         *string `json:"agent_id"`
	Schedule        *string `json:"schedule"`
	Enabled         *bool   `json:"enabled"`
}

func (r *scanDefRequest) validateScope() error {
	switch r.ScopeKind {
	case model.ScanDefinitionScopeAssetEndpoint:
		if r.AssetEndpointID == nil || *r.AssetEndpointID == "" {
			return errors.New("scope=asset_endpoint requires asset_endpoint_id")
		}
		if r.CollectionID != nil || (r.CIDR != nil && *r.CIDR != "") {
			return errors.New("scope=asset_endpoint must not set collection_id or cidr")
		}
	case model.ScanDefinitionScopeCollection:
		if r.CollectionID == nil || *r.CollectionID == "" {
			return errors.New("scope=collection requires collection_id")
		}
		if r.AssetEndpointID != nil || (r.CIDR != nil && *r.CIDR != "") {
			return errors.New("scope=collection must not set asset_endpoint_id or cidr")
		}
	case model.ScanDefinitionScopeCIDR:
		if r.CIDR == nil || *r.CIDR == "" {
			return errors.New("scope=cidr requires cidr")
		}
		if r.AssetEndpointID != nil || r.CollectionID != nil {
			return errors.New("scope=cidr must not set asset_endpoint_id or collection_id")
		}
	case model.ScanDefinitionScopeAgentAllowlist:
		if r.AgentID == nil || *r.AgentID == "" {
			return errors.New("scope=agent_allowlist requires agent_id")
		}
		if r.AssetEndpointID != nil || r.CollectionID != nil || (r.CIDR != nil && *r.CIDR != "") {
			return errors.New("scope=agent_allowlist must not set asset_endpoint_id, collection_id, or cidr")
		}
	default:
		return errors.New("scope_kind must be asset_endpoint | collection | cidr | agent_allowlist")
	}
	return nil
}

func (r *scanDefRequest) validateKind() error {
	switch r.Kind {
	case model.ScanDefinitionKindCompliance, model.ScanDefinitionKindDiscovery:
		return nil
	}
	return errors.New("kind must be compliance | discovery")
}

func (r *scanDefRequest) toModel() model.ScanDefinition {
	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	d := model.ScanDefinition{
		Name:            r.Name,
		Kind:            r.Kind,
		BundleID:        r.BundleID,
		ScopeKind:       r.ScopeKind,
		AssetEndpointID: r.AssetEndpointID,
		CollectionID:    r.CollectionID,
		AgentID:         r.AgentID,
		Schedule:        r.Schedule,
		Enabled:         enabled,
	}
	if r.CIDR != nil && *r.CIDR != "" {
		d.CIDR = r.CIDR
	}
	return d
}

func computeNextRunAt(schedule *string, enabled bool) (*time.Time, error) {
	if schedule == nil || *schedule == "" || !enabled {
		return nil, nil
	}
	c, err := scheduler.ParseCron(*schedule)
	if err != nil {
		return nil, err
	}
	next, err := c.Next(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return &next, nil
}

func (h *ScanDefinitionsHandler) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListScanDefinitions(r.Context())
	if err != nil {
		slog.Error("listing scan definitions", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list scan definitions")
		return
	}
	if items == nil {
		items = []model.ScanDefinition{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *ScanDefinitionsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := h.store.GetScanDefinition(r.Context(), id)
	if err != nil {
		slog.Error("getting scan definition", "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	if d == nil {
		writeError(w, http.StatusNotFound, "scan definition not found")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *ScanDefinitionsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req scanDefRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := req.validateKind(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := req.validateScope(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	def := req.toModel()
	next, err := computeNextRunAt(def.Schedule, def.Enabled)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid schedule: "+err.Error())
		return
	}
	def.NextRunAt = next
	out, err := h.store.CreateScanDefinition(r.Context(), def)
	if err != nil {
		slog.Error("creating scan definition", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create scan definition")
		return
	}
	claims := middleware.GetClaims(r.Context())
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: out.TenantID, EventType: audit.EventScanDefCreated,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "scan_definition", ResourceID: out.ID,
		Payload: map[string]any{"name": out.Name, "kind": out.Kind},
	})
	writeJSON(w, http.StatusCreated, out)
}

func (h *ScanDefinitionsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := h.store.GetScanDefinition(r.Context(), id)
	if err != nil || existing == nil {
		writeError(w, http.StatusNotFound, "scan definition not found")
		return
	}
	var req scanDefRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := req.validateKind(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := req.validateScope(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	def := req.toModel()
	def.ID = existing.ID
	next, err := computeNextRunAt(def.Schedule, def.Enabled)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid schedule: "+err.Error())
		return
	}
	def.NextRunAt = next
	out, err := h.store.UpdateScanDefinition(r.Context(), def)
	if err != nil {
		slog.Error("updating scan definition", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update scan definition")
		return
	}
	if out == nil {
		writeError(w, http.StatusNotFound, "scan definition not found")
		return
	}
	claims := middleware.GetClaims(r.Context())
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: out.TenantID, EventType: audit.EventScanDefUpdated,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "scan_definition", ResourceID: out.ID,
	})
	writeJSON(w, http.StatusOK, out)
}

func (h *ScanDefinitionsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.DeleteScanDefinition(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "scan definition not found")
			return
		}
		slog.Error("deleting scan definition", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete scan definition")
		return
	}
	claims := middleware.GetClaims(r.Context())
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: claims.TenantID, EventType: audit.EventScanDefDeleted,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "scan_definition", ResourceID: id,
	})
	w.WriteHeader(http.StatusNoContent)
}

// Execute dispatches the definition now, bypassing next_run_at.
// last_run_at is updated; next_run_at is untouched so the next scheduled
// run is unaffected (ADR 007 D5).
func (h *ScanDefinitionsHandler) Execute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	def, err := h.store.GetScanDefinition(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	if def == nil {
		writeError(w, http.StatusNotFound, "scan definition not found")
		return
	}
	if err := h.disp.Execute(r.Context(), *def); err != nil {
		slog.Error("executing scan definition", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "dispatch failed: "+err.Error())
		return
	}
	now := time.Now().UTC()
	_ = h.store.SetScanDefinitionLastRun(r.Context(), id, now, "dispatched")
	claims := middleware.GetClaims(r.Context())
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: def.TenantID, EventType: audit.EventScanDefExecuted,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "scan_definition", ResourceID: id,
		Payload: map[string]any{"name": def.Name},
	})
	w.WriteHeader(http.StatusAccepted)
}

func (h *ScanDefinitionsHandler) Enable(w http.ResponseWriter, r *http.Request) {
	h.setEnabled(w, r, true)
}

func (h *ScanDefinitionsHandler) Disable(w http.ResponseWriter, r *http.Request) {
	h.setEnabled(w, r, false)
}

func (h *ScanDefinitionsHandler) setEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id := r.PathValue("id")
	def, err := h.store.GetScanDefinition(r.Context(), id)
	if err != nil || def == nil {
		writeError(w, http.StatusNotFound, "scan definition not found")
		return
	}
	var next *time.Time
	if enabled {
		n, err := computeNextRunAt(def.Schedule, true)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid stored schedule: "+err.Error())
			return
		}
		next = n
	}
	if err := h.store.SetScanDefinitionEnabled(r.Context(), id, enabled, next); err != nil {
		slog.Error("toggling scan definition", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Coverage reports how many endpoints the definition would run against.
// For asset_endpoint scope: 1. For collection scope: the resolved set
// size + a sample of ids. For cidr scope: 0 until the CIDR-range
// resolver lands in P4.
func (h *ScanDefinitionsHandler) Coverage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	def, err := h.store.GetScanDefinition(r.Context(), id)
	if err != nil || def == nil {
		writeError(w, http.StatusNotFound, "scan definition not found")
		return
	}
	resp := map[string]any{"endpoint_count": 0, "matched_endpoints": []string{}}
	switch def.ScopeKind {
	case model.ScanDefinitionScopeAssetEndpoint:
		resp["endpoint_count"] = 1
		if def.AssetEndpointID != nil {
			resp["matched_endpoints"] = []string{*def.AssetEndpointID}
		}
	case model.ScanDefinitionScopeCollection:
		if def.CollectionID == nil {
			break
		}
		ids, err := h.store.CollectionEndpointIDs(r.Context(), *def.CollectionID)
		if err != nil {
			slog.Error("resolving collection endpoints", "id", *def.CollectionID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed")
			return
		}
		sample := ids
		if len(sample) > 25 {
			sample = sample[:25]
		}
		resp["endpoint_count"] = len(ids)
		resp["matched_endpoints"] = sample
	}
	writeJSON(w, http.StatusOK, resp)
}
