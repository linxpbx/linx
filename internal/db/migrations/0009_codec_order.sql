-- Codec order (Phase 1B demo finding, docs/PBX.md §3). Asterisk can pass
-- Opus through but can't encode it, so a phone that put Opus first heard
-- silence (and was hung up on) whenever Linx itself played a message:
-- "Unable to find a codec translation path: (gsm) -> (opus)". Until Linx has
-- an Opus encoder (decided with Phase 1C), Asterisk answers with its own
-- order: G.722 (HD voice, which it can encode), then G.711, then Opus.
-- prefer:configured makes that order win over the phone's.
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
    'prefer:configured,operation:intersect,keep:all,transcode:allow' AS incoming_offer_codec_prefs
FROM device d
JOIN extension e ON e.id = d.extension_id
WHERE d.enabled AND e.enabled AND e.deleted_at IS NULL;
