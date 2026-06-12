-- Revert: drop agent_allowlist from the scope constraint. Remove any
-- agent_allowlist definitions first so the narrower constraint can be re-added.
DELETE FROM scan_definitions WHERE scope_kind = 'agent_allowlist';
ALTER TABLE scan_definitions DROP CONSTRAINT scan_definitions_scope_exactly_one;
ALTER TABLE scan_definitions ADD CONSTRAINT scan_definitions_scope_exactly_one CHECK (
    (scope_kind = 'asset_endpoint' AND asset_endpoint_id IS NOT NULL
        AND collection_id IS NULL AND cidr IS NULL) OR
    (scope_kind = 'collection' AND collection_id IS NOT NULL
        AND asset_endpoint_id IS NULL AND cidr IS NULL) OR
    (scope_kind = 'cidr' AND cidr IS NOT NULL
        AND asset_endpoint_id IS NULL AND collection_id IS NULL)
);
