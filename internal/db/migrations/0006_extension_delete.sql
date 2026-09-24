-- Extensions are soft-deleted (docs/PBX.md §5 "Deleting an extension"):
-- deleted_at is set, not the row removed, so device.extension_id never
-- needs ON DELETE handling and a deleted extension's audit trail (and its
-- devices', revoked in the same transaction) stays intact. The number
-- itself frees up for reuse at once.
ALTER TABLE extension ADD COLUMN deleted_at timestamptz;

ALTER TABLE extension DROP CONSTRAINT extension_tenant_id_number_key;
CREATE UNIQUE INDEX extension_tenant_number_idx ON extension (tenant_id, number) WHERE deleted_at IS NULL;

-- Belt and suspenders alongside application code clearing enabled on
-- delete: a deleted extension's devices never appear over realtime, even
-- if that code ever regresses.
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
    'no'              AS ice_support
FROM device d
JOIN extension e ON e.id = d.extension_id
WHERE d.enabled AND e.enabled AND e.deleted_at IS NULL;

CREATE OR REPLACE VIEW asterisk.ps_aors AS
SELECT
    d.sip_username AS id,
    '1'            AS max_contacts,
    'yes'          AS remove_existing
FROM device d
JOIN extension e ON e.id = d.extension_id
WHERE d.enabled AND e.enabled AND e.deleted_at IS NULL;

CREATE OR REPLACE VIEW asterisk.ps_auths AS
SELECT
    d.sip_username AS id,
    'md5'          AS auth_type,
    d.sip_username AS username,
    d.digest_hash  AS md5_cred,
    'linxpbx'      AS realm
FROM device d
JOIN extension e ON e.id = d.extension_id
WHERE d.enabled AND e.enabled AND e.deleted_at IS NULL;

CREATE OR REPLACE VIEW asterisk.linx_ring_targets AS
SELECT
    e.number       AS number,
    d.sip_username AS aor
FROM device d
JOIN extension e ON e.id = d.extension_id
WHERE d.enabled AND e.enabled AND e.deleted_at IS NULL;
