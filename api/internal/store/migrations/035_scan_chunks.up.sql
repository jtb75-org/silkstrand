-- Large discovery scans are checkpointed as durable chunks. Each chunk runs
-- the full recon pipeline and commits at the chunk boundary; the parent scans
-- row remains the user-visible scan.
CREATE TABLE scan_chunks (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    scan_id UUID NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    agent_id UUID REFERENCES agents(id) ON DELETE SET NULL,
    chunk_index INT NOT NULL,
    target_type TEXT NOT NULL,
    target_identifier TEXT NOT NULL,
    ip_start INET,
    ip_end INET,
    ip_count INT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    assets_found INT NOT NULL DEFAULT 0,
    hosts_scanned INT NOT NULL DEFAULT 0,
    error_message TEXT,
    dispatched_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT scan_chunks_status_check CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    CONSTRAINT scan_chunks_index_nonnegative CHECK (chunk_index >= 0),
    CONSTRAINT scan_chunks_counts_nonnegative CHECK (
        ip_count >= 0 AND attempts >= 0 AND assets_found >= 0 AND hosts_scanned >= 0
    ),
    UNIQUE (scan_id, chunk_index)
);

CREATE INDEX idx_scan_chunks_next
    ON scan_chunks (agent_id, scan_id, chunk_index)
    WHERE status IN ('pending', 'failed');

CREATE INDEX idx_scan_chunks_scan_status
    ON scan_chunks (scan_id, status);
