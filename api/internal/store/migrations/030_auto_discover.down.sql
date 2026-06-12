ALTER TABLE agents
    DROP COLUMN IF EXISTS auto_discover_pending,
    DROP COLUMN IF EXISTS discover_cron;

ALTER TABLE install_tokens
    DROP COLUMN IF EXISTS auto_discover,
    DROP COLUMN IF EXISTS discover_cron;
