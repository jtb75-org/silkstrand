-- ADR 013 PR 3: auto-discover on connect.
-- The install token carries the operator's intent; it is copied onto the
-- agent at bootstrap and consumed (fire-once) when the agent reports its
-- first allowlist snapshot.
ALTER TABLE install_tokens
    ADD COLUMN auto_discover BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN discover_cron TEXT;

ALTER TABLE agents
    ADD COLUMN auto_discover_pending BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN discover_cron TEXT;
