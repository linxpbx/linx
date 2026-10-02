-- Voicemail part 2 (ADR-069, docs/PHASE1F.md §8, Phase 1F step 14): a
-- box's own greetings, recorded in the browser, and how long messages are
-- kept.

-- How long a message is kept before Linx deletes it (System → Settings).
ALTER TABLE pbx_setting ADD COLUMN voicemail_keep_days smallint NOT NULL DEFAULT 60
    CHECK (voicemail_keep_days BETWEEN 7 AND 365);

-- A box's own greeting: "unavailable" (the usual one) or "closed" (outside
-- office hours and on holidays). The audio is 8 kHz G.711 mu-law like the
-- messages, at most 30 seconds (the browser sends plain 16-bit audio,
-- internal/voicemail converts it). Kept when the box goes back to Linx's
-- own greeting (in_use false), so switching back needs no new recording.
-- The control plane copies the greetings in use into a folder only it and
-- Asterisk share; the dialplan plays the file when it's there.
CREATE TABLE voicemail_greeting (
    box_id      uuid NOT NULL REFERENCES voicemail_box (id) ON DELETE CASCADE,
    kind        text NOT NULL CHECK (kind IN ('unavailable', 'closed')),
    tenant_id   uuid NOT NULL REFERENCES tenant (id),
    in_use      boolean NOT NULL DEFAULT true,
    audio       bytea NOT NULL CHECK (length(audio) BETWEEN 4000 AND 248000),
    recorded_at timestamptz NOT NULL,
    recorded_by uuid REFERENCES app_user (id) ON DELETE SET NULL,
    PRIMARY KEY (box_id, kind)
);
ALTER TABLE voicemail_greeting ALTER COLUMN audio SET STORAGE EXTERNAL;
