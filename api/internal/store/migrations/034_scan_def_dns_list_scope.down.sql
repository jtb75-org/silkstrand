-- Revert to the agent_allowlist-era constraint. Existing dns_list definitions
-- would violate it, so remove them first.
DELETE FROM scan_definitions WHERE scope_kind = 'dns_list';
ALTER TABLE scan_definitions DROP CONSTRAINT scan_definitions_scope_exactly_one;
ALTER TABLE scan_definitions ADD CONSTRAINT scan_definitions_scope_exactly_one CHECK (
    (scope_kind = 'asset_endpoint' AND asset_endpoint_id IS NOT NULL
        AND collection_id IS NULL AND cidr IS NULL) OR
    (scope_kind = 'collection' AND collection_id IS NOT NULL
        AND asset_endpoint_id IS NULL AND cidr IS NULL) OR
    (scope_kind = 'cidr' AND cidr IS NOT NULL
        AND asset_endpoint_id IS NULL AND collection_id IS NULL) OR
    (scope_kind = 'agent_allowlist' AND agent_id IS NOT NULL
        AND asset_endpoint_id IS NULL AND collection_id IS NULL AND cidr IS NULL)
);
