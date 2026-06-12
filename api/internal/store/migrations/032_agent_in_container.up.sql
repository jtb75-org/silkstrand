-- ADR 013 follow-up: agents report whether they run inside a container, so the
-- UI can offer "recreate from image" instead of the in-place self-upgrade that
-- the agent refuses for containers. Three-state and NULLABLE on purpose: NULL =
-- "not reported yet" (agents predating this, e.g. the existing v0.1.101
-- container agent, never send the field), distinct from a reported false
-- (known binary) or true (known container). The UI treats NULL conservatively.
ALTER TABLE agents ADD COLUMN in_container BOOLEAN;
