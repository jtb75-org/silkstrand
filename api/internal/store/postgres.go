// Package store — PostgreSQL implementation of the Store interface.
//
// P1 asset-first refactor shape: legacy recon-pipeline methods (upsert
// discovered_assets, rules, channels, asset sets, one-shots, scan
// results) are gone along with the tables they read. The new surface
// is asset / endpoint / collection / finding / scan_definition; P1
// ships minimal working impls for collections + targets + the read
// side of scans/assets, and stub-returns ErrNotImplemented for the
// entities whose full impls land in P2+.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jtb75/silkstrand/api/internal/model"
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

func (s *PostgresStore) Close() error { return s.db.Close() }
func (s *PostgresStore) DB() *sql.DB  { return s.db }
func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// ======================================================================
// Targets (CIDR / network_range only — ADR 006 D8)
// ======================================================================

func (s *PostgresStore) ListTargets(ctx context.Context) ([]model.Target, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, agent_id, type, identifier, config, environment, credential_source_id, created_at, updated_at
		   FROM targets WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing targets: %w", err)
	}
	defer rows.Close()
	var targets []model.Target
	for rows.Next() {
		var t model.Target
		if err := rows.Scan(&t.ID, &t.TenantID, &t.AgentID, &t.Type, &t.Identifier, &t.Config,
			&t.Environment, &t.CredentialSourceID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning target: %w", err)
		}
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

func (s *PostgresStore) GetTarget(ctx context.Context, id string) (*model.Target, error) {
	tenantID := TenantID(ctx)
	var t model.Target
	err := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, agent_id, type, identifier, config, environment, credential_source_id, created_at, updated_at
		   FROM targets WHERE id = $1 AND tenant_id = $2`, id, tenantID).
		Scan(&t.ID, &t.TenantID, &t.AgentID, &t.Type, &t.Identifier, &t.Config,
			&t.Environment, &t.CredentialSourceID, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting target: %w", err)
	}
	return &t, nil
}

func (s *PostgresStore) GetTargetByID(ctx context.Context, id string) (*model.Target, error) {
	var t model.Target
	err := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, agent_id, type, identifier, config, environment, credential_source_id, created_at, updated_at
		   FROM targets WHERE id = $1`, id).
		Scan(&t.ID, &t.TenantID, &t.AgentID, &t.Type, &t.Identifier, &t.Config,
			&t.Environment, &t.CredentialSourceID, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting target by id: %w", err)
	}
	return &t, nil
}

func (s *PostgresStore) CreateTarget(ctx context.Context, req model.CreateTargetRequest) (*model.Target, error) {
	tenantID := TenantID(ctx)
	cfg := req.Config
	if cfg == nil {
		cfg = json.RawMessage(`{}`)
	}
	var envPtr *string
	if req.Environment != "" {
		e := req.Environment
		envPtr = &e
	}
	var t model.Target
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO targets (tenant_id, agent_id, type, identifier, config, environment)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, tenant_id, agent_id, type, identifier, config, environment, credential_source_id, created_at, updated_at`,
		tenantID, req.AgentID, req.Type, req.Identifier, cfg, envPtr).
		Scan(&t.ID, &t.TenantID, &t.AgentID, &t.Type, &t.Identifier, &t.Config,
			&t.Environment, &t.CredentialSourceID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating target: %w", err)
	}
	return &t, nil
}

func (s *PostgresStore) UpdateTarget(ctx context.Context, id string, req model.UpdateTargetRequest) (*model.Target, error) {
	tenantID := TenantID(ctx)
	existing, err := s.GetTarget(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}
	if req.Type != nil {
		existing.Type = *req.Type
	}
	if req.Identifier != nil {
		existing.Identifier = *req.Identifier
	}
	if req.Config != nil {
		existing.Config = req.Config
	}
	if req.Environment != nil {
		existing.Environment = req.Environment
	}
	if req.AgentID != nil {
		existing.AgentID = req.AgentID
	}
	var t model.Target
	err = s.db.QueryRowContext(ctx,
		`UPDATE targets SET type = $1, identifier = $2, config = $3, environment = $4, agent_id = $5, updated_at = NOW()
		   WHERE id = $6 AND tenant_id = $7
		 RETURNING id, tenant_id, agent_id, type, identifier, config, environment, credential_source_id, created_at, updated_at`,
		existing.Type, existing.Identifier, existing.Config, existing.Environment, existing.AgentID, id, tenantID).
		Scan(&t.ID, &t.TenantID, &t.AgentID, &t.Type, &t.Identifier, &t.Config,
			&t.Environment, &t.CredentialSourceID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("updating target: %w", err)
	}
	return &t, nil
}

func (s *PostgresStore) DeleteTarget(ctx context.Context, id string) error {
	tenantID := TenantID(ctx)
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM targets WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err != nil {
		return fmt.Errorf("deleting target: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpsertTargetByCIDR upserts on the partial unique index
// `targets_cidr_key` (tenant_id, type, identifier) WHERE type IN
// ('cidr','network_range'). On conflict we refresh agent_id so a
// scan_definition that is later bound to a different agent steers
// subsequent ticks to the new agent without churning rows. environment
// is only set on insert; on conflict we leave the existing value in
// place (operators edit targets independently of scheduler dispatch).
func (s *PostgresStore) UpsertTargetByCIDR(ctx context.Context, tenantID, cidr string, agentID *string, environment string) (string, error) {
	var envPtr *string
	if environment != "" {
		e := environment
		envPtr = &e
	}
	var id string
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO targets (tenant_id, type, identifier, agent_id, environment)
		 VALUES ($1, 'cidr', $2, $3, $4)
		 ON CONFLICT (tenant_id, type, identifier)
		   WHERE type IN ('cidr','network_range')
		 DO UPDATE SET agent_id = EXCLUDED.agent_id, updated_at = NOW()
		 RETURNING id`,
		tenantID, cidr, agentID, envPtr).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("upserting cidr target: %w", err)
	}
	return id, nil
}

// ======================================================================
// Scans (execution history; ADR 007 D3). CreateScan is retained for the
// ad-hoc debug path (POST /api/v1/scans) — scope expansion for scan
// definitions lands in P3.
// ======================================================================

const scanCols = `id, tenant_id, scan_definition_id, agent_id, target_id, asset_endpoint_id, bundle_id, scan_type, status, error_message, started_at, completed_at, created_at`

func scanScan(sc *model.Scan, row interface{ Scan(...any) error }) error {
	return row.Scan(&sc.ID, &sc.TenantID, &sc.ScanDefinitionID, &sc.AgentID,
		&sc.TargetID, &sc.AssetEndpointID, &sc.BundleID, &sc.ScanType,
		&sc.Status, &sc.ErrorMessage, &sc.StartedAt, &sc.CompletedAt, &sc.CreatedAt)
}

func (s *PostgresStore) ListScans(ctx context.Context) ([]model.Scan, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+scanCols+` FROM scans WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing scans: %w", err)
	}
	defer rows.Close()
	var scans []model.Scan
	for rows.Next() {
		var sc model.Scan
		if err := scanScan(&sc, rows); err != nil {
			return nil, fmt.Errorf("scanning scan row: %w", err)
		}
		scans = append(scans, sc)
	}
	return scans, rows.Err()
}

func (s *PostgresStore) GetScan(ctx context.Context, id string) (*model.Scan, error) {
	tenantID := TenantID(ctx)
	var sc model.Scan
	row := s.db.QueryRowContext(ctx,
		`SELECT `+scanCols+` FROM scans WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err := scanScan(&sc, row); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting scan: %w", err)
	}
	return &sc, nil
}

func (s *PostgresStore) GetScanByID(ctx context.Context, id string) (*model.Scan, error) {
	var sc model.Scan
	row := s.db.QueryRowContext(ctx, `SELECT `+scanCols+` FROM scans WHERE id = $1`, id)
	if err := scanScan(&sc, row); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting scan by id: %w", err)
	}
	return &sc, nil
}

func (s *PostgresStore) CreateScan(ctx context.Context, req model.CreateScanRequest) (*model.Scan, error) {
	tenantID := TenantID(ctx)
	target, err := s.GetTarget(ctx, req.TargetID)
	if err != nil {
		return nil, fmt.Errorf("looking up target: %w", err)
	}
	if target == nil {
		return nil, fmt.Errorf("target not found")
	}
	scanType := req.ScanType
	if scanType == "" {
		scanType = model.ScanTypeCompliance
	}
	var sc model.Scan
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO scans (tenant_id, agent_id, target_id, bundle_id, scan_type, status)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING `+scanCols,
		tenantID, target.AgentID, req.TargetID, req.BundleID, scanType, model.ScanStatusPending)
	if err := scanScan(&sc, row); err != nil {
		return nil, fmt.Errorf("creating scan: %w", err)
	}
	return &sc, nil
}

func (s *PostgresStore) UpdateScanStatus(ctx context.Context, id, status string) error {
	var query string
	switch status {
	case model.ScanStatusRunning:
		query = `UPDATE scans SET status = $1, started_at = NOW() WHERE id = $2`
	case model.ScanStatusCompleted, model.ScanStatusFailed:
		query = `UPDATE scans SET status = $1, completed_at = NOW() WHERE id = $2`
	default:
		query = `UPDATE scans SET status = $1 WHERE id = $2`
	}
	if _, err := s.db.ExecContext(ctx, query, status, id); err != nil {
		return fmt.Errorf("updating scan status: %w", err)
	}
	return nil
}

func (s *PostgresStore) FailScan(ctx context.Context, id, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE scans SET status = $1, completed_at = NOW(), error_message = NULLIF($2, '') WHERE id = $3`,
		model.ScanStatusFailed, reason, id)
	if err != nil {
		return fmt.Errorf("failing scan: %w", err)
	}
	return nil
}

func (s *PostgresStore) DeleteScan(ctx context.Context, id string) error {
	tenantID := TenantID(ctx)
	if tenantID == "" {
		return fmt.Errorf("tenant not set in context")
	}
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM scans WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err != nil {
		return fmt.Errorf("deleting scan: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *PostgresStore) FailRunningScansForAgent(ctx context.Context, agentID string) (int, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE scans SET status = $1, completed_at = NOW()
		   WHERE agent_id = $2 AND status IN ($3, $4, $5)`,
		model.ScanStatusFailed, agentID, model.ScanStatusPending, model.ScanStatusRunning, model.ScanStatusQueued)
	if err != nil {
		return 0, fmt.Errorf("failing running scans for agent: %w", err)
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}

func (s *PostgresStore) AgentHasRunningScan(ctx context.Context, agentID string) (bool, error) {
	return s.AgentHasRunningScanExcluding(ctx, agentID, "")
}

func (s *PostgresStore) AgentHasRunningScanExcluding(ctx context.Context, agentID, excludeScanID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM scans WHERE agent_id = $1 AND status IN ($2, $3) AND id != COALESCE(NULLIF($4, '')::uuid, '00000000-0000-0000-0000-000000000000'::uuid))`,
		agentID, model.ScanStatusPending, model.ScanStatusRunning, excludeScanID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking agent running scan: %w", err)
	}
	return exists, nil
}

func (s *PostgresStore) OldestQueuedScanForAgent(ctx context.Context, agentID string) (*model.Scan, error) {
	var sc model.Scan
	row := s.db.QueryRowContext(ctx,
		`SELECT `+scanCols+` FROM scans
		   WHERE agent_id = $1 AND status = $2
		   ORDER BY created_at ASC LIMIT 1`,
		agentID, model.ScanStatusQueued)
	if err := scanScan(&sc, row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("loading oldest queued scan: %w", err)
	}
	return &sc, nil
}

func (s *PostgresStore) FailStaleQueuedScans(ctx context.Context, maxAge time.Duration) (int, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE scans
		   SET status = $1, completed_at = NOW(), error_message = 'queued scan timed out'
		   WHERE status = $2 AND created_at < NOW() - make_interval(secs => $3)`,
		model.ScanStatusFailed, model.ScanStatusQueued, int(maxAge.Seconds()))
	if err != nil {
		return 0, fmt.Errorf("failing stale queued scans: %w", err)
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}

// ======================================================================
// Agents
// ======================================================================

func (s *PostgresStore) GetAgent(ctx context.Context, id string) (*model.Agent, error) {
	tenantID := TenantID(ctx)
	var a model.Agent
	err := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, zone, status, last_heartbeat, version, created_at
		   FROM agents WHERE id = $1 AND tenant_id = $2`, id, tenantID).
		Scan(&a.ID, &a.TenantID, &a.Name, &a.Zone, &a.Status, &a.LastHeartbeat, &a.Version, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting agent: %w", err)
	}
	return &a, nil
}

func (s *PostgresStore) GetAgentByID(ctx context.Context, id string) (*model.Agent, error) {
	var a model.Agent
	var keyHash sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, zone, status, last_heartbeat, version, key_hash, next_key_hash, key_rotated_at, created_at
		   FROM agents WHERE id = $1`, id).
		Scan(&a.ID, &a.TenantID, &a.Name, &a.Zone, &a.Status, &a.LastHeartbeat, &a.Version,
			&keyHash, &a.NextKeyHash, &a.KeyRotatedAt, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting agent by id: %w", err)
	}
	a.KeyHash = keyHash.String
	return &a, nil
}

func generateAgentKey() (raw string, hash string, err error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", "", fmt.Errorf("generating random key: %w", err)
	}
	rawKey := hex.EncodeToString(key)
	h := sha256.Sum256([]byte(rawKey))
	return rawKey, hex.EncodeToString(h[:]), nil
}

func (s *PostgresStore) CreateAgent(ctx context.Context, req model.CreateAgentRequest) (*model.Agent, string, error) {
	rawKey, keyHash, err := generateAgentKey()
	if err != nil {
		return nil, "", err
	}
	var a model.Agent
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO agents (tenant_id, name, zone, version, key_hash, auto_discover_pending, discover_cron)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, tenant_id, name, zone, status, last_heartbeat, version, key_hash, next_key_hash, key_rotated_at, created_at`,
		req.TenantID, req.Name, req.Zone, req.Version, keyHash, req.AutoDiscoverPending, req.DiscoverCron).
		Scan(&a.ID, &a.TenantID, &a.Name, &a.Zone, &a.Status, &a.LastHeartbeat, &a.Version,
			&a.KeyHash, &a.NextKeyHash, &a.KeyRotatedAt, &a.CreatedAt)
	if err != nil {
		return nil, "", fmt.Errorf("creating agent: %w", err)
	}
	return &a, rawKey, nil
}

func (s *PostgresStore) UpdateAgentStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET status = $1, last_heartbeat = NOW() WHERE id = $2`, status, id)
	if err != nil {
		return fmt.Errorf("updating agent status: %w", err)
	}
	return nil
}

func (s *PostgresStore) UpdateAgentHeartbeat(ctx context.Context, id, version string) error {
	if version == "" {
		return s.UpdateAgentStatus(ctx, id, "connected")
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET status = 'connected', last_heartbeat = NOW(), version = $1 WHERE id = $2`,
		version, id)
	if err != nil {
		return fmt.Errorf("updating agent heartbeat: %w", err)
	}
	return nil
}

func (s *PostgresStore) RotateAgentKey(ctx context.Context, id string) (string, error) {
	rawKey, keyHash, err := generateAgentKey()
	if err != nil {
		return "", err
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE agents SET next_key_hash = $1 WHERE id = $2`, keyHash, id)
	if err != nil {
		return "", fmt.Errorf("rotating agent key: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return "", fmt.Errorf("agent not found")
	}
	return rawKey, nil
}

func (s *PostgresStore) PromoteAgentKey(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE agents SET key_hash = next_key_hash, next_key_hash = NULL, key_rotated_at = NOW()
		   WHERE id = $1 AND next_key_hash IS NOT NULL`, id)
	if err != nil {
		return fmt.Errorf("promoting agent key: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("agent not found or no pending key rotation")
	}
	return nil
}

func (s *PostgresStore) ListAgents(ctx context.Context) ([]model.Agent, error) {
	tenantID := TenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant not set in context")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, name, zone, status, last_heartbeat, version, created_at
		   FROM agents WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}
	defer rows.Close()
	var out []model.Agent
	for rows.Next() {
		var a model.Agent
		if err := rows.Scan(&a.ID, &a.TenantID, &a.Name, &a.Zone, &a.Status, &a.LastHeartbeat, &a.Version, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning agent: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListAllAgents(ctx context.Context) ([]model.Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, name, zone, status, last_heartbeat, version, created_at
		   FROM agents ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing all agents: %w", err)
	}
	defer rows.Close()
	var agents []model.Agent
	for rows.Next() {
		var a model.Agent
		if err := rows.Scan(&a.ID, &a.TenantID, &a.Name, &a.Zone, &a.Status, &a.LastHeartbeat, &a.Version, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning agent: %w", err)
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

func (s *PostgresStore) DeleteAgent(ctx context.Context, id string) error {
	tenantID := TenantID(ctx)
	if tenantID == "" {
		return fmt.Errorf("tenant not set in context")
	}
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM agents WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err != nil {
		return fmt.Errorf("deleting agent: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ======================================================================
// Install tokens
// ======================================================================

func (s *PostgresStore) CreateInstallToken(ctx context.Context, in CreateInstallTokenInput) error {
	var createdByArg interface{}
	if in.CreatedBy != "" {
		createdByArg = in.CreatedBy
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO install_tokens (token_hash, tenant_id, created_by, expires_at, auto_discover, discover_cron, zone)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		in.TokenHash, in.TenantID, createdByArg, in.ExpiresAt, in.AutoDiscover, in.DiscoverCron, in.Zone)
	if err != nil {
		return fmt.Errorf("creating install token: %w", err)
	}
	return nil
}

func (s *PostgresStore) ConsumeInstallToken(ctx context.Context, tokenHash []byte, agentID string) (*ConsumedInstallToken, error) {
	var out ConsumedInstallToken
	err := s.db.QueryRowContext(ctx,
		`UPDATE install_tokens
		   SET used_at = NOW(), used_agent_id = $2
		 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > NOW()
		 RETURNING tenant_id, auto_discover, discover_cron, zone`,
		tokenHash, agentID).Scan(&out.TenantID, &out.AutoDiscover, &out.DiscoverCron, &out.Zone)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("consuming install token: %w", err)
	}
	return &out, nil
}

// ClaimAutoDiscover atomically clears the agent's pending auto-discover flag
// and returns whether it was set plus the recurring cron (ADR 013 D5). The
// WHERE auto_discover_pending guard makes this fire-once under concurrent
// snapshot reports.
func (s *PostgresStore) ClaimAutoDiscover(ctx context.Context, agentID string) (bool, *string, error) {
	var cron *string
	err := s.db.QueryRowContext(ctx,
		`UPDATE agents SET auto_discover_pending = FALSE
		 WHERE id = $1 AND auto_discover_pending = TRUE
		 RETURNING discover_cron`,
		agentID).Scan(&cron)
	if err == sql.ErrNoRows {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("claiming auto-discover: %w", err)
	}
	return true, cron, nil
}

// ======================================================================
// Bundles
// ======================================================================

func (s *PostgresStore) GetBundle(ctx context.Context, id string) (*model.Bundle, error) {
	var b model.Bundle
	err := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, version, framework, target_type, engine, control_count, gcs_path, signature, tarball_hash, created_at
		   FROM bundles WHERE id = $1`, id).
		Scan(&b.ID, &b.TenantID, &b.Name, &b.Version, &b.Framework, &b.TargetType,
			&b.Engine, &b.ControlCount, &b.GCSPath, &b.Signature, &b.TarballHash, &b.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting bundle: %w", err)
	}
	return &b, nil
}

func (s *PostgresStore) ListBundlesForTenant(ctx context.Context, tenantID string) ([]model.Bundle, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, name, version, framework, target_type, engine, control_count, gcs_path, signature, tarball_hash, created_at
		   FROM bundles WHERE tenant_id IS NULL OR tenant_id = $1 ORDER BY name, version`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing bundles: %w", err)
	}
	defer rows.Close()
	var out []model.Bundle
	for rows.Next() {
		var b model.Bundle
		if err := rows.Scan(&b.ID, &b.TenantID, &b.Name, &b.Version, &b.Framework, &b.TargetType,
			&b.Engine, &b.ControlCount, &b.GCSPath, &b.Signature, &b.TarballHash, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning bundle: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpsertBundle(ctx context.Context, b model.Bundle) (*model.Bundle, error) {
	var out model.Bundle
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO bundles (id, tenant_id, name, version, framework, target_type, engine, control_count, gcs_path, signature, tarball_hash)
		 VALUES (COALESCE(NULLIF($1::text, '')::uuid, uuid_generate_v4()), NULLIF($2::text, '')::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 ON CONFLICT (name, version) DO UPDATE SET
		   framework = EXCLUDED.framework,
		   target_type = EXCLUDED.target_type, engine = EXCLUDED.engine, control_count = EXCLUDED.control_count,
		   gcs_path = EXCLUDED.gcs_path, signature = EXCLUDED.signature, tarball_hash = EXCLUDED.tarball_hash
		 RETURNING id, tenant_id, name, version, framework, target_type, engine, control_count, gcs_path, signature, tarball_hash, created_at`,
		b.ID, b.TenantID, b.Name, b.Version, b.Framework, b.TargetType, b.Engine, b.ControlCount, b.GCSPath, b.Signature, b.TarballHash).
		Scan(&out.ID, &out.TenantID, &out.Name, &out.Version, &out.Framework, &out.TargetType,
			&out.Engine, &out.ControlCount, &out.GCSPath, &out.Signature, &out.TarballHash, &out.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("upserting bundle: %w", err)
	}
	return &out, nil
}

func (s *PostgresStore) ReplaceBundleControls(ctx context.Context, bundleID string, controls []model.BundleControl) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, `DELETE FROM bundle_controls WHERE bundle_id = $1`, bundleID); err != nil {
		return fmt.Errorf("deleting old controls: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO bundle_controls (bundle_id, control_id, name, severity, section, engine, engine_versions, tags)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`)
	if err != nil {
		return fmt.Errorf("preparing insert: %w", err)
	}
	defer stmt.Close()

	for _, c := range controls {
		ev := c.EngineVersions
		if ev == nil {
			ev = []byte("[]")
		}
		tags := c.Tags
		if tags == nil {
			tags = []byte("[]")
		}
		if _, err := stmt.ExecContext(ctx, bundleID, c.ControlID, c.Name, c.Severity, c.Section, c.Engine, ev, tags); err != nil {
			return fmt.Errorf("inserting control %s: %w", c.ControlID, err)
		}
	}

	return tx.Commit()
}

func (s *PostgresStore) ListBundleControls(ctx context.Context, bundleID string) ([]model.BundleControl, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT bundle_id, control_id, name, severity, section, engine, engine_versions, tags
		   FROM bundle_controls WHERE bundle_id = $1 ORDER BY control_id`, bundleID)
	if err != nil {
		return nil, fmt.Errorf("listing bundle controls: %w", err)
	}
	defer rows.Close()
	var out []model.BundleControl
	for rows.Next() {
		var c model.BundleControl
		if err := rows.Scan(&c.BundleID, &c.ControlID, &c.Name, &c.Severity, &c.Section,
			&c.Engine, &c.EngineVersions, &c.Tags); err != nil {
			return nil, fmt.Errorf("scanning bundle control: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListControls(ctx context.Context, tenantID string, filter ControlFilter) ([]ControlRow, int, error) {
	// Build WHERE clauses dynamically.
	where := []string{"(b.tenant_id IS NULL OR b.tenant_id = $1)"}
	args := []interface{}{tenantID}
	idx := 2

	if filter.Framework != "" {
		where = append(where, fmt.Sprintf("b.framework = $%d", idx))
		args = append(args, filter.Framework)
		idx++
	}
	if filter.Engine != "" {
		where = append(where, fmt.Sprintf("bc.engine = $%d", idx))
		args = append(args, filter.Engine)
		idx++
	}
	if filter.Severity != "" {
		where = append(where, fmt.Sprintf("bc.severity = $%d", idx))
		args = append(args, filter.Severity)
		idx++
	}
	if filter.Tag != "" {
		where = append(where, fmt.Sprintf("bc.tags @> $%d::jsonb", idx))
		args = append(args, fmt.Sprintf(`[%q]`, filter.Tag))
		idx++
	}
	if filter.Q != "" {
		where = append(where, fmt.Sprintf("(bc.control_id ILIKE '%%' || $%d || '%%' OR bc.name ILIKE '%%' || $%d || '%%')", idx, idx))
		args = append(args, filter.Q)
		_ = idx // last param; suppress ineffassign
	}

	whereClause := strings.Join(where, " AND ")

	// Count query (distinct control_ids).
	countQ := fmt.Sprintf(
		`SELECT COUNT(DISTINCT bc.control_id) FROM bundle_controls bc JOIN bundles b ON b.id = bc.bundle_id WHERE %s`,
		whereClause,
	)
	var total int
	if err := s.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting controls: %w", err)
	}

	// Data query — fetch all matching rows ordered for grouping.
	dataQ := fmt.Sprintf(
		`SELECT bc.control_id, bc.name, bc.severity, bc.engine, bc.engine_versions, bc.tags,
		        bc.section, b.id, b.name
		   FROM bundle_controls bc
		   JOIN bundles b ON b.id = bc.bundle_id
		  WHERE %s
		  ORDER BY bc.control_id, b.name`,
		whereClause,
	)

	rows, err := s.db.QueryContext(ctx, dataQ, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing controls: %w", err)
	}
	defer rows.Close()

	var out []ControlRow
	for rows.Next() {
		var r ControlRow
		if err := rows.Scan(&r.ControlID, &r.Name, &r.Severity, &r.Engine,
			&r.EngineVersions, &r.Tags, &r.Section,
			&r.BundleID, &r.BundleName); err != nil {
			return nil, 0, fmt.Errorf("scanning control row: %w", err)
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// ======================================================================
// Tenants (internal)
// ======================================================================

func (s *PostgresStore) CreateTenant(ctx context.Context, name string) (*model.Tenant, error) {
	var t model.Tenant
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO tenants (name) VALUES ($1)
		 RETURNING id, name, status, config, created_at`, name).
		Scan(&t.ID, &t.Name, &t.Status, &t.Config, &t.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating tenant: %w", err)
	}
	return &t, nil
}

func (s *PostgresStore) ListAllTenants(ctx context.Context) ([]model.Tenant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, status, config, created_at FROM tenants ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing tenants: %w", err)
	}
	defer rows.Close()
	var tenants []model.Tenant
	for rows.Next() {
		var t model.Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.Status, &t.Config, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning tenant: %w", err)
		}
		tenants = append(tenants, t)
	}
	return tenants, rows.Err()
}

func (s *PostgresStore) GetTenantByID(ctx context.Context, id string) (*model.Tenant, error) {
	var t model.Tenant
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, status, config, created_at FROM tenants WHERE id = $1`, id).
		Scan(&t.ID, &t.Name, &t.Status, &t.Config, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting tenant by id: %w", err)
	}
	return &t, nil
}

func (s *PostgresStore) UpdateTenantStatus(ctx context.Context, id, status string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE tenants SET status = $1 WHERE id = $2`, status, id)
	if err != nil {
		return fmt.Errorf("updating tenant status: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("tenant not found")
	}
	return nil
}

func (s *PostgresStore) UpdateTenantConfig(ctx context.Context, id string, config json.RawMessage) error {
	result, err := s.db.ExecContext(ctx, `UPDATE tenants SET config = $1 WHERE id = $2`, config, id)
	if err != nil {
		return fmt.Errorf("updating tenant config: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("tenant not found")
	}
	return nil
}

func (s *PostgresStore) UpdateTenantName(ctx context.Context, id, name string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE tenants SET name = $1 WHERE id = $2`, name, id)
	if err != nil {
		return fmt.Errorf("updating tenant name: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("tenant not found")
	}
	return nil
}

// ======================================================================
// Stats
// ======================================================================

func (s *PostgresStore) GetDCStats(ctx context.Context) (*model.DCStats, error) {
	var stats model.DCStats
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tenants WHERE status = $1`,
		model.TenantStatusActive).Scan(&stats.TenantCount); err != nil {
		return nil, fmt.Errorf("counting tenants: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&stats.AgentCount); err != nil {
		return nil, fmt.Errorf("counting agents: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scans`).Scan(&stats.ScanCount); err != nil {
		return nil, fmt.Errorf("counting scans: %w", err)
	}
	return &stats, nil
}

// ======================================================================
// Credential sources (ADR 004 C0)
// ======================================================================

func (s *PostgresStore) CreateCredentialSource(ctx context.Context, tenantID, name, srcType string, config json.RawMessage) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO credential_sources (tenant_id, name, type, config) VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, name, srcType, config).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("creating credential source: %w", err)
	}
	return id, nil
}

func (s *PostgresStore) GetCredentialSource(ctx context.Context, id string) (*model.CredentialSource, error) {
	var cs model.CredentialSource
	err := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, type, config, created_at, updated_at FROM credential_sources WHERE id = $1`, id).
		Scan(&cs.ID, &cs.TenantID, &cs.Name, &cs.Type, &cs.Config, &cs.CreatedAt, &cs.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting credential source: %w", err)
	}
	return &cs, nil
}

func (s *PostgresStore) GetCredentialSourceByTarget(ctx context.Context, targetID string) (*model.CredentialSource, error) {
	var cs model.CredentialSource
	err := s.db.QueryRowContext(ctx,
		`SELECT cs.id, cs.tenant_id, cs.name, cs.type, cs.config, cs.created_at, cs.updated_at
		   FROM credential_sources cs JOIN targets t ON t.credential_source_id = cs.id
		  WHERE t.id = $1`, targetID).
		Scan(&cs.ID, &cs.TenantID, &cs.Name, &cs.Type, &cs.Config, &cs.CreatedAt, &cs.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting credential source by target: %w", err)
	}
	return &cs, nil
}

func (s *PostgresStore) UpdateCredentialSource(ctx context.Context, id, name string, config json.RawMessage) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE credential_sources SET name = $1, config = $2, updated_at = NOW() WHERE id = $3`, name, config, id)
	if err != nil {
		return fmt.Errorf("updating credential source: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ResolveCredentialForEndpoint finds a static credential_source for the
// given endpoint using most-specific-first precedence:
//  1. Endpoint-level mapping (scope_kind='asset_endpoint')
//  2. Asset-level mapping (scope_kind='asset')
//  3. Collection-level mapping (scope_kind='collection') — legacy path
//
// First match wins; returns nil if no mapping resolves.
func (s *PostgresStore) ResolveCredentialForEndpoint(ctx context.Context, tenantID, endpointID string) (*model.CredentialSource, error) {
	// 1. Endpoint-level: direct mapping to this endpoint.
	cs, err := s.resolveBySQL(ctx,
		`SELECT cs.id, cs.tenant_id, cs.name, cs.type, cs.config, cs.created_at, cs.updated_at
		   FROM credential_sources cs
		   JOIN credential_mappings cm ON cm.credential_source_id = cs.id
		  WHERE cm.scope_kind = 'asset_endpoint'
		    AND cm.asset_endpoint_id = $1
		    AND cm.tenant_id = $2
		  LIMIT 1`, endpointID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("resolve endpoint-level: %w", err)
	}
	if cs != nil {
		return cs, nil
	}

	// 2. Asset-level: look up the endpoint's parent asset, then find a
	//    mapping scoped to that asset.
	var assetID sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT asset_id FROM asset_endpoints WHERE id = $1`, endpointID).
		Scan(&assetID); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("looking up endpoint asset: %w", err)
	}
	if assetID.Valid {
		cs, err = s.resolveBySQL(ctx,
			`SELECT cs.id, cs.tenant_id, cs.name, cs.type, cs.config, cs.created_at, cs.updated_at
			   FROM credential_sources cs
			   JOIN credential_mappings cm ON cm.credential_source_id = cs.id
			  WHERE cm.scope_kind = 'asset'
			    AND cm.asset_id = $1
			    AND cm.tenant_id = $2
			  LIMIT 1`, assetID.String, tenantID)
		if err != nil {
			return nil, fmt.Errorf("resolve asset-level: %w", err)
		}
		if cs != nil {
			return cs, nil
		}
	}

	// 3. Collection-level: iterate collection-scoped mappings and check
	//    if any collection contains this endpoint (legacy path).
	mappings, err := s.ListCredentialMappings(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing credential mappings: %w", err)
	}
	for _, m := range mappings {
		if m.ScopeKind != model.MappingScopeCollection || m.CollectionID == nil {
			continue
		}
		members, err := s.CollectionEndpointIDs(ctx, *m.CollectionID)
		if err != nil {
			slog.Warn("resolveCredentialForEndpoint: collection lookup failed",
				"collection_id", *m.CollectionID, "error", err)
			continue
		}
		for _, id := range members {
			if id == endpointID {
				cs, err := s.GetCredentialSource(ctx, m.CredentialSourceID)
				if err != nil {
					return nil, fmt.Errorf("loading credential source %s: %w", m.CredentialSourceID, err)
				}
				if cs != nil && resolvableSourceTypes[cs.Type] {
					return cs, nil
				}
			}
		}
	}
	return nil, nil
}

// resolvableSourceTypes lists credential source types that can be resolved
// at scan time. Static sources are decrypted server-side; aws_secrets_manager
// sources are fetched from AWS server-side. Future resolvers (vault, cyberark)
// will be added here as they ship.
var resolvableSourceTypes = map[string]bool{
	model.CredentialSourceTypeStatic:            true,
	model.CredentialSourceTypeAWSSecretsManager: true,
}

// resolveBySQL executes the given query (must return credential_source columns)
// and returns the first resolvable credential_source or nil.
func (s *PostgresStore) resolveBySQL(ctx context.Context, query string, args ...any) (*model.CredentialSource, error) {
	var cs model.CredentialSource
	var cfgRaw []byte
	err := s.db.QueryRowContext(ctx, query, args...).
		Scan(&cs.ID, &cs.TenantID, &cs.Name, &cs.Type, &cfgRaw, &cs.CreatedAt, &cs.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !resolvableSourceTypes[cs.Type] {
		return nil, nil
	}
	if len(cfgRaw) > 0 {
		_ = json.Unmarshal(cfgRaw, &cs.Config)
	}
	return &cs, nil
}

func (s *PostgresStore) DeleteCredentialSource(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM credential_sources WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting credential source: %w", err)
	}
	return nil
}

func (s *PostgresStore) SetTargetCredentialSource(ctx context.Context, targetID, sourceID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE targets SET credential_source_id = $1, updated_at = NOW() WHERE id = $2`, sourceID, targetID)
	if err != nil {
		return fmt.Errorf("setting target credential source: %w", err)
	}
	return nil
}

func (s *PostgresStore) ClearTargetCredentialSource(ctx context.Context, targetID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE targets SET credential_source_id = NULL, updated_at = NOW() WHERE id = $1`, targetID)
	if err != nil {
		return fmt.Errorf("clearing target credential source: %w", err)
	}
	return nil
}

func (s *PostgresStore) UpsertStaticCredentialSource(ctx context.Context, tenantID, targetID, credType string, encryptedData []byte) error {
	cfgJSON, err := json.Marshal(model.StaticCredentialConfig{
		Type:          credType,
		EncryptedData: base64.StdEncoding.EncodeToString(encryptedData),
	})
	if err != nil {
		return fmt.Errorf("marshalling static credential config: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var existingID, existingType sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT cs.id, cs.type FROM targets t
		   LEFT JOIN credential_sources cs ON cs.id = t.credential_source_id
		  WHERE t.id = $1`, targetID).Scan(&existingID, &existingType)
	if err != nil {
		return fmt.Errorf("looking up existing source: %w", err)
	}
	switch {
	case existingID.Valid && existingType.String == model.CredentialSourceTypeStatic:
		if _, err := tx.ExecContext(ctx,
			`UPDATE credential_sources SET config = $1, updated_at = NOW() WHERE id = $2`,
			cfgJSON, existingID.String); err != nil {
			return fmt.Errorf("updating static credential source: %w", err)
		}
	case existingID.Valid && existingType.String != model.CredentialSourceTypeStatic:
		return fmt.Errorf("target %s has non-static credential source; cannot overwrite", targetID)
	default:
		var newID string
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO credential_sources (tenant_id, type, config) VALUES ($1, $2, $3) RETURNING id`,
			tenantID, model.CredentialSourceTypeStatic, cfgJSON).Scan(&newID); err != nil {
			return fmt.Errorf("inserting static credential source: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE targets SET credential_source_id = $1, updated_at = NOW() WHERE id = $2`,
			newID, targetID); err != nil {
			return fmt.Errorf("linking target to credential source: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetStaticCredentialForTarget(ctx context.Context, targetID string) ([]byte, string, error) {
	var srcType, cfgRaw sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT cs.type, cs.config::text FROM targets t
		   LEFT JOIN credential_sources cs ON cs.id = t.credential_source_id
		  WHERE t.id = $1`, targetID).Scan(&srcType, &cfgRaw)
	if err != nil && err != sql.ErrNoRows {
		return nil, "", fmt.Errorf("looking up credential source: %w", err)
	}
	if !srcType.Valid || srcType.String != model.CredentialSourceTypeStatic || !cfgRaw.Valid {
		return nil, "", nil
	}
	var cfg model.StaticCredentialConfig
	if err := json.Unmarshal([]byte(cfgRaw.String), &cfg); err != nil {
		return nil, "", fmt.Errorf("decoding static credential config: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(cfg.EncryptedData)
	if err != nil {
		return nil, "", fmt.Errorf("decoding static credential data: %w", err)
	}
	return data, cfg.Type, nil
}

func (s *PostgresStore) HasCredentialForTarget(ctx context.Context, targetID string) (bool, string, error) {
	var srcType, cfgRaw sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT cs.type, cs.config::text FROM targets t
		   LEFT JOIN credential_sources cs ON cs.id = t.credential_source_id
		  WHERE t.id = $1`, targetID).Scan(&srcType, &cfgRaw)
	if err != nil && err != sql.ErrNoRows {
		return false, "", fmt.Errorf("checking credential source: %w", err)
	}
	if !srcType.Valid || srcType.String != model.CredentialSourceTypeStatic || !cfgRaw.Valid {
		return false, "", nil
	}
	var cfg model.StaticCredentialConfig
	if err := json.Unmarshal([]byte(cfgRaw.String), &cfg); err != nil {
		return false, "", fmt.Errorf("decoding static credential config: %w", err)
	}
	return true, cfg.Type, nil
}

func (s *PostgresStore) DeleteCredentialForTarget(ctx context.Context, tenantID, targetID string) error {
	_ = tenantID
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var sourceID, sourceType sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT cs.id, cs.type FROM targets t
		   LEFT JOIN credential_sources cs ON cs.id = t.credential_source_id
		  WHERE t.id = $1`, targetID).Scan(&sourceID, &sourceType); err != nil {
		return fmt.Errorf("looking up credential source: %w", err)
	}
	if !sourceID.Valid || sourceType.String != model.CredentialSourceTypeStatic {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE targets SET credential_source_id = NULL, updated_at = NOW() WHERE id = $1`,
		targetID); err != nil {
		return fmt.Errorf("clearing target credential source: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM credential_sources WHERE id = $1`, sourceID.String); err != nil {
		return fmt.Errorf("deleting credential source: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func (s *PostgresStore) ListCredentialSources(ctx context.Context, tenantID string) ([]model.CredentialSource, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, name, type, config, created_at, updated_at
		   FROM credential_sources WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing credential sources: %w", err)
	}
	defer rows.Close()
	out := []model.CredentialSource{}
	for rows.Next() {
		var cs model.CredentialSource
		if err := rows.Scan(&cs.ID, &cs.TenantID, &cs.Name, &cs.Type, &cs.Config, &cs.CreatedAt, &cs.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning credential source: %w", err)
		}
		out = append(out, cs)
	}
	return out, rows.Err()
}

// ======================================================================
// Credential mappings (ADR 006 P6)
// ======================================================================

func (s *PostgresStore) ListCredentialMappings(ctx context.Context, tenantID string) ([]model.CredentialMapping, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, scope_kind, collection_id, asset_endpoint_id, asset_id,
		        credential_source_id, created_at
		   FROM credential_mappings WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing credential mappings: %w", err)
	}
	defer rows.Close()
	out := []model.CredentialMapping{}
	for rows.Next() {
		var m model.CredentialMapping
		if err := rows.Scan(&m.ID, &m.TenantID, &m.ScopeKind, &m.CollectionID,
			&m.AssetEndpointID, &m.AssetID, &m.CredentialSourceID, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning credential mapping: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetCredentialMapping(ctx context.Context, id string) (*model.CredentialMapping, error) {
	var m model.CredentialMapping
	err := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, scope_kind, collection_id, asset_endpoint_id, asset_id,
		        credential_source_id, created_at
		   FROM credential_mappings WHERE id = $1`, id).
		Scan(&m.ID, &m.TenantID, &m.ScopeKind, &m.CollectionID,
			&m.AssetEndpointID, &m.AssetID, &m.CredentialSourceID, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting credential mapping: %w", err)
	}
	return &m, nil
}

func (s *PostgresStore) CreateCredentialMapping(ctx context.Context, in CreateCredentialMappingInput) (*model.CredentialMapping, error) {
	var m model.CredentialMapping
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO credential_mappings
		   (tenant_id, scope_kind, collection_id, asset_endpoint_id, asset_id, credential_source_id)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, tenant_id, scope_kind, collection_id, asset_endpoint_id, asset_id,
		           credential_source_id, created_at`,
		in.TenantID, in.ScopeKind, in.CollectionID, in.AssetEndpointID, in.AssetID, in.CredentialSourceID).
		Scan(&m.ID, &m.TenantID, &m.ScopeKind, &m.CollectionID,
			&m.AssetEndpointID, &m.AssetID, &m.CredentialSourceID, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating credential mapping: %w", err)
	}
	return &m, nil
}

func (s *PostgresStore) DeleteCredentialMapping(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM credential_mappings WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting credential mapping: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *PostgresStore) CountMappingsForSource(ctx context.Context, sourceID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM credential_mappings WHERE credential_source_id = $1`, sourceID).
		Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting mappings: %w", err)
	}
	return n, nil
}

// ======================================================================
// Assets + endpoints (ADR 006 D2) — minimal working read impls; write
// path is wired for P2 ingest to call.
// ======================================================================

func (s *PostgresStore) UpsertAsset(ctx context.Context, in UpsertAssetInput) (*model.Asset, error) {
	if in.ResourceType == "" {
		in.ResourceType = model.ResourceTypeHost
	}
	fp := in.Fingerprint
	if len(fp) == 0 {
		fp = json.RawMessage(`{}`)
	}
	var primaryIP *string
	if in.PrimaryIP != "" {
		p := in.PrimaryIP
		primaryIP = &p
	}
	var hostname *string
	if in.Hostname != "" {
		h := in.Hostname
		hostname = &h
	}
	// Postgres partial unique index (tenant_id, primary_ip) WHERE primary_ip IS NOT NULL
	// handles the upsert shape for real hosts. For IP-less assets we always insert.
	var a model.Asset
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO assets (tenant_id, primary_ip, hostname, fingerprint, resource_type, source, environment)
		 VALUES ($1, $2::inet, $3, $4, $5, $6, $7)
		 ON CONFLICT (tenant_id, primary_ip) WHERE primary_ip IS NOT NULL DO UPDATE SET
		   hostname = COALESCE(EXCLUDED.hostname, assets.hostname),
		   fingerprint = assets.fingerprint || EXCLUDED.fingerprint,
		   last_seen = NOW()
		 RETURNING id, tenant_id, host(primary_ip), hostname, fingerprint, resource_type, source, environment, first_seen, last_seen, created_at`,
		in.TenantID, primaryIP, hostname, fp, in.ResourceType, in.Source, in.Environment).
		Scan(&a.ID, &a.TenantID, &a.PrimaryIP, &a.Hostname, &a.Fingerprint, &a.ResourceType,
			&a.Source, &a.Environment, &a.FirstSeen, &a.LastSeen, &a.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("upserting asset: %w", err)
	}
	return &a, nil
}

func (s *PostgresStore) UpsertAssetEndpoint(ctx context.Context, in UpsertAssetEndpointInput) (*model.AssetEndpoint, error) {
	if in.Protocol == "" {
		in.Protocol = "tcp"
	}
	tech := in.Technologies
	if len(tech) == 0 {
		tech = json.RawMessage(`[]`)
	}
	var svc, ver *string
	if in.Service != "" {
		v := in.Service
		svc = &v
	}
	if in.Version != "" {
		v := in.Version
		ver = &v
	}
	var ae model.AssetEndpoint
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO asset_endpoints (asset_id, port, protocol, service, version, technologies, allowlist_status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (asset_id, port, protocol) DO UPDATE SET
		   service = COALESCE(EXCLUDED.service, asset_endpoints.service),
		   version = COALESCE(EXCLUDED.version, asset_endpoints.version),
		   technologies = EXCLUDED.technologies,
		   allowlist_status = COALESCE(EXCLUDED.allowlist_status, asset_endpoints.allowlist_status),
		   last_seen = NOW(),
		   updated_at = NOW()
		 RETURNING id, asset_id, port, protocol, service, version, technologies,
		           compliance_status, allowlist_status, allowlist_checked_at,
		           last_scan_id, missed_scan_count, metadata,
		           first_seen, last_seen, created_at, updated_at`,
		in.AssetID, in.Port, in.Protocol, svc, ver, tech, in.AllowlistStatus).
		Scan(&ae.ID, &ae.AssetID, &ae.Port, &ae.Protocol, &ae.Service, &ae.Version,
			&ae.Technologies, &ae.ComplianceStatus, &ae.AllowlistStatus, &ae.AllowlistCheckedAt,
			&ae.LastScanID, &ae.MissedScanCount, &ae.Metadata,
			&ae.FirstSeen, &ae.LastSeen, &ae.CreatedAt, &ae.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("upserting asset_endpoint: %w", err)
	}
	return &ae, nil
}

func (s *PostgresStore) GetAssetByID(ctx context.Context, id string) (*model.Asset, error) {
	var a model.Asset
	err := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, host(primary_ip), hostname, fingerprint, resource_type, source, environment,
		        first_seen, last_seen, created_at
		   FROM assets WHERE id = $1`, id).
		Scan(&a.ID, &a.TenantID, &a.PrimaryIP, &a.Hostname, &a.Fingerprint, &a.ResourceType,
			&a.Source, &a.Environment, &a.FirstSeen, &a.LastSeen, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting asset: %w", err)
	}
	return &a, nil
}

func (s *PostgresStore) ListAssets(ctx context.Context, filter AssetFilter) ([]model.Asset, int, error) {
	tenantID := TenantID(ctx)
	if filter.PageSize <= 0 || filter.PageSize > 500 {
		filter.PageSize = 50
	}
	if filter.Page <= 0 {
		filter.Page = 1
	}
	offset := (filter.Page - 1) * filter.PageSize
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, host(primary_ip), hostname, fingerprint, resource_type, source, environment,
		        first_seen, last_seen, created_at
		   FROM assets
		  WHERE tenant_id = $1
		    AND ($2 = '' OR source = $2)
		    AND ($5 = '' OR host(primary_ip)::text ILIKE '%' || $5 || '%' OR COALESCE(hostname,'') ILIKE '%' || $5 || '%')
		  ORDER BY last_seen DESC
		  LIMIT $3 OFFSET $4`,
		tenantID, filter.Source, filter.PageSize, offset, filter.Q)
	if err != nil {
		return nil, 0, fmt.Errorf("listing assets: %w", err)
	}
	defer rows.Close()
	var items []model.Asset
	for rows.Next() {
		var a model.Asset
		if err := rows.Scan(&a.ID, &a.TenantID, &a.PrimaryIP, &a.Hostname, &a.Fingerprint,
			&a.ResourceType, &a.Source, &a.Environment, &a.FirstSeen, &a.LastSeen, &a.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scanning asset: %w", err)
		}
		items = append(items, a)
	}
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assets WHERE tenant_id = $1 AND ($2 = '' OR source = $2)`,
		tenantID, filter.Source).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting assets: %w", err)
	}
	return items, total, nil
}

func (s *PostgresStore) ListAssetEndpoints(ctx context.Context, filter AssetEndpointFilter) ([]AssetEndpointRow, int, error) {
	tenantID := TenantID(ctx)
	if filter.PageSize <= 0 || filter.PageSize > 500 {
		filter.PageSize = 50
	}
	if filter.Page <= 0 {
		filter.Page = 1
	}
	offset := (filter.Page - 1) * filter.PageSize

	rows, err := s.db.QueryContext(ctx,
		`SELECT ae.id, ae.asset_id,
		        COALESCE(a.hostname, host(a.primary_ip)::text, ''),
		        COALESCE(host(a.primary_ip)::text, ''),
		        ae.port, ae.protocol, ae.service, ae.version, ae.technologies,
		        (SELECT COUNT(*) FROM findings f WHERE f.asset_endpoint_id = ae.id AND f.status = 'open'),
		        ae.last_seen
		   FROM asset_endpoints ae
		   JOIN assets a ON a.id = ae.asset_id
		  WHERE a.tenant_id = $1
		    AND ($2 = '' OR ae.service = $2)
		    AND ($3 = 0 OR ae.port = $3)
		    AND ($4 = '' OR a.source = $4)
		    AND ($5 = '' OR host(a.primary_ip)::text ILIKE '%' || $5 || '%'
		         OR COALESCE(a.hostname,'') ILIKE '%' || $5 || '%'
		         OR COALESCE(ae.service,'') ILIKE '%' || $5 || '%'
		         OR ae.port::text LIKE '%' || $5 || '%')
		  ORDER BY a.primary_ip, ae.port
		  LIMIT $6 OFFSET $7`,
		tenantID, filter.Service, filter.Port, filter.Source, filter.Q,
		filter.PageSize, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("listing asset endpoints: %w", err)
	}
	defer rows.Close()
	var items []AssetEndpointRow
	for rows.Next() {
		var r AssetEndpointRow
		if err := rows.Scan(&r.ID, &r.AssetID, &r.Host, &r.IP, &r.Port, &r.Protocol,
			&r.Service, &r.Version, &r.Technologies, &r.FindingsCount, &r.LastSeen); err != nil {
			return nil, 0, fmt.Errorf("scanning asset endpoint: %w", err)
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*)
		   FROM asset_endpoints ae
		   JOIN assets a ON a.id = ae.asset_id
		  WHERE a.tenant_id = $1
		    AND ($2 = '' OR ae.service = $2)
		    AND ($3 = 0 OR ae.port = $3)
		    AND ($4 = '' OR a.source = $4)
		    AND ($5 = '' OR host(a.primary_ip)::text ILIKE '%' || $5 || '%'
		         OR COALESCE(a.hostname,'') ILIKE '%' || $5 || '%'
		         OR COALESCE(ae.service,'') ILIKE '%' || $5 || '%'
		         OR ae.port::text LIKE '%' || $5 || '%')`,
		tenantID, filter.Service, filter.Port, filter.Source, filter.Q).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting asset endpoints: %w", err)
	}
	return items, total, nil
}

// ======================================================================
// Collections (ADR 006 D5)
// ======================================================================

const collectionCols = `id, tenant_id, name, description, scope, predicate, is_dashboard_widget, widget_kind, created_at, updated_at, created_by`

func scanCollection(c *model.Collection, row interface{ Scan(...any) error }) error {
	return row.Scan(&c.ID, &c.TenantID, &c.Name, &c.Description, &c.Scope, &c.Predicate,
		&c.IsDashboardWidget, &c.WidgetKind, &c.CreatedAt, &c.UpdatedAt, &c.CreatedBy)
}

func (s *PostgresStore) CreateCollection(ctx context.Context, c model.Collection) (*model.Collection, error) {
	tenantID := TenantID(ctx)
	if c.Scope == "" {
		c.Scope = model.CollectionScopeEndpoint
	}
	if len(c.Predicate) == 0 {
		c.Predicate = json.RawMessage(`{}`)
	}
	var out model.Collection
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO collections (tenant_id, name, description, scope, predicate, is_dashboard_widget, widget_kind, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING `+collectionCols,
		tenantID, c.Name, c.Description, c.Scope, c.Predicate, c.IsDashboardWidget, c.WidgetKind, c.CreatedBy)
	if err := scanCollection(&out, row); err != nil {
		return nil, fmt.Errorf("creating collection: %w", err)
	}
	return &out, nil
}

func (s *PostgresStore) GetCollection(ctx context.Context, id string) (*model.Collection, error) {
	tenantID := TenantID(ctx)
	var c model.Collection
	row := s.db.QueryRowContext(ctx,
		`SELECT `+collectionCols+` FROM collections WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err := scanCollection(&c, row); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting collection: %w", err)
	}
	return &c, nil
}

func (s *PostgresStore) ListCollections(ctx context.Context) ([]model.Collection, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+collectionCols+` FROM collections WHERE tenant_id = $1 ORDER BY name`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing collections: %w", err)
	}
	defer rows.Close()
	var out []model.Collection
	for rows.Next() {
		var c model.Collection
		if err := scanCollection(&c, rows); err != nil {
			return nil, fmt.Errorf("scanning collection: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateCollection(ctx context.Context, c model.Collection) (*model.Collection, error) {
	tenantID := TenantID(ctx)
	var out model.Collection
	row := s.db.QueryRowContext(ctx,
		`UPDATE collections SET
		   name = $1, description = $2, scope = $3, predicate = $4,
		   is_dashboard_widget = $5, widget_kind = $6, updated_at = NOW()
		 WHERE id = $7 AND tenant_id = $8
		 RETURNING `+collectionCols,
		c.Name, c.Description, c.Scope, c.Predicate, c.IsDashboardWidget, c.WidgetKind, c.ID, tenantID)
	if err := scanCollection(&out, row); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("updating collection: %w", err)
	}
	return &out, nil
}

func (s *PostgresStore) DeleteCollection(ctx context.Context, id string) error {
	tenantID := TenantID(ctx)
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM collections WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err != nil {
		return fmt.Errorf("deleting collection: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ======================================================================
// P2 ingest surface (ADR 006 D4, D7, D9)
// ======================================================================

func (s *PostgresStore) AppendAssetEvents(ctx context.Context, events []model.AssetEvent) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO asset_events (tenant_id, asset_id, scan_id, event_type, severity, payload, occurred_at)
		 VALUES ($1, $2, $3, $4, $5, $6, COALESCE($7, NOW()))`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()
	for _, e := range events {
		payload := e.Payload
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		var occurred interface{}
		if !e.OccurredAt.IsZero() {
			occurred = e.OccurredAt
		}
		if _, err := stmt.ExecContext(ctx,
			e.TenantID, e.AssetID, e.ScanID, e.EventType, e.Severity, payload, occurred); err != nil {
			return fmt.Errorf("insert event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s *PostgresStore) RecordDiscoverySource(ctx context.Context, in DiscoverySourceInput) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO asset_discovery_sources (asset_id, target_id, agent_id, scan_id)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (asset_id, discovered_at) DO NOTHING`,
		in.AssetID, in.TargetID, in.AgentID, in.ScanID)
	if err != nil {
		return fmt.Errorf("recording discovery source: %w", err)
	}
	return nil
}

func (s *PostgresStore) ListEndpointsForAsset(ctx context.Context, assetID string) ([]model.AssetEndpoint, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, asset_id, port, protocol, service, version, technologies,
		        compliance_status, allowlist_status, allowlist_checked_at,
		        last_scan_id, missed_scan_count, metadata,
		        first_seen, last_seen, created_at, updated_at
		   FROM asset_endpoints WHERE asset_id = $1`, assetID)
	if err != nil {
		return nil, fmt.Errorf("listing endpoints: %w", err)
	}
	defer rows.Close()
	var out []model.AssetEndpoint
	for rows.Next() {
		var e model.AssetEndpoint
		if err := rows.Scan(&e.ID, &e.AssetID, &e.Port, &e.Protocol, &e.Service, &e.Version,
			&e.Technologies, &e.ComplianceStatus, &e.AllowlistStatus, &e.AllowlistCheckedAt,
			&e.LastScanID, &e.MissedScanCount, &e.Metadata,
			&e.FirstSeen, &e.LastSeen, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning endpoint: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateEndpointAllowlistStatus(ctx context.Context, endpointID, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE asset_endpoints
		    SET allowlist_status = $1,
		        allowlist_checked_at = NOW(),
		        updated_at = NOW()
		  WHERE id = $2`, status, endpointID)
	if err != nil {
		return fmt.Errorf("updating endpoint allowlist status: %w", err)
	}
	return nil
}

// ======================================================================
// Correlation rules (ADR 006 D6) — load side only in P2; CRUD in P2+.
// ======================================================================

func (s *PostgresStore) ListActiveRulesForTrigger(ctx context.Context, tenantID, trigger string) ([]model.CorrelationRule, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, name, version, enabled, trigger, event_type_filter, body, created_at, created_by
		   FROM correlation_rules
		  WHERE tenant_id = $1 AND enabled = TRUE AND trigger = $2`,
		tenantID, trigger)
	if err != nil {
		return nil, fmt.Errorf("listing rules: %w", err)
	}
	defer rows.Close()
	var out []model.CorrelationRule
	for rows.Next() {
		var r model.CorrelationRule
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Name, &r.Version, &r.Enabled,
			&r.Trigger, &r.EventTypeFilter, &r.Body, &r.CreatedAt, &r.CreatedBy); err != nil {
			return nil, fmt.Errorf("scanning rule: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ======================================================================
// Agent allowlists (migration 018)
// ======================================================================

func (s *PostgresStore) UpsertAgentAllowlist(ctx context.Context, in AgentAllowlistInput) (bool, error) {
	allow, _ := json.Marshal(in.Allow)
	deny, _ := json.Marshal(in.Deny)
	if len(in.Allow) == 0 {
		allow = json.RawMessage(`[]`)
	}
	if len(in.Deny) == 0 {
		deny = json.RawMessage(`[]`)
	}
	var existingHash sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT snapshot_hash FROM agent_allowlists WHERE agent_id = $1`, in.AgentID).
		Scan(&existingHash)
	if err != nil && err != sql.ErrNoRows {
		return false, fmt.Errorf("loading existing allowlist: %w", err)
	}
	changed := !existingHash.Valid || existingHash.String != in.Hash
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO agent_allowlists (agent_id, snapshot_hash, allow, deny, rate_limit_pps, reported_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
		 ON CONFLICT (agent_id) DO UPDATE SET
		   snapshot_hash = EXCLUDED.snapshot_hash,
		   allow = EXCLUDED.allow,
		   deny = EXCLUDED.deny,
		   rate_limit_pps = EXCLUDED.rate_limit_pps,
		   reported_at = NOW(),
		   updated_at = NOW()`,
		in.AgentID, in.Hash, allow, deny, in.RateLimitPPS)
	if err != nil {
		return false, fmt.Errorf("upserting allowlist: %w", err)
	}
	return changed, nil
}

func (s *PostgresStore) GetAgentAllowlist(ctx context.Context, agentID string) (*AgentAllowlistSnapshot, error) {
	var snap AgentAllowlistSnapshot
	var allowJSON, denyJSON []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT agent_id, snapshot_hash, allow, deny, rate_limit_pps, reported_at, updated_at
		   FROM agent_allowlists WHERE agent_id = $1`, agentID).
		Scan(&snap.AgentID, &snap.Hash, &allowJSON, &denyJSON, &snap.RateLimitPPS, &snap.ReportedAt, &snap.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting allowlist: %w", err)
	}
	_ = json.Unmarshal(allowJSON, &snap.Allow)
	_ = json.Unmarshal(denyJSON, &snap.Deny)
	return &snap, nil
}

// ======================================================================
// Notification deliveries (ADR 006 P6)
// ======================================================================

func (s *PostgresStore) InsertNotificationDelivery(ctx context.Context, d model.NotificationDelivery) error {
	payload := d.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO notification_deliveries
		   (tenant_id, channel_source_id, rule_id, event_id, severity, status,
		    attempt, response_code, error, payload, dispatched_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())`,
		d.TenantID, d.ChannelSourceID, d.RuleID, d.EventID, d.Severity, d.Status,
		d.Attempt, d.ResponseCode, d.Error, payload)
	if err != nil {
		return fmt.Errorf("inserting notification delivery: %w", err)
	}
	return nil
}

// ======================================================================
// P4 — Correlation-rule CRUD (ADR 006 D6).
// ======================================================================

const ruleCols = `id, tenant_id, name, version, enabled, trigger, event_type_filter, body, created_at, created_by`

func scanRule(r *model.CorrelationRule, row interface{ Scan(...any) error }) error {
	return row.Scan(&r.ID, &r.TenantID, &r.Name, &r.Version, &r.Enabled,
		&r.Trigger, &r.EventTypeFilter, &r.Body, &r.CreatedAt, &r.CreatedBy)
}

// ListCorrelationRules returns only the latest version per (tenant, name).
// Disabled rows are included — callers can filter client-side.
func (s *PostgresStore) ListCorrelationRules(ctx context.Context) ([]model.CorrelationRule, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT ON (name) `+ruleCols+`
		   FROM correlation_rules
		  WHERE tenant_id = $1
		  ORDER BY name, version DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing rules: %w", err)
	}
	defer rows.Close()
	var out []model.CorrelationRule
	for rows.Next() {
		var r model.CorrelationRule
		if err := scanRule(&r, rows); err != nil {
			return nil, fmt.Errorf("scanning rule: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetCorrelationRule(ctx context.Context, id string) (*model.CorrelationRule, error) {
	tenantID := TenantID(ctx)
	var r model.CorrelationRule
	row := s.db.QueryRowContext(ctx,
		`SELECT `+ruleCols+` FROM correlation_rules WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err := scanRule(&r, row); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting rule: %w", err)
	}
	return &r, nil
}

func (s *PostgresStore) CreateCorrelationRule(ctx context.Context, r model.CorrelationRule) (*model.CorrelationRule, error) {
	tenantID := TenantID(ctx)
	if len(r.Body) == 0 {
		r.Body = json.RawMessage(`{}`)
	}
	var out model.CorrelationRule
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO correlation_rules (tenant_id, name, version, enabled, trigger, event_type_filter, body, created_by)
		 VALUES ($1, $2, 1, $3, $4, $5, $6, $7)
		 RETURNING `+ruleCols,
		tenantID, r.Name, r.Enabled, r.Trigger, r.EventTypeFilter, r.Body, r.CreatedBy)
	if err := scanRule(&out, row); err != nil {
		return nil, fmt.Errorf("creating rule: %w", err)
	}
	return &out, nil
}

// UpdateCorrelationRule auto-versions: the existing row stays (for
// history) but gets disabled, and a new row is inserted with
// version = max(version)+1. Returns the new row.
func (s *PostgresStore) UpdateCorrelationRule(ctx context.Context, r model.CorrelationRule) (*model.CorrelationRule, error) {
	tenantID := TenantID(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var maxVersion int
	var name string
	if err := tx.QueryRowContext(ctx,
		`SELECT name, MAX(version) FROM correlation_rules
		  WHERE tenant_id = $1 AND name = (SELECT name FROM correlation_rules WHERE id = $2 AND tenant_id = $1)
		  GROUP BY name`, tenantID, r.ID).Scan(&name, &maxVersion); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("looking up rule: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE correlation_rules SET enabled = FALSE
		  WHERE tenant_id = $1 AND name = $2`, tenantID, name); err != nil {
		return nil, fmt.Errorf("disabling prior versions: %w", err)
	}
	if len(r.Body) == 0 {
		r.Body = json.RawMessage(`{}`)
	}
	var out model.CorrelationRule
	row := tx.QueryRowContext(ctx,
		`INSERT INTO correlation_rules (tenant_id, name, version, enabled, trigger, event_type_filter, body, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING `+ruleCols,
		tenantID, name, maxVersion+1, r.Enabled, r.Trigger, r.EventTypeFilter, r.Body, r.CreatedBy)
	if err := scanRule(&out, row); err != nil {
		return nil, fmt.Errorf("inserting new rule version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &out, nil
}

// DeleteCorrelationRule is a soft delete: it disables every version of
// the rule's name (consistent with ADR 003 R1b's prior semantics).
func (s *PostgresStore) DeleteCorrelationRule(ctx context.Context, id string) error {
	tenantID := TenantID(ctx)
	res, err := s.db.ExecContext(ctx,
		`UPDATE correlation_rules SET enabled = FALSE
		  WHERE tenant_id = $1
		    AND name = (SELECT name FROM correlation_rules WHERE id = $2 AND tenant_id = $1)`,
		tenantID, id)
	if err != nil {
		return fmt.Errorf("deleting rule: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ======================================================================
// P4 — Read surface for preview / members / coverage / endpoint detail.
// ======================================================================

func (s *PostgresStore) ListAllAssetsTenant(ctx context.Context) ([]model.Asset, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, host(primary_ip), hostname, fingerprint, resource_type, source, environment,
		        first_seen, last_seen, created_at
		   FROM assets WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing assets: %w", err)
	}
	defer rows.Close()
	var out []model.Asset
	for rows.Next() {
		var a model.Asset
		if err := rows.Scan(&a.ID, &a.TenantID, &a.PrimaryIP, &a.Hostname, &a.Fingerprint,
			&a.ResourceType, &a.Source, &a.Environment, &a.FirstSeen, &a.LastSeen, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListEndpointsForAssetTenant(ctx context.Context, assetID string) ([]model.AssetEndpoint, error) {
	tenantID := TenantID(ctx)
	// Tenant safety: join to assets to filter.
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.id, e.asset_id, e.port, e.protocol, e.service, e.version, e.technologies,
		        e.compliance_status, e.allowlist_status, e.allowlist_checked_at,
		        e.last_scan_id, e.missed_scan_count, e.metadata,
		        e.first_seen, e.last_seen, e.created_at, e.updated_at
		   FROM asset_endpoints e
		   JOIN assets a ON a.id = e.asset_id
		  WHERE e.asset_id = $1 AND a.tenant_id = $2`, assetID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing endpoints: %w", err)
	}
	defer rows.Close()
	var out []model.AssetEndpoint
	for rows.Next() {
		var e model.AssetEndpoint
		if err := rows.Scan(&e.ID, &e.AssetID, &e.Port, &e.Protocol, &e.Service, &e.Version,
			&e.Technologies, &e.ComplianceStatus, &e.AllowlistStatus, &e.AllowlistCheckedAt,
			&e.LastScanID, &e.MissedScanCount, &e.Metadata,
			&e.FirstSeen, &e.LastSeen, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetAssetEndpointByID(ctx context.Context, endpointID string) (*model.AssetEndpoint, *model.Asset, error) {
	tenantID := TenantID(ctx)
	var e model.AssetEndpoint
	var a model.Asset
	err := s.db.QueryRowContext(ctx,
		`SELECT e.id, e.asset_id, e.port, e.protocol, e.service, e.version, e.technologies,
		        e.compliance_status, e.allowlist_status, e.allowlist_checked_at,
		        e.last_scan_id, e.missed_scan_count, e.metadata,
		        e.first_seen, e.last_seen, e.created_at, e.updated_at,
		        a.id, a.tenant_id, host(a.primary_ip), a.hostname, a.fingerprint, a.resource_type, a.source, a.environment,
		        a.first_seen, a.last_seen, a.created_at
		   FROM asset_endpoints e
		   JOIN assets a ON a.id = e.asset_id
		  WHERE e.id = $1 AND a.tenant_id = $2`, endpointID, tenantID).
		Scan(&e.ID, &e.AssetID, &e.Port, &e.Protocol, &e.Service, &e.Version,
			&e.Technologies, &e.ComplianceStatus, &e.AllowlistStatus, &e.AllowlistCheckedAt,
			&e.LastScanID, &e.MissedScanCount, &e.Metadata,
			&e.FirstSeen, &e.LastSeen, &e.CreatedAt, &e.UpdatedAt,
			&a.ID, &a.TenantID, &a.PrimaryIP, &a.Hostname, &a.Fingerprint, &a.ResourceType,
			&a.Source, &a.Environment, &a.FirstSeen, &a.LastSeen, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("getting endpoint: %w", err)
	}
	return &e, &a, nil
}

func (s *PostgresStore) ListAllEndpointViewsTenant(ctx context.Context) ([]EndpointRow, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT a.id, a.tenant_id, host(a.primary_ip), a.hostname, a.fingerprint, a.resource_type, a.source, a.environment,
		        a.first_seen, a.last_seen, a.created_at,
		        e.id, e.asset_id, e.port, e.protocol, e.service, e.version, e.technologies,
		        e.compliance_status, e.allowlist_status, e.allowlist_checked_at,
		        e.last_scan_id, e.missed_scan_count, e.metadata,
		        e.first_seen, e.last_seen, e.created_at, e.updated_at
		   FROM asset_endpoints e
		   JOIN assets a ON a.id = e.asset_id
		  WHERE a.tenant_id = $1`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing endpoint views: %w", err)
	}
	defer rows.Close()
	var out []EndpointRow
	for rows.Next() {
		var r EndpointRow
		if err := rows.Scan(
			&r.Asset.ID, &r.Asset.TenantID, &r.Asset.PrimaryIP, &r.Asset.Hostname, &r.Asset.Fingerprint,
			&r.Asset.ResourceType, &r.Asset.Source, &r.Asset.Environment,
			&r.Asset.FirstSeen, &r.Asset.LastSeen, &r.Asset.CreatedAt,
			&r.Endpoint.ID, &r.Endpoint.AssetID, &r.Endpoint.Port, &r.Endpoint.Protocol,
			&r.Endpoint.Service, &r.Endpoint.Version, &r.Endpoint.Technologies,
			&r.Endpoint.ComplianceStatus, &r.Endpoint.AllowlistStatus, &r.Endpoint.AllowlistCheckedAt,
			&r.Endpoint.LastScanID, &r.Endpoint.MissedScanCount, &r.Endpoint.Metadata,
			&r.Endpoint.FirstSeen, &r.Endpoint.LastSeen, &r.Endpoint.CreatedAt, &r.Endpoint.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const findingCols = `id, tenant_id, asset_endpoint_id, scan_id, source_kind, source, source_id,
	cve_id, severity, title, status, evidence, remediation, first_seen, last_seen, resolved_at`

func scanFinding(f *model.Finding, row interface{ Scan(...any) error }) error {
	return row.Scan(&f.ID, &f.TenantID, &f.AssetEndpointID, &f.ScanID, &f.SourceKind, &f.Source,
		&f.SourceID, &f.CVEID, &f.Severity, &f.Title, &f.Status, &f.Evidence, &f.Remediation,
		&f.FirstSeen, &f.LastSeen, &f.ResolvedAt)
}

func (s *PostgresStore) ListAllFindingsTenant(ctx context.Context) ([]model.Finding, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+findingCols+` FROM findings WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing findings: %w", err)
	}
	defer rows.Close()
	var out []model.Finding
	for rows.Next() {
		var f model.Finding
		if err := scanFinding(&f, rows); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListFindingsForEndpoint(ctx context.Context, endpointID string) ([]model.Finding, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+findingCols+` FROM findings
		  WHERE asset_endpoint_id = $1 AND tenant_id = $2
		  ORDER BY severity, last_seen DESC`, endpointID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing findings for endpoint: %w", err)
	}
	defer rows.Close()
	var out []model.Finding
	for rows.Next() {
		var f model.Finding
		if err := scanFinding(&f, rows); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CountEndpointsByAsset(ctx context.Context) (map[string]int, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT a.id, COUNT(e.id)
		   FROM assets a LEFT JOIN asset_endpoints e ON e.asset_id = a.id
		  WHERE a.tenant_id = $1
		  GROUP BY a.id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("counting endpoints: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// EndpointsWithScanDefinitionByAsset returns, per asset id, the set of
// endpoint ids that have at least one scan_definitions row targeting
// them directly (scope_kind='asset_endpoint').
func (s *PostgresStore) EndpointsWithScanDefinitionByAsset(ctx context.Context) (map[string]map[string]struct{}, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT e.asset_id, e.id
		   FROM scan_definitions sd
		   JOIN asset_endpoints e ON e.id = sd.asset_endpoint_id
		  WHERE sd.tenant_id = $1 AND sd.scope_kind = 'asset_endpoint'`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("scan def coverage: %w", err)
	}
	defer rows.Close()
	out := map[string]map[string]struct{}{}
	for rows.Next() {
		var aid, eid string
		if err := rows.Scan(&aid, &eid); err != nil {
			return nil, err
		}
		if out[aid] == nil {
			out[aid] = map[string]struct{}{}
		}
		out[aid][eid] = struct{}{}
	}
	return out, rows.Err()
}

func (s *PostgresStore) LastScanAtByAsset(ctx context.Context) (map[string]time.Time, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.asset_id, MAX(s.completed_at)
		   FROM scans s
		   JOIN asset_endpoints e ON e.id = s.asset_endpoint_id
		  WHERE s.tenant_id = $1 AND s.completed_at IS NOT NULL
		  GROUP BY e.asset_id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("last scan by asset: %w", err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var aid string
		var t sql.NullTime
		if err := rows.Scan(&aid, &t); err != nil {
			return nil, err
		}
		if t.Valid {
			out[aid] = t.Time
		}
	}
	return out, rows.Err()
}

func (s *PostgresStore) NextScanAtByAsset(ctx context.Context) (map[string]time.Time, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.asset_id, MIN(sd.next_run_at)
		   FROM scan_definitions sd
		   JOIN asset_endpoints e ON e.id = sd.asset_endpoint_id
		  WHERE sd.tenant_id = $1 AND sd.enabled = TRUE AND sd.next_run_at IS NOT NULL
		  GROUP BY e.asset_id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("next scan by asset: %w", err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var aid string
		var t sql.NullTime
		if err := rows.Scan(&aid, &t); err != nil {
			return nil, err
		}
		if t.Valid {
			out[aid] = t.Time
		}
	}
	return out, rows.Err()
}

// FindingsSeverityByEndpoint returns open-finding severity counts keyed
// by endpoint id: { endpointID: { "critical": N, "high": N, ... } }.
func (s *PostgresStore) FindingsSeverityByEndpoint(ctx context.Context) (map[string]map[string]int, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT asset_endpoint_id, COALESCE(severity, 'info'), COUNT(*)
		   FROM findings
		  WHERE tenant_id = $1 AND status = 'open'
		  GROUP BY asset_endpoint_id, severity`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("finding severity rollup: %w", err)
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var eid, sev string
		var n int
		if err := rows.Scan(&eid, &sev, &n); err != nil {
			return nil, err
		}
		if out[eid] == nil {
			out[eid] = map[string]int{}
		}
		out[eid][sev] = n
	}
	return out, rows.Err()
}

// CollectionsWithCredentialMappings returns collections that have at
// least one credential_mapping row — the set we evaluate against each
// endpoint to decide coverage.endpoints_with_credential_mapping.
func (s *PostgresStore) CollectionsWithCredentialMappings(ctx context.Context) ([]model.Collection, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT c.id, c.tenant_id, c.name, c.description, c.scope, c.predicate,
		        c.is_dashboard_widget, c.widget_kind, c.created_at, c.updated_at, c.created_by
		   FROM collections c
		   JOIN credential_mappings m ON m.collection_id = c.id
		  WHERE c.tenant_id = $1`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("collections with mappings: %w", err)
	}
	defer rows.Close()
	var out []model.Collection
	for rows.Next() {
		var c model.Collection
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Name, &c.Description, &c.Scope, &c.Predicate,
			&c.IsDashboardWidget, &c.WidgetKind, &c.CreatedAt, &c.UpdatedAt, &c.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpsertFinding upserts on (asset_endpoint_id, source_kind, source, source_id).
// A re-seen finding bumps last_seen and, if status was not suppressed, reopens
// status per ADR 007 D2. If the incoming status is 'resolved' (compliance pass),
// existing open rows flip to resolved + resolved_at set.
func (s *PostgresStore) UpsertFinding(ctx context.Context, in UpsertFindingInput) (*model.Finding, error) {
	ev := in.Evidence
	if len(ev) == 0 {
		ev = json.RawMessage(`{}`)
	}
	srcID := in.SourceID
	// Try update first (partial unique on (endpoint, source_kind, source, source_id)).
	// We use a SELECT + branch so we can set resolved_at correctly.
	var existing model.Finding
	row := s.db.QueryRowContext(ctx,
		`SELECT `+findingCols+` FROM findings
		   WHERE asset_endpoint_id = $1 AND source_kind = $2 AND source = $3
		     AND COALESCE(source_id,'') = COALESCE($4,'')
		   ORDER BY first_seen DESC LIMIT 1`,
		in.AssetEndpointID, in.SourceKind, in.Source, srcID)
	err := scanFinding(&existing, row)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("looking up finding: %w", err)
	}
	if err == nil {
		// Row exists — update last_seen, possibly transition status.
		newStatus := existing.Status
		var resolvedAt *time.Time
		switch {
		case existing.Status == model.FindingStatusSuppressed:
			// Honor suppression; don't re-open just because we saw it again.
		case in.Status == model.FindingStatusResolved:
			newStatus = model.FindingStatusResolved
			now := time.Now().UTC()
			resolvedAt = &now
		default:
			// Any re-appearance of a finding that is not resolved should
			// be open (reopen if it was resolved previously).
			newStatus = model.FindingStatusOpen
			resolvedAt = nil
		}
		var out model.Finding
		upd := s.db.QueryRowContext(ctx,
			`UPDATE findings SET
			   scan_id = COALESCE($2, scan_id),
			   cve_id = COALESCE($3, cve_id),
			   severity = COALESCE($4, severity),
			   title = $5,
			   status = $6,
			   evidence = $7,
			   remediation = COALESCE($8, remediation),
			   last_seen = NOW(),
			   resolved_at = $9
			 WHERE id = $1 AND first_seen = $10
			 RETURNING `+findingCols,
			existing.ID, in.ScanID, in.CVEID, in.Severity, in.Title, newStatus, ev,
			in.Remediation, resolvedAt, existing.FirstSeen)
		if err := scanFinding(&out, upd); err != nil {
			return nil, fmt.Errorf("updating finding: %w", err)
		}
		return &out, nil
	}
	// Insert.
	status := in.Status
	if status == "" {
		status = model.FindingStatusOpen
	}
	var resolvedAt *time.Time
	if status == model.FindingStatusResolved {
		now := time.Now().UTC()
		resolvedAt = &now
	}
	var out model.Finding
	ins := s.db.QueryRowContext(ctx,
		`INSERT INTO findings
		   (tenant_id, asset_endpoint_id, scan_id, source_kind, source, source_id,
		    cve_id, severity, title, status, evidence, remediation, resolved_at)
		 VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,$10,$11,$12,$13)
		 RETURNING `+findingCols,
		in.TenantID, in.AssetEndpointID, in.ScanID, in.SourceKind, in.Source, srcID,
		in.CVEID, in.Severity, in.Title, status, ev, in.Remediation, resolvedAt)
	if err := scanFinding(&out, ins); err != nil {
		return nil, fmt.Errorf("inserting finding: %w", err)
	}
	return &out, nil
}

func (s *PostgresStore) ListFindings(ctx context.Context, f FindingFilter) ([]model.Finding, error) {
	tenantID := TenantID(ctx)
	args := []any{tenantID}
	where := `tenant_id = $1`
	add := func(clause string, v any) {
		args = append(args, v)
		where += fmt.Sprintf(" AND %s = $%d", clause, len(args))
	}
	if f.SourceKind != "" {
		add("source_kind", f.SourceKind)
	}
	if f.Source != "" {
		add("source", f.Source)
	}
	if f.Severity != "" {
		add("severity", f.Severity)
	}
	if f.Status != "" {
		add("status", f.Status)
	}
	if f.AssetEndpointID != "" {
		add("asset_endpoint_id", f.AssetEndpointID)
	}
	if f.CVEID != "" {
		add("cve_id", f.CVEID)
	}
	if f.Since != nil {
		args = append(args, *f.Since)
		where += fmt.Sprintf(" AND last_seen >= $%d", len(args))
	}
	if f.Until != nil {
		args = append(args, *f.Until)
		where += fmt.Sprintf(" AND last_seen <= $%d", len(args))
	}
	if f.CollectionID != "" {
		ids, err := s.CollectionEndpointIDs(ctx, f.CollectionID)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return []model.Finding{}, nil
		}
		args = append(args, ids)
		where += fmt.Sprintf(" AND asset_endpoint_id = ANY($%d::uuid[])", len(args))
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := `SELECT ` + findingCols + ` FROM findings WHERE ` + where +
		` ORDER BY last_seen DESC LIMIT ` + fmt.Sprintf("%d", limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("listing findings: %w", err)
	}
	defer rows.Close()
	var out []model.Finding
	for rows.Next() {
		var f model.Finding
		if err := scanFinding(&f, rows); err != nil {
			return nil, fmt.Errorf("scanning finding: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetFindingByID(ctx context.Context, id string) (*model.Finding, error) {
	tenantID := TenantID(ctx)
	var f model.Finding
	row := s.db.QueryRowContext(ctx,
		`SELECT `+findingCols+` FROM findings WHERE id = $1 AND tenant_id = $2
		   ORDER BY first_seen DESC LIMIT 1`, id, tenantID)
	if err := scanFinding(&f, row); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting finding: %w", err)
	}
	return &f, nil
}

func (s *PostgresStore) SetFindingStatus(ctx context.Context, id, status string) error {
	tenantID := TenantID(ctx)
	var resolvedExpr string
	switch status {
	case model.FindingStatusResolved, model.FindingStatusSuppressed:
		resolvedExpr = `resolved_at = NOW()`
	case model.FindingStatusOpen:
		resolvedExpr = `resolved_at = NULL`
	default:
		return fmt.Errorf("invalid finding status: %s", status)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE findings SET status = $1, `+resolvedExpr+`
		   WHERE id = $2 AND tenant_id = $3`, status, id, tenantID)
	if err != nil {
		return fmt.Errorf("updating finding status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ======================================================================
// Scan definitions (ADR 007 D3)
// ======================================================================

const scanDefCols = `id, tenant_id, name, kind, bundle_id, scope_kind,
	asset_endpoint_id, collection_id, cidr::text,
	agent_id, schedule, enabled, next_run_at, last_run_at, last_run_status,
	created_at, updated_at, created_by`

func scanScanDefinition(d *model.ScanDefinition, row interface{ Scan(...any) error }) error {
	return row.Scan(&d.ID, &d.TenantID, &d.Name, &d.Kind, &d.BundleID, &d.ScopeKind,
		&d.AssetEndpointID, &d.CollectionID, &d.CIDR, &d.AgentID, &d.Schedule,
		&d.Enabled, &d.NextRunAt, &d.LastRunAt, &d.LastRunStatus,
		&d.CreatedAt, &d.UpdatedAt, &d.CreatedBy)
}

func (s *PostgresStore) CreateScanDefinition(ctx context.Context, in model.ScanDefinition) (*model.ScanDefinition, error) {
	tenantID := TenantID(ctx)
	var out model.ScanDefinition
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO scan_definitions
		   (tenant_id, name, kind, bundle_id, scope_kind,
		    asset_endpoint_id, collection_id, cidr,
		    agent_id, schedule, enabled, next_run_at, created_by)
		 VALUES ($1,$2,$3,$4,$5,$6,$7, NULLIF($8,'')::cidr, $9,$10,$11,$12,$13)
		 RETURNING `+scanDefCols,
		tenantID, in.Name, in.Kind, in.BundleID, in.ScopeKind,
		in.AssetEndpointID, in.CollectionID, strOrEmpty(in.CIDR),
		in.AgentID, in.Schedule, in.Enabled, in.NextRunAt, in.CreatedBy)
	if err := scanScanDefinition(&out, row); err != nil {
		return nil, fmt.Errorf("creating scan definition: %w", err)
	}
	return &out, nil
}

func (s *PostgresStore) GetScanDefinition(ctx context.Context, id string) (*model.ScanDefinition, error) {
	tenantID := TenantID(ctx)
	var d model.ScanDefinition
	row := s.db.QueryRowContext(ctx,
		`SELECT `+scanDefCols+` FROM scan_definitions WHERE id = $1 AND tenant_id = $2`,
		id, tenantID)
	if err := scanScanDefinition(&d, row); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting scan definition: %w", err)
	}
	return &d, nil
}

func (s *PostgresStore) ListScanDefinitions(ctx context.Context) ([]model.ScanDefinition, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+scanDefCols+` FROM scan_definitions WHERE tenant_id = $1
		   ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing scan definitions: %w", err)
	}
	defer rows.Close()
	var out []model.ScanDefinition
	for rows.Next() {
		var d model.ScanDefinition
		if err := scanScanDefinition(&d, rows); err != nil {
			return nil, fmt.Errorf("scanning scan definition: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateScanDefinition(ctx context.Context, in model.ScanDefinition) (*model.ScanDefinition, error) {
	tenantID := TenantID(ctx)
	var out model.ScanDefinition
	row := s.db.QueryRowContext(ctx,
		`UPDATE scan_definitions SET
		   name = $1, kind = $2, bundle_id = $3, scope_kind = $4,
		   asset_endpoint_id = $5, collection_id = $6,
		   cidr = NULLIF($7,'')::cidr,
		   agent_id = $8, schedule = $9, enabled = $10, next_run_at = $11,
		   updated_at = NOW()
		 WHERE id = $12 AND tenant_id = $13
		 RETURNING `+scanDefCols,
		in.Name, in.Kind, in.BundleID, in.ScopeKind,
		in.AssetEndpointID, in.CollectionID, strOrEmpty(in.CIDR),
		in.AgentID, in.Schedule, in.Enabled, in.NextRunAt,
		in.ID, tenantID)
	if err := scanScanDefinition(&out, row); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("updating scan definition: %w", err)
	}
	return &out, nil
}

func (s *PostgresStore) DeleteScanDefinition(ctx context.Context, id string) error {
	tenantID := TenantID(ctx)
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM scan_definitions WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err != nil {
		return fmt.Errorf("deleting scan definition: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *PostgresStore) SetScanDefinitionEnabled(ctx context.Context, id string, enabled bool, nextRunAt *time.Time) error {
	tenantID := TenantID(ctx)
	_, err := s.db.ExecContext(ctx,
		`UPDATE scan_definitions SET enabled = $1, next_run_at = $2, updated_at = NOW()
		   WHERE id = $3 AND tenant_id = $4`, enabled, nextRunAt, id, tenantID)
	if err != nil {
		return fmt.Errorf("toggling scan definition: %w", err)
	}
	return nil
}

func (s *PostgresStore) SetScanDefinitionNextRun(ctx context.Context, id string, nextRunAt *time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE scan_definitions SET next_run_at = $1, updated_at = NOW() WHERE id = $2`,
		nextRunAt, id)
	if err != nil {
		return fmt.Errorf("setting next_run_at: %w", err)
	}
	return nil
}

func (s *PostgresStore) SetScanDefinitionLastRun(ctx context.Context, id string, at time.Time, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE scan_definitions SET last_run_at = $1, last_run_status = $2, updated_at = NOW()
		   WHERE id = $3`, at, status, id)
	if err != nil {
		return fmt.Errorf("setting last_run: %w", err)
	}
	return nil
}

// ClaimDueScanDefinitions atomically advances next_run_at for due
// definitions and returns the claimed rows. `nextFn(schedule, from)`
// computes the next run time for a given cron expression. The advance
// happens inside the same UPDATE ... WHERE id IN (SELECT ... FOR UPDATE
// SKIP LOCKED) so concurrent pollers never claim the same row.
//
// Crash recovery: if the caller crashes after claim but before dispatch,
// next_run_at has advanced but the scans row was never created; the
// definition simply misses that tick and fires on the next one. ADR 007
// D4 accepts this — operators lose a tick, not a definition.
func (s *PostgresStore) ClaimDueScanDefinitions(ctx context.Context, now time.Time, nextFn func(string, time.Time) (time.Time, error), limit int) ([]model.ScanDefinition, error) {
	if limit <= 0 {
		limit = 32
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning tx: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`SELECT `+scanDefCols+` FROM scan_definitions
		   WHERE enabled = TRUE AND schedule IS NOT NULL AND next_run_at IS NOT NULL
		     AND next_run_at <= $1
		   ORDER BY next_run_at ASC
		   FOR UPDATE SKIP LOCKED
		   LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("selecting due scan definitions: %w", err)
	}
	var due []model.ScanDefinition
	for rows.Next() {
		var d model.ScanDefinition
		if err := scanScanDefinition(&d, rows); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning due row: %w", err)
		}
		due = append(due, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, d := range due {
		var schedule string
		if d.Schedule != nil {
			schedule = *d.Schedule
		}
		next, err := nextFn(schedule, now)
		if err != nil {
			// Skip advancing — leave next_run_at as-is so operator sees
			// it as perpetually due and can fix the expression.
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE scan_definitions SET next_run_at = $1, updated_at = NOW() WHERE id = $2`,
			next, d.ID); err != nil {
			return nil, fmt.Errorf("advancing next_run_at: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing tx: %w", err)
	}
	return due, nil
}

func (s *PostgresStore) CreateScanForDefinition(ctx context.Context, in CreateScanForDefinitionInput) (*model.Scan, error) {
	if in.ScanType == "" {
		in.ScanType = model.ScanTypeCompliance
	}
	var sc model.Scan
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO scans
		   (tenant_id, scan_definition_id, agent_id, target_id, asset_endpoint_id,
		    bundle_id, scan_type, status)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 RETURNING `+scanCols,
		in.TenantID, in.ScanDefinitionID, in.AgentID, in.TargetID, in.AssetEndpointID,
		in.BundleID, in.ScanType, model.ScanStatusPending)
	if err := scanScan(&sc, row); err != nil {
		return nil, fmt.Errorf("creating scan for definition: %w", err)
	}
	return &sc, nil
}

// CollectionEndpointIDs resolves a collection to the concrete endpoint
// ids that match its predicate. For scope=asset collections it returns
// all endpoints for matching assets. For scope=finding it returns the
// distinct endpoint ids of open findings matching the predicate.
//
// P3 supports a narrow predicate grammar: pass-through (empty object
// returns all endpoints for the tenant), plus the simple `{and|or: [...]}`
// shape. Deep predicate evaluation lands in P4 when `rules.Match` grows
// a collection-backed SQL translator; for now the scheduler's dispatch
// gets a working "every endpoint in this tenant" result for
// empty/permissive predicates, which matches the shape of the studio
// tenant's test collections.
func (s *PostgresStore) CollectionEndpointIDs(ctx context.Context, collectionID string) ([]string, error) {
	tenantID := TenantID(ctx)
	coll, err := s.GetCollection(ctx, collectionID)
	if err != nil {
		return nil, err
	}
	if coll == nil {
		return nil, nil
	}
	_ = tenantID
	// P3 best-effort resolver: return every endpoint owned by this tenant.
	// P4 will replace this with a predicate translator that honors
	// scope=asset | endpoint | finding.
	rows, err := s.db.QueryContext(ctx,
		`SELECT ae.id FROM asset_endpoints ae
		   JOIN assets a ON a.id = ae.asset_id
		   WHERE a.tenant_id = $1`, coll.TenantID)
	if err != nil {
		return nil, fmt.Errorf("resolving collection endpoints: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func strOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ======================================================================
// Agent log events
// ======================================================================

func (s *PostgresStore) InsertAgentLogEvent(ctx context.Context, in AgentLogEventInput) error {
	var scanID *string
	if in.ScanID != "" {
		scanID = &in.ScanID
	}
	attrs := in.Attrs
	if len(attrs) == 0 {
		attrs = json.RawMessage("{}")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_log_events (tenant_id, agent_id, scan_id, level, msg, attrs)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		in.TenantID, in.AgentID, scanID, in.Level, in.Msg, attrs)
	if err != nil {
		return fmt.Errorf("inserting agent log event: %w", err)
	}
	return nil
}

func (s *PostgresStore) ListAgentLogEvents(ctx context.Context, agentID string, f AgentLogFilter) ([]model.AgentLogEvent, int, error) {
	tenantID := TenantID(ctx)

	// Build WHERE clause
	where := "WHERE tenant_id = $1 AND agent_id = $2"
	args := []any{tenantID, agentID}
	idx := 3

	if f.Since != nil {
		where += fmt.Sprintf(" AND occurred_at >= $%d", idx)
		args = append(args, *f.Since)
		idx++
	}
	if f.Until != nil {
		where += fmt.Sprintf(" AND occurred_at <= $%d", idx)
		args = append(args, *f.Until)
		idx++
	}
	if f.Level != "" {
		where += fmt.Sprintf(" AND UPPER(level) = UPPER($%d)", idx)
		args = append(args, f.Level)
		idx++
	}
	if f.ScanID != "" {
		where += fmt.Sprintf(" AND scan_id = $%d", idx)
		args = append(args, f.ScanID)
		idx++
	}

	// Count
	var total int
	countQ := "SELECT COUNT(*) FROM agent_log_events " + where
	if err := s.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting agent log events: %w", err)
	}

	// Order
	order := "DESC"
	if f.Order == "asc" {
		order = "ASC"
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}

	q := fmt.Sprintf(
		`SELECT id, agent_id, scan_id, level, msg, attrs, occurred_at
		 FROM agent_log_events %s
		 ORDER BY occurred_at %s
		 LIMIT $%d`, where, order, idx)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing agent log events: %w", err)
	}
	defer rows.Close()

	var items []model.AgentLogEvent
	for rows.Next() {
		var e model.AgentLogEvent
		if err := rows.Scan(&e.ID, &e.AgentID, &e.ScanID, &e.Level, &e.Msg, &e.Attrs, &e.OccurredAt); err != nil {
			return nil, 0, fmt.Errorf("scanning agent log event: %w", err)
		}
		items = append(items, e)
	}
	if items == nil {
		items = []model.AgentLogEvent{}
	}
	return items, total, rows.Err()
}

func (s *PostgresStore) DeleteOldAgentLogs(ctx context.Context, maxAge time.Duration) (int, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM agent_log_events WHERE occurred_at < NOW() - make_interval(secs => $1)`,
		int(maxAge.Seconds()))
	if err != nil {
		return 0, fmt.Errorf("deleting old agent logs: %w", err)
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}

// --- Compliance profiles (ADR 010 D9 — Level 3A) --------------------

const profileCols = `id, tenant_id, name, description, base_framework, status, version, bundle_id, created_at, updated_at, created_by`

type profileScanner interface {
	Scan(dest ...any) error
}

func scanProfile(p *model.ComplianceProfile, s profileScanner) error {
	return s.Scan(&p.ID, &p.TenantID, &p.Name, &p.Description, &p.BaseFramework,
		&p.Status, &p.Version, &p.BundleID, &p.CreatedAt, &p.UpdatedAt, &p.CreatedBy)
}

func (s *PostgresStore) ListComplianceProfiles(ctx context.Context) ([]model.ComplianceProfile, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+profileCols+`,
		        (SELECT COUNT(*) FROM profile_controls pc WHERE pc.profile_id = cp.id) AS control_count
		 FROM compliance_profiles cp
		 WHERE cp.tenant_id = $1
		 ORDER BY cp.name`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing compliance profiles: %w", err)
	}
	defer rows.Close()
	var out []model.ComplianceProfile
	for rows.Next() {
		var p model.ComplianceProfile
		if err := rows.Scan(&p.ID, &p.TenantID, &p.Name, &p.Description, &p.BaseFramework,
			&p.Status, &p.Version, &p.BundleID, &p.CreatedAt, &p.UpdatedAt, &p.CreatedBy, &p.ControlCount); err != nil {
			return nil, fmt.Errorf("scanning compliance profile: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetComplianceProfile(ctx context.Context, id string) (*model.ComplianceProfile, error) {
	tenantID := TenantID(ctx)
	var p model.ComplianceProfile
	row := s.db.QueryRowContext(ctx,
		`SELECT `+profileCols+`,
		        (SELECT COUNT(*) FROM profile_controls pc WHERE pc.profile_id = cp.id) AS control_count
		 FROM compliance_profiles cp
		 WHERE cp.id = $1 AND cp.tenant_id = $2`, id, tenantID)
	if err := row.Scan(&p.ID, &p.TenantID, &p.Name, &p.Description, &p.BaseFramework,
		&p.Status, &p.Version, &p.BundleID, &p.CreatedAt, &p.UpdatedAt, &p.CreatedBy, &p.ControlCount); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting compliance profile: %w", err)
	}
	return &p, nil
}

func (s *PostgresStore) CreateComplianceProfile(ctx context.Context, p model.ComplianceProfile) (*model.ComplianceProfile, error) {
	tenantID := TenantID(ctx)
	var out model.ComplianceProfile
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO compliance_profiles (tenant_id, name, description, base_framework, created_by)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING `+profileCols,
		tenantID, p.Name, p.Description, p.BaseFramework, p.CreatedBy)
	if err := scanProfile(&out, row); err != nil {
		return nil, fmt.Errorf("creating compliance profile: %w", err)
	}
	return &out, nil
}

func (s *PostgresStore) UpdateComplianceProfile(ctx context.Context, id string, p model.ComplianceProfile) (*model.ComplianceProfile, error) {
	tenantID := TenantID(ctx)
	var out model.ComplianceProfile
	row := s.db.QueryRowContext(ctx,
		`UPDATE compliance_profiles SET name = $1, description = $2, updated_at = NOW()
		 WHERE id = $3 AND tenant_id = $4
		 RETURNING `+profileCols,
		p.Name, p.Description, id, tenantID)
	if err := scanProfile(&out, row); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("updating compliance profile: %w", err)
	}
	return &out, nil
}

func (s *PostgresStore) DeleteComplianceProfile(ctx context.Context, id string) error {
	tenantID := TenantID(ctx)
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM compliance_profiles WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err != nil {
		return fmt.Errorf("deleting compliance profile: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *PostgresStore) SetProfileControls(ctx context.Context, profileID string, controlIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, `DELETE FROM profile_controls WHERE profile_id = $1`, profileID); err != nil {
		return fmt.Errorf("clearing profile controls: %w", err)
	}

	if len(controlIDs) > 0 {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO profile_controls (profile_id, control_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`)
		if err != nil {
			return fmt.Errorf("preparing insert: %w", err)
		}
		defer stmt.Close()
		for _, cid := range controlIDs {
			if _, err := stmt.ExecContext(ctx, profileID, cid); err != nil {
				return fmt.Errorf("inserting profile control %s: %w", cid, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing profile controls: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetProfileControls(ctx context.Context, profileID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT control_id FROM profile_controls WHERE profile_id = $1 ORDER BY control_id`, profileID)
	if err != nil {
		return nil, fmt.Errorf("listing profile controls: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			return nil, fmt.Errorf("scanning profile control: %w", err)
		}
		out = append(out, cid)
	}
	return out, rows.Err()
}

func (s *PostgresStore) PublishProfile(ctx context.Context, profileID string, bundleID string) error {
	tenantID := TenantID(ctx)
	result, err := s.db.ExecContext(ctx,
		`UPDATE compliance_profiles
		 SET status = $1, version = version + 1, bundle_id = $2, updated_at = NOW()
		 WHERE id = $3 AND tenant_id = $4`,
		model.ProfileStatusPublished, bundleID, profileID, tenantID)
	if err != nil {
		return fmt.Errorf("publishing profile: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ======================================================================
// Collected facts (ADR 011 D4)
// ======================================================================

func (s *PostgresStore) InsertCollectedFacts(ctx context.Context, f model.CollectedFacts) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO collected_facts (id, tenant_id, asset_endpoint_id, scan_id, collector_id, facts, collected_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		f.ID, f.TenantID, f.AssetEndpointID, f.ScanID, f.CollectorID, f.Facts, f.CollectedAt)
	if err != nil {
		return fmt.Errorf("inserting collected facts: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetCollectedFactsByScan(ctx context.Context, scanID string) ([]model.CollectedFacts, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, asset_endpoint_id, scan_id, collector_id, facts, collected_at
		   FROM collected_facts WHERE scan_id = $1 ORDER BY collected_at ASC`, scanID)
	if err != nil {
		return nil, fmt.Errorf("querying collected facts by scan: %w", err)
	}
	defer rows.Close()
	var out []model.CollectedFacts
	for rows.Next() {
		var f model.CollectedFacts
		if err := rows.Scan(&f.ID, &f.TenantID, &f.AssetEndpointID, &f.ScanID, &f.CollectorID, &f.Facts, &f.CollectedAt); err != nil {
			return nil, fmt.Errorf("scanning collected facts: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetLatestFactsForEndpoint(ctx context.Context, endpointID string) (*model.CollectedFacts, error) {
	var f model.CollectedFacts
	err := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, asset_endpoint_id, scan_id, collector_id, facts, collected_at
		   FROM collected_facts WHERE asset_endpoint_id = $1 ORDER BY collected_at DESC LIMIT 1`, endpointID).
		Scan(&f.ID, &f.TenantID, &f.AssetEndpointID, &f.ScanID, &f.CollectorID, &f.Facts, &f.CollectedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting latest facts for endpoint: %w", err)
	}
	return &f, nil
}

func (s *PostgresStore) DeleteOldCollectedFacts(ctx context.Context, maxAge time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-maxAge)
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM collected_facts WHERE collected_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("deleting old collected facts: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// ======================================================================
// Tenant policies (ADR 011 D9)
// ======================================================================

const tenantPolicyCols = `id, tenant_id, control_id, origin, based_on,
	name, severity, rego_source, collector_id,
	fact_keys, frameworks, tags, enabled, created_at, updated_at`

func scanTenantPolicy(row interface{ Scan(dest ...any) error }) (*model.TenantPolicy, error) {
	var p model.TenantPolicy
	if err := row.Scan(
		&p.ID, &p.TenantID, &p.ControlID, &p.Origin, &p.BasedOn,
		&p.Name, &p.Severity, &p.RegoSource, &p.CollectorID,
		&p.FactKeys, &p.Frameworks, &p.Tags, &p.Enabled,
		&p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *PostgresStore) ListTenantPolicies(ctx context.Context) ([]model.TenantPolicy, error) {
	tenantID := TenantID(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+tenantPolicyCols+` FROM tenant_policies
		 WHERE tenant_id = $1 ORDER BY name`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing tenant policies: %w", err)
	}
	defer rows.Close()
	var out []model.TenantPolicy
	for rows.Next() {
		p, err := scanTenantPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning tenant policy: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetTenantPolicy(ctx context.Context, id string) (*model.TenantPolicy, error) {
	tenantID := TenantID(ctx)
	row := s.db.QueryRowContext(ctx,
		`SELECT `+tenantPolicyCols+` FROM tenant_policies
		 WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	p, err := scanTenantPolicy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting tenant policy: %w", err)
	}
	return p, nil
}

func (s *PostgresStore) CreateTenantPolicy(ctx context.Context, p model.TenantPolicy) (*model.TenantPolicy, error) {
	tenantID := TenantID(ctx)
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO tenant_policies
			(tenant_id, control_id, origin, based_on, name, severity,
			 rego_source, collector_id, fact_keys, frameworks, tags, enabled)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		 RETURNING `+tenantPolicyCols,
		tenantID, p.ControlID, p.Origin, p.BasedOn, p.Name, p.Severity,
		p.RegoSource, p.CollectorID, p.FactKeys, p.Frameworks, p.Tags, p.Enabled)
	out, err := scanTenantPolicy(row)
	if err != nil {
		return nil, fmt.Errorf("creating tenant policy: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) UpdateTenantPolicy(ctx context.Context, id string, p model.TenantPolicy) (*model.TenantPolicy, error) {
	tenantID := TenantID(ctx)
	row := s.db.QueryRowContext(ctx,
		`UPDATE tenant_policies SET
			control_id = $3, name = $4, severity = $5, rego_source = $6,
			collector_id = $7, fact_keys = $8, frameworks = $9, tags = $10,
			enabled = $11, updated_at = NOW()
		 WHERE id = $1 AND tenant_id = $2
		 RETURNING `+tenantPolicyCols,
		id, tenantID, p.ControlID, p.Name, p.Severity, p.RegoSource,
		p.CollectorID, p.FactKeys, p.Frameworks, p.Tags, p.Enabled)
	out, err := scanTenantPolicy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("updating tenant policy: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) DeleteTenantPolicy(ctx context.Context, id string) error {
	tenantID := TenantID(ctx)
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM tenant_policies WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err != nil {
		return fmt.Errorf("deleting tenant policy: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("tenant policy not found")
	}
	return nil
}

func (s *PostgresStore) ListTenantPoliciesForTenant(ctx context.Context, tenantID string) ([]model.TenantPolicy, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+tenantPolicyCols+` FROM tenant_policies
		 WHERE tenant_id = $1 AND enabled = true ORDER BY name`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing tenant policies for tenant: %w", err)
	}
	defer rows.Close()
	var out []model.TenantPolicy
	for rows.Next() {
		p, err := scanTenantPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning tenant policy: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
