-- Browsers (docs/WEB.md §2, Phase 1C). A web device connects over the secure
-- websocket (transport-wss, reached only through the control plane's /sip
-- relay) and uses WebRTC media: ICE, DTLS-SRTP keyed by certificate
-- fingerprints (never SDES keys in the SDP), AVPF and rtcp-mux. Everything
-- else keeps SIP over TLS and SDES-SRTP. Columns a kind doesn't use are NULL,
-- which Asterisk's realtime reader skips.
--
-- Opus first again for everyone (ADR-041): the image now has an Opus encoder,
-- so Linx's messages play to Opus callers. That reverses migration 0009's
-- workaround, so the caller's own codec order wins again (Asterisk's
-- default) instead of prefer:configured.
CREATE OR REPLACE VIEW asterisk.ps_endpoints AS
SELECT
    d.sip_username    AS id,
    CASE WHEN d.kind = 'web' THEN 'transport-wss' ELSE 'transport-tls' END AS transport,
    d.sip_username    AS aors,
    d.sip_username    AS auth,
    'linx-extensions' AS context,
    'all'             AS disallow,
    'opus,g722,ulaw'  AS allow,
    'no'              AS direct_media,
    CASE WHEN d.kind = 'web' THEN 'dtls' ELSE 'sdes' END AS media_encryption,
    'no'              AS media_encryption_optimistic,
    'yes'             AS force_rport,
    'yes'             AS rewrite_contact,
    'yes'             AS rtp_symmetric,
    CASE WHEN d.kind = 'web' THEN 'yes' ELSE 'no' END AS ice_support,
    '"' || regexp_replace(e.display_name, '["<>\;[:cntrl:]]', '', 'g') || '" <' || e.number || '>' AS callerid,
    NULL::text        AS incoming_offer_codec_prefs,
    nullif(s.sip_domain, '') AS from_domain,
    CASE WHEN d.kind = 'web' THEN 'yes' ELSE 'no' END AS use_avpf,
    CASE WHEN d.kind = 'web' THEN 'yes' ELSE 'no' END AS rtcp_mux,
    CASE WHEN d.kind = 'web' THEN 'fingerprint' END AS dtls_verify,
    CASE WHEN d.kind = 'web' THEN 'actpass' END AS dtls_setup,
    CASE WHEN d.kind = 'web' THEN 'yes' END AS dtls_auto_generate_cert,
    CASE WHEN d.kind = 'web' THEN 'yes' ELSE 'no' END AS media_use_received_transport
FROM device d
JOIN extension e ON e.id = d.extension_id
CROSS JOIN pbx_setting s
WHERE d.enabled AND e.enabled AND e.deleted_at IS NULL;
