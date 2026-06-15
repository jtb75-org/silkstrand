package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jtb75/silkstrand/backoffice/internal/audit"
	"github.com/jtb75/silkstrand/backoffice/internal/crypto"
	"github.com/jtb75/silkstrand/backoffice/internal/dcclient"
	"github.com/jtb75/silkstrand/backoffice/internal/mailer"
	"github.com/jtb75/silkstrand/backoffice/internal/model"
	"github.com/jtb75/silkstrand/backoffice/internal/store"
)

type TenantHandler struct {
	store        store.Store
	dc           *dcclient.Client
	mailer       mailer.Mailer
	tenantWebURL string
	encKey       []byte
}

func NewTenantHandler(s store.Store, dc *dcclient.Client, m mailer.Mailer, tenantWebURL string, encKey []byte) *TenantHandler {
	return &TenantHandler{
		store:        s,
		dc:           dc,
		mailer:       m,
		tenantWebURL: tenantWebURL,
		encKey:       encKey,
	}
}

func (h *TenantHandler) List(w http.ResponseWriter, r *http.Request) {
	dcID := r.URL.Query().Get("data_center_id")
	var tenants []model.Tenant
	var err error
	if dcID != "" {
		tenants, err = h.store.ListTenantsByDataCenter(r.Context(), dcID)
	} else {
		tenants, err = h.store.ListTenants(r.Context())
	}
	if err != nil {
		slog.Error("listing tenants", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list tenants")
		return
	}
	if tenants == nil {
		tenants = []model.Tenant{}
	}
	writeJSON(w, http.StatusOK, tenants)
}

func (h *TenantHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tenant, err := h.store.GetTenant(r.Context(), id)
	if err != nil {
		slog.Error("getting tenant", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if tenant == nil {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}
	writeJSON(w, http.StatusOK, tenant)
}

func (h *TenantHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req model.CreateTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" || req.DataCenterID == "" {
		writeError(w, http.StatusBadRequest, "name and data_center_id are required")
		return
	}

	if len(req.Invites) > 3 {
		writeError(w, http.StatusBadRequest, "at most 3 invites allowed")
		return
	}
	for i, inv := range req.Invites {
		if inv.Email == "" {
			writeError(w, http.StatusBadRequest, "invite email is required")
			return
		}
		// Accept both legacy "basic" and canonical "member"; normalise to "member".
		if inv.Role == "basic" {
			req.Invites[i].Role = model.InviteRoleBasic
		}
		if req.Invites[i].Role != model.InviteRoleAdmin && req.Invites[i].Role != model.InviteRoleBasic {
			writeError(w, http.StatusBadRequest, "invite role must be 'admin' or 'member'")
			return
		}
	}

	// Look up the data center
	dc, err := h.store.GetDataCenter(r.Context(), req.DataCenterID)
	if err != nil {
		slog.Error("looking up data center", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to look up data center")
		return
	}
	if dc == nil {
		writeError(w, http.StatusBadRequest, "data center not found")
		return
	}

	// Create tenant record locally
	tenant, err := h.store.CreateTenant(r.Context(), model.Tenant{
		DataCenterID: req.DataCenterID,
		Name:         req.Name,
		Config:       req.Config,
	})
	if err != nil {
		slog.Error("creating tenant", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create tenant")
		return
	}

	// Two-phase provisioning: try to create in DC
	if len(h.encKey) > 0 {
		conn, err := dcConnFromRecord(dc, h.encKey)
		if err != nil {
			slog.Error("decrypting DC API key for tenant provisioning", "error", err)
			if err := h.store.UpdateTenantProvisioning(r.Context(), tenant.ID, model.ProvisioningFailed, nil); err != nil {
				slog.Error("updating tenant provisioning to failed", "error", err)
			}
			// Return 202 to indicate local creation succeeded but DC provisioning failed
			tenant.ProvisioningStatus = model.ProvisioningFailed
			tenant.InviteResults = failAllInvites(req.Invites, "tenant provisioning failed")
			writeJSON(w, http.StatusAccepted, tenant)
			return
		}

		dcTenant, err := h.dc.CreateTenant(*conn, req.Name)
		if err != nil {
			slog.Error("provisioning tenant in DC", "dc_id", dc.ID, "error", err)
			if err := h.store.UpdateTenantProvisioning(r.Context(), tenant.ID, model.ProvisioningFailed, nil); err != nil {
				slog.Error("updating tenant provisioning to failed", "error", err)
			}
			tenant.ProvisioningStatus = model.ProvisioningFailed
			tenant.InviteResults = failAllInvites(req.Invites, "tenant provisioning failed")
			writeJSON(w, http.StatusAccepted, tenant)
			return
		}

		if err := h.store.UpdateTenantProvisioning(r.Context(), tenant.ID, model.ProvisioningProvisioned, &dcTenant.ID); err != nil {
			slog.Error("updating tenant provisioning to provisioned", "error", err)
		}
		tenant.ProvisioningStatus = model.ProvisioningProvisioned
		tenant.DCTenantID = &dcTenant.ID

		// Create invitation rows and email each recipient (best-effort).
		tenant.InviteResults = h.sendInvites(r, tenant.ID, tenant.Name, req.Invites)
	} else if len(req.Invites) > 0 {
		// No encryption key means we can't provision on DC, so no Clerk org either.
		tenant.InviteResults = failAllInvites(req.Invites, "tenant not provisioned; invites skipped")
	}

	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionTenantCreate,
		TargetType: "tenant",
		TargetID:   tenant.ID,
		TenantID:   tenant.ID,
		Metadata: map[string]any{
			"name":           tenant.Name,
			"data_center_id": tenant.DataCenterID,
			"invites":        len(req.Invites),
		},
	})
	writeJSON(w, http.StatusCreated, tenant)
}

// sendInvites creates invitation rows for each requested invite and emails
// the recipient a signed link to accept. Failures are captured per-invite
// and never abort tenant creation.
func (h *TenantHandler) sendInvites(r *http.Request, tenantID, tenantName string, invites []model.TenantInvite) []model.InviteResult {
	if len(invites) == 0 {
		return nil
	}
	if h.mailer == nil {
		return failAllInvites(invites, "mailer not configured")
	}

	expiry := time.Now().Add(7 * 24 * time.Hour)
	results := make([]model.InviteResult, 0, len(invites))
	for _, inv := range invites {
		plaintext, tokenHash, err := crypto.NewURLToken()
		if err != nil {
			results = append(results, model.InviteResult{
				Email: inv.Email, Role: inv.Role, Status: "failed", Error: err.Error(),
			})
			continue
		}
		if _, err := h.store.CreateInvitation(r.Context(), tenantID, inv.Email, inv.Role, tokenHash, expiry, nil); err != nil {
			slog.Warn("creating invitation row", "email", inv.Email, "error", err)
			results = append(results, model.InviteResult{
				Email: inv.Email, Role: inv.Role, Status: "failed", Error: err.Error(),
			})
			continue
		}
		inviteURL := h.tenantWebURL + "/accept-invite?token=" + plaintext
		if err := h.mailer.SendInvite(inv.Email, inviteURL, tenantName); err != nil {
			slog.Warn("sending invitation email", "email", inv.Email, "error", err)
			results = append(results, model.InviteResult{
				Email: inv.Email, Role: inv.Role, Status: "failed", Error: "email send failed",
			})
			continue
		}
		results = append(results, model.InviteResult{
			Email: inv.Email, Role: inv.Role, Status: "invited",
		})
	}
	return results
}

func failAllInvites(invites []model.TenantInvite, reason string) []model.InviteResult {
	results := make([]model.InviteResult, 0, len(invites))
	for _, inv := range invites {
		results = append(results, model.InviteResult{
			Email:  inv.Email,
			Role:   inv.Role,
			Status: "failed",
			Error:  reason,
		})
	}
	return results
}

func (h *TenantHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req model.UpdateTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	tenant, err := h.store.UpdateTenant(r.Context(), id, req.Name, req.Config)
	if err != nil {
		slog.Error("updating tenant", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update tenant")
		return
	}
	if tenant == nil {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionTenantUpdate,
		TargetType: "tenant",
		TargetID:   id,
		TenantID:   id,
		Metadata:   map[string]any{"name": tenant.Name},
	})
	writeJSON(w, http.StatusOK, tenant)
}

func (h *TenantHandler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req model.UpdateTenantStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Status == "" {
		writeError(w, http.StatusBadRequest, "status is required")
		return
	}

	// Look up the tenant to get the DC tenant ID and DC ID
	tenant, err := h.store.GetTenant(r.Context(), id)
	if err != nil {
		slog.Error("getting tenant for status update", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if tenant == nil {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	// Sync status to DC if provisioned
	if tenant.DCTenantID != nil && len(h.encKey) > 0 {
		dc, err := h.store.GetDataCenter(r.Context(), tenant.DataCenterID)
		if err != nil {
			slog.Error("looking up data center for status sync", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to look up data center")
			return
		}
		if dc != nil {
			conn, err := dcConnFromRecord(dc, h.encKey)
			if err != nil {
				slog.Error("decrypting DC API key for status sync", "error", err)
				writeError(w, http.StatusInternalServerError, "failed to sync status to data center")
				return
			}
			if err := h.dc.UpdateTenant(*conn, *tenant.DCTenantID, req.Status); err != nil {
				slog.Error("syncing tenant status to DC", "dc_id", dc.ID, "error", err)
				writeError(w, http.StatusInternalServerError, "failed to sync status to data center")
				return
			}
		}
	}

	if err := h.store.UpdateTenantStatus(r.Context(), id, req.Status); err != nil {
		slog.Error("updating tenant status", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update tenant status")
		return
	}

	tenant.Status = req.Status
	action := audit.ActionTenantActivate
	if req.Status == model.TenantStatusSuspended {
		action = audit.ActionTenantSuspend
	}
	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action: action, TargetType: "tenant", TargetID: id, TenantID: id,
	})
	writeJSON(w, http.StatusOK, tenant)
}

func (h *TenantHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	tenant, err := h.store.GetTenant(r.Context(), id)
	if err != nil {
		slog.Error("getting tenant for delete", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if tenant == nil {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	// Best-effort delete in the DC (soft-delete — sets status=inactive there).
	if tenant.DCTenantID != nil && len(h.encKey) > 0 {
		dc, err := h.store.GetDataCenter(r.Context(), tenant.DataCenterID)
		if err == nil && dc != nil {
			if conn, err := dcConnFromRecord(dc, h.encKey); err == nil {
				if err := h.dc.DeleteTenant(*conn, *tenant.DCTenantID); err != nil {
					slog.Warn("deleting tenant in DC", "tenant_id", tenant.ID, "error", err)
				}
			} else {
				slog.Warn("decrypting DC API key for tenant delete", "error", err)
			}
		}
	}

	if err := h.store.DeleteTenant(r.Context(), id); err != nil {
		slog.Error("deleting tenant", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete tenant")
		return
	}

	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionTenantDelete,
		TargetType: "tenant", TargetID: id, TenantID: id,
		Metadata: map[string]any{"name": tenant.Name},
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *TenantHandler) Retry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	tenant, err := h.store.GetTenant(r.Context(), id)
	if err != nil {
		slog.Error("getting tenant for retry", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	if tenant == nil {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	if tenant.ProvisioningStatus != model.ProvisioningFailed {
		writeError(w, http.StatusBadRequest, "tenant is not in failed provisioning state")
		return
	}

	dc, err := h.store.GetDataCenter(r.Context(), tenant.DataCenterID)
	if err != nil {
		slog.Error("looking up data center for retry", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to look up data center")
		return
	}
	if dc == nil {
		writeError(w, http.StatusInternalServerError, "data center not found")
		return
	}

	if len(h.encKey) == 0 {
		writeError(w, http.StatusInternalServerError, "encryption key not configured")
		return
	}

	conn, err := dcConnFromRecord(dc, h.encKey)
	if err != nil {
		slog.Error("decrypting DC API key for retry", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to decrypt API key")
		return
	}

	dcTenant, err := h.dc.CreateTenant(*conn, tenant.Name)
	if err != nil {
		slog.Error("retrying tenant provisioning in DC", "dc_id", dc.ID, "error", err)
		writeError(w, http.StatusBadGateway, "failed to provision tenant in data center")
		return
	}

	if err := h.store.UpdateTenantProvisioning(r.Context(), tenant.ID, model.ProvisioningProvisioned, &dcTenant.ID); err != nil {
		slog.Error("updating tenant provisioning after retry", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update provisioning status")
		return
	}

	tenant.ProvisioningStatus = model.ProvisioningProvisioned
	tenant.DCTenantID = &dcTenant.ID
	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionTenantRetry,
		TargetType: "tenant",
		TargetID:   id,
		TenantID:   id,
		Metadata:   map[string]any{"name": tenant.Name},
	})
	writeJSON(w, http.StatusOK, tenant)
}
