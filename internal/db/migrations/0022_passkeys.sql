-- Passkeys (ADR-051, docs/ADMIN.md §5, Phase 1E step 3). A passkey is a
-- whole sign-in: the device proves it holds the key and unlocks it with
-- Face ID, a fingerprint or a PIN (user verification required). Nothing
-- here is secret: the private key never leaves the person's device.

CREATE TABLE user_passkey (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL REFERENCES tenant (id),
    user_id            uuid NOT NULL REFERENCES app_user (id),
    -- The authenticator's credential id: at most 1023 bytes (WebAuthn §5.1),
    -- unique across every account since the browser picks one by it alone.
    credential_id      bytea NOT NULL CHECK (length(credential_id) BETWEEN 16 AND 1023),
    -- COSE-encoded public key, checked by go-webauthn at registration.
    public_key         bytea NOT NULL,
    -- Signature counter: a counter that stops going up (other than a synced
    -- passkey's constant 0) suggests a copied key, and is refused.
    sign_count         bigint NOT NULL DEFAULT 0 CHECK (sign_count BETWEEN 0 AND 4294967295),
    aaguid             bytea,
    -- Backup flags: eligible can't change for a credential; state (synced
    -- right now, e.g. iCloud Keychain) can.
    backup_eligible    boolean NOT NULL,
    backup_state       boolean NOT NULL,
    transports         text[] NOT NULL DEFAULT '{}',
    attestation_format text NOT NULL DEFAULT '',
    name               text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    created_at         timestamptz NOT NULL,
    last_used_at       timestamptz
);

CREATE UNIQUE INDEX user_passkey_credential_idx ON user_passkey (credential_id);
CREATE INDEX user_passkey_user_idx ON user_passkey (user_id);

-- An admin or system admin who chose to sign in with a password only,
-- having accepted the "not recommended" warning (docs/ADMIN.md §5, owner
-- decision 2026-09-27). NULL means they haven't: an admin with no passkey
-- and no authenticator app is asked to set one up (or accept the warning)
-- at sign-in. Cleared once they add a passkey or authenticator.
ALTER TABLE app_user ADD COLUMN password_only_accepted_at timestamptz;

-- A person who set up their account with a passkey has no password until
-- they add one in My account: password_hash is '' (never a valid Argon2id
-- hash, so no password can match it).
COMMENT ON COLUMN app_user.password_hash IS 'Argon2id hash, or empty for a passkey-only account (migration 0022)';
