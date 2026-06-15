package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jtb75/silkstrand/backoffice/internal/audit"
	"github.com/jtb75/silkstrand/backoffice/internal/crypto"
	"github.com/jtb75/silkstrand/backoffice/internal/dcclient"
	"github.com/jtb75/silkstrand/backoffice/internal/model"
	"github.com/jtb75/silkstrand/backoffice/internal/store"
)

type DataCenterHandler struct {
	store  store.Store
	dc     *dcclient.Client
	encKey []byte
}

func NewDataCenterHandler(s store.Store, dc *dcclient.Client, encKey []byte) *DataCenterHandler {
	return &DataCenterHandler{store: s, dc: dc, encKey: encKey}
}

func (h *DataCenterHandler) List(w http.ResponseWriter, r *http.Request) {
	dcs, err := h.store.ListDataCenters(r.Context())
	if err != nil {
		slog.Error("listing data centers", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list data centers")
		return
	}

	// Compute tenant counts per DC from the backoffice DB
	tenants, err := h.store.ListTenants(r.Context())
	if err != nil {
		slog.Error("listing tenants for DC counts", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list tenants")
		return
	}
	counts := make(map[string]int)
	for _, t := range tenants {
		counts[t.DataCenterID]++
	}

	items := make([]model.DataCenterListItem, len(dcs))
	for i, dc := range dcs {
		items[i] = model.DataCenterListItem{
			DataCenter:  dc,
			TenantCount: counts[dc.ID],
		}
	}

	writeJSON(w, http.StatusOK, items)
}

func (h *DataCenterHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dc, err := h.store.GetDataCenter(r.Context(), id)
	if err != nil {
		slog.Error("getting data center", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get data center")
		return
	}
	if dc == nil {
		writeError(w, http.StatusNotFound, "data center not found")
		return
	}

	// Optionally fetch live stats from the DC
	if r.URL.Query().Get("stats") == "true" && dc.Status == model.DCStatusActive && len(h.encKey) > 0 {
		conn, err := dcConnFromRecord(dc, h.encKey)
		if err != nil {
			slog.Error("decrypting DC API key", "error", err)
		} else {
			stats, err := h.dc.GetStats(*conn)
			if err != nil {
				slog.Warn("fetching DC stats", "dc_id", dc.ID, "error", err)
			} else {
				writeJSON(w, http.StatusOK, map[string]any{
					"data_center": dc,
					"stats":       stats,
				})
				return
			}
		}
	}

	writeJSON(w, http.StatusOK, dc)
}

func (h *DataCenterHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req model.CreateDataCenterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" || req.Region == "" || req.APIURL == "" || req.APIKey == "" {
		writeError(w, http.StatusBadRequest, "name, region, api_url, and api_key are required")
		return
	}

	env := req.Environment
	if env == "" {
		env = model.DCEnvStage
	}
	if env != model.DCEnvStage && env != model.DCEnvProd {
		writeError(w, http.StatusBadRequest, "environment must be 'stage' or 'prod'")
		return
	}

	apiURL, err := dcclient.NormalizeBaseURL(req.APIURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid api_url: "+err.Error())
		return
	}

	if len(h.encKey) == 0 {
		writeError(w, http.StatusInternalServerError, "encryption key not configured")
		return
	}

	encrypted, err := crypto.Encrypt([]byte(req.APIKey), h.encKey)
	if err != nil {
		slog.Error("encrypting API key", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to encrypt API key")
		return
	}

	dc, err := h.store.CreateDataCenter(r.Context(), model.DataCenter{
		Name:            req.Name,
		Region:          req.Region,
		Environment:     env,
		APIURL:          apiURL,
		APIKeyEncrypted: encrypted,
	})
	if err != nil {
		slog.Error("creating data center", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create data center")
		return
	}

	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionDCCreate,
		TargetType: "data_center",
		TargetID:   dc.ID,
		Metadata:   map[string]any{"name": dc.Name, "region": dc.Region, "environment": dc.Environment},
	})
	writeJSON(w, http.StatusCreated, dc)
}

func (h *DataCenterHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req model.UpdateDataCenterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	existing, err := h.store.GetDataCenter(r.Context(), id)
	if err != nil {
		slog.Error("getting data center for update", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get data center")
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "data center not found")
		return
	}

	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.Region != nil {
		existing.Region = *req.Region
	}
	if req.Environment != nil {
		if *req.Environment != model.DCEnvStage && *req.Environment != model.DCEnvProd {
			writeError(w, http.StatusBadRequest, "environment must be 'stage' or 'prod'")
			return
		}
		existing.Environment = *req.Environment
	}
	if req.APIURL != nil {
		apiURL, err := dcclient.NormalizeBaseURL(*req.APIURL)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid api_url: "+err.Error())
			return
		}
		existing.APIURL = apiURL
	}
	if req.APIKey != nil {
		if len(h.encKey) == 0 {
			writeError(w, http.StatusInternalServerError, "encryption key not configured")
			return
		}
		encrypted, err := crypto.Encrypt([]byte(*req.APIKey), h.encKey)
		if err != nil {
			slog.Error("encrypting API key", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to encrypt API key")
			return
		}
		existing.APIKeyEncrypted = encrypted
	}
	if req.Status != nil {
		existing.Status = *req.Status
	}

	dc, err := h.store.UpdateDataCenter(r.Context(), id, *existing)
	if err != nil {
		slog.Error("updating data center", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update data center")
		return
	}
	if dc == nil {
		writeError(w, http.StatusNotFound, "data center not found")
		return
	}

	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionDCUpdate,
		TargetType: "data_center",
		TargetID:   dc.ID,
		Metadata:   map[string]any{"name": dc.Name, "region": dc.Region, "environment": dc.Environment, "status": dc.Status},
	})
	writeJSON(w, http.StatusOK, dc)
}

func (h *DataCenterHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Capture the name for the audit trail before deletion (best-effort).
	var name string
	if existing, err := h.store.GetDataCenter(r.Context(), id); err == nil && existing != nil {
		name = existing.Name
	}
	if err := h.store.DeleteDataCenter(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "data center not found")
		return
	}
	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionDCDelete,
		TargetType: "data_center",
		TargetID:   id,
		Metadata:   map[string]any{"name": name},
	})
	w.WriteHeader(http.StatusNoContent)
}
