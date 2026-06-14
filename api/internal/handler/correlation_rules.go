package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jtb75/silkstrand/api/internal/audit"
	"github.com/jtb75/silkstrand/api/internal/middleware"
	"github.com/jtb75/silkstrand/api/internal/model"
	rulesengine "github.com/jtb75/silkstrand/api/internal/rules"
	"github.com/jtb75/silkstrand/api/internal/store"
)

// CorrelationRulesHandler serves the ADR 006 D6 rule shape where rule
// bodies reference a Collection by id instead of inlining a predicate.
// P4 wires full CRUD + scope/trigger compatibility validation against
// the collections table.
type CorrelationRulesHandler struct {
	store store.Store
	audit audit.Writer
}

func NewCorrelationRulesHandler(s store.Store, aw audit.Writer) *CorrelationRulesHandler {
	return &CorrelationRulesHandler{store: s, audit: aw}
}

// CreateRuleRequest — ADR 006 D6 body shape:
//
//	{
//	  "name": "auto-scan-new-postgres",
//	  "trigger": "asset_discovered",
//	  "enabled": true,
//	  "body": {
//	    "match":   { "collection_id": "<uuid>" },
//	    "actions": [ ... ]
//	  }
//	}
type CreateRuleRequest struct {
	Name    string          `json:"name"`
	Trigger string          `json:"trigger"`
	Enabled *bool           `json:"enabled,omitempty"`
	Body    json.RawMessage `json:"body"`
}

func (h *CorrelationRulesHandler) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListCorrelationRules(r.Context())
	if err != nil {
		slog.Error("listing rules", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list rules")
		return
	}
	if items == nil {
		items = []model.CorrelationRule{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *CorrelationRulesHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	r0, err := h.store.GetCorrelationRule(r.Context(), id)
	if err != nil {
		slog.Error("getting rule", "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	if r0 == nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	writeJSON(w, http.StatusOK, r0)
}

func (h *CorrelationRulesHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !isValidTrigger(req.Trigger) {
		writeError(w, http.StatusBadRequest, "invalid trigger: must be asset_discovered | asset_event | finding")
		return
	}
	if code, msg := validateRuleBody(r.Context(), h.store, req.Trigger, req.Body); code != 0 {
		writeError(w, code, msg)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	rule := model.CorrelationRule{
		Name:    req.Name,
		Trigger: req.Trigger,
		Enabled: enabled,
		Body:    req.Body,
	}
	out, err := h.store.CreateCorrelationRule(r.Context(), rule)
	if err != nil {
		slog.Error("creating rule", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create rule")
		return
	}
	claims := middleware.GetClaims(r.Context())
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: out.TenantID, EventType: audit.EventRuleCreated,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "rule", ResourceID: out.ID,
		Payload: map[string]any{"name": out.Name, "trigger": out.Trigger, "resource_label": out.Name},
	})
	writeJSON(w, http.StatusCreated, out)
}

func (h *CorrelationRulesHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := h.store.GetCorrelationRule(r.Context(), id)
	if err != nil || existing == nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	var req CreateRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Trigger != "" {
		if !isValidTrigger(req.Trigger) {
			writeError(w, http.StatusBadRequest, "invalid trigger")
			return
		}
		existing.Trigger = req.Trigger
	}
	if len(req.Body) > 0 {
		existing.Body = req.Body
	}
	if code, msg := validateRuleBody(r.Context(), h.store, existing.Trigger, existing.Body); code != 0 {
		writeError(w, code, msg)
		return
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	out, err := h.store.UpdateCorrelationRule(r.Context(), *existing)
	if err != nil {
		slog.Error("updating rule", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update rule")
		return
	}
	if out == nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	claims := middleware.GetClaims(r.Context())
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: out.TenantID, EventType: audit.EventRuleUpdated,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "rule", ResourceID: out.ID,
		Payload: map[string]any{"name": out.Name, "resource_label": out.Name},
	})
	writeJSON(w, http.StatusOK, out)
}

func (h *CorrelationRulesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.DeleteCorrelationRule(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "rule not found")
			return
		}
		slog.Error("deleting rule", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete rule")
		return
	}
	claims := middleware.GetClaims(r.Context())
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: claims.TenantID, EventType: audit.EventRuleDeleted,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "rule", ResourceID: id,
	})
	w.WriteHeader(http.StatusNoContent)
}

func isValidTrigger(t string) bool {
	switch t {
	case model.RuleTriggerAssetDiscovered, model.RuleTriggerAssetEvent, model.RuleTriggerFinding:
		return true
	}
	return false
}

// validateRuleBody enforces ADR 006 D6 + D5 scope compatibility:
//   - body must parse into {match:{collection_id}, actions}
//   - collection must exist
//   - collection.scope must be compatible with the rule trigger
//
// Returns (0, "") on success; (status, message) on validation failure.
func validateRuleBody(ctx context.Context, s store.Store, trigger string, body json.RawMessage) (int, string) {
	if len(body) == 0 {
		return http.StatusBadRequest, "body is required"
	}
	var parsed rulesengine.RuleBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return http.StatusBadRequest, "body must be JSON object {match, actions}"
	}
	if len(parsed.Match) == 0 {
		return http.StatusBadRequest, "body.match is required"
	}
	var ref rulesengine.MatchRef
	if err := json.Unmarshal(parsed.Match, &ref); err != nil || ref.CollectionID == "" {
		return http.StatusBadRequest, "body.match must be {collection_id: <uuid>} (ADR 006 D6)"
	}
	c, err := s.GetCollection(ctx, ref.CollectionID)
	if err != nil {
		slog.Warn("rule validate: collection load", "error", err)
		return http.StatusInternalServerError, "failed to validate collection"
	}
	if c == nil {
		return http.StatusBadRequest, "referenced collection not found"
	}
	if !triggerScopeCompatible(trigger, c.Scope) {
		return http.StatusBadRequest,
			"trigger/scope mismatch: asset_discovered & asset_event require asset/endpoint scope; finding trigger requires finding scope"
	}
	return 0, ""
}

func triggerScopeCompatible(trigger, scope string) bool {
	switch trigger {
	case model.RuleTriggerAssetDiscovered, model.RuleTriggerAssetEvent:
		return scope == model.CollectionScopeAsset || scope == model.CollectionScopeEndpoint
	case model.RuleTriggerFinding:
		return scope == model.CollectionScopeFinding
	}
	return false
}