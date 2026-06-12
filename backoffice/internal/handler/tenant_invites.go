package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jtb75/silkstrand/backoffice/internal/audit"
	"github.com/jtb75/silkstrand/backoffice/internal/crypto"
	"github.com/jtb75/silkstrand/backoffice/internal/middleware"
	"github.com/jtb75/silkstrand/backoffice/internal/model"
)

// Admin-side invite management for a tenant. The tenant-auth handler already
// exposes the same operations to *tenant* admins; these are the *backoffice
// admin* equivalents (admin JWT, tenant id from the path), so an operator can
// bootstrap or recover a tenant's first invite without a tenant user existing.

const adminInviteExpiry = 7 * 24 * time.Hour

// GET /api/v1/tenants/{id}/invites — pending (unaccepted) invitations.
func (h *TenantHandler) ListInvites(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	invites, err := h.store.ListPendingInvitations(r.Context(), tenantID)
	if err != nil {
		slog.Error("listing invitations", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list invitations")
		return
	}
	if invites == nil {
		invites = []model.PendingInvite{}
	}
	writeJSON(w, http.StatusOK, invites)
}

// GET /api/v1/tenants/{id}/members — accepted users (memberships joined to users).
func (h *TenantHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	members, err := h.store.ListTenantMembers(r.Context(), tenantID)
	if err != nil {
		slog.Error("listing tenant members", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list members")
		return
	}
	if members == nil {
		members = []model.TenantMember{}
	}
	writeJSON(w, http.StatusOK, members)
}

// POST /api/v1/tenants/{id}/invites — body {email, role}. Creates + emails an invite.
func (h *TenantHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")

	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}
	if req.Role == "" {
		req.Role = model.MembershipRoleMember
	}
	if req.Role != model.MembershipRoleAdmin && req.Role != model.MembershipRoleMember {
		writeError(w, http.StatusBadRequest, "role must be 'admin' or 'member'")
		return
	}

	tenant, err := h.store.GetTenant(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tenant lookup failed")
		return
	}
	if tenant == nil {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	plaintext, tokenHash, err := crypto.NewURLToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token generation failed")
		return
	}
	var invitedBy *string
	if claims := middleware.GetAdminClaims(r.Context()); claims != nil && claims.AdminID != "" {
		invitedBy = &claims.AdminID
	}
	expiry := time.Now().Add(adminInviteExpiry)
	if _, err := h.store.CreateInvitation(r.Context(), tenantID, req.Email, req.Role, tokenHash, expiry, invitedBy); err != nil {
		slog.Error("creating invitation", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create invitation")
		return
	}

	inviteURL := h.tenantWebURL + "/accept-invite?token=" + plaintext
	if h.mailer == nil {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "created_but_email_disabled"})
		return
	}
	if err := h.mailer.SendInvite(req.Email, inviteURL, tenant.Name); err != nil {
		slog.Warn("sending invitation email", "error", err)
		writeJSON(w, http.StatusAccepted, map[string]string{
			"status": "created_but_email_failed",
			"error":  err.Error(),
		})
		return
	}
	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionMemberInvite,
		TargetType: "invitation",
		TenantID:   tenantID,
		Metadata:   map[string]any{"email": req.Email, "role": req.Role},
	})
	writeJSON(w, http.StatusCreated, map[string]string{"status": "invited"})
}

// POST /api/v1/tenants/{id}/invites/{inviteId}/resend — rotate the token and re-email.
func (h *TenantHandler) ResendInvite(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	inviteID := r.PathValue("inviteId")

	invites, err := h.store.ListPendingInvitations(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to look up invitation")
		return
	}
	var match *model.PendingInvite
	for i := range invites {
		if invites[i].ID == inviteID {
			match = &invites[i]
			break
		}
	}
	if match == nil {
		writeError(w, http.StatusNotFound, "invitation not found")
		return
	}

	tenant, err := h.store.GetTenant(r.Context(), tenantID)
	if err != nil || tenant == nil {
		writeError(w, http.StatusInternalServerError, "tenant lookup failed")
		return
	}

	plaintext, tokenHash, err := crypto.NewURLToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token generation failed")
		return
	}
	expiry := time.Now().Add(adminInviteExpiry)
	if err := h.store.UpdateInvitationToken(r.Context(), inviteID, tokenHash, expiry); err != nil {
		slog.Error("rotating invitation token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to regenerate invitation")
		return
	}
	inviteURL := h.tenantWebURL + "/accept-invite?token=" + plaintext
	if h.mailer == nil {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "regenerated_but_email_disabled"})
		return
	}
	if err := h.mailer.SendInvite(match.Email, inviteURL, tenant.Name); err != nil {
		slog.Warn("resending invitation email", "error", err)
		writeJSON(w, http.StatusAccepted, map[string]string{
			"status": "regenerated_but_email_failed",
			"error":  err.Error(),
		})
		return
	}
	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionInvitationResend,
		TargetType: "invitation",
		TenantID:   tenantID,
		Metadata:   map[string]any{"email": match.Email},
	})
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/tenants/{id}/invites/{inviteId} — revoke a pending invite.
func (h *TenantHandler) DeleteInvite(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	inviteID := r.PathValue("inviteId")
	if err := h.store.DeleteInvitation(r.Context(), inviteID, tenantID); err != nil {
		writeError(w, http.StatusNotFound, "invitation not found")
		return
	}
	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionInvitationCancel,
		TargetType: "invitation",
		TenantID:   tenantID,
	})
	w.WriteHeader(http.StatusNoContent)
}
