-- ADR 013 D10: agent zone/site label. Server-side deployment metadata that
-- disambiguates reused private address space for the overlap heuristic (D6).
-- Never an authorization input — labeling only.
ALTER TABLE install_tokens ADD COLUMN zone TEXT;
ALTER TABLE agents ADD COLUMN zone TEXT;
