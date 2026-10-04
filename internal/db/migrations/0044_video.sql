-- 1:1 video for the app (docs/PHASE2.md §7, Phase 2 step 7).
--
-- A Linx call is audio from the moment it rings — that is what the lock
-- screen, a car and a headset understand — and video is added to a call
-- that is already up, by either side asking for it (a re-INVITE). For that
-- to work Asterisk has to let the two WebRTC endpoints agree on a video
-- codec; it never transcodes video, it only passes it between them.
--
-- H.264 first because an iPhone encodes and decodes it in hardware, which
-- is the difference between a warm phone and a flat battery; VP8 after it
-- for anything that hasn't got H.264. Both are passed through untouched.
--
-- Only the app and the browser get video. A desk phone or a gateway keeps
-- exactly the codecs it had, because offering video to a device that does
-- not expect it is how a working phone call stops working.
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
    CASE WHEN l.kind IN ('web', 'ios') THEN '1' END AS max_video_streams
FROM device_live l
CROSS JOIN pbx_setting s;
