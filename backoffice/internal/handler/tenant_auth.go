package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jtb75/silkstrand/backoffice/internal/audit"
	"github.com/jtb75/silkstrand/backoffice/internal/crypto"
	"github.com/jtb75/silkstrand/backoffice/internal/mailer"
	"github.com/jtb75/silkstrand/backoffice/internal/middleware"
	"github.com/jtb75/silkstrand/backoffice/internal/model"
	"github.com/jtb75/silkstrand/backoffice/internal/store"
)

const (
	tenantJWTExpiry     = time.Hour
	inviteExpiry        = 7 * 24 * time.Hour
	passwordResetExpiry = time.Hour
)

// TenantAuthHandler handles authentication for tenant end users.
type TenantAuthHandler struct {
	store        store.Store
	mailer       mailer.Mailer
	jwtSecret    string
	tenantWebURL string // Base URL for building invite / reset links in emails
}

func NewTenantAuthHandler(s store.Store, m mailer.Mailer, jwtSecret, tenantWebURL string) *TenantAuthHandler {
	return &TenantAuthHandler{
		store:        s,
		mailer:       m,
		jwtSecret:    jwtSecret,
		tenantWebURL: tenantWebURL,
	}
}

// GET /api/v1/tenant-auth/invitation-preview?token=...
// Public endpoint that lets the frontend render the right form (set-password
// vs sign-in) before the user submits. Returns 404 if token is invalid or
// already accepted.
func (h *TenantAuthHandler) PreviewInvitation(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	if tok == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	inv, err := h.store.GetInvitationByTokenHash(r.Context(), crypto.HashToken(tok))
	if err != nil {
		slog.Error("preview invitation", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to look up invitation")
		return
	}
	if inv == nil || inv.AcceptedAt != nil || time.Now().After(inv.ExpiresAt) {
		writeError(w, http.StatusNotFound, "invitation not found, expired, or already accepted")
		return
	}

	// Is there already an account with this email?
	user, err := h.store.GetUserByEmail(r.Context(), inv.Email)
	if err != nil {
		slog.Error("preview invitation: user lookup", "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	existingUser := user != nil && user.PasswordHash != ""

	tenant, err := h.store.GetTenant(r.Context(), inv.TenantID)
	tenantName := ""
	if err == nil && tenant != nil {
		tenantName = tenant.Name
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"email":         inv.Email,
		"role":          inv.Role,
		"tenant_name":   tenantName,
		"existing_user": existingUser,
	})
}

// POST /api/v1/tenant-auth/accept-invite
// Body: {token, password}
// Creates the user if new, sets/replaces password, marks invite accepted,
// creates the tenant membership, returns a JWT.
func (h *TenantAuthHandler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Token == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "token and password are required")
		return
	}

	inv, err := h.store.GetInvitationByTokenHash(r.Context(), crypto.HashToken(req.Token))
	if err != nil {
		slog.Error("getting invitation", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to look up invitation")
		return
	}
	if inv == nil {
		writeError(w, http.StatusNotFound, "invitation not found or invalid")
		return
	}
	if inv.AcceptedAt != nil {
		writeError(w, http.StatusGone, "invitation already accepted")
		return
	}
	if time.Now().After(inv.ExpiresAt) {
		writeError(w, http.StatusGone, "invitation has expired")
		return
	}

	// Look up any existing user with this email — they may already have an
	// account from another tenant.
	user, err := h.store.GetUserByEmail(r.Context(), inv.Email)
	if err != nil {
		slog.Error("looking up user by email", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to look up user")
		return
	}

	if user != nil && user.PasswordHash != "" {
		// Existing user with an established password — verify they know it.
		// We never overwrite a real password from an invite flow; that would
		// let anyone with the invite link take over the account.
		if err := crypto.CheckPassword(user.PasswordHash, req.Password); err != nil {
			writeError(w, http.StatusUnauthorized, "incorrect password for existing account")
			return
		}
	} else {
		// New user, or existing user that never set a password — create/set.
		hash, err := crypto.HashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if user == nil {
			user, err = h.store.CreateUser(r.Context(), inv.Email, hash)
			if err != nil {
				slog.Error("creating user", "error", err)
				writeError(w, http.StatusInternalServerError, "failed to create user")
				return
			}
		} else {
			if err := h.store.UpdateUserPassword(r.Context(), user.ID, hash); err != nil {
				slog.Error("setting user password", "error", err)
				writeError(w, http.StatusInternalServerError, "failed to set password")
				return
			}
			user.PasswordHash = hash
		}
	}
	// Invite click proves email ownership.
	if user.EmailVerifiedAt == nil {
		_ = h.store.MarkUserEmailVerified(r.Context(), user.ID)
	}

	// Enforce membership cap.
	count, err := h.store.CountMembershipsByUser(r.Context(), user.ID)
	if err != nil {
		slog.Error("counting memberships", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check memberships")
		return
	}
	existing, err := h.store.GetMembership(r.Context(), user.ID, inv.TenantID)
	if err != nil {
		slog.Error("checking existing membership", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check existing membership")
		return
	}
	if existing == nil && count >= model.MaxMembershipsPerUser {
		writeError(w, http.StatusForbidden, "user has reached the maximum number of tenants")
		return
	}

	if _, err := h.store.CreateMembership(r.Context(), user.ID, inv.TenantID, inv.Role); err != nil {
		slog.Error("creating membership", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create membership")
		return
	}
	if err := h.store.MarkInvitationAccepted(r.Context(), inv.ID); err != nil {
		slog.Error("marking invitation accepted", "error", err)
	}

	h.issueJWTForTenant(w, r, user, inv.TenantID, inv.Role)
}

// POST /api/v1/tenant-auth/login
// Body: {email, password}
// Returns JWT scoped to the user's most recent tenant (first membership).
func (h *TenantAuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	user, err := h.store.GetUserByEmail(r.Context(), req.Email)
	if err != nil {
		slog.Error("login: getting user", "error", err)
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	if user == nil || user.PasswordHash == "" {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	if err := crypto.CheckPassword(user.PasswordHash, req.Password); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	if user.Status == model.UserStatusSuspended {
		writeError(w, http.StatusForbidden, "account suspended — contact your administrator")
		return
	}

	memberships, err := h.store.ListMembershipsByUser(r.Context(), user.ID)
	if err != nil {
		slog.Error("login: listing memberships", "error", err)
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	if len(memberships) == 0 {
		writeError(w, http.StatusForbidden, "no active tenant memberships")
		return
	}
	_ = h.store.TouchUserLogin(r.Context(), user.ID)

	m := memberships[0]
	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionLogin,
		TargetType: "user", TargetID: user.ID, TenantID: m.TenantID,
	})
	h.issueJWTForTenant(w, r, user, m.TenantID, m.Role)
}

// POST /api/v1/tenant-auth/switch-org (authenticated)
// Body: {tenant_id}
// Verifies the authenticated user belongs to tenant_id, issues a new JWT
// scoped to that tenant.
func (h *TenantAuthHandler) SwitchOrg(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetTenantClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		TenantID string `json:"tenant_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant_id is required")
		return
	}

	membership, err := h.store.GetMembership(r.Context(), claims.Sub, req.TenantID)
	if err != nil {
		slog.Error("switch-org: getting membership", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to switch")
		return
	}
	if membership == nil {
		writeError(w, http.StatusForbidden, "not a member of this tenant")
		return
	}
	if membership.Status != model.MembershipStatusActive {
		writeError(w, http.StatusForbidden, "your access to this tenant has been suspended")
		return
	}

	user, err := h.store.GetUserByID(r.Context(), claims.Sub)
	if err != nil || user == nil {
		writeError(w, http.StatusInternalServerError, "failed to load user")
		return
	}
	h.issueJWTForTenant(w, r, user, req.TenantID, membership.Role)
}

// GET /api/v1/tenant-auth/me (authenticated)
// Returns the user and all active memberships.
func (h *TenantAuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetTenantClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	user, err := h.store.GetUserByID(r.Context(), claims.Sub)
	if err != nil || user == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if user.Status == model.UserStatusSuspended {
		writeError(w, http.StatusForbidden, "account suspended")
		return
	}
	memberships, err := h.store.ListMembershipsByUser(r.Context(), user.ID)
	if err != nil {
		slog.Error("me: listing memberships", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list memberships")
		return
	}
	if memberships == nil {
		memberships = []model.MembershipSummary{}
	}

	// Find the active membership so we can include the DC API URL — the
	// frontend needs it to call the right DC for scans/targets/etc.
	var activeDCAPIURL string
	for _, m := range memberships {
		if m.TenantID == claims.BoTenantID {
			activeDCAPIURL = m.DCAPIURL
			break
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user":        user,
		"memberships": memberships,
		"active": map[string]string{
			"tenant_id":  claims.BoTenantID,
			"dc_id":      claims.DCID,
			"dc_api_url": activeDCAPIURL,
			"role":       claims.Role,
		},
	})
}

// POST /api/v1/tenant-auth/forgot-password
// Body: {email}
// Always returns 204 so attackers can't enumerate registered emails.
func (h *TenantAuthHandler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		// Still return 204 — don't leak shape either.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	user, err := h.store.GetUserByEmail(r.Context(), req.Email)
	if err != nil {
		slog.Error("forgot-password: getting user", "error", err)
		// Don't leak to caller, but log.
	}
	if user != nil {
		plaintext, hash, err := crypto.NewURLToken()
		if err == nil {
			expiry := time.Now().Add(passwordResetExpiry)
			if err := h.store.CreatePasswordReset(r.Context(), user.ID, hash, expiry); err == nil {
				resetURL := h.tenantWebURL + "/reset-password?token=" + plaintext
				if err := h.mailer.SendPasswordReset(user.Email, resetURL); err != nil {
					slog.Warn("sending password reset email", "error", err)
				}
			} else {
				slog.Error("creating password reset", "error", err)
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/tenant-auth/reset-password
// Body: {token, password}
func (h *TenantAuthHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Token == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "token and password are required")
		return
	}

	hash := crypto.HashToken(req.Token)
	userID, expiresAt, usedAt, err := h.store.GetPasswordResetByTokenHash(r.Context(), hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "reset link not found or invalid")
			return
		}
		slog.Error("reset-password: looking up token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	if usedAt != nil {
		writeError(w, http.StatusGone, "reset link already used")
		return
	}
	if time.Now().After(expiresAt) {
		writeError(w, http.StatusGone, "reset link expired")
		return
	}

	pwHash, err := crypto.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.UpdateUserPassword(r.Context(), userID, pwHash); err != nil {
		slog.Error("reset-password: updating password", "error", err)
		writeError(w, http.StatusInternalServerError, "failed")
		return
	}
	_ = h.store.MarkPasswordResetUsed(r.Context(), hash)
	w.WriteHeader(http.StatusNoContent)
}

// issueJWTForTenant mints a JWT scoped to the given tenant and writes it.
//
// The tenant_id claim is the **DC's** tenant ID (tenants.dc_tenant_id),
// not the backoffice's. The DC validates the JWT and looks up the tenant
// by that ID in its own database — it has no knowledge of the backoffice's
// primary keys. The backoffice's tenant UUID is kept out of the token.
func (h *TenantAuthHandler) issueJWTForTenant(w http.ResponseWriter, r *http.Request, user *model.User, tenantID, role string) {
	tenant, err := h.store.GetTenant(r.Context(), tenantID)
	if err != nil || tenant == nil {
		writeError(w, http.StatusInternalServerError, "tenant lookup failed")
		return
	}
	if tenant.DCTenantID == nil || *tenant.DCTenantID == "" {
		writeError(w, http.StatusFailedDependency, "tenant not provisioned on its data center")
		return
	}
	dc, err := h.store.GetDataCenter(r.Context(), tenant.DataCenterID)
	if err != nil || dc == nil {
		writeError(w, http.StatusInternalServerError, "data center lookup failed")
		return
	}

	token, err := middleware.CreateTenantJWT(
		h.jwtSecret, user.ID, user.Email, *tenant.DCTenantID, tenant.ID, tenant.DataCenterID, role, tenantJWTExpiry)
	if err != nil {
		slog.Error("creating tenant JWT", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token,
		"user": map[string]string{
			"id":    user.ID,
			"email": user.Email,
		},
		"active": map[string]string{
			"tenant_id":      tenantID, // Backoffice tenant UUID (for UI routing)
			"dc_tenant_id":   *tenant.DCTenantID,
			"data_center_id": tenant.DataCenterID,
			"dc_api_url":     dc.APIURL,
			"role":           role,
		},
	})
}

// GET /api/v1/tenant-auth/members (authenticated)
// Returns the list of users in the caller's active tenant.
func (h *TenantAuthHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetTenantClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	members, err := h.store.ListTenantMembers(r.Context(), claims.BoTenantID)
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

// POST /api/v1/tenant-auth/invites (authenticated, admin-only)
// Body: {email, role}
func (h *TenantAuthHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetTenantClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if claims.Role != model.MembershipRoleAdmin {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}

	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Email == "" {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}
	if req.Role != model.MembershipRoleAdmin && req.Role != model.MembershipRoleMember {
		writeError(w, http.StatusBadRequest, "role must be 'admin' or 'member'")
		return
	}

	tenant, err := h.store.GetTenant(r.Context(), claims.BoTenantID)
	if err != nil || tenant == nil {
		writeError(w, http.StatusInternalServerError, "tenant lookup failed")
		return
	}

	plaintext, tokenHash, err := crypto.NewURLToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token generation failed")
		return
	}
	expiry := time.Now().Add(inviteExpiry)
	if _, err := h.store.CreateInvitation(r.Context(), claims.BoTenantID, req.Email, req.Role, tokenHash, expiry, nil); err != nil {
		slog.Error("creating invitation", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create invitation")
		return
	}
	inviteURL := h.tenantWebURL + "/accept-invite?token=" + plaintext
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
		TenantID:   claims.BoTenantID,
		Metadata:   map[string]any{"email": req.Email, "role": req.Role},
	})
	writeJSON(w, http.StatusCreated, map[string]string{"status": "invited"})
}

// DELETE /api/v1/tenant-auth/members/{user_id} (authenticated, admin-only)
func (h *TenantAuthHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetTenantClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if claims.Role != model.MembershipRoleAdmin {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	userID := r.PathValue("user_id")
	if userID == claims.Sub {
		writeError(w, http.StatusBadRequest, "cannot remove yourself")
		return
	}
	if existing, _ := h.store.GetMembership(r.Context(), userID, claims.BoTenantID); existing != nil &&
		existing.Role == model.MembershipRoleAdmin && existing.Status == model.MembershipStatusActive {
		if count, _ := h.store.CountActiveAdmins(r.Context(), claims.BoTenantID); count <= 1 {
			writeError(w, http.StatusConflict, "cannot remove the last admin in this tenant")
			return
		}
	}
	if err := h.store.DeleteMembership(r.Context(), userID, claims.BoTenantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "membership not found")
			return
		}
		slog.Error("removing member", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to remove member")
		return
	}
	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionMemberRemove,
		TargetType: "membership",
		TargetID:   userID,
		TenantID:   claims.BoTenantID,
		Metadata:   map[string]any{"user_id": userID},
	})
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/tenant-auth/invitations (authenticated, admin-only)
// Lists pending invitations for the caller's active tenant.
func (h *TenantAuthHandler) ListInvitations(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetTenantClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if claims.Role != model.MembershipRoleAdmin {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	invites, err := h.store.ListPendingInvitations(r.Context(), claims.BoTenantID)
	if err != nil {
		slog.Error("listing pending invitations", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list invitations")
		return
	}
	if invites == nil {
		invites = []model.PendingInvite{}
	}
	writeJSON(w, http.StatusOK, invites)
}

// DELETE /api/v1/tenant-auth/invitations/{id} (authenticated, admin-only)
func (h *TenantAuthHandler) CancelInvitation(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetTenantClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if claims.Role != model.MembershipRoleAdmin {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	id := r.PathValue("id")
	if err := h.store.DeleteInvitation(r.Context(), id, claims.BoTenantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "invitation not found")
			return
		}
		slog.Error("deleting invitation", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to cancel invitation")
		return
	}
	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionInvitationCancel,
		TargetType: "invitation",
		TargetID:   id,
		TenantID:   claims.BoTenantID,
	})
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/v1/tenant-auth/members/{user_id}/status (authenticated, admin-only)
// Body: {status: "active" | "suspended"}
func (h *TenantAuthHandler) UpdateMemberStatus(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetTenantClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if claims.Role != model.MembershipRoleAdmin {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	userID := r.PathValue("user_id")
	if userID == claims.Sub {
		writeError(w, http.StatusBadRequest, "cannot suspend yourself")
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Status != model.MembershipStatusActive && req.Status != model.MembershipStatusSuspended {
		writeError(w, http.StatusBadRequest, "status must be 'active' or 'suspended'")
		return
	}
	// Last-admin protection: blocks suspending the lone remaining admin.
	if req.Status == model.MembershipStatusSuspended {
		if existing, _ := h.store.GetMembership(r.Context(), userID, claims.BoTenantID); existing != nil &&
			existing.Role == model.MembershipRoleAdmin && existing.Status == model.MembershipStatusActive {
			if count, _ := h.store.CountActiveAdmins(r.Context(), claims.BoTenantID); count <= 1 {
				writeError(w, http.StatusConflict, "cannot suspend the last admin in this tenant")
				return
			}
		}
	}
	if err := h.store.UpdateMembershipStatus(r.Context(), userID, claims.BoTenantID, req.Status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "membership not found")
			return
		}
		slog.Error("updating membership status", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update membership status")
		return
	}
	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionMemberStatus,
		TargetType: "membership",
		TargetID:   userID,
		TenantID:   claims.BoTenantID,
		Metadata:   map[string]any{"user_id": userID, "status": req.Status},
	})
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/v1/tenant-auth/members/{user_id}/role (admin-only)
// Body: {role: "admin" | "member"}
func (h *TenantAuthHandler) UpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetTenantClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if claims.Role != model.MembershipRoleAdmin {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	userID := r.PathValue("user_id")
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Role != model.MembershipRoleAdmin && req.Role != model.MembershipRoleMember {
		writeError(w, http.StatusBadRequest, "role must be 'admin' or 'member'")
		return
	}

	// Last-admin protection: if demoting an admin to member, require
	// there be another active admin remaining.
	if req.Role == model.MembershipRoleMember {
		existing, err := h.store.GetMembership(r.Context(), userID, claims.BoTenantID)
		if err != nil || existing == nil {
			writeError(w, http.StatusNotFound, "membership not found")
			return
		}
		if existing.Role == model.MembershipRoleAdmin && existing.Status == model.MembershipStatusActive {
			if count, err := h.store.CountActiveAdmins(r.Context(), claims.BoTenantID); err == nil && count <= 1 {
				writeError(w, http.StatusConflict, "cannot demote the last admin in this tenant")
				return
			}
		}
	}

	if err := h.store.UpdateMembershipRole(r.Context(), userID, claims.BoTenantID, req.Role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "membership not found")
			return
		}
		slog.Error("updating membership role", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update role")
		return
	}
	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionMemberRole,
		TargetType: "membership",
		TargetID:   userID,
		TenantID:   claims.BoTenantID,
		Metadata:   map[string]any{"user_id": userID, "role": req.Role},
	})
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/tenant-auth/invitations/{id}/resend (admin-only)
// Rotates the token + expiry on a pending invitation and re-sends the email.
func (h *TenantAuthHandler) ResendInvitation(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetTenantClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if claims.Role != model.MembershipRoleAdmin {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	id := r.PathValue("id")

	// Look up the full list for this tenant so we can pick up the one
	// matching the id (simpler than adding a GetInvitationByID method).
	invites, err := h.store.ListPendingInvitations(r.Context(), claims.BoTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to look up invitation")
		return
	}
	var match *model.PendingInvite
	for i := range invites {
		if invites[i].ID == id {
			match = &invites[i]
			break
		}
	}
	if match == nil {
		writeError(w, http.StatusNotFound, "invitation not found")
		return
	}

	tenant, err := h.store.GetTenant(r.Context(), claims.BoTenantID)
	if err != nil || tenant == nil {
		writeError(w, http.StatusInternalServerError, "tenant lookup failed")
		return
	}

	plaintext, tokenHash, err := crypto.NewURLToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token generation failed")
		return
	}
	expiry := time.Now().Add(inviteExpiry)
	if err := h.store.UpdateInvitationToken(r.Context(), id, tokenHash, expiry); err != nil {
		slog.Error("rotating invitation token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to regenerate invitation")
		return
	}
	// The invitation was regenerated here; audit regardless of whether the
	// follow-up email send succeeds.
	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionInvitationResend,
		TargetType: "invitation",
		TargetID:   id,
		TenantID:   claims.BoTenantID,
		Metadata:   map[string]any{"email": match.Email},
	})
	inviteURL := h.tenantWebURL + "/accept-invite?token=" + plaintext
	if err := h.mailer.SendInvite(match.Email, inviteURL, tenant.Name); err != nil {
		slog.Warn("resending invitation email", "error", err)
		writeJSON(w, http.StatusAccepted, map[string]string{
			"status": "regenerated_but_email_failed",
			"error":  err.Error(),
		})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/v1/tenant-auth/me/password (authenticated)
// Body: {current_password, new_password}
func (h *TenantAuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetTenantClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, "current_password and new_password are required")
		return
	}
	user, err := h.store.GetUserByID(r.Context(), claims.Sub)
	if err != nil || user == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err := crypto.CheckPassword(user.PasswordHash, req.CurrentPassword); err != nil {
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	hash, err := crypto.HashPassword(req.NewPassword)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.UpdateUserPassword(r.Context(), user.ID, hash); err != nil {
		slog.Error("updating password", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update password")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/v1/tenant-auth/me (authenticated)
// Body: {display_name}
func (h *TenantAuthHandler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetTenantClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.store.UpdateUserDisplayName(r.Context(), claims.Sub, req.DisplayName); err != nil {
		slog.Error("updating display name", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update profile")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
