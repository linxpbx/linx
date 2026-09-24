-- People accounts and sessions (ADR-036; docs/WEB.md §4).
-- Passwords are Argon2id (never reversible); a TOTP secret and recovery
-- codes are only ever present for accounts with MFA turned on, sealed with
-- ADR-030's key (mfa_secret_enc) or hashed (recovery_code_hashes), like a
-- webhook secret or an API key's secret.

CREATE TABLE app_user (
    id                    uuid PRIMARY KEY,
    tenant_id             uuid NOT NULL REFERENCES tenant (id),
    email                 text NOT NULL CHECK (length(email) BETWEEN 3 AND 200 AND email ~ '^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$'),
    name                  text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    role                  text NOT NULL CHECK (role IN ('system_admin', 'admin', 'user', 'reporter')),
    -- The extension this person answers on the web client (docs/WEB.md §5).
    -- Not every account needs one (e.g. a reporter).
    extension_id          uuid REFERENCES extension (id),
    password_hash         text NOT NULL,
    password_updated_at   timestamptz NOT NULL,
    -- mfa_secret_enc is the confirmed, in-use secret (mfa_enabled true
    -- whenever it's set). mfa_pending_secret_enc holds an enrollment in
    -- progress separately, so starting (or restarting) enrollment never
    -- weakens an already-confirmed secret until a code from the *new* one
    -- is checked (ConfirmMFAEnrollment) and it's promoted into
    -- mfa_secret_enc.
    mfa_secret_enc          bytea,
    mfa_pending_secret_enc  bytea,
    mfa_enabled             boolean NOT NULL DEFAULT false,
    recovery_code_hashes    bytea[],
    -- Per-account lockout (docs/WEB.md §4): after 5 failures, each further
    -- try waits, doubling, up to 1 hour. failure_window_* is separate: a
    -- rolling count over the last hour, for the "someone is guessing
    -- passwords" alert, reset once it's stale.
    failed_attempts        smallint NOT NULL DEFAULT 0,
    locked_until           timestamptz,
    failure_window_start   timestamptz,
    failure_window_count   smallint NOT NULL DEFAULT 0,
    disabled_at            timestamptz,
    version                integer NOT NULL DEFAULT 1,
    created_at              timestamptz NOT NULL,
    updated_at              timestamptz NOT NULL
);

CREATE UNIQUE INDEX app_user_tenant_email_idx ON app_user (tenant_id, lower(email));
CREATE INDEX app_user_tenant_idx ON app_user (tenant_id, id DESC);
CREATE INDEX app_user_extension_idx ON app_user (extension_id) WHERE extension_id IS NOT NULL;

-- One-time links handed over by an admin so a new person can pick their own
-- password (docs/WEB.md §4). Single-use, stored hashed like a session token.
CREATE TABLE user_setup_link (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenant (id),
    user_id     uuid NOT NULL REFERENCES app_user (id),
    token_hash  bytea NOT NULL CHECK (length(token_hash) = 32),
    created_at  timestamptz NOT NULL,
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz
);

CREATE UNIQUE INDEX user_setup_link_token_idx ON user_setup_link (token_hash);
CREATE INDEX user_setup_link_user_idx ON user_setup_link (user_id);

-- A signed-in browser (docs/API.md §3, docs/WEB.md §4). token_hash is the
-- __Host-linx_session cookie's SHA-256; csrf_hash is the separate
-- __Host-linx_csrf cookie's, checked against the X-CSRF-Token header on
-- every write. mfa_verified is false for a session still pending its
-- authenticator code or its first-run MFA enrollment: until then it
-- authenticates to nothing that needs a scope (auth.Session.Principal).
CREATE TABLE user_session (
    id              uuid PRIMARY KEY,
    tenant_id       uuid NOT NULL REFERENCES tenant (id),
    user_id         uuid NOT NULL REFERENCES app_user (id),
    role            text NOT NULL,
    token_hash      bytea NOT NULL CHECK (length(token_hash) = 32),
    csrf_hash       bytea NOT NULL CHECK (length(csrf_hash) = 32),
    mfa_verified    boolean NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL,
    expires_at      timestamptz NOT NULL,
    idle_expires_at timestamptz NOT NULL,
    last_seen_at    timestamptz NOT NULL,
    last_seen_ip    inet,
    user_agent      text NOT NULL DEFAULT '',
    revoked_at      timestamptz
);

CREATE UNIQUE INDEX user_session_token_idx ON user_session (token_hash);
CREATE INDEX user_session_user_idx ON user_session (user_id);
