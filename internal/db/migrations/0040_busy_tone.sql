-- Listening for the busy tone (docs/PBX.md §4): an analog landline behind an
-- office gateway never signals the far end hanging up; the exchange plays
-- its busy tone instead, so Linx kept recording it (Demo B, Phase 1F) and
-- the line stayed busy. The dialplan's LINX_BUSY_TONE(endpoint) asks
-- whether a call's line is one of the kinds that can carry such a landline
-- (a phone system on the LAN, or one that signs in to Linx) and, if so,
-- which country's busy tone to listen for; Asterisk's entrypoint renders
-- each country's tone (internal/asteriskconf). No row: the line is a
-- provider's, which signals hang-ups itself, or isn't a line at all.
-- Like linx_inbound: SECURITY DEFINER, the only thing linx_asterisk gains.
CREATE FUNCTION asterisk.linx_busy_tone(endpoint text) RETURNS SETOF text
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT s.country
    FROM trunk t CROSS JOIN pbx_setting s
    WHERE 'trunk-' || t.id::text = endpoint AND t.enabled AND t.kind IN ('lan_peer', 'registers_here')
$$;
REVOKE ALL ON FUNCTION asterisk.linx_busy_tone(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION asterisk.linx_busy_tone(text) TO linx_asterisk;
