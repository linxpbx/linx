-- Email as an admin alert channel (ADR-066, Phase 1F step 9): the code
-- offered it from step 9, but alert_channel's kind check from 0004 still
-- listed only the first six kinds, so adding one failed (found in Demo B).
ALTER TABLE alert_channel DROP CONSTRAINT alert_channel_kind_check;
ALTER TABLE alert_channel ADD CONSTRAINT alert_channel_kind_check
    CHECK (kind IN ('ntfy', 'gotify', 'slack', 'teams', 'telegram', 'webhook', 'email'));
