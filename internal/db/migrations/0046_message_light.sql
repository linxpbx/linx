-- The message-waiting light, and listening from a desk phone
-- (docs/PHASE2.md §12 step 9b, owner 2026-10-04).
--
-- A desk phone's light comes on when there is a new message in its
-- person's voicemail box and goes out when there isn't. Asterisk does the
-- telling (a SIP NOTIFY to the phone); what it tells is set from outside
-- by the control plane over ARI, because Linx keeps voicemail itself and
-- has no mailbox of Asterisk's own to read (ADR-069). The mailbox is
-- named after the box, which for a person is their extension's id.
--
-- Only desk phones and softphones are given a mailbox. The web app and
-- the iPhone app count their own new messages and show a badge, and an
-- unsolicited NOTIFY to a browser's line is noise on a websocket that
-- allows exactly the messages a phone line needs.

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
    'yes'             AS mwi_subscribe_replaces_unsolicited
FROM device_live l
CROSS JOIN pbx_setting s
LEFT JOIN voicemail_box b ON b.extension_id = l.extension_id AND b.enabled;

COMMENT ON VIEW asterisk.ps_endpoints IS
    'Asterisk''s endpoints: one per live device. mailboxes is the person''s voicemail box, for the message-waiting light on desk phones.';

CREATE OR REPLACE VIEW asterisk.ps_aors AS
SELECT
    l.sip_username AS id,
    '1'            AS max_contacts,
    'yes'          AS remove_existing,
    '30'           AS qualify_frequency,
    NULL::text     AS contact,
    -- A phone that subscribes to its own mailbox is answered from here.
    CASE WHEN l.kind NOT IN ('web', 'ios') THEN b.id::text END AS mailboxes
FROM device_live l
LEFT JOIN voicemail_box b ON b.extension_id = l.extension_id AND b.enabled;
