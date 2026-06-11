package store

import (
	"context"
	"time"

	"github.com/jtb75/silkstrand/backoffice/internal/model"
)

// Store defines the data access interface for the backoffice.
type Store interface {
	// Data Centers
	ListDataCenters(ctx context.Context) ([]model.DataCenter, error)
	GetDataCenter(ctx context.Context, id string) (*model.DataCenter, error)
	GetDataCenterByName(ctx context.Context, name string) (*model.DataCenter, error)
	CreateDataCenter(ctx context.Context, dc model.DataCenter) (*model.DataCenter, error)
	UpdateDataCenter(ctx context.Context, id string, dc model.DataCenter) (*model.DataCenter, error)
	DeleteDataCenter(ctx context.Context, id string) error
	UpdateDataCenterHealth(ctx context.Context, id string, status string) error

	// Tenants
	ListTenants(ctx context.Context) ([]model.Tenant, error)
	GetTenant(ctx context.Context, id string) (*model.Tenant, error)
	CreateTenant(ctx context.Context, t model.Tenant) (*model.Tenant, error)
	UpdateTenant(ctx context.Context, id string, name *string, config []byte) (*model.Tenant, error)
	UpdateTenantStatus(ctx context.Context, id string, status string) error
	UpdateTenantProvisioning(ctx context.Context, id string, provStatus string, dcTenantID *string) error
	ListTenantsByDataCenter(ctx context.Context, dcID string) ([]model.Tenant, error)
	DeleteTenant(ctx context.Context, id string) error

	// Admin Users
	GetAdminByEmail(ctx context.Context, email string) (*model.AdminUser, error)
	CreateAdmin(ctx context.Context, email, passwordHash, role string) (*model.AdminUser, error)
	CountAdmins(ctx context.Context) (int, error)

	// Tenant Users (end users of the tenant frontend)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
	GetUserByID(ctx context.Context, id string) (*model.User, error)
	CreateUser(ctx context.Context, email, passwordHash string) (*model.User, error)
	UpdateUserPassword(ctx context.Context, userID, passwordHash string) error
	UpdateUserDisplayName(ctx context.Context, userID, displayName string) error
	TouchUserLogin(ctx context.Context, userID string) error
	MarkUserEmailVerified(ctx context.Context, userID string) error
	ListAllUsers(ctx context.Context) ([]model.UserListItem, error)
	GetUserDetail(ctx context.Context, id string) (*model.UserDetail, error)
	UpdateUserStatus(ctx context.Context, id, status string) error
	DeleteUser(ctx context.Context, id string) error

	// Memberships
	CreateMembership(ctx context.Context, userID, tenantID, role string) (*model.Membership, error)
	DeleteMembership(ctx context.Context, userID, tenantID string) error
	GetMembership(ctx context.Context, userID, tenantID string) (*model.Membership, error)
	ListMembershipsByUser(ctx context.Context, userID string) ([]model.MembershipSummary, error)
	ListMembershipsByTenant(ctx context.Context, tenantID string) ([]model.Membership, error)
	ListTenantMembers(ctx context.Context, tenantID string) ([]model.TenantMember, error)
	UpdateMembershipStatus(ctx context.Context, userID, tenantID, status string) error
	UpdateMembershipRole(ctx context.Context, userID, tenantID, role string) error
	CountActiveAdmins(ctx context.Context, tenantID string) (int, error)
	UpdateInvitationToken(ctx context.Context, id string, tokenHash []byte, expiresAt time.Time) error
	ListPendingInvitations(ctx context.Context, tenantID string) ([]model.PendingInvite, error)
	DeleteInvitation(ctx context.Context, id, tenantID string) error
	CountMembershipsByUser(ctx context.Context, userID string) (int, error)

	// Invitations
	CreateInvitation(ctx context.Context, tenantID, email, role string, tokenHash []byte, expiresAt time.Time, invitedByAdmin *string) (*model.Invitation, error)
	GetInvitationByTokenHash(ctx context.Context, tokenHash []byte) (*model.Invitation, error)
	MarkInvitationAccepted(ctx context.Context, id string) error

	// Password resets
	CreatePasswordReset(ctx context.Context, userID string, tokenHash []byte, expiresAt time.Time) error
	GetPasswordResetByTokenHash(ctx context.Context, tokenHash []byte) (userID string, expiresAt time.Time, usedAt *time.Time, err error)
	MarkPasswordResetUsed(ctx context.Context, tokenHash []byte) error

	// Audit log
	LogAudit(ctx context.Context, entry model.AuditEntry) error
	ListAuditLog(ctx context.Context, filter AuditFilter) ([]model.AuditEntry, error)

	// Health
	Ping(ctx context.Context) error
}

// AuditFilter constrains which audit entries are returned. All fields optional.
type AuditFilter struct {
	TenantID *string
	ActorID  *string
	Action   *string
	Limit    int
}
