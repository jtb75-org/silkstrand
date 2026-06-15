// Package audit provides tiny convenience helpers for writing audit log
// entries from request handlers without every call-site re-implementing
// actor/IP extraction.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/jtb75/silkstrand/backoffice/internal/middleware"
	"github.com/jtb75/silkstrand/backoffice/internal/model"
	"github.com/jtb75/silkstrand/backoffice/internal/store"
)

// Action name constants — keep them dotted + lowercase.
const (
	// Backoffice admin
	ActionTenantCreate      = "tenant.create"
	ActionTenantUpdate      = "tenant.update"
	ActionTenantDelete      = "tenant.delete"
	ActionTenantSuspend     = "tenant.suspend"
	ActionTenantActivate    = "tenant.activate"
	ActionTenantRetry       = "tenant.retry"
	ActionDCCreate          = "datacenter.create"
	ActionDCUpdate          = "datacenter.update"
	ActionDCDelete          = "datacenter.delete"
	ActionUserSuspend       = "user.suspend"
	ActionUserActivate      = "user.activate"
	ActionUserDelete        = "user.delete"
	ActionUserMembershipSet = "user.membership.status"
	ActionUserMembershipRm  = "user.membership.remove"
	// Tenant admin
	ActionMemberInvite       = "member.invite"
	ActionMemberRemove       = "member.remove"
	ActionMemberStatus       = "member.status"
	ActionMemberRole         = "member.role"
	ActionInvitationCancel   = "invitation.cancel"
	ActionInvitationResend   = "invitation.resend"
	// Tenant user
	ActionLogin          = "auth.login"
	ActionLoginFailed    = "auth.login_failed"
	ActionPasswordChange = "auth.password_change"
	ActionPasswordReset  = "auth.password_reset"
	ActionInviteAccepted = "auth.invite_accepted"
)

// Entry is the simplified shape handlers pass; helpers fill the rest.
type Entry struct {
	Action     string
	TargetType string
	TargetID   string
	TenantID   string
	Metadata   map[string]any
}

// Log records an event. Never fails the request — audit failures log at
// warn level but don't propagate.
func Log(ctx context.Context, s store.Store, r *http.Request, e Entry) {
	actorType, actorID, actorEmail := actorFromContext(ctx)
	meta, _ := json.Marshal(e.Metadata)
	entry := model.AuditEntry{
		ActorType: actorType,
		Action:    e.Action,
	}
	if actorID != "" {
		entry.ActorID = &actorID
	}
	if actorEmail != "" {
		entry.ActorEmail = &actorEmail
	}
	if e.TargetType != "" {
		entry.TargetType = &e.TargetType
	}
	if e.TargetID != "" {
		entry.TargetID = &e.TargetID
	}
	if e.TenantID != "" {
		entry.TenantID = &e.TenantID
	}
	if r != nil {
		if ip := clientIP(r); ip != "" {
			entry.IP = &ip
		}
	}
	if len(meta) > 0 && string(meta) != "null" {
		entry.Metadata = meta
	}
	if err := s.LogAudit(ctx, entry); err != nil {
		slog.Warn("audit log write failed", "action", e.Action, "error", err)
	}
}

// actorFromContext figures out which actor (admin, tenant user, or system)
// initiated this request based on whichever claims middleware attached.
func actorFromContext(ctx context.Context) (string, string, string) {
	if c := middleware.GetAdminClaims(ctx); c != nil {
		return model.ActorTypeAdmin, c.AdminID, c.Email
	}
	if c := middleware.GetTenantClaims(ctx); c != nil {
		return model.ActorTypeTenantUser, c.Sub, c.Email
	}
	return model.ActorTypeSystem, "", ""
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
