-- A phone system or gateway that signs in to Linx, the way a desk phone
-- does (docs/SIMPLER.md §1, ADR-061): trunk kind 'registers_here'. Linx
-- doesn't reach out to it, so it has no host, no certificate to pin and no
-- password of its own to keep: like a device, only the digest hash of the
-- login Linx made for it (ADR-033), and its login name is its PJSIP
-- endpoint's name ("trunk-<id>"), which is how Asterisk tells whose
-- request it is (identify_by=auth_username). TLS and SRTP only: a gateway
-- that can't encrypt stays a 'lan_peer' (ADR-023).
ALTER TABLE trunk DROP CONSTRAINT trunk_kind_check;
ALTER TABLE trunk ADD CONSTRAINT trunk_kind_check
    CHECK (kind IN ('registration', 'ip_authenticated', 'lan_peer', 'registers_here'));
ALTER TABLE trunk DROP CONSTRAINT trunk_host_check;
ALTER TABLE trunk ADD CONSTRAINT trunk_host_check
    CHECK (CASE WHEN kind = 'registers_here' THEN host = '' ELSE length(host) BETWEEN 1 AND 255 END);

-- MD5(username:realm:password), Asterisk's md5_cred, as for devices
-- (migration 0005); only for 'registers_here'.
ALTER TABLE trunk ADD COLUMN digest_hash text CHECK (digest_hash ~ '^[0-9a-f]{32}$');
ALTER TABLE trunk ADD CONSTRAINT trunk_registers_here_login
    CHECK ((kind = 'registers_here') = (digest_hash IS NOT NULL));
ALTER TABLE trunk ADD CONSTRAINT trunk_registers_here_encrypted
    CHECK (kind <> 'registers_here'
           OR (transport = 'tls' AND media_encryption = 'srtp' AND wireguard_profile_id IS NULL
               AND password_enc IS NULL AND username = 'trunk-' || id::text));

-- "Calls on this line ring…" (docs/SIMPLER.md §1.2): where a call from
-- this line goes when it's for none of the line's own numbers (an analog
-- landline often sends none). NULL: such calls hear "not in use", as
-- before. Extensions are only ever soft-deleted; deleting one clears this
-- (internal/store) and linx_line_rings ignores a deleted one anyway.
ALTER TABLE trunk ADD COLUMN rings_extension_id uuid REFERENCES extension (id) ON DELETE SET NULL;

-- The extension number a trunk's call for none of its numbers rings, if
-- any (the dialplan's LINX_LINE_RINGS, after LINX_INBOUND found no DID).
-- Like linx_inbound: SECURITY DEFINER, the only thing linx_asterisk gains.
CREATE FUNCTION asterisk.linx_line_rings(endpoint text) RETURNS SETOF text
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT e.number
    FROM trunk t JOIN extension e ON e.id = t.rings_extension_id
    WHERE 'trunk-' || t.id::text = endpoint AND t.enabled AND e.enabled AND e.deleted_at IS NULL
$$;
REVOKE ALL ON FUNCTION asterisk.linx_line_rings(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION asterisk.linx_line_rings(text) TO linx_asterisk;
