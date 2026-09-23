-- API keys, OAuth clients and token revocation (ADR-027; docs/API.md §3).
-- Secrets are never stored: only SHA-256 of their random part.

CREATE TABLE api_key (
    id           uuid PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenant (id),
    -- The public part of linx_<public_id>_<secret>, used to find the key.
    public_id    text NOT NULL UNIQUE CHECK (public_id ~ '^[a-z2-7]{12}$'),
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    secret_hash  bytea NOT NULL CHECK (length(secret_hash) = 32),
    role         text NOT NULL CHECK (role IN ('system_admin', 'admin', 'user', 'reporter')),
    scopes       text[] NOT NULL,
    -- NULL or empty: usable from any address.
    allowed_ips  cidr[],
    created_by   text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_used_at timestamptz,
    last_used_ip inet,
    revoked_at   timestamptz
);

CREATE INDEX api_key_tenant_idx ON api_key (tenant_id, id DESC);

CREATE TABLE oauth_client (
    id           uuid PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenant (id),
    -- The client_id sent to /oauth/token.
    public_id    text NOT NULL UNIQUE CHECK (public_id ~ '^[a-z2-7]{12}$'),
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    secret_hash  bytea NOT NULL CHECK (length(secret_hash) = 32),
    role         text NOT NULL CHECK (role IN ('system_admin', 'admin', 'user', 'reporter')),
    scopes       text[] NOT NULL,
    allowed_ips  cidr[],
    created_by   text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_used_at timestamptz,
    last_used_ip inet,
    revoked_at   timestamptz
);

CREATE INDEX oauth_client_tenant_idx ON oauth_client (tenant_id, id DESC);

-- Revoked or used JWT ids (ADR-012). Rows can be deleted once expires_at
-- has passed, because the token itself is no longer accepted then.
CREATE TABLE token_revocation (
    jti        text PRIMARY KEY,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz NOT NULL DEFAULT now()
);
