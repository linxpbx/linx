-- Sound and picture on one route for the app and the browser (2026-10-12).
--
-- Without bundling, a picture added to a call found a route of its own:
-- slower to start (it gathered its own relay route), and on mobile data it
-- settled on the relay over TLS while the sound had UDP, so the picture
-- froze and came back while the sound was fine. Asterisk also offered that
-- new stream with no ICE details, which WebRTC refuses. Bundled, a picture
-- rides the sound's route from its first frame. Browsers and Google's
-- WebRTC both bundle by default; Asterisk does it when asked (bundle).
-- Desk phones are untouched.

CREATE OR REPLACE VIEW asterisk.ps_endpoints AS
SELECT
    l.sip_username    AS id,
    CASE WHEN l.kind IN ('web', 'ios') THEN 'transport-wss' ELSE 'transport-tls' END AS transport,
    l.sip_username    AS aors,
    l.sip_username    AS auth,
    'linx-extensions' AS context,
    'all'             AS disallow,
    CASE WHEN l.kind IN ('web', 'ios') THEN 'opus,g722,ulaw,h264,vp8' ELSE 'opus,g722,ulaw' END AS allow,
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
    -- One video stream each way and no more: a 1:1 call, never a grid.
    -- Meetings are the SFU's job (Phase 3), not the switch's.
    CASE WHEN l.kind IN ('web', 'ios') THEN '1' END AS max_video_streams,
    -- The light (above). A phone that asks for the mailbox itself gets
    -- what it asked for instead of both, so it is never told twice.
    CASE WHEN l.kind NOT IN ('web', 'ios') THEN b.id::text END AS mailboxes,
    'yes'             AS mwi_subscribe_replaces_unsolicited,
    -- Sound and picture on one route (above). Last, because a view can
    -- only gain columns at its end.
    CASE WHEN l.kind IN ('web', 'ios') THEN 'yes' ELSE 'no' END AS bundle
FROM device_live l
CROSS JOIN pbx_setting s
LEFT JOIN voicemail_box b ON b.extension_id = l.extension_id AND b.enabled;

