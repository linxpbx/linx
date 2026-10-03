-- Setting up an iPhone or iPad (ADR-073, docs/PHASE2.md §4, Phase 2 step 2).
-- An admin (or the person themselves) makes a one-time ticket; the phone
-- redeems it once, makes a key pair inside its Secure Enclave and gets a
-- certificate for it. That certificate is the phone's identity from then on:
-- it signs a short proof with the Secure Enclave key to get a device token,
-- and the key itself can never be read, copied or backed up.

-- One ticket per phone being set up. It carries no SIP password and nothing
-- else secret: the token is a signed JWT (ADR-012) that is never stored, and
-- the 8 characters someone may type instead are kept hashed.
CREATE TABLE device_enrollment (
    id           uuid PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenant (id),
    -- Whose phone it is, and the extension it will answer for.
    user_id      uuid NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    extension_id uuid NOT NULL REFERENCES extension (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind = 'ios'),
    device_name  text NOT NULL,
    -- sha256 of the typed code; the token's jti, so a token can be used once.
    code_hash    bytea NOT NULL,
    token_jti    uuid NOT NULL UNIQUE,
    -- Who made it (audit_log.actor), and how it was given out.
    created_by   text NOT NULL,
    delivery     text NOT NULL CHECK (delivery IN ('qr', 'email', 'by_hand')),
    created_at   timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    -- Exactly one of these ends a ticket's life.
    used_at      timestamptz,
    canceled_at  timestamptz,
    device_id    uuid REFERENCES device (id),
    -- Wrong codes typed at it; too many and it is dead (checked in Go).
    attempts     int NOT NULL DEFAULT 0,
    CHECK ((device_id IS NULL) = (used_at IS NULL))
);

CREATE INDEX device_enrollment_open_idx ON device_enrollment (tenant_id, created_at DESC)
    WHERE used_at IS NULL AND canceled_at IS NULL;

-- A code is only looked up while its ticket is still open, so the same 8
-- characters may be used again later.
CREATE UNIQUE INDEX device_enrollment_code_idx ON device_enrollment (code_hash)
    WHERE used_at IS NULL AND canceled_at IS NULL;

-- What a set-up phone is. One row per ios device, made when the ticket is
-- redeemed and kept fresh every time the phone is in touch. The certificate
-- lasts six months and is renewed while the phone keeps coming back; six
-- months with no contact at all, or a change of its person's password, and
-- the phone must be set up again (ADR-077, owner 2026-10-03).
CREATE TABLE device_identity (
    device_id        uuid PRIMARY KEY REFERENCES device (id) ON DELETE CASCADE,
    tenant_id        uuid NOT NULL REFERENCES tenant (id),
    user_id          uuid NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    -- The Secure Enclave's public key (SPKI DER) and the certificate Linx
    -- signed for it. The private half is only ever inside that phone.
    public_key       bytea NOT NULL,
    cert_serial      text NOT NULL,
    cert_fingerprint bytea NOT NULL,
    cert_not_after   timestamptz NOT NULL,
    enrolled_at      timestamptz NOT NULL,
    last_seen_at     timestamptz NOT NULL,
    expires_at       timestamptz NOT NULL,
    expired_at       timestamptz,
    app_version      text NOT NULL DEFAULT '',
    os_version       text NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX device_identity_fingerprint_idx ON device_identity (cert_fingerprint);
CREATE INDEX device_identity_expires_idx ON device_identity (expires_at) WHERE expired_at IS NULL;

-- Each proof a phone signs may be used once (ADR-012's single-use rule). The
-- rows are small and swept once an hour, a minute after they could no longer
-- be replayed anyway.
CREATE TABLE device_proof (
    jti       uuid PRIMARY KEY,
    device_id uuid NOT NULL REFERENCES device (id) ON DELETE CASCADE,
    used_at   timestamptz NOT NULL
);

CREATE INDEX device_proof_used_idx ON device_proof (used_at);

-- Migration 0013's rule, now with the app's phones: an ios device is live
-- while it is enabled, its certificate hasn't expired from inactivity, and
-- the person it belongs to is still there with that same extension — the
-- same conditions a browser's line has, checked on every lookup.
CREATE OR REPLACE VIEW device_live AS
SELECT
    d.id,
    d.kind,
    d.sip_username,
    d.digest_hash,
    d.extension_id,
    e.number,
    e.display_name
FROM device d
JOIN extension e ON e.id = d.extension_id
LEFT JOIN user_session s ON s.id = d.user_session_id
LEFT JOIN app_user u ON u.id = s.user_id
LEFT JOIN device_identity i ON i.device_id = d.id
LEFT JOIN app_user iu ON iu.id = i.user_id
WHERE d.enabled AND e.enabled AND e.deleted_at IS NULL
  AND (d.kind <> 'web' OR (
        s.id IS NOT NULL AND s.revoked_at IS NULL AND s.mfa_verified
        AND s.expires_at > now() AND s.idle_expires_at > now()
        AND u.disabled_at IS NULL AND u.extension_id = d.extension_id))
  AND (d.kind <> 'ios' OR (
        i.device_id IS NOT NULL AND i.expired_at IS NULL AND i.expires_at > now()
        AND iu.disabled_at IS NULL AND iu.extension_id = d.extension_id));

-- The app talks SIP over the same relay the browser uses and the same WebRTC
-- media, so it gets the browser's transport and encryption (migration 0013).
CREATE OR REPLACE VIEW asterisk.ps_endpoints AS
SELECT
    l.sip_username    AS id,
    CASE WHEN l.kind IN ('web', 'ios') THEN 'transport-wss' ELSE 'transport-tls' END AS transport,
    l.sip_username    AS aors,
    l.sip_username    AS auth,
    'linx-extensions' AS context,
    'all'             AS disallow,
    'opus,g722,ulaw'  AS allow,
    'no'              AS direct_media,
    CASE WHEN l.kind IN ('web', 'ios') THEN 'dtls' ELSE 'sdes' END AS media_encryption,
    'no'              AS media_encryption_optimistic,
    'yes'             AS force_rport,
    'yes'             AS rewrite_contact,
    'yes'             AS rtp_symmetric,
    CASE WHEN l.kind IN ('web', 'ios') THEN 'yes' ELSE 'no' END AS ice_support,
    '"' || regexp_replace(l.display_name, '["<>\;[:cntrl:]]', '', 'g') || '" <' || l.number || '>' AS callerid,
    NULL::text        AS incoming_offer_codec_prefs,
    nullif(s.sip_domain, '') AS from_domain,
    CASE WHEN l.kind IN ('web', 'ios') THEN 'yes' ELSE 'no' END AS use_avpf,
    CASE WHEN l.kind IN ('web', 'ios') THEN 'yes' ELSE 'no' END AS rtcp_mux,
    CASE WHEN l.kind IN ('web', 'ios') THEN 'fingerprint' END AS dtls_verify,
    CASE WHEN l.kind IN ('web', 'ios') THEN 'actpass' END AS dtls_setup,
    CASE WHEN l.kind IN ('web', 'ios') THEN 'yes' END AS dtls_auto_generate_cert,
    CASE WHEN l.kind IN ('web', 'ios') THEN 'yes' ELSE 'no' END AS media_use_received_transport
FROM device_live l
CROSS JOIN pbx_setting s;
