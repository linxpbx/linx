-- Calls (docs/PBX.md §4, Phase 1B step 4).
--
-- Every endpoint gets its extension as caller ID: PJSIP then ignores what the
-- phone itself claims in From, so the dialplan and call events can trust
-- CALLERID(num) to be the caller's extension number. The display name is
-- stripped of the characters caller ID syntax treats specially.
CREATE OR REPLACE VIEW asterisk.ps_endpoints AS
SELECT
    d.sip_username    AS id,
    'transport-tls'   AS transport,
    d.sip_username    AS aors,
    d.sip_username    AS auth,
    'linx-extensions' AS context,
    'all'             AS disallow,
    'opus,g722,ulaw'  AS allow,
    'no'              AS direct_media,
    'sdes'            AS media_encryption,
    'no'              AS media_encryption_optimistic,
    'yes'             AS force_rport,
    'yes'             AS rewrite_contact,
    'yes'             AS rtp_symmetric,
    'no'              AS ice_support,
    '"' || regexp_replace(e.display_name, '["<>\\;[:cntrl:]]', '', 'g') || '" <' || e.number || '>' AS callerid
FROM device d
JOIN extension e ON e.id = d.extension_id
WHERE d.enabled AND e.enabled AND e.deleted_at IS NULL;

-- One row per enabled extension, and one per enabled device of it: aor is
-- NULL for an extension with no devices, so the dialplan can tell "exists
-- but nothing to ring" (not available) from "no such number" (not in use).
CREATE OR REPLACE VIEW asterisk.linx_ring_targets AS
SELECT
    e.number       AS number,
    d.sip_username AS aor
FROM extension e
LEFT JOIN device d ON d.extension_id = e.id AND d.enabled
WHERE e.enabled AND e.deleted_at IS NULL;

-- Asterisk checks every registered device every 30 s (SIP OPTIONS,
-- "qualify"). That's what makes it report a device online or offline to the
-- control plane (ARI ContactStatusChange), and it keeps NAT mappings open.
CREATE OR REPLACE VIEW asterisk.ps_aors AS
SELECT
    d.sip_username AS id,
    '1'            AS max_contacts,
    'yes'          AS remove_existing,
    '30'           AS qualify_frequency
FROM device d
JOIN extension e ON e.id = d.extension_id
WHERE d.enabled AND e.enabled AND e.deleted_at IS NULL;

-- Whether the device is signed in right now, as Asterisk last reported it
-- over ARI (ContactStatusChange). Not an admin setting, so changing it
-- doesn't bump version (the etag).
ALTER TABLE device ADD COLUMN online boolean NOT NULL DEFAULT false;
