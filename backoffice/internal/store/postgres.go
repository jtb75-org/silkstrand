package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jtb75/silkstrand/backoffice/internal/model"
)

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(databaseURL string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

func (s *PostgresStore) DB() *sql.DB {
	return s.db
}

func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// --- Data Centers ---

func (s *PostgresStore) ListDataCenters(ctx context.Context) ([]model.DataCenter, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, region, environment, api_url, api_key_encrypted, status, last_health_check, last_health_status, created_at, updated_at
		 FROM data_centers ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing data centers: %w", err)
	}
	defer rows.Close()

	var dcs []model.DataCenter
	for rows.Next() {
		var dc model.DataCenter
		if err := rows.Scan(&dc.ID, &dc.Name, &dc.Region, &dc.Environment, &dc.APIURL, &dc.APIKeyEncrypted,
			&dc.Status, &dc.LastHealthCheck, &dc.LastHealthStatus, &dc.CreatedAt, &dc.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning data center: %w", err)
		}
		dcs = append(dcs, dc)
	}
	return dcs, rows.Err()
}

func (s *PostgresStore) GetDataCenter(ctx context.Context, id string) (*model.DataCenter, error) {
	var dc model.DataCenter
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, region, environment, api_url, api_key_encrypted, status, last_health_check, last_health_status, created_at, updated_at
		 FROM data_centers WHERE id = $1`, id).
		Scan(&dc.ID, &dc.Name, &dc.Region, &dc.Environment, &dc.APIURL, &dc.APIKeyEncrypted,
			&dc.Status, &dc.LastHealthCheck, &dc.LastHealthStatus, &dc.CreatedAt, &dc.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting data center: %w", err)
	}
	return &dc, nil
}

// GetDataCenterByName returns the data center with the given name, or nil if
// none exists. Used by the register-dc bootstrap command for idempotency
// (name is the registration key). If several rows share a name, the most
// recently created is returned.
func (s *PostgresStore) GetDataCenterByName(ctx context.Context, name string) (*model.DataCenter, error) {
	var dc model.DataCenter
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, region, environment, api_url, api_key_encrypted, status, last_health_check, last_health_status, created_at, updated_at
		 FROM data_centers WHERE name = $1 ORDER BY created_at DESC LIMIT 1`, name).
		Scan(&dc.ID, &dc.Name, &dc.Region, &dc.Environment, &dc.APIURL, &dc.APIKeyEncrypted,
			&dc.Status, &dc.LastHealthCheck, &dc.LastHealthStatus, &dc.CreatedAt, &dc.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting data center by name: %w", err)
	}
	return &dc, nil
}

func (s *PostgresStore) CreateDataCenter(ctx context.Context, dc model.DataCenter) (*model.DataCenter, error) {
	env := dc.Environment
	if env == "" {
		env = model.DCEnvStage
	}
	var created model.DataCenter
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO data_centers (name, region, environment, api_url, api_key_encrypted, status)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, name, region, environment, api_url, api_key_encrypted, status, last_health_check, last_health_status, created_at, updated_at`,
		dc.Name, dc.Region, env, dc.APIURL, dc.APIKeyEncrypted, model.DCStatusActive).
		Scan(&created.ID, &created.Name, &created.Region, &created.Environment, &created.APIURL, &created.APIKeyEncrypted,
			&created.Status, &created.LastHealthCheck, &created.LastHealthStatus, &created.CreatedAt, &created.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating data center: %w", err)
	}
	return &created, nil
}

func (s *PostgresStore) UpdateDataCenter(ctx context.Context, id string, dc model.DataCenter) (*model.DataCenter, error) {
	var updated model.DataCenter
	err := s.db.QueryRowContext(ctx,
		`UPDATE data_centers SET name = $1, region = $2, environment = $3, api_url = $4, api_key_encrypted = $5, status = $6, updated_at = NOW()
		 WHERE id = $7
		 RETURNING id, name, region, environment, api_url, api_key_encrypted, status, last_health_check, last_health_status, created_at, updated_at`,
		dc.Name, dc.Region, dc.Environment, dc.APIURL, dc.APIKeyEncrypted, dc.Status, id).
		Scan(&updated.ID, &updated.Name, &updated.Region, &updated.Environment, &updated.APIURL, &updated.APIKeyEncrypted,
			&updated.Status, &updated.LastHealthCheck, &updated.LastHealthStatus, &updated.CreatedAt, &updated.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("updating data center: %w", err)
	}
	return &updated, nil
}

func (s *PostgresStore) DeleteDataCenter(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE data_centers SET status = $1, updated_at = NOW() WHERE id = $2`, model.DCStatusInactive, id)
	if err != nil {
		return fmt.Errorf("soft-deleting data center: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *PostgresStore) UpdateDataCenterHealth(ctx context.Context, id string, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE data_centers SET last_health_check = NOW(), last_health_status = $1, updated_at = NOW() WHERE id = $2`,
		status, id)
	if err != nil {
		return fmt.Errorf("updating data center health: %w", err)
	}
	return nil
}

// --- Tenants ---

func (s *PostgresStore) ListTenants(ctx context.Context) ([]model.Tenant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, dc_tenant_id, data_center_id, name, status, config, provisioning_status, created_at, updated_at
		 FROM tenants ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing tenants: %w", err)
	}
	defer rows.Close()

	var tenants []model.Tenant
	for rows.Next() {
		var t model.Tenant
		if err := rows.Scan(&t.ID, &t.DCTenantID, &t.DataCenterID, &t.Name, &t.Status, &t.Config,
			&t.ProvisioningStatus, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning tenant: %w", err)
		}
		tenants = append(tenants, t)
	}
	return tenants, rows.Err()
}

func (s *PostgresStore) GetTenant(ctx context.Context, id string) (*model.Tenant, error) {
	var t model.Tenant
	err := s.db.QueryRowContext(ctx,
		`SELECT id, dc_tenant_id, data_center_id, name, status, config, provisioning_status, created_at, updated_at
		 FROM tenants WHERE id = $1`, id).
		Scan(&t.ID, &t.DCTenantID, &t.DataCenterID, &t.Name, &t.Status, &t.Config,
			&t.ProvisioningStatus, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting tenant: %w", err)
	}
	return &t, nil
}

func (s *PostgresStore) CreateTenant(ctx context.Context, t model.Tenant) (*model.Tenant, error) {
	cfg := t.Config
	if cfg == nil {
		cfg = json.RawMessage(`{}`)
	}

	var created model.Tenant
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO tenants (data_center_id, name, config, provisioning_status)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, dc_tenant_id, data_center_id, name, status, config, provisioning_status, created_at, updated_at`,
		t.DataCenterID, t.Name, cfg, model.ProvisioningPending).
		Scan(&created.ID, &created.DCTenantID, &created.DataCenterID, &created.Name, &created.Status, &created.Config,
			&created.ProvisioningStatus, &created.CreatedAt, &created.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating tenant: %w", err)
	}
	return &created, nil
}

func (s *PostgresStore) UpdateTenant(ctx context.Context, id string, name *string, config []byte) (*model.Tenant, error) {
	existing, err := s.GetTenant(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}

	if name != nil {
		existing.Name = *name
	}
	if config != nil {
		existing.Config = config
	}

	var updated model.Tenant
	err = s.db.QueryRowContext(ctx,
		`UPDATE tenants SET name = $1, config = $2, updated_at = NOW()
		 WHERE id = $3
		 RETURNING id, dc_tenant_id, data_center_id, name, status, config, provisioning_status, created_at, updated_at`,
		existing.Name, existing.Config, id).
		Scan(&updated.ID, &updated.DCTenantID, &updated.DataCenterID, &updated.Name, &updated.Status, &updated.Config,
			&updated.ProvisioningStatus, &updated.CreatedAt, &updated.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("updating tenant: %w", err)
	}
	return &updated, nil
}

func (s *PostgresStore) UpdateTenantStatus(ctx context.Context, id string, status string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE tenants SET status = $1, updated_at = NOW() WHERE id = $2`, status, id)
	if err != nil {
		return fmt.Errorf("updating tenant status: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("tenant not found")
	}
	return nil
}

func (s *PostgresStore) UpdateTenantProvisioning(ctx context.Context, id string, provStatus string, dcTenantID *string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE tenants SET provisioning_status = $1, dc_tenant_id = $2, updated_at = NOW() WHERE id = $3`,
		provStatus, dcTenantID, id)
	if err != nil {
		return fmt.Errorf("updating tenant provisioning: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("tenant not found")
	}
	return nil
}

func (s *PostgresStore) DeleteTenant(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM tenants WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting tenant: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *PostgresStore) ListTenantsByDataCenter(ctx context.Context, dcID string) ([]model.Tenant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, dc_tenant_id, data_center_id, name, status, config, provisioning_status, created_at, updated_at
		 FROM tenants WHERE data_center_id = $1 ORDER BY created_at DESC`, dcID)
	if err != nil {
		return nil, fmt.Errorf("listing tenants by data center: %w", err)
	}
	defer rows.Close()

	var tenants []model.Tenant
	for rows.Next() {
		var t model.Tenant
		if err := rows.Scan(&t.ID, &t.DCTenantID, &t.DataCenterID, &t.Name, &t.Status, &t.Config,
			&t.ProvisioningStatus, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning tenant: %w", err)
		}
		tenants = append(tenants, t)
	}
	return tenants, rows.Err()
}

// --- Admin Users ---

func (s *PostgresStore) GetAdminByEmail(ctx context.Context, email string) (*model.AdminUser, error) {
	var a model.AdminUser
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, role, created_at FROM admin_users WHERE email = $1`, email).
		Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Role, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting admin by email: %w", err)
	}
	return &a, nil
}

func (s *PostgresStore) CreateAdmin(ctx context.Context, email, passwordHash, role string) (*model.AdminUser, error) {
	var a model.AdminUser
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO admin_users (email, password_hash, role) VALUES ($1, $2, $3)
		 RETURNING id, email, password_hash, role, created_at`,
		email, passwordHash, role).
		Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Role, &a.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating admin: %w", err)
	}
	return &a, nil
}

func (s *PostgresStore) CountAdmins(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_users`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting admins: %w", err)
	}
	return count, nil
}

// --- Tenant Users ---

func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	var u model.User
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, COALESCE(password_hash, ''), COALESCE(display_name, ''), status, email_verified_at, last_login_at, created_at, updated_at
		   FROM users WHERE email = $1`, email).
		Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Status, &u.EmailVerifiedAt, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting user by email: %w", err)
	}
	return &u, nil
}

func (s *PostgresStore) GetUserByID(ctx context.Context, id string) (*model.User, error) {
	var u model.User
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, COALESCE(password_hash, ''), COALESCE(display_name, ''), status, email_verified_at, last_login_at, created_at, updated_at
		   FROM users WHERE id = $1`, id).
		Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Status, &u.EmailVerifiedAt, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting user by id: %w", err)
	}
	return &u, nil
}

func (s *PostgresStore) CreateUser(ctx context.Context, email, passwordHash string) (*model.User, error) {
	var u model.User
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO users (email, password_hash)
		 VALUES ($1, $2)
		 RETURNING id, email, COALESCE(password_hash, ''), COALESCE(display_name, ''), status, email_verified_at, last_login_at, created_at, updated_at`,
		email, passwordHash).
		Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Status, &u.EmailVerifiedAt, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating user: %w", err)
	}
	return &u, nil
}

func (s *PostgresStore) UpdateUserPassword(ctx context.Context, userID, passwordHash string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = $1, updated_at = NOW() WHERE id = $2`,
		passwordHash, userID)
	if err != nil {
		return fmt.Errorf("updating user password: %w", err)
	}
	return nil
}

func (s *PostgresStore) TouchUserLogin(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET last_login_at = NOW() WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("touching user login: %w", err)
	}
	return nil
}

func (s *PostgresStore) MarkUserEmailVerified(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET email_verified_at = NOW(), updated_at = NOW() WHERE id = $1 AND email_verified_at IS NULL`,
		userID)
	if err != nil {
		return fmt.Errorf("marking email verified: %w", err)
	}
	return nil
}

// --- Memberships ---

func (s *PostgresStore) CreateMembership(ctx context.Context, userID, tenantID, role string) (*model.Membership, error) {
	var m model.Membership
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO memberships (user_id, tenant_id, role)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, tenant_id) DO UPDATE
		   SET role = EXCLUDED.role, status = 'active'
		 RETURNING id, user_id, tenant_id, role, status, created_at`,
		userID, tenantID, role).
		Scan(&m.ID, &m.UserID, &m.TenantID, &m.Role, &m.Status, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating membership: %w", err)
	}
	return &m, nil
}

func (s *PostgresStore) DeleteMembership(ctx context.Context, userID, tenantID string) error {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM memberships WHERE user_id = $1 AND tenant_id = $2`, userID, tenantID)
	if err != nil {
		return fmt.Errorf("deleting membership: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *PostgresStore) GetMembership(ctx context.Context, userID, tenantID string) (*model.Membership, error) {
	var m model.Membership
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, tenant_id, role, status, created_at
		   FROM memberships WHERE user_id = $1 AND tenant_id = $2`,
		userID, tenantID).
		Scan(&m.ID, &m.UserID, &m.TenantID, &m.Role, &m.Status, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting membership: %w", err)
	}
	return &m, nil
}

func (s *PostgresStore) ListMembershipsByUser(ctx context.Context, userID string) ([]model.MembershipSummary, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.tenant_id, t.name, t.data_center_id, dc.api_url, m.role
		   FROM memberships m
		   JOIN tenants t ON t.id = m.tenant_id
		   JOIN data_centers dc ON dc.id = t.data_center_id
		  WHERE m.user_id = $1
		    AND m.status = 'active'
		    AND t.status = 'active'
		    AND dc.status = 'active'
		  ORDER BY t.name`, userID)
	if err != nil {
		return nil, fmt.Errorf("listing memberships: %w", err)
	}
	defer rows.Close()

	var out []model.MembershipSummary
	for rows.Next() {
		var m model.MembershipSummary
		if err := rows.Scan(&m.TenantID, &m.TenantName, &m.DCID, &m.DCAPIURL, &m.Role); err != nil {
			return nil, fmt.Errorf("scanning membership: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListMembershipsByTenant(ctx context.Context, tenantID string) ([]model.Membership, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, tenant_id, role, status, created_at
		   FROM memberships WHERE tenant_id = $1 ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing memberships by tenant: %w", err)
	}
	defer rows.Close()
	var out []model.Membership
	for rows.Next() {
		var m model.Membership
		if err := rows.Scan(&m.ID, &m.UserID, &m.TenantID, &m.Role, &m.Status, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CountMembershipsByUser(ctx context.Context, userID string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memberships WHERE user_id = $1`, userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting memberships: %w", err)
	}
	return n, nil
}

// --- Invitations ---

func (s *PostgresStore) CreateInvitation(ctx context.Context, tenantID, email, role string, tokenHash []byte, expiresAt time.Time, invitedByAdmin *string) (*model.Invitation, error) {
	var inv model.Invitation
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO invitations (tenant_id, email, role, token_hash, expires_at, invited_by_admin)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, tenant_id, email, role, expires_at, accepted_at, invited_by_admin, created_at`,
		tenantID, email, role, tokenHash, expiresAt, invitedByAdmin).
		Scan(&inv.ID, &inv.TenantID, &inv.Email, &inv.Role, &inv.ExpiresAt, &inv.AcceptedAt, &inv.InvitedByAdmin, &inv.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating invitation: %w", err)
	}
	return &inv, nil
}

func (s *PostgresStore) GetInvitationByTokenHash(ctx context.Context, tokenHash []byte) (*model.Invitation, error) {
	var inv model.Invitation
	err := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, email, role, expires_at, accepted_at, invited_by_admin, created_at
		   FROM invitations WHERE token_hash = $1`, tokenHash).
		Scan(&inv.ID, &inv.TenantID, &inv.Email, &inv.Role, &inv.ExpiresAt, &inv.AcceptedAt, &inv.InvitedByAdmin, &inv.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting invitation: %w", err)
	}
	return &inv, nil
}

func (s *PostgresStore) MarkInvitationAccepted(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE invitations SET accepted_at = NOW() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("marking invitation accepted: %w", err)
	}
	return nil
}

// --- Password Resets ---

func (s *PostgresStore) CreatePasswordReset(ctx context.Context, userID string, tokenHash []byte, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO password_resets (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		tokenHash, userID, expiresAt)
	if err != nil {
		return fmt.Errorf("creating password reset: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetPasswordResetByTokenHash(ctx context.Context, tokenHash []byte) (string, time.Time, *time.Time, error) {
	var userID string
	var expiresAt time.Time
	var usedAt *time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, expires_at, used_at FROM password_resets WHERE token_hash = $1`, tokenHash).
		Scan(&userID, &expiresAt, &usedAt)
	if err == sql.ErrNoRows {
		return "", time.Time{}, nil, sql.ErrNoRows
	}
	if err != nil {
		return "", time.Time{}, nil, fmt.Errorf("getting password reset: %w", err)
	}
	return userID, expiresAt, usedAt, nil
}

func (s *PostgresStore) MarkPasswordResetUsed(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE password_resets SET used_at = NOW() WHERE token_hash = $1`, tokenHash)
	if err != nil {
		return fmt.Errorf("marking password reset used: %w", err)
	}
	return nil
}

func (s *PostgresStore) ListTenantMembers(ctx context.Context, tenantID string) ([]model.TenantMember, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT u.id, u.email, m.role, m.status, m.created_at
		   FROM memberships m
		   JOIN users u ON u.id = m.user_id
		  WHERE m.tenant_id = $1
		  ORDER BY m.created_at`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing tenant members: %w", err)
	}
	defer rows.Close()
	var out []model.TenantMember
	for rows.Next() {
		var m model.TenantMember
		if err := rows.Scan(&m.UserID, &m.Email, &m.Role, &m.Status, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateMembershipStatus(ctx context.Context, userID, tenantID, status string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE memberships SET status = $1 WHERE user_id = $2 AND tenant_id = $3`,
		status, userID, tenantID)
	if err != nil {
		return fmt.Errorf("updating membership status: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *PostgresStore) ListPendingInvitations(ctx context.Context, tenantID string) ([]model.PendingInvite, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, email, role, expires_at, created_at
		   FROM invitations
		  WHERE tenant_id = $1
		    AND accepted_at IS NULL
		    AND expires_at > NOW()
		  ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing pending invitations: %w", err)
	}
	defer rows.Close()
	var out []model.PendingInvite
	for rows.Next() {
		var p model.PendingInvite
		if err := rows.Scan(&p.ID, &p.Email, &p.Role, &p.ExpiresAt, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning invitation: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteInvitation removes a pending invitation. Requires tenantID match
// so an admin can only cancel invitations for their own tenant.
func (s *PostgresStore) DeleteInvitation(ctx context.Context, id, tenantID string) error {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM invitations WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err != nil {
		return fmt.Errorf("deleting invitation: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *PostgresStore) ListAllUsers(ctx context.Context) ([]model.UserListItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT u.id, u.email, COALESCE(u.display_name, ''), u.status, u.email_verified_at, u.last_login_at, u.created_at, u.updated_at,
		        COALESCE((SELECT COUNT(*) FROM memberships m WHERE m.user_id = u.id), 0) AS tenant_count
		   FROM users u
		  ORDER BY u.email`)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close()
	var out []model.UserListItem
	for rows.Next() {
		var it model.UserListItem
		if err := rows.Scan(&it.ID, &it.Email, &it.DisplayName, &it.Status,
			&it.EmailVerifiedAt, &it.LastLoginAt, &it.CreatedAt, &it.UpdatedAt,
			&it.TenantCount); err != nil {
			return nil, fmt.Errorf("scanning user: %w", err)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetUserDetail(ctx context.Context, id string) (*model.UserDetail, error) {
	user, err := s.GetUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, nil
	}
	// Memberships
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.tenant_id, t.name,
		        t.data_center_id, dc.name, dc.environment,
		        m.role, m.status, m.created_at
		   FROM memberships m
		   JOIN tenants t ON t.id = m.tenant_id
		   JOIN data_centers dc ON dc.id = t.data_center_id
		  WHERE m.user_id = $1
		  ORDER BY t.name`, id)
	if err != nil {
		return nil, fmt.Errorf("listing user memberships: %w", err)
	}
	defer rows.Close()
	memberships := []model.UserMembership{}
	for rows.Next() {
		var m model.UserMembership
		if err := rows.Scan(&m.TenantID, &m.TenantName, &m.DataCenterID, &m.DCName,
			&m.Environment, &m.Role, &m.Status, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning user membership: %w", err)
		}
		memberships = append(memberships, m)
	}
	// Pending invites for this email
	inviteRows, err := s.db.QueryContext(ctx,
		`SELECT id, email, role, expires_at, created_at
		   FROM invitations
		  WHERE email = $1
		    AND accepted_at IS NULL
		    AND expires_at > NOW()
		  ORDER BY created_at DESC`, user.Email)
	if err != nil {
		return nil, fmt.Errorf("listing user pending invites: %w", err)
	}
	defer inviteRows.Close()
	invites := []model.PendingInvite{}
	for inviteRows.Next() {
		var p model.PendingInvite
		if err := inviteRows.Scan(&p.ID, &p.Email, &p.Role, &p.ExpiresAt, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning invite: %w", err)
		}
		invites = append(invites, p)
	}
	return &model.UserDetail{
		User:           *user,
		Memberships:    memberships,
		PendingInvites: invites,
	}, nil
}

func (s *PostgresStore) UpdateUserStatus(ctx context.Context, id, status string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE users SET status = $1, updated_at = NOW() WHERE id = $2`, status, id)
	if err != nil {
		return fmt.Errorf("updating user status: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *PostgresStore) DeleteUser(ctx context.Context, id string) error {
	// memberships/password_resets have ON DELETE CASCADE so this removes them.
	result, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting user: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *PostgresStore) UpdateMembershipRole(ctx context.Context, userID, tenantID, role string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE memberships SET role = $1 WHERE user_id = $2 AND tenant_id = $3`,
		role, userID, tenantID)
	if err != nil {
		return fmt.Errorf("updating membership role: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// CountActiveAdmins returns the number of active admin memberships in a tenant.
// Used to prevent removing/suspending/demoting the last admin and locking the
// tenant out.
func (s *PostgresStore) CountActiveAdmins(ctx context.Context, tenantID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memberships
		  WHERE tenant_id = $1 AND role = 'admin' AND status = 'active'`,
		tenantID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting admins: %w", err)
	}
	return n, nil
}

// UpdateInvitationToken rotates the token on an existing, still-pending
// invitation row (used by the resend flow).
func (s *PostgresStore) UpdateInvitationToken(ctx context.Context, id string, tokenHash []byte, expiresAt time.Time) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE invitations
		   SET token_hash = $1, expires_at = $2
		 WHERE id = $3 AND accepted_at IS NULL`,
		tokenHash, expiresAt, id)
	if err != nil {
		return fmt.Errorf("rotating invitation token: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *PostgresStore) UpdateUserDisplayName(ctx context.Context, userID, displayName string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET display_name = $1, updated_at = NOW() WHERE id = $2`,
		displayName, userID)
	if err != nil {
		return fmt.Errorf("updating display name: %w", err)
	}
	return nil
}

// --- Audit Log ---

func (s *PostgresStore) LogAudit(ctx context.Context, e model.AuditEntry) error {
	metadata := e.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage("{}")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log
		   (actor_type, actor_id, actor_email, action, target_type, target_id, tenant_id, ip, metadata)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		e.ActorType, e.ActorID, e.ActorEmail, e.Action,
		e.TargetType, e.TargetID, e.TenantID, e.IP, string(metadata))
	if err != nil {
		return fmt.Errorf("writing audit log: %w", err)
	}
	return nil
}

func (s *PostgresStore) ListAuditLog(ctx context.Context, f AuditFilter) ([]model.AuditEntry, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id, occurred_at, actor_type, actor_id, actor_email, action,
	             target_type, target_id, tenant_id, ip, metadata
	        FROM audit_log
	       WHERE 1=1`
	args := []any{}
	i := 1
	if f.TenantID != nil {
		q += fmt.Sprintf(" AND tenant_id = $%d", i)
		args = append(args, *f.TenantID)
		i++
	}
	if f.ActorID != nil {
		q += fmt.Sprintf(" AND actor_id = $%d", i)
		args = append(args, *f.ActorID)
		i++
	}
	if f.Action != nil {
		q += fmt.Sprintf(" AND action = $%d", i)
		args = append(args, *f.Action)
		i++
	}
	q += fmt.Sprintf(" ORDER BY occurred_at DESC LIMIT $%d", i)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("listing audit log: %w", err)
	}
	defer rows.Close()
	var out []model.AuditEntry
	for rows.Next() {
		var e model.AuditEntry
		var metadata []byte
		if err := rows.Scan(&e.ID, &e.OccurredAt, &e.ActorType, &e.ActorID, &e.ActorEmail, &e.Action,
			&e.TargetType, &e.TargetID, &e.TenantID, &e.IP, &metadata); err != nil {
			return nil, fmt.Errorf("scanning audit entry: %w", err)
		}
		e.Metadata = metadata
		out = append(out, e)
	}
	return out, rows.Err()
}
