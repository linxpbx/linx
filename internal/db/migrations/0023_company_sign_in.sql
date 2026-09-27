-- Company sign-in (ADR-052, docs/ADMIN.md §6, Phase 1E step 4): OpenID
-- Connect providers (Google, Microsoft, Authentik, Keycloak, any other) and
-- the links between a provider's account and a Linx person. Company sign-in
-- never creates people: a link is made only for someone who already exists.

CREATE TABLE sso_provider (
    id                uuid PRIMARY KEY,
    tenant_id         uuid NOT NULL REFERENCES tenant (id),
    -- Which template it was made from: decides how a verified email is read
    -- from the ID token (Microsoft's differs) and the button's icon.
    kind              text NOT NULL CHECK (kind IN ('google', 'microsoft', 'authentik', 'keycloak', 'oidc')),
    -- Shown on the button: "Continue with <name>".
    name              text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    issuer            text NOT NULL CHECK (issuer ~ '^https://' AND length(issuer) <= 500),
    client_id         text NOT NULL CHECK (length(client_id) BETWEEN 1 AND 500),
    -- Sealed with ADR-030's key, row id "sso_provider:<id>". Some providers
    -- (public clients using PKCE alone) have none: NULL.
    client_secret_enc bytea,
    -- enabled: it can be used at all (sign in, link, confirm). shown: its
    -- button is on the sign-in page (an enabled provider can be tried and
    -- linked before it's shown to everyone).
    enabled           boolean NOT NULL DEFAULT true,
    shown             boolean NOT NULL DEFAULT true,
    position          integer NOT NULL DEFAULT 0,
    version           integer NOT NULL DEFAULT 1,
    created_at        timestamptz NOT NULL,
    updated_at        timestamptz NOT NULL,
    UNIQUE (tenant_id, name)
);

CREATE INDEX sso_provider_tenant_idx ON sso_provider (tenant_id, position, name);

-- A person's company account. The first link is made by the provider's
-- verified email matching the person's; after that only the provider's
-- stable subject id is trusted (an email can be reassigned, a subject
-- can't).
CREATE TABLE user_sso_link (
    id           uuid PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenant (id),
    user_id      uuid NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    provider_id  uuid NOT NULL REFERENCES sso_provider (id) ON DELETE CASCADE,
    subject      text NOT NULL CHECK (length(subject) BETWEEN 1 AND 255),
    -- The email the provider vouched for when the link was made: shown in
    -- My account and People, never used to sign in.
    email        text NOT NULL DEFAULT '' CHECK (length(email) <= 320),
    created_at   timestamptz NOT NULL,
    last_used_at timestamptz,
    -- One provider account signs in as one person, and a person has at
    -- most one account per provider.
    UNIQUE (provider_id, subject),
    UNIQUE (user_id, provider_id)
);

CREATE INDEX user_sso_link_user_idx ON user_sso_link (user_id);

-- "People must use company sign-in" (docs/ADMIN.md §6): passwords stop
-- working for everyone except system admins, so a broken provider can't
-- lock the owner out. Passkeys keep working.
ALTER TABLE pbx_setting ADD COLUMN company_sign_in_required boolean NOT NULL DEFAULT false;
