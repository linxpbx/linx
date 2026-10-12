-- Asterisk carries sound only (ADR-087, 2026-10-12).
--
-- A call's picture between two Linx devices now travels on a direct link
-- between them, set up through the control plane's call link; a video call
-- is marked by the X-Linx-Video header, not a video stream. Asterisk
-- relayed video without the feedback two ends need to agree on quality,
-- and every camera change was a re-INVITE through it to each side in turn
-- (the owner's iPad and iPhone, 2026-10-12 22:29). With no video codec
-- allowed, a video stream an older app still offers is refused and the
-- call goes on as sound. Bundling (migration 0051) stays.

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
    CASE WHEN l.kind IN ('web', 'ios') THEN 'yes' ELSE 'no' END AS media_use_received_transport,
    -- No video codec is allowed, so a video stream is refused whatever
    -- this says; left at Asterisk's default.
    NULL::text        AS max_video_streams,
    -- The light (above). A phone that asks for the mailbox itself gets
    -- what it asked for instead of both, so it is never told twice.
    CASE WHEN l.kind NOT IN ('web', 'ios') THEN b.id::text END AS mailboxes,
    'yes'             AS mwi_subscribe_replaces_unsolicited,
    -- One route for everything the call carries (migration 0051).
    CASE WHEN l.kind IN ('web', 'ios') THEN 'yes' ELSE 'no' END AS bundle
FROM device_live l
CROSS JOIN pbx_setting s
LEFT JOIN voicemail_box b ON b.extension_id = l.extension_id AND b.enabled;

