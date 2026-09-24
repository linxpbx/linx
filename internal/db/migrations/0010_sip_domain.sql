-- The name phones know the server by (Phase 1B demo finding). Without
-- from_domain, Asterisk put its own container address (e.g. 172.21.0.3) in
-- the From header of every call it sent to a phone; Linphone kept that
-- address in its call history, and calling back from there went to an
-- address that only exists inside the server. The control plane sets
-- sip_domain ("sip.<domain>") at every start, from LINX_DOMAIN. One row.
CREATE TABLE pbx_setting (
    id         boolean PRIMARY KEY DEFAULT true CHECK (id),
    sip_domain text NOT NULL DEFAULT '' CHECK (sip_domain ~ '^[a-z0-9.-]*$')
);
INSERT INTO pbx_setting DEFAULT VALUES;

CREATE OR REPLACE VIEW asterisk.ps_endpoints AS
SELECT
    d.sip_username    AS id,
    'transport-tls'   AS transport,
    d.sip_username    AS aors,
    d.sip_username    AS auth,
    'linx-extensions' AS context,
    'all'             AS disallow,
    'g722,ulaw,opus'  AS allow,
    'no'              AS direct_media,
    'sdes'            AS media_encryption,
    'no'              AS media_encryption_optimistic,
    'yes'             AS force_rport,
    'yes'             AS rewrite_contact,
    'yes'             AS rtp_symmetric,
    'no'              AS ice_support,
    '"' || regexp_replace(e.display_name, '["<>\;[:cntrl:]]', '', 'g') || '" <' || e.number || '>' AS callerid,
    'prefer:configured,operation:intersect,keep:all,transcode:allow' AS incoming_offer_codec_prefs,
    nullif(s.sip_domain, '') AS from_domain
FROM device d
JOIN extension e ON e.id = d.extension_id
CROSS JOIN pbx_setting s
WHERE d.enabled AND e.enabled AND e.deleted_at IS NULL;

-- Asterisk sometimes looks AORs up by their fixed ("permanent") contact,
-- e.g. after a phone signs out; without the column Postgres answers
-- 'column "contact" does not exist' (seen in the demo). Linx devices always
-- register, so it's always empty.
CREATE OR REPLACE VIEW asterisk.ps_aors AS
SELECT
    d.sip_username AS id,
    '1'            AS max_contacts,
    'yes'          AS remove_existing,
    '30'           AS qualify_frequency,
    NULL::text     AS contact
FROM device d
JOIN extension e ON e.id = d.extension_id
WHERE d.enabled AND e.enabled AND e.deleted_at IS NULL;
