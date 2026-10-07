-- Where calls abroad may go (ADR-085, owner 2026-10-07).
--
-- Linx is set up in any country now, and a calling level that allows calls
-- abroad allows them either everywhere or only to the countries it lists:
--
--   'international' not in allowed_categories   no calls abroad (as before)
--   ... in it, abroad_countries NULL            everywhere (as before)
--   ... in it, abroad_countries a list          only those countries
--
-- "Abroad" is any number with another country calling code than the home
-- country's, which is what numbering_classify already calls international.
-- A premium-rate number abroad is still premium (it needs that switch too),
-- and when the level has a list it must also be in a listed country: a
-- list is a promise that calls go nowhere else. Numbers sharing the home
-- calling code (Canada from the US, Guernsey from the UK) are domestic to
-- libphonenumber and to Linx, so a list never stops them.

ALTER TABLE call_permission_level ADD COLUMN abroad_countries text[]
    CHECK (abroad_countries IS NULL
           OR (cardinality(abroad_countries) BETWEEN 1 AND 300
               AND array_to_string(abroad_countries, ',') ~ '^[A-Z]{2}(,[A-Z]{2})*$'));

COMMENT ON COLUMN call_permission_level.abroad_countries IS
    'NULL: calls abroad (if allowed) go anywhere; otherwise only to these ISO 3166 countries.';

CREATE OR REPLACE FUNCTION numbering_route(caller uuid, dialled text,
    OUT category text, OUT number_type text, OUT region text, OUT e164 text, OUT dial text, OUT label text,
    OUT allowed boolean, OUT reason text)
LANGUAGE plpgsql STABLE AS $$
DECLARE
    home text;
    caller_categories text[];
    caller_countries text[];
BEGIN
    SELECT s.country INTO home FROM pbx_setting s;
    SELECT c.* INTO category, number_type, region, e164, dial, label
    FROM numbering_classify(home, dialled) c;
    allowed := false;
    IF NOT EXISTS (SELECT 1 FROM extension e WHERE e.id = caller AND e.enabled AND e.deleted_at IS NULL) THEN
        reason := 'unknown_caller';
    ELSIF category = 'emergency' THEN
        allowed := true;
        reason := 'emergency';
    ELSIF category = 'invalid' THEN
        reason := 'invalid';
    ELSE
        SELECT l.allowed_categories, l.abroad_countries INTO caller_categories, caller_countries
        FROM extension e JOIN call_permission_level l ON l.id = e.call_permission_level_id
        WHERE e.id = caller;
        IF caller_categories IS NULL OR NOT (category = ANY (caller_categories)) THEN
            reason := 'not_permitted';
        ELSIF caller_countries IS NOT NULL
              AND category IN ('international', 'premium')
              AND NOT numbering_same_calling_code(home, e164)
              AND NOT (region = ANY (caller_countries)) THEN
            reason := 'not_permitted';
        ELSIF NOT EXISTS (SELECT 1 FROM numbering_lines(caller, e164, region, dial)) THEN
            reason := 'no_lines';
        ELSE
            allowed := true;
            reason := 'allowed';
        END IF;
    END IF;
END $$;

-- Whether e164 ("+15145550100") is under home's country calling code.
-- Calling codes are a prefix code, so a prefix match is exact.
CREATE FUNCTION numbering_same_calling_code(home text, e164 text) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT coalesce(e164 LIKE '+' || (SELECT r.calling_code FROM numbering_region r WHERE r.region = home LIMIT 1)::text || '%', false)
$$;
