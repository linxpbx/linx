-- Email (ADR-066, docs/PHASE1F.md §4): Linx sends through an SMTP account
-- the owner already has. One setting per tenant, made the first time a
-- system admin saves; and the queue every email waits in.

CREATE TABLE email_settings (
    tenant_id    uuid PRIMARY KEY REFERENCES tenant (id),
    enabled      boolean NOT NULL DEFAULT false,
    preset       text NOT NULL CHECK (length(preset) BETWEEN 1 AND 30),
    host         text NOT NULL DEFAULT '' CHECK (length(host) <= 253),
    port         integer NOT NULL CHECK (port BETWEEN 1 AND 65535 AND port <> 25),
    -- Always encrypted: there is no plain choice.
    security     text NOT NULL CHECK (security IN ('tls', 'starttls')),
    username     text NOT NULL DEFAULT '' CHECK (length(username) <= 254),
    from_address text NOT NULL DEFAULT '' CHECK (length(from_address) <= 254),
    from_name    text NOT NULL DEFAULT '' CHECK (length(from_name) <= 400),
    -- Sealed with ADR-030's key, row id "email_settings:<tenant_id>".
    password_enc bytea,
    hourly_limit integer NOT NULL DEFAULT 60 CHECK (hourly_limit BETWEEN 1 AND 1000),
    -- When an admin said the test email arrived (the admin home's
    -- "Set up email"); cleared when the server or account changes.
    arrived_at   timestamptz,
    version      integer NOT NULL DEFAULT 1,
    updated_at   timestamptz NOT NULL,
    CHECK (NOT enabled OR (host <> '' AND from_address <> '' AND password_enc IS NOT NULL))
);

-- The queue. The subject and text are sealed as one JSON object (row id
-- "email_outbox:<id>"): invites and password resets carry links that work
-- like passwords. They're emptied once the email is sent or given up on;
-- the row stays a while for the Email card's counts and the Activity log.
CREATE TABLE email_outbox (
    id              uuid PRIMARY KEY,
    tenant_id       uuid NOT NULL REFERENCES tenant (id),
    kind            text NOT NULL CHECK (length(kind) BETWEEN 1 AND 30),
    to_addresses    text[] NOT NULL CHECK (cardinality(to_addresses) BETWEEN 1 AND 10),
    content_enc     bytea,
    status          text NOT NULL CHECK (status IN ('pending', 'sent', 'failed')),
    attempts        integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz,
    lease_until     timestamptz,
    last_error      text NOT NULL DEFAULT '' CHECK (length(last_error) <= 1000),
    created_at      timestamptz NOT NULL,
    sent_at         timestamptz,
    finished_at     timestamptz,
    CHECK ((status = 'pending') = (content_enc IS NOT NULL)),
    CHECK ((status = 'pending') = (next_attempt_at IS NOT NULL))
);

-- The worker's next email, and the hour's count for the limit.
CREATE INDEX email_outbox_due ON email_outbox (next_attempt_at) WHERE status = 'pending';
CREATE INDEX email_outbox_sent ON email_outbox (tenant_id, sent_at) WHERE sent_at IS NOT NULL;
