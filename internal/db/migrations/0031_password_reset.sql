-- "Forgot your password?" (ADR-067, docs/PHASE1F.md §5): an emailed reset
-- link is a one-time link like a set-password one, only shorter-lived and
-- never usable as the other kind (a reset still asks for the second step
-- before anything changes).

ALTER TABLE user_setup_link
    ADD COLUMN purpose text NOT NULL DEFAULT 'setup' CHECK (purpose IN ('setup', 'reset'));
