-- A call ends when one phone's sound has stopped for good (2026-10-16).
--
-- The owner's iPhone lost its line mid-call and gave up on the call, but
-- Asterisk never heard a goodbye: the iPad went on showing the call as up,
-- with nobody at the other end, until it was hung up by hand. Asterisk now
-- hangs a call up when an app or a browser has sent it no sound at all for
-- 30 s. A call that is merely quiet still sends (Opus's silence packets),
-- and a phone that has only stepped out of coverage for a few seconds comes
-- back long before. Held calls are left alone (rtp_timeout_hold stays off),
-- and desk phones keep Asterisk's default.

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
    CASE WHEN l.kind IN ('web', 'ios') THEN 'yes' ELSE 'no' END AS bundle,
    -- Above.
    CASE WHEN l.kind IN ('web', 'ios') THEN '30' END AS rtp_timeout
FROM device_live l
CROSS JOIN pbx_setting s
LEFT JOIN voicemail_box b ON b.extension_id = l.extension_id AND b.enabled;
