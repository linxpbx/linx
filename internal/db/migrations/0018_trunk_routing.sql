-- Trunks reach Asterisk (docs/TRUNKS.md §4-§5, 1D step 3): outgoing calls
-- now get real lines, and calls from a trunk find their DID. Asterisk asks
-- two SECURITY DEFINER functions through func_odbc, as with ring targets,
-- so it never reads the trunk tables themselves (their passwords are
-- sealed anyway; the control plane renders pjsip_trunks.conf from them,
-- ADR-043).

-- The number to send a trunk for a call to e164 (NULL for an emergency or
-- service number, which every trunk gets exactly as dialled, in dial), in
-- the trunk's dial format (internal/trunk DialFormats). 'local' writes the
-- home country's numbers the national way ("0501234567") and others with
-- the home international prefix ("0044..."); when that prefix is a pattern
-- rather than plain digits, "00".
CREATE FUNCTION numbering_trunk_number(home text, dial_format text, e164 text, number_region text, dial text) RETURNS text
LANGUAGE plpgsql STABLE AS $$
DECLARE
    h numbering_region;
    cc integer;
BEGIN
    IF e164 IS NULL THEN
        RETURN dial;
    END IF;
    IF dial_format = 'e164' THEN
        RETURN e164;
    ELSIF dial_format = '00_prefix' THEN
        RETURN '00' || substr(e164, 2);
    END IF;
    SELECT * INTO h FROM numbering_region WHERE region = home;
    SELECT calling_code INTO cc FROM numbering_region WHERE region = number_region;
    IF cc = h.calling_code THEN
        RETURN h.national_prefix || substr(e164, length(cc::text) + 2);
    END IF;
    RETURN CASE WHEN h.international_prefix ~ '^[0-9]+$' THEN h.international_prefix ELSE '00' END || substr(e164, 2);
END $$;

-- The lines an allowed call from extension caller goes out on, in the
-- order they're tried (docs/TRUNKS.md §5): every enabled trunk with an
-- outbound priority. caller_id is what the called person sees: the
-- caller's own DID on that trunk if it has one, else the trunk's main
-- number, else nothing (the provider's default).
CREATE FUNCTION numbering_lines(caller uuid, e164 text, number_region text, dial text)
RETURNS TABLE (trunk_id uuid, trunk_name text, endpoint text, number text, caller_id text, max_calls integer, priority integer)
LANGUAGE sql STABLE AS $$
    SELECT t.id, t.name, 'trunk-' || t.id::text,
           numbering_trunk_number(s.country, t.dial_format, e164, number_region, dial),
           coalesce((SELECT d.number FROM trunk_did d WHERE d.trunk_id = t.id AND d.extension_id = caller ORDER BY d.number LIMIT 1),
                    t.caller_id_number, ''),
           t.max_calls, t.outbound_priority
    FROM trunk t, pbx_setting s, extension e
    WHERE e.id = caller AND t.tenant_id = e.tenant_id AND t.enabled AND t.outbound_priority IS NOT NULL
    ORDER BY t.outbound_priority
$$;

-- numbering_route (migrations 0016, 0017) gains lines: an allowed call with
-- at least one line is 'allowed'; with none, still 'no_lines'. Emergency
-- numbers stay 'emergency' (allowed) either way: the dialplan tries every
-- line, and says "all circuits are busy" if there's none. Same signature,
-- so nothing that reads it changes.
CREATE OR REPLACE FUNCTION numbering_route(caller uuid, dialled text,
    OUT category text, OUT number_type text, OUT region text, OUT e164 text, OUT dial text, OUT label text,
    OUT allowed boolean, OUT reason text)
LANGUAGE plpgsql STABLE AS $$
DECLARE
    caller_categories text[];
BEGIN
    SELECT c.* INTO category, number_type, region, e164, dial, label
    FROM pbx_setting s, numbering_classify(s.country, dialled) c;
    allowed := false;
    IF NOT EXISTS (SELECT 1 FROM extension e WHERE e.id = caller AND e.enabled AND e.deleted_at IS NULL) THEN
        reason := 'unknown_caller';
    ELSIF category = 'emergency' THEN
        allowed := true;
        reason := 'emergency';
    ELSIF category = 'invalid' THEN
        reason := 'invalid';
    ELSE
        SELECT l.allowed_categories INTO caller_categories
        FROM extension e JOIN call_permission_level l ON l.id = e.call_permission_level_id
        WHERE e.id = caller;
        IF caller_categories IS NULL OR NOT (category = ANY (caller_categories)) THEN
            reason := 'not_permitted';
        ELSIF NOT EXISTS (SELECT 1 FROM numbering_lines(caller, e164, region, dial)) THEN
            reason := 'no_lines';
        ELSE
            allowed := true;
            reason := 'allowed';
        END IF;
    END IF;
END $$;

-- What the dialplan (linx-outbound) asks for a call from one of Asterisk's
-- endpoints: the caller is a live device's or browser line's SIP username,
-- so trunks and anything else are unknown callers (ADR-048). One row, four
-- columns, because func_odbc hands the dialplan a comma-separated row:
-- lines is "endpoint/number/caller_id/max_calls" per line, joined by "&",
-- and holds only trunk ids, digits and "+", so nothing in it can smuggle
-- options into Dial(). withhold: the caller's level hides its number
-- (never for an emergency call).
DROP FUNCTION asterisk.linx_route_outbound(text, text);
CREATE FUNCTION asterisk.linx_outbound(endpoint text, dialled text,
    OUT reason text, OUT category text, OUT withhold boolean, OUT lines text)
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
    caller uuid := coalesce((SELECT l.extension_id FROM device_live l WHERE l.sip_username = endpoint),
                            '00000000-0000-0000-0000-000000000000'::uuid);
    r record;
BEGIN
    SELECT * INTO r FROM numbering_route(caller, dialled);
    reason := r.reason;
    category := r.category;
    withhold := false;
    lines := '';
    IF r.allowed THEN
        SELECT coalesce(string_agg(l.endpoint || '/' || l.number || '/' || l.caller_id || '/' || l.max_calls, '&' ORDER BY l.priority), '')
        INTO lines
        FROM numbering_lines(caller, r.e164, r.region, r.dial) l
        WHERE l.number ~ '^\+?[0-9*#]+$' AND l.caller_id ~ '^\+?[0-9]*$';
        IF r.category <> 'emergency' THEN
            SELECT coalesce(lv.withhold_caller_id, false) INTO withhold
            FROM extension e LEFT JOIN call_permission_level lv ON lv.id = e.call_permission_level_id
            WHERE e.id = caller;
        END IF;
    END IF;
END $$;
REVOKE ALL ON FUNCTION asterisk.linx_outbound(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION asterisk.linx_outbound(text, text) TO linx_asterisk;

-- What the dialplan (linx-from-trunk) asks for a call arriving from trunk
-- endpoint to dialled (the number the provider sent): the extension number
-- to ring, '' if the trunk owns that DID but it rings nobody (or its
-- extension is off), and no row if the trunk doesn't own it: calls from a
-- trunk reach that trunk's own DIDs and nothing else (ADR-048). The number
-- matches written as stored, or the same number in any other way of
-- writing it ("+97142345678", "97142345678", "042345678").
CREATE FUNCTION asterisk.linx_inbound(endpoint text, dialled text) RETURNS SETOF text
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    WITH n AS (
        SELECT (numbering_classify(s.country, dialled)).e164 AS e164,
               (numbering_classify(s.country, '+' || ltrim(dialled, '+'))).e164 AS plus, s.country
        FROM pbx_setting s
    )
    SELECT coalesce((SELECT e.number FROM extension e WHERE e.id = d.extension_id AND e.enabled AND e.deleted_at IS NULL), '')
    FROM trunk_did d JOIN trunk t ON t.id = d.trunk_id, n
    WHERE 'trunk-' || t.id::text = endpoint AND t.enabled AND dialled ~ '^\+?[0-9]{2,20}$'
      AND (d.number = dialled
           OR (numbering_classify(n.country, d.number)).e164 IN (n.e164, n.plus))
    ORDER BY d.number = dialled DESC, d.number
    LIMIT 1
$$;
REVOKE ALL ON FUNCTION asterisk.linx_inbound(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION asterisk.linx_inbound(text, text) TO linx_asterisk;
