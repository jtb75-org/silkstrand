package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jtb75/silkstrand/backoffice/internal/audit"
	"github.com/jtb75/silkstrand/backoffice/internal/model"
	"github.com/jtb75/silkstrand/backoffice/internal/store"
)

// UserHandler serves the backoffice's cross-tenant user management endpoints.
// These are authed with the backoffice admin JWT (not the tenant user JWT).
type UserHandler struct {
	store store.Store
}

func NewUserHandler(s store.Store) *UserHandler {
	return &UserHandler{store: s}
}

// GET /api/v1/users
func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListAllUsers(r.Context())
	if err != nil {
		slog.Error("listing users", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list users")
		return
	}
	if users == nil {
		users = []model.UserListItem{}
	}
	writeJSON(w, http.StatusOK, users)
}

// GET /api/v1/users/{id}
func (h *UserHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	detail, err := h.store.GetUserDetail(r.Context(), id)
	if err != nil {
		slog.Error("getting user detail", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get user")
		return
	}
	if detail == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// PUT /api/v1/users/{id}/status
// Body: {status: "active" | "suspended"}
func (h *UserHandler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Status != model.UserStatusActive && req.Status != model.UserStatusSuspended {
		writeError(w, http.StatusBadRequest, "status must be 'active' or 'suspended'")
		return
	}
	if err := h.store.UpdateUserStatus(r.Context(), id, req.Status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		slog.Error("updating user status", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update user status")
		return
	}
	action := audit.ActionUserActivate
	if req.Status == model.UserStatusSuspended {
		action = audit.ActionUserSuspend
	}
	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     action,
		TargetType: "user",
		TargetID:   id,
		Metadata:   map[string]any{"status": req.Status},
	})
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/users/{id}
func (h *UserHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.DeleteUser(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		slog.Error("deleting user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete user")
		return
	}
	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionUserDelete,
		TargetType: "user",
		TargetID:   id,
	})
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/v1/users/{id}/memberships/{tenant_id}/status
// Body: {status: "active" | "suspended"}
func (h *UserHandler) UpdateMembershipStatus(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	tenantID := r.PathValue("tenant_id")
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
	if err := h.store.UpdateMembershipStatus(r.Context(), userID, tenantID, req.Status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "membership not found")
			return
		}
		slog.Error("updating membership status", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update membership status")
		return
	}
	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionUserMembershipSet,
		TargetType: "membership",
		TargetID:   userID,
		TenantID:   tenantID,
		Metadata:   map[string]any{"user_id": userID, "status": req.Status},
	})
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/users/{id}/memberships/{tenant_id}
func (h *UserHandler) DeleteMembership(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	tenantID := r.PathValue("tenant_id")
	if err := h.store.DeleteMembership(r.Context(), userID, tenantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "membership not found")
			return
		}
		slog.Error("deleting membership", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to remove membership")
		return
	}
	audit.Log(r.Context(), h.store, r, audit.Entry{
		Action:     audit.ActionUserMembershipRm,
		TargetType: "membership",
		TargetID:   userID,
		TenantID:   tenantID,
		Metadata:   map[string]any{"user_id": userID},
	})
	w.WriteHeader(http.StatusNoContent)
}
