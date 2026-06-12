-- NOTE: restoring the IP-only unique index will fail if http_service assets
-- already share a primary_ip (with each other or a host) — that data is by
-- definition unrepresentable in the pre-014 schema, so a rollback after such
-- assets exist must first remove/merge them. On a compatible DB this reverses
-- cleanly.
DROP INDEX IF EXISTS idx_assets_tenant_hostname;
DROP INDEX IF EXISTS idx_assets_tenant_ip;
CREATE UNIQUE INDEX idx_assets_tenant_ip ON assets(tenant_id, primary_ip)
    WHERE primary_ip IS NOT NULL;
