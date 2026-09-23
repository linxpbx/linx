-- Webhooks: outbox, endpoints, delivery log, outbound allowlist
-- (ADR-028; docs/API.md §4).

-- Events waiting to be (or already) fanned out to webhook endpoints. Code
-- that changes something inserts its event here in the same transaction,
-- so an event is never lost and never sent for a change that rolled back.
CREATE TABLE event_outbox (
    id            uuid PRIMARY KEY, -- also the webhook-id receivers de-duplicate on
    tenant_id     uuid NOT NULL REFERENCES tenant (id),
    type          text NOT NULL,
    -- The exact JSON body sent (envelope included), so every retry and
    -- replay sends identical bytes.
    body          bytea NOT NULL,
    created_at    timestamptz NOT NULL,
    dispatched_at timestamptz
);

CREATE INDEX event_outbox_undispatched_idx ON event_outbox (id) WHERE dispatched_at IS NULL;

CREATE TABLE webhook_endpoint (
    id              uuid PRIMARY KEY,
    tenant_id       uuid NOT NULL REFERENCES tenant (id),
    url             text NOT NULL CHECK (url ~ '^https://' AND length(url) <= 2048),
    description     text NOT NULL DEFAULT '' CHECK (length(description) <= 200),
    -- Empty: every event type.
    event_types     text[] NOT NULL DEFAULT '{}',
    enabled         boolean NOT NULL DEFAULT true,
    -- 'gone' (the receiver answered 410), 'failing' (5 days of failures) or
    -- 'admin'.
    disabled_reason text CHECK (disabled_reason IN ('gone', 'failing', 'admin')),
    disabled_at     timestamptz,
    -- AES-256-GCM (ADR-030), row id "webhook_endpoint:<id>" as additional data.
    secret_enc      bytea NOT NULL,
    -- The secret before the last rotation keeps signing until it expires.
    previous_secret_enc        bytea,
    previous_secret_expires_at timestamptz,
    -- Start of the current run of failed attempts; NULL after a success.
    failing_since   timestamptz,
    last_success_at timestamptz,
    -- Bumped on every change; the API's etag.
    version         integer NOT NULL DEFAULT 1,
    created_by      text NOT NULL,
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL
);

CREATE INDEX webhook_endpoint_tenant_idx ON webhook_endpoint (tenant_id, id DESC);

CREATE TABLE webhook_delivery (
    id              uuid PRIMARY KEY,
    tenant_id       uuid NOT NULL REFERENCES tenant (id),
    endpoint_id     uuid NOT NULL REFERENCES webhook_endpoint (id) ON DELETE CASCADE,
    event_id        uuid NOT NULL REFERENCES event_outbox (id),
    event_type      text NOT NULL,
    status          text NOT NULL CHECK (status IN ('pending', 'succeeded', 'failed', 'cancelled')),
    attempts        smallint NOT NULL DEFAULT 0,
    max_attempts    smallint NOT NULL,
    -- For a pending delivery: when to try next. While a worker is sending it,
    -- a lease: another worker may take it over once this has passed.
    next_attempt_at timestamptz,
    replay_of       uuid REFERENCES webhook_delivery (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL,
    finished_at     timestamptz
);

CREATE INDEX webhook_delivery_due_idx ON webhook_delivery (next_attempt_at) WHERE status = 'pending';
CREATE INDEX webhook_delivery_endpoint_idx ON webhook_delivery (endpoint_id, id DESC);
CREATE INDEX webhook_delivery_event_idx ON webhook_delivery (event_id);

-- One row per HTTP attempt, kept 30 days (with its delivery).
CREATE TABLE webhook_attempt (
    id               uuid PRIMARY KEY,
    delivery_id      uuid NOT NULL REFERENCES webhook_delivery (id) ON DELETE CASCADE,
    at               timestamptz NOT NULL,
    status_code      smallint,
    duration_ms      integer NOT NULL,
    -- First 4 KB of the response body.
    response_excerpt text,
    error            text
);

CREATE INDEX webhook_attempt_delivery_idx ON webhook_attempt (delivery_id, at);

-- Private addresses outbound connections may reach (NAS, Home Assistant).
-- Server-wide: it decides what the server itself may connect to.
CREATE TABLE outbound_allowlist (
    id          uuid PRIMARY KEY,
    -- Either a CIDR range or a host name, never both.
    cidr        cidr,
    host        text CHECK (host ~ '^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$'),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 200),
    created_by  text NOT NULL,
    created_at  timestamptz NOT NULL,
    CHECK ((cidr IS NULL) <> (host IS NULL))
);

CREATE UNIQUE INDEX outbound_allowlist_cidr_idx ON outbound_allowlist (cidr) WHERE cidr IS NOT NULL;
CREATE UNIQUE INDEX outbound_allowlist_host_idx ON outbound_allowlist (host) WHERE host IS NOT NULL;
