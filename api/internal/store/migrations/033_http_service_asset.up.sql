-- ADR 014 D1: a virtual host (resource_type='http_service') is keyed on its
-- NAME, so multiple vhosts behind one ingress / reverse-proxy IP are distinct
-- assets instead of collapsing onto a shared primary_ip.
--   (a) Exclude http_service from IP-uniqueness — these assets legitimately
--       share the ingress primary_ip, so it must not be a conflict axis.
--   (b) Give them their own name-uniqueness index.
DROP INDEX IF EXISTS idx_assets_tenant_ip;
CREATE UNIQUE INDEX idx_assets_tenant_ip ON assets(tenant_id, primary_ip)
    WHERE primary_ip IS NOT NULL AND resource_type <> 'http_service';
CREATE UNIQUE INDEX idx_assets_tenant_hostname ON assets(tenant_id, lower(hostname))
    WHERE resource_type = 'http_service' AND hostname IS NOT NULL;
