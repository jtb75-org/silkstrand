-- ADR 013 follow-up: agents report whether they run inside a container, so the
-- UI can offer "recreate from image" instead of the in-place self-upgrade that
-- the agent refuses for containers. Reported on every heartbeat; default false
-- (assume binary/upgradeable) until the first heartbeat flips it.
ALTER TABLE agents ADD COLUMN in_container BOOLEAN NOT NULL DEFAULT FALSE;
