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
	"github.com/jtb75/silkstrand/api/internal/rules"
	"github.com/jtb75/silkstrand/api/internal/store"
)

// CollectionsHandler serves the ADR 006 D5 `collections` CRUD surface.
// Scopes are asset / endpoint / finding; the predicate evaluator's
// scope-aware dispatcher lands in P4, so in P1 we accept + persist
// arbitrary predicates without validating them beyond JSON-wellformedness.
type CollectionsHandler struct {
	store store.Store
	audit audit.Writer
}

func NewCollectionsHandler(s store.Store, aw audit.Writer) *CollectionsHandler {
	return &CollectionsHandler{store: s, audit: aw}
}

type createCollectionRequest struct {
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	Scope             string          `json:"scope"`
	Predicate         json.RawMessage `json:"predicate"`
	IsDashboardWidget bool            `json:"is_dashboard_widget"`
	WidgetKind        string          `json:"widget_kind"`
}

func (h *CollectionsHandler) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListCollections(r.Context())
	if err != nil {
		slog.Error("listing collections", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list collections")
		return
	}
	if items == nil {
		items = []model.Collection{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *CollectionsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := h.store.GetCollection(r.Context(), id)
	if err != nil {
		slog.Error("getting collection", "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	if c == nil {
		writeError(w, http.StatusNotFound, "collection not found")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *CollectionsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createCollectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Scope == "" {
		req.Scope = model.CollectionScopeEndpoint
	}
	switch req.Scope {
	case model.CollectionScopeAsset, model.CollectionScopeEndpoint, model.CollectionScopeFinding:
	default:
		writeError(w, http.StatusBadRequest, "invalid scope: must be asset | endpoint | finding")
		return
	}
	var desc *string
	if req.Description != "" {
		d := req.Description
		desc = &d
	}
	var widget *string
	if req.WidgetKind != "" {
		w := req.WidgetKind
		widget = &w
	}
	c := model.Collection{
		Name:              req.Name,
		Description:       desc,
		Scope:             req.Scope,
		Predicate:         req.Predicate,
		IsDashboardWidget: req.IsDashboardWidget,
		WidgetKind:        widget,
	}
	out, err := h.store.CreateCollection(r.Context(), c)
	if err != nil {
		slog.Error("creating collection", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create collection")
		return
	}
	claims := middleware.GetClaims(r.Context())
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: out.TenantID, EventType: audit.EventCollectionCreated,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "collection", ResourceID: out.ID,
		Payload: map[string]any{"name": out.Name, "scope": out.Scope, "resource_label": out.Name},
	})
	writeJSON(w, http.StatusCreated, out)
}

func (h *CollectionsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := h.store.GetCollection(r.Context(), id)
	if err != nil || existing == nil {
		writeError(w, http.StatusNotFound, "collection not found")
		return
	}
	var req createCollectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.Scope != "" {
		existing.Scope = req.Scope
	}
	if len(req.Predicate) > 0 {
		existing.Predicate = req.Predicate
	}
	if req.Description != "" {
		d := req.Description
		existing.Description = &d
	}
	if req.WidgetKind != "" {
		wk := req.WidgetKind
		existing.WidgetKind = &wk
	}
	existing.IsDashboardWidget = req.IsDashboardWidget
	out, err := h.store.UpdateCollection(r.Context(), *existing)
	if err != nil {
		slog.Error("updating collection", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update collection")
		return
	}
	if out == nil {
		writeError(w, http.StatusNotFound, "collection not found")
		return
	}
	claims := middleware.GetClaims(r.Context())
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: out.TenantID, EventType: audit.EventCollectionUpdated,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "collection", ResourceID: out.ID,
		Payload: map[string]any{"resource_label": out.Name},
	})
	writeJSON(w, http.StatusOK, out)
}

func (h *CollectionsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.DeleteCollection(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "collection not found")
			return
		}
		slog.Error("deleting collection", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete collection")
		return
	}
	claims := middleware.GetClaims(r.Context())
	h.audit.Emit(r.Context(), audit.Event{
		TenantID: claims.TenantID, EventType: audit.EventCollectionDeleted,
		ActorType: audit.ActorUser, ActorID: claimsActorID(claims),
		ResourceType: "collection", ResourceID: id,
	})
	w.WriteHeader(http.StatusNoContent)
}

// previewLimit caps the sample in preview responses (ADR 006 D5 — UI spec).
const previewLimit = 50

type previewRequest struct {
	Scope     string          `json:"scope"`
	Predicate json.RawMessage `json:"predicate"`
}

// Preview evaluates a predicate and returns {count, sample, scope}.
// Handles two shapes:
//   - POST /collections/{id}/preview  (saved collection — empty body OK)
//   - POST /collections/preview       (ad-hoc — body required)
func (h *CollectionsHandler) Preview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var scope string
	var pred json.RawMessage

	if id != "" {
		c, err := h.store.GetCollection(r.Context(), id)
		if err != nil {
			slog.Error("preview: load collection", "error", err)
			writeError(w, http.StatusInternalServerError, "failed")
			return
		}
		if c == nil {
			writeError(w, http.StatusNotFound, "collection not found")
			return
		}
		scope, pred = c.Scope, c.Predicate
	} else {
		var req previewRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Scope == "" {
			req.Scope = model.CollectionScopeEndpoint
		}
		scope, pred = req.Scope, req.Predicate
	}

	count, sample, err := evaluateCollection(r.Context(), h.store, scope, pred, previewLimit, false)
	if err != nil {
		slog.Error("preview: evaluate", "error", err, "scope", scope)
		writeError(w, http.StatusInternalServerError, "failed to evaluate predicate")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scope":  scope,
		"count":  count,
		"sample": sample,
	})
}

// Members returns the full id list (plus minimal display fields) for
// rows matching the saved collection's predicate. Not paginated yet —
// callers that need large lists should page via scope-specific APIs.
func (h *CollectionsHandler) Members(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := h.store.GetCollection(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	if c == nil {
		writeError(w, http.StatusNotFound, "collection not found")
		return
	}
	count, members, err := evaluateCollection(r.Context(), h.store, c.Scope, c.Predicate, 0, true)
	if err != nil {
		slog.Error("members: evaluate", "error", err, "collection", id)
		writeError(w, http.StatusInternalServerError, "failed to evaluate predicate")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scope":   c.Scope,
		"count":   count,
		"members": members,
	})
}

// evaluateCollection dispatches on scope and runs the predicate matcher
// in Go. When `limit` is 0 and `all` is true, returns every match.
// Otherwise samples up to `limit` matches alongside the total count.
func evaluateCollection(
	ctx context.Context,
	s store.Store,
	scope string,
	predicate json.RawMessage,
	limit int,
	all bool,
) (count int, sample []any, err error) {
	switch scope {
	case model.CollectionScopeAsset:
		assets, lerr := s.ListAllAssetsTenant(ctx)
		if lerr != nil {
			return 0, nil, lerr
		}
		for i := range assets {
			ok, merr := rules.Match(predicate, rules.ScopeAsset, &assets[i])
			if merr != nil {
				return 0, nil, merr
			}
			if !ok {
				continue
			}
			count++
			if all || len(sample) < limit {
				sample = append(sample, map[string]any{
					"id":        assets[i].ID,
					"hostname":  assets[i].Hostname,
					"ip":        assets[i].PrimaryIP,
					"source":    assets[i].Source,
					"last_seen": assets[i].LastSeen,
				})
			}
		}
	case model.CollectionScopeEndpoint:
		views, lerr := s.ListAllEndpointViewsTenant(ctx)
		if lerr != nil {
			return 0, nil, lerr
		}
		for i := range views {
			ev := rules.EndpointView{Asset: &views[i].Asset, Endpoint: &views[i].Endpoint}
			ok, merr := rules.Match(predicate, rules.ScopeEndpoint, ev)
			if merr != nil {
				return 0, nil, merr
			}
			if !ok {
				continue
			}
			count++
			if all || len(sample) < limit {
				sample = append(sample, map[string]any{
					"id":         views[i].Endpoint.ID,
					"asset_id":   views[i].Asset.ID,
					"ip":         views[i].Asset.PrimaryIP,
					"hostname":   views[i].Asset.Hostname,
					"port":       views[i].Endpoint.Port,
					"service":    views[i].Endpoint.Service,
					"version":    views[i].Endpoint.Version,
					"last_seen":  views[i].Endpoint.LastSeen,
				})
			}
		}
	case model.CollectionScopeFinding:
		findings, lerr := s.ListAllFindingsTenant(ctx)
		if lerr != nil {
			return 0, nil, lerr
		}
		for i := range findings {
			ok, merr := rules.Match(predicate, rules.ScopeFinding, &findings[i])
			if merr != nil {
				return 0, nil, merr
			}
			if !ok {
				continue
			}
			count++
			if all || len(sample) < limit {
				sample = append(sample, map[string]any{
					"id":                findings[i].ID,
					"asset_endpoint_id": findings[i].AssetEndpointID,
					"severity":          findings[i].Severity,
					"title":             findings[i].Title,
					"status":            findings[i].Status,
					"cve_id":            findings[i].CVEID,
					"last_seen":         findings[i].LastSeen,
				})
			}
		}
	default:
		return 0, nil, errors.New("unsupported scope")
	}
	if sample == nil {
		sample = []any{}
	}
	return count, sample, nil
}
