-- How each phone's leg of each call went, judged by Linx itself
-- (internal/callquality; owner, 2026-10-16: "I need a mechanism to know if
-- a call went through issues even if the user didn't report it").
--
-- One row per phone per call as its leg hangs up — what Asterisk measured
-- of its sound, and Good / Had problems / Bad with the reasons in words —
-- and one for a phone woken for a call that never arrived. An admin reads
-- them beside the phone's own report (Calls → Call quality). Kept seven
-- days; saving one clears what is older, so nothing runs to tidy up.
CREATE TABLE call_quality (
    id           uuid PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenant (id),
    device_id    uuid NOT NULL REFERENCES device (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('call', 'wake')),
    call_id      text NOT NULL DEFAULT '' CHECK (length(call_id) <= 200),
    started_at   timestamptz NOT NULL,
    ended_at     timestamptz NOT NULL,
    verdict      text NOT NULL CHECK (verdict IN ('good', 'problems', 'bad')),
    reasons      text[] NOT NULL DEFAULT '{}',
    in_packets   integer NOT NULL DEFAULT 0,
    out_packets  integer NOT NULL DEFAULT 0,
    in_lost      real NOT NULL DEFAULT 0,
    out_lost     real NOT NULL DEFAULT 0,
    jitter_ms    real NOT NULL DEFAULT 0,
    rtt_ms       real NOT NULL DEFAULT 0
);

CREATE INDEX call_quality_tenant ON call_quality (tenant_id, ended_at DESC);
CREATE INDEX call_quality_device ON call_quality (device_id, ended_at DESC);
