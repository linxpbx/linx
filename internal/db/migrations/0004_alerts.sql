-- Admin alerts: channels, alerts and their delivery log (ADR-029;
-- docs/API.md §5).

CREATE TABLE alert_channel (
    id           uuid PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenant (id),
    kind         text NOT NULL CHECK (kind IN ('ntfy', 'gotify', 'slack', 'teams', 'telegram', 'webhook')),
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    -- Kind-specific fields (URLs, tokens, chat id) as one encrypted JSON
    -- object (ADR-030): a Slack/Teams URL or a Telegram bot token is a
    -- secret. Never decrypted for the API; only Test and the engine read it.
    config_enc   bytea NOT NULL,
    min_severity text NOT NULL DEFAULT 'info' CHECK (min_severity IN ('info', 'warning', 'critical')),
    -- Local time of day, minutes since midnight; NULL (all three) = off.
    quiet_hours_start            smallint CHECK (quiet_hours_start BETWEEN 0 AND 1439),
    quiet_hours_end              smallint CHECK (quiet_hours_end BETWEEN 0 AND 1439),
    quiet_hours_tz               text,
    quiet_hours_bypass_critical  boolean NOT NULL DEFAULT true,
    enabled      boolean NOT NULL DEFAULT true,
    version      integer NOT NULL DEFAULT 1,
    created_by   text NOT NULL,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    CHECK ((quiet_hours_start IS NULL) = (quiet_hours_end IS NULL)
       AND (quiet_hours_start IS NULL) = (quiet_hours_tz IS NULL))
);

CREATE INDEX alert_channel_tenant_idx ON alert_channel (tenant_id, id DESC);

-- One row per problem; "key" (e.g. "trunk.down:<id>") de-duplicates it
-- while the problem lasts (docs/API.md §5). Sources call Fire repeatedly
-- (bumping last_seen_at) and Resolve once it clears.
CREATE TABLE alert (
    id                    uuid PRIMARY KEY,
    tenant_id             uuid NOT NULL REFERENCES tenant (id),
    key                   text NOT NULL CHECK (length(key) BETWEEN 1 AND 200),
    severity              text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    title                 text NOT NULL,
    message               text NOT NULL,
    link                  text,
    status                text NOT NULL CHECK (status IN ('open', 'resolved')),
    first_seen_at         timestamptz NOT NULL,
    last_seen_at          timestamptz NOT NULL,
    -- When this run of the problem started; reset each time it goes from
    -- resolved (or new) to open. The engine waits 5 minutes of stability
    -- from here before notifying, so a flapping problem is held back.
    stable_since          timestamptz NOT NULL,
    notified_at           timestamptz,
    last_reminder_at      timestamptz,
    resolved_at           timestamptz,
    resolved_notified_at  timestamptz
);

-- At most one open alert per key; its history (resolved rows) is kept for
-- "recent alerts".
CREATE UNIQUE INDEX alert_open_key_idx ON alert (tenant_id, key) WHERE status = 'open';
CREATE INDEX alert_tenant_status_idx ON alert (tenant_id, status, last_seen_at DESC);
CREATE INDEX alert_due_notify_idx ON alert (stable_since) WHERE status = 'open' AND notified_at IS NULL;
CREATE INDEX alert_due_reminder_idx ON alert (last_reminder_at) WHERE status = 'open' AND notified_at IS NOT NULL;
CREATE INDEX alert_due_resolved_idx ON alert (resolved_at)
    WHERE status = 'resolved' AND notified_at IS NOT NULL AND resolved_notified_at IS NULL;

-- One row per channel send: usually one alert, several when a quiet-hours
-- digest combines what built up while the channel was quiet.
CREATE TABLE alert_delivery (
    id              uuid PRIMARY KEY,
    tenant_id       uuid NOT NULL REFERENCES tenant (id),
    channel_id      uuid NOT NULL REFERENCES alert_channel (id) ON DELETE CASCADE,
    alert_ids       uuid[] NOT NULL,
    kind            text NOT NULL CHECK (kind IN ('fired', 'reminder', 'resolved', 'test', 'digest')),
    status          text NOT NULL CHECK (status IN ('held', 'pending', 'succeeded', 'failed', 'cancelled')),
    attempts        smallint NOT NULL DEFAULT 0,
    max_attempts    smallint NOT NULL,
    next_attempt_at timestamptz, -- NULL while held for quiet hours
    created_at      timestamptz NOT NULL,
    finished_at     timestamptz
);

CREATE INDEX alert_delivery_due_idx ON alert_delivery (next_attempt_at) WHERE status = 'pending';
CREATE INDEX alert_delivery_held_idx ON alert_delivery (channel_id) WHERE status = 'held';

CREATE TABLE alert_attempt (
    id               uuid PRIMARY KEY,
    delivery_id      uuid NOT NULL REFERENCES alert_delivery (id) ON DELETE CASCADE,
    at               timestamptz NOT NULL,
    status_code      smallint,
    duration_ms      integer NOT NULL,
    response_excerpt text,
    error            text
);

CREATE INDEX alert_attempt_delivery_idx ON alert_attempt (delivery_id, at);
