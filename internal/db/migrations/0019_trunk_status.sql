-- Phase 1D step 4 (docs/TRUNKS.md §9, §10): each trunk's state, one-shot
-- alerts, and what the toll-fraud alerts count.

-- What Asterisk last said about the trunk (internal/trunkstatus), kept by
-- the control plane's trunk monitor. Not part of the trunk's settings: a
-- change doesn't bump version (an admin's If-Match stays valid).
ALTER TABLE trunk
    ADD COLUMN status        text NOT NULL DEFAULT 'unknown'
        CHECK (status IN ('registered', 'reachable', 'rejected', 'unreachable', 'unknown', 'disabled')),
    ADD COLUMN status_detail text NOT NULL DEFAULT '',
    ADD COLUMN status_since  timestamptz;

-- A one-shot alert tells of something that happened (an emergency call, a
-- first call to a country) rather than a problem that lasts: it notifies at
-- once and closes itself as it does, with no "resolved" notice.
ALTER TABLE alert ADD COLUMN one_shot boolean NOT NULL DEFAULT false;

-- "Unusual international calling" (docs/TRUNKS.md §9): more than this many
-- minutes, or this many calls, to international numbers in an hour.
ALTER TABLE pbx_setting
    ADD COLUMN international_alert_minutes integer NOT NULL DEFAULT 30 CHECK (international_alert_minutes BETWEEN 1 AND 10000),
    ADD COLUMN international_alert_calls   integer NOT NULL DEFAULT 10 CHECK (international_alert_calls BETWEEN 1 AND 10000);

-- Calls that went out on a trunk, as the call tracker saw them: what the
-- international-calling alert counts. Not call history (CDR comes later);
-- rows older than 30 days are removed.
CREATE TABLE outside_call (
    id           uuid PRIMARY KEY,            -- the call's id in its events
    tenant_id    uuid NOT NULL REFERENCES tenant (id),
    trunk_id     uuid REFERENCES trunk (id) ON DELETE SET NULL,
    extension    text NOT NULL,
    number       text NOT NULL,               -- E.164, or as dialled for short numbers
    region       text NOT NULL,               -- the number's country ("001": non-geographic)
    category     text NOT NULL,
    started_at   timestamptz NOT NULL,
    answered_at  timestamptz,
    ended_at     timestamptz,
    talk_seconds integer NOT NULL DEFAULT 0
);
CREATE INDEX outside_call_window_idx ON outside_call (tenant_id, category, started_at);

-- Every country Linx has called out to, for "a country called for the
-- first time".
CREATE TABLE called_region (
    tenant_id       uuid NOT NULL REFERENCES tenant (id),
    region          text NOT NULL,
    first_called_at timestamptz NOT NULL,
    first_called_by text NOT NULL,            -- the extension
    PRIMARY KEY (tenant_id, region)
);
