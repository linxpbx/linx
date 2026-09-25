-- The browser's phone line (docs/WEB.md §5, ADR-038). Each signed-in browser
-- session gets its own web device, with a fresh random password the page
-- keeps in memory only. The device belongs to that session: once the
-- session ends (signed out, expired, the person disabled, their password or
-- role changed), Asterisk stops seeing the device at once, because the views
-- it reads check the session is still live on every lookup. The control
-- plane also marks such devices revoked shortly after (with a
-- device.revoked event), but nothing waits for that.
ALTER TABLE device ADD COLUMN user_session_id uuid REFERENCES user_session (id);

-- Web devices from before this migration (the call suite's only; the API
-- never created them) had no session, so they can never be live again.
UPDATE device SET enabled = false, revoked_at = now(), version = version + 1, updated_at = now()
WHERE kind = 'web' AND revoked_at IS NULL;

ALTER TABLE device ADD CONSTRAINT device_session_is_web CHECK (user_session_id IS NULL OR kind = 'web');
ALTER TABLE device ADD CONSTRAINT device_web_has_session
    CHECK (kind <> 'web' OR user_session_id IS NOT NULL OR revoked_at IS NOT NULL);

-- One line per session at a time.
CREATE UNIQUE INDEX device_web_session_idx ON device (user_session_id) WHERE revoked_at IS NULL;

-- Every device Asterisk may use right now: enabled, on a live extension,
-- and, for a web device, whose session is live (signed in past its
-- authenticator code, not revoked or expired), whose person isn't disabled
-- and still has that extension. The realtime views below all read this, so
-- the rule lives in one place. Not granted to linx_asterisk: the views
-- read it with their owner's rights.
CREATE VIEW device_live AS
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
WHERE d.enabled AND e.enabled AND e.deleted_at IS NULL
  AND (d.kind <> 'web' OR (
        s.id IS NOT NULL AND s.revoked_at IS NULL AND s.mfa_verified
        AND s.expires_at > now() AND s.idle_expires_at > now()
        AND u.disabled_at IS NULL AND u.extension_id = d.extension_id));

-- Same columns as migration 0011's, now from device_live.
CREATE OR REPLACE VIEW asterisk.ps_endpoints AS
SELECT
    l.sip_username    AS id,
    CASE WHEN l.kind = 'web' THEN 'transport-wss' ELSE 'transport-tls' END AS transport,
    l.sip_username    AS aors,
    l.sip_username    AS auth,
    'linx-extensions' AS context,
    'all'             AS disallow,
    'opus,g722,ulaw'  AS allow,
    'no'              AS direct_media,
    CASE WHEN l.kind = 'web' THEN 'dtls' ELSE 'sdes' END AS media_encryption,
    'no'              AS media_encryption_optimistic,
    'yes'             AS force_rport,
    'yes'             AS rewrite_contact,
    'yes'             AS rtp_symmetric,
    CASE WHEN l.kind = 'web' THEN 'yes' ELSE 'no' END AS ice_support,
    '"' || regexp_replace(l.display_name, '["<>\;[:cntrl:]]', '', 'g') || '" <' || l.number || '>' AS callerid,
    NULL::text        AS incoming_offer_codec_prefs,
    nullif(s.sip_domain, '') AS from_domain,
    CASE WHEN l.kind = 'web' THEN 'yes' ELSE 'no' END AS use_avpf,
    CASE WHEN l.kind = 'web' THEN 'yes' ELSE 'no' END AS rtcp_mux,
    CASE WHEN l.kind = 'web' THEN 'fingerprint' END AS dtls_verify,
    CASE WHEN l.kind = 'web' THEN 'actpass' END AS dtls_setup,
    CASE WHEN l.kind = 'web' THEN 'yes' END AS dtls_auto_generate_cert,
    CASE WHEN l.kind = 'web' THEN 'yes' ELSE 'no' END AS media_use_received_transport
FROM device_live l
CROSS JOIN pbx_setting s;

CREATE OR REPLACE VIEW asterisk.ps_aors AS
SELECT
    l.sip_username AS id,
    '1'            AS max_contacts,
    'yes'          AS remove_existing,
    '30'           AS qualify_frequency,
    NULL::text     AS contact
FROM device_live l;

CREATE OR REPLACE VIEW asterisk.ps_auths AS
SELECT
    l.sip_username AS id,
    'md5'          AS auth_type,
    l.sip_username AS username,
    l.digest_hash  AS md5_cred,
    'linxpbx'      AS realm
FROM device_live l;

CREATE OR REPLACE VIEW asterisk.linx_ring_targets AS
SELECT
    e.number       AS number,
    l.sip_username AS aor
FROM extension e
LEFT JOIN device_live l ON l.extension_id = e.id
WHERE e.enabled AND e.deleted_at IS NULL;
