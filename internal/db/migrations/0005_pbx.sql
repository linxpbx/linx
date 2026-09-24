-- Phone engine: extensions, devices, and the realtime views Asterisk reads
-- straight from Postgres (ADR-032, ADR-033; docs/PBX.md §3).

CREATE TABLE extension (
    id           uuid PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenant (id),
    number       text NOT NULL CHECK (number ~ '^[0-9]{2,6}$'),
    display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 100),
    email        text CHECK (email IS NULL OR length(email) <= 200),
    enabled      boolean NOT NULL DEFAULT true,
    version      integer NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    UNIQUE (tenant_id, number)
);

CREATE INDEX extension_tenant_idx ON extension (tenant_id, id DESC);

-- Devices are the phones and apps that ring for an extension (docs/PBX.md
-- §1), each with its own login. sip_username is what Asterisk calls the
-- endpoint (random, so an extension number never reveals a login);
-- digest_hash is Asterisk's md5_cred, MD5(username:realm:password) — never
-- the password itself (ADR-033).
CREATE TABLE device (
    id                   uuid PRIMARY KEY,
    tenant_id            uuid NOT NULL REFERENCES tenant (id),
    extension_id         uuid NOT NULL REFERENCES extension (id),
    name                 text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    kind                 text NOT NULL DEFAULT 'softphone' CHECK (kind IN ('softphone', 'web', 'ios', 'desk')),
    sip_username         text NOT NULL UNIQUE CHECK (sip_username ~ '^d_[A-Za-z0-9]{8}$'),
    digest_hash          text NOT NULL CHECK (digest_hash ~ '^[0-9a-f]{32}$'),
    enabled              boolean NOT NULL DEFAULT true,
    last_registered_at   timestamptz,
    last_registered_from inet,
    version              integer NOT NULL DEFAULT 1,
    created_at           timestamptz NOT NULL,
    updated_at           timestamptz NOT NULL
);

CREATE INDEX device_extension_idx ON device (extension_id);
CREATE INDEX device_tenant_idx ON device (tenant_id, id DESC);

-- Below is what Asterisk itself reads, over ODBC as role linx_asterisk
-- (ADR-032): views only, so the tables above can change shape later without
-- Asterisk noticing, and the role can SELECT these four objects and nothing
-- else in the database — no tenants, no API keys, no webhooks, no write
-- access anywhere.
CREATE SCHEMA asterisk;

-- linx_asterisk logs in with the password the control plane sets, from the
-- linx_asterisk_db_password Docker secret, at every startup (docs/PBX.md
-- §3). This migration only creates the role, never a usable password, so
-- there's no default credential baked into the schema.
CREATE ROLE linx_asterisk NOLOGIN;
GRANT USAGE ON SCHEMA asterisk TO linx_asterisk;

-- MD5(username:realm:password) only proves a login on this server when
-- hashed with this exact realm (ADR-033); changing it invalidates every
-- device's stored digest_hash.
CREATE VIEW asterisk.ps_endpoints AS
SELECT
    d.sip_username    AS id,
    'transport-tls'   AS transport,
    d.sip_username    AS aors,
    d.sip_username    AS auth,
    'linx-extensions' AS context,
    'all'             AS disallow,
    'opus,g722,ulaw'  AS allow,
    'no'              AS direct_media,
    'sdes'            AS media_encryption,
    'no'              AS media_encryption_optimistic,
    'yes'             AS force_rport,
    'yes'             AS rewrite_contact,
    'yes'             AS rtp_symmetric,
    'no'              AS ice_support
FROM device d
JOIN extension e ON e.id = d.extension_id
WHERE d.enabled AND e.enabled;

CREATE VIEW asterisk.ps_aors AS
SELECT
    d.sip_username AS id,
    '1'            AS max_contacts,
    'yes'          AS remove_existing
FROM device d
JOIN extension e ON e.id = d.extension_id
WHERE d.enabled AND e.enabled;

CREATE VIEW asterisk.ps_auths AS
SELECT
    d.sip_username AS id,
    'md5'          AS auth_type,
    d.sip_username AS username,
    d.digest_hash  AS md5_cred,
    'linxpbx'      AS realm
FROM device d
JOIN extension e ON e.id = d.extension_id
WHERE d.enabled AND e.enabled;

-- Read by func_odbc in the dialplan (docs/PBX.md §4, step 4): every enabled
-- device's AOR for an extension number, to ring all of them at once.
CREATE VIEW asterisk.linx_ring_targets AS
SELECT
    e.number       AS number,
    d.sip_username AS aor
FROM device d
JOIN extension e ON e.id = d.extension_id
WHERE d.enabled AND e.enabled;

GRANT SELECT ON asterisk.ps_endpoints, asterisk.ps_aors, asterisk.ps_auths, asterisk.linx_ring_targets TO linx_asterisk;
