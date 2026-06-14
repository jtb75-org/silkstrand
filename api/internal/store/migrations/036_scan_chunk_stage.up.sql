-- #387: live scan progress. Persist the last recon stage a chunk reported
-- (naabu|httpx|nuclei) so GET /api/v1/scans/{id} can show current_stage after
-- a drawer reopen / SSE reconnect, and so stage_progress events can be deduped
-- to stage changes. Nullable; only chunked discovery sets it.
ALTER TABLE scan_chunks ADD COLUMN current_stage TEXT;
