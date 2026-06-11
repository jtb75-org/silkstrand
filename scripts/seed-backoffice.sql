-- Seed data for the backoffice database.
-- Idempotent: safe to run multiple times.
-- Targets the local Docker Postgres on port 15433.

-- Data center entry pointing at the local DC API.
-- Local dev uses http://localhost:8080. In a k3s pseudo-DC deployment the
-- api_url is the in-cluster service DNS name of that DC's API, e.g.
--   http://silkstrand-api.dc-us.svc.cluster.local:8080
-- (no trailing slash; NormalizeBaseURL validates/trims it on write).
INSERT INTO data_centers (id, name, region, api_url, api_key_encrypted, status) VALUES (
  '00000000-0000-0000-0000-000000000050',
  'Local Dev DC',
  'us-central1',
  'http://localhost:8080',
  'dev-internal-key',
  'active'
) ON CONFLICT (id) DO NOTHING;

-- Admin user (password: admin123)
-- Hash generated with bcrypt cost 10
INSERT INTO admin_users (id, email, password_hash, role) VALUES (
  '00000000-0000-0000-0000-000000000100',
  'admin@silkstrand.io',
  '$2a$10$w.nVhdiYF6oo4qtrNAG0t.qNndy3VLNhHbXxW5mPSA82KGhzGwaiu',
  'super_admin'
) ON CONFLICT (id) DO NOTHING;

-- Backoffice tenant linked to the DC tenant
INSERT INTO tenants (id, dc_tenant_id, data_center_id, name, status, provisioning_status) VALUES (
  '00000000-0000-0000-0000-000000000060',
  '00000000-0000-0000-0000-000000000001',
  '00000000-0000-0000-0000-000000000050',
  'Test Tenant',
  'active',
  'provisioned'
) ON CONFLICT (id) DO NOTHING;
