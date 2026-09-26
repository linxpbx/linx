-- Outgoing numbers (docs/TRUNKS.md §5, ADR-044). What someone dialled is
-- decided here, in the database, so outgoing calls keep working while the
-- control plane restarts (ADR-034's rule): Asterisk asks
-- asterisk.linx_route_outbound through func_odbc, like ring targets.
--
-- The numbering_* tables hold Google's libphonenumber data (through
-- github.com/nyaruka/phonenumbers). They're filled by the control plane at
-- every start when the library's data changed (internal/numbering,
-- store.SyncNumbering), never by hand. The functions below repeat
-- libphonenumber's parsing and validation step for step (the names in
-- comments are the library's); internal/numbering's Docker test checks they
-- agree with the library on thousands of numbers.

-- The country Linx is set up in (docs/TRUNKS.md §11 item 3). Changed only
-- through the control plane, which checks it's a country on offer
-- (numbering.Countries).
ALTER TABLE pbx_setting ADD COLUMN country text NOT NULL DEFAULT 'AE' CHECK (country ~ '^[A-Z]{2}$');

-- Which libphonenumber data the tables hold (numbering.Data.Version).
CREATE TABLE numbering_data (
    id         boolean PRIMARY KEY DEFAULT true CHECK (id),
    version    text NOT NULL,
    updated_at timestamptz NOT NULL
);

-- One region's rules. Patterns are PostgreSQL regular expressions.
-- position orders the regions sharing a calling code (0 = main region).
CREATE TABLE numbering_region (
    calling_code                integer NOT NULL,
    region                      text NOT NULL,
    position                    integer NOT NULL,
    international_prefix        text NOT NULL,
    national_prefix             text NOT NULL,
    national_prefix_for_parsing text NOT NULL,
    transform_rule              text NOT NULL,
    leading_digits              text NOT NULL,
    same_mobile_and_fixed       boolean NOT NULL,
    general_pattern             text NOT NULL,
    general_lengths             integer[] NOT NULL,
    general_local_lengths       integer[] NOT NULL,
    PRIMARY KEY (calling_code, region),
    UNIQUE (calling_code, position)
);

-- The pattern for one type of number in a region, checked in check_order
-- (fixed_line and mobile, 100 and 101, are handled specially).
CREATE TABLE numbering_desc (
    calling_code integer NOT NULL,
    region       text NOT NULL,
    number_type  text NOT NULL,
    check_order  integer NOT NULL,
    pattern      text NOT NULL,
    lengths      integer[] NOT NULL,
    PRIMARY KEY (calling_code, region, number_type),
    FOREIGN KEY (calling_code, region) REFERENCES numbering_region ON DELETE CASCADE
);

-- A region's short numbers (libphonenumber's short-number data).
CREATE TABLE numbering_short (
    region             text PRIMARY KEY,
    general_pattern    text NOT NULL,
    general_lengths    integer[] NOT NULL,
    short_code_pattern text NOT NULL,
    short_code_lengths integer[] NOT NULL,
    emergency_pattern  text NOT NULL
);

-- Numbers that are always allowed, from Linx's own list per country
-- (numbering.Countries: for the UAE 999, 998, 997, 112 and 901).
CREATE TABLE numbering_always (
    region text NOT NULL,
    number text NOT NULL,
    label  text NOT NULL,
    PRIMARY KEY (region, number)
);

-- isNumberMatchingDesc: the whole number matches pattern and, if any are
-- given, has one of the lengths.
CREATE FUNCTION numbering_matches(num text, pattern text, lengths integer[]) RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT pattern <> ''
        AND (cardinality(lengths) = 0 OR length(num) = ANY (lengths))
        AND num ~ ('^(?:' || pattern || ')$')
$$;

-- getNumberTypeHelper: the type of a national number in one region, or
-- NULL (UNKNOWN).
CREATE FUNCTION numbering_type(cc integer, reg text, nsn text) RETURNS text
LANGUAGE plpgsql STABLE AS $$
DECLARE
    r numbering_region;
    t text;
BEGIN
    SELECT * INTO r FROM numbering_region WHERE calling_code = cc AND region = reg;
    IF NOT FOUND OR NOT numbering_matches(nsn, r.general_pattern, r.general_lengths) THEN
        RETURN NULL;
    END IF;
    SELECT d.number_type INTO t FROM numbering_desc d
    WHERE d.calling_code = cc AND d.region = reg AND d.check_order < 100
      AND numbering_matches(nsn, d.pattern, d.lengths)
    ORDER BY d.check_order LIMIT 1;
    IF t IS NOT NULL THEN
        RETURN t;
    END IF;
    IF EXISTS (SELECT 1 FROM numbering_desc d WHERE d.calling_code = cc AND d.region = reg
               AND d.number_type = 'fixed_line' AND numbering_matches(nsn, d.pattern, d.lengths)) THEN
        IF r.same_mobile_and_fixed OR EXISTS (SELECT 1 FROM numbering_desc d WHERE d.calling_code = cc
               AND d.region = reg AND d.number_type = 'mobile' AND numbering_matches(nsn, d.pattern, d.lengths)) THEN
            RETURN 'fixed_line_or_mobile';
        END IF;
        RETURN 'fixed_line';
    END IF;
    IF NOT r.same_mobile_and_fixed AND EXISTS (SELECT 1 FROM numbering_desc d WHERE d.calling_code = cc
           AND d.region = reg AND d.number_type = 'mobile' AND numbering_matches(nsn, d.pattern, d.lengths)) THEN
        RETURN 'mobile';
    END IF;
    RETURN NULL;
END $$;

-- getRegionCodeForNumber: which of the calling code's regions a national
-- number belongs to, or NULL.
CREATE FUNCTION numbering_region_for(cc integer, nsn text) RETURNS text
LANGUAGE plpgsql STABLE AS $$
DECLARE
    r numbering_region;
    n integer;
BEGIN
    SELECT count(*) INTO n FROM numbering_region WHERE calling_code = cc;
    IF n = 1 THEN
        RETURN (SELECT region FROM numbering_region WHERE calling_code = cc);
    END IF;
    FOR r IN SELECT * FROM numbering_region WHERE calling_code = cc ORDER BY position LOOP
        IF r.leading_digits <> '' THEN
            IF nsn ~ ('^(?:' || r.leading_digits || ')') THEN
                RETURN r.region;
            END IF;
        ELSIF numbering_type(cc, r.region, nsn) IS NOT NULL THEN
            RETURN r.region;
        END IF;
    END LOOP;
    RETURN NULL;
END $$;

-- testNumberLength for the UNKNOWN type (the general description).
CREATE FUNCTION numbering_test_length(num text, r numbering_region) RETURNS text
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    l integer := length(num);
    pl integer[] := r.general_lengths;
BEGIN
    IF cardinality(pl) = 0 OR pl[1] = -1 THEN
        RETURN 'invalid_length';
    END IF;
    IF l = ANY (r.general_local_lengths) THEN
        RETURN 'local_only';
    END IF;
    IF pl[1] = l THEN
        RETURN 'possible';
    ELSIF pl[1] > l THEN
        RETURN 'too_short';
    ELSIF pl[cardinality(pl)] < l THEN
        RETURN 'too_long';
    ELSIF l = ANY (pl[2:]) THEN
        RETURN 'possible';
    END IF;
    RETURN 'invalid_length';
END $$;

-- maybeStripNationalPrefixAndCarrierCode: num without its national prefix
-- ("0" in the UAE), or num unchanged.
CREATE FUNCTION numbering_strip_national_prefix(num text, r numbering_region) RETURNS text
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    m text[];
    groups integer;
    rest text;
    general text := '^(?:' || r.general_pattern || ')$';
    viable boolean;
    out text;
BEGIN
    IF num = '' OR r.national_prefix_for_parsing = '' THEN
        RETURN num;
    END IF;
    -- Group 1 is the whole prefix; the pattern's own groups follow.
    m := regexp_match(num, '^((?:' || r.national_prefix_for_parsing || '))');
    IF m IS NULL THEN
        RETURN num;
    END IF;
    groups := cardinality(m) - 1;
    rest := substr(num, length(m[1]) + 1);
    viable := num ~ general;
    IF r.transform_rule = '' OR m[groups + 1] IS NULL THEN
        IF viable AND NOT rest ~ general THEN
            RETURN num;
        END IF;
        RETURN rest;
    END IF;
    out := r.transform_rule;
    FOR i IN REVERSE groups..1 LOOP
        out := replace(out, '$' || i, coalesce(m[i + 1], ''));
    END LOOP;
    out := out || rest;
    IF viable AND NOT out ~ general THEN
        RETURN num;
    END IF;
    RETURN out;
END $$;

-- maybeExtractCountryCode: the calling code at the start of s (after "+" or
-- the international prefix), and the rest in national_number. 0 = none found
-- (the number is national); NULL = not a number.
CREATE FUNCTION numbering_extract_calling_code(s text, home numbering_region, OUT cc integer, OUT national_number text)
LANGUAGE plpgsql STABLE AS $$
DECLARE
    full_number text := s;
    is_international boolean := false;
    idd text;
    pot text;
BEGIN
    IF s LIKE '+%' THEN
        full_number := ltrim(s, '+');
        is_international := true;
    ELSIF home.international_prefix <> '' THEN
        idd := (regexp_match(s, '^((?:' || home.international_prefix || '))'))[1];
        -- Calling codes never start with 0, so a 0 after the prefix means it
        -- wasn't one (parsePrefixAsIdd).
        IF idd IS NOT NULL AND substr(s, length(idd) + 1, 1) <> '0' THEN
            full_number := substr(s, length(idd) + 1);
            is_international := true;
        END IF;
    END IF;
    IF is_international THEN
        IF length(full_number) <= 2 THEN
            RETURN; -- too short after the prefix
        END IF;
        IF full_number NOT LIKE '0%' THEN
            FOR i IN 1..least(3, length(full_number)) LOOP
                IF EXISTS (SELECT 1 FROM numbering_region WHERE calling_code = substr(full_number, 1, i)::integer) THEN
                    cc := substr(full_number, 1, i)::integer;
                    national_number := substr(full_number, i + 1);
                    RETURN;
                END IF;
            END LOOP;
        END IF;
        -- An unknown calling code after "+": libphonenumber tries again
        -- without the "+", and fails unless that finds a calling code.
        IF s LIKE '+%' THEN
            SELECT e.cc, e.national_number INTO cc, national_number
            FROM numbering_extract_calling_code(substr(s, 2), home) e;
            IF cc = 0 THEN
                cc := NULL;
                national_number := NULL;
            END IF;
        END IF;
        RETURN;
    END IF;
    -- The home calling code without "+" ("971501234567" in the UAE) counts
    -- when the number only makes sense that way.
    IF s LIKE home.calling_code::text || '%' THEN
        pot := numbering_strip_national_prefix(substr(s, length(home.calling_code::text) + 1), home);
        IF (NOT s ~ ('^(?:' || home.general_pattern || ')$') AND pot ~ ('^(?:' || home.general_pattern || ')$'))
           OR numbering_test_length(s, home) = 'too_long' THEN
            cc := home.calling_code;
            national_number := pot;
            RETURN;
        END IF;
    END IF;
    cc := 0;
    national_number := s;
END $$;

-- parseHelper: the calling code and national significant number of a
-- cleaned number dialled in the region home, or NULLs if it isn't one.
CREATE FUNCTION numbering_parse(home text, s text, OUT cc integer, OUT nsn text)
LANGUAGE plpgsql STABLE AS $$
DECLARE
    h numbering_region;
    r numbering_region;
    national_number text;
    pot text;
BEGIN
    IF length(regexp_replace(s, '[^0-9]', '', 'g')) < 2 THEN
        RETURN;
    END IF;
    SELECT * INTO h FROM numbering_region WHERE region = home;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    SELECT e.cc, e.national_number INTO cc, national_number FROM numbering_extract_calling_code(s, h) e;
    IF cc IS NULL THEN
        RETURN;
    END IF;
    IF cc = 0 THEN
        cc := h.calling_code;
        r := h;
    ELSE
        SELECT * INTO r FROM numbering_region WHERE calling_code = cc ORDER BY position LIMIT 1;
    END IF;
    IF length(national_number) < 2 THEN
        cc := NULL;
        RETURN;
    END IF;
    -- Keep the national prefix if the number is too short without it (it
    -- may be a short number that starts with the prefix's digits).
    pot := numbering_strip_national_prefix(national_number, r);
    IF numbering_test_length(pot, r) NOT IN ('too_short', 'local_only', 'invalid_length') THEN
        national_number := pot;
    END IF;
    IF length(national_number) < 2 OR length(national_number) > 17 THEN
        cc := NULL;
        RETURN;
    END IF;
    nsn := national_number;
END $$;

-- What a number dialled in the region home is (numbering.Classify):
-- category is emergency, service, landline, mobile, national, shared_cost,
-- toll_free, premium, international or invalid.
CREATE FUNCTION numbering_classify(home text, dialled text,
    OUT category text, OUT number_type text, OUT region text, OUT e164 text, OUT dial text, OUT label text)
LANGUAGE plpgsql STABLE AS $$
DECLARE
    s text := regexp_replace(dialled, '[ \t().-]', '', 'g');
    sh numbering_short;
    p record;
    home_cc integer;
BEGIN
    category := 'invalid';
    IF s !~ '^\+?[0-9]+$' OR length(s) > 250 THEN
        RETURN;
    END IF;
    IF s NOT LIKE '+%' THEN
        SELECT a.label INTO label FROM numbering_always a WHERE a.region = home AND a.number = s;
        SELECT * INTO sh FROM numbering_short WHERE numbering_short.region = home;
        IF label IS NULL AND sh.emergency_pattern <> '' AND s ~ ('^(?:' || sh.emergency_pattern || ')$') THEN
            label := 'emergency';
        END IF;
        IF label IS NOT NULL THEN
            category := 'emergency';
            region := home;
            dial := s;
            RETURN;
        END IF;
        IF numbering_matches(s, sh.general_pattern, sh.general_lengths)
           AND numbering_matches(s, sh.short_code_pattern, sh.short_code_lengths) THEN
            category := 'service';
            region := home;
            dial := s;
            RETURN;
        END IF;
    END IF;
    SELECT * INTO p FROM numbering_parse(home, s);
    IF p.cc IS NULL THEN
        RETURN;
    END IF;
    region := numbering_region_for(p.cc, p.nsn);
    number_type := numbering_type(p.cc, region, p.nsn);
    IF number_type IS NULL THEN
        region := NULL;
        RETURN;
    END IF;
    e164 := '+' || p.cc || p.nsn;
    dial := e164;
    category := CASE number_type
        WHEN 'fixed_line' THEN 'landline'
        WHEN 'fixed_line_or_mobile' THEN 'landline'
        WHEN 'mobile' THEN 'mobile'
        WHEN 'toll_free' THEN 'toll_free'
        WHEN 'premium_rate' THEN 'premium'
        WHEN 'shared_cost' THEN 'shared_cost'
        ELSE 'national' END;
    SELECT calling_code INTO home_cc FROM numbering_region WHERE numbering_region.region = home;
    IF p.cc <> home_cc AND category <> 'premium' THEN
        category := 'international';
    END IF;
END $$;

-- Why an extension number can't be used in the region home, or NULL if it
-- can (docs/TRUNKS.md §5): it starts like an outside number, or it's an
-- emergency or other short number.
CREATE FUNCTION numbering_extension_clash(home text, number text) RETURNS text
LANGUAGE plpgsql STABLE AS $$
DECLARE
    h numbering_region;
    c text;
BEGIN
    SELECT * INTO h FROM numbering_region WHERE region = home;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    IF h.national_prefix <> '' AND number LIKE h.national_prefix || '%' THEN
        RETURN 'national_prefix';
    END IF;
    IF h.international_prefix <> '' AND number ~ ('^(?:' || h.international_prefix || ')') THEN
        RETURN 'international_prefix';
    END IF;
    c := (numbering_classify(home, number)).category;
    IF c IN ('emergency', 'service') THEN
        RETURN c;
    END IF;
    RETURN NULL;
END $$;

-- Extensions can't take such numbers. Checked when a number is set, so a
-- country change doesn't lock existing extensions (the control plane lists
-- those clashes instead).
CREATE FUNCTION extension_number_check() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    home text := (SELECT country FROM pbx_setting);
    reason text;
BEGIN
    IF NEW.deleted_at IS NULL THEN
        reason := numbering_extension_clash(home, NEW.number);
        IF reason IS NOT NULL THEN
            RAISE EXCEPTION 'extension number % is reserved (%)', NEW.number, reason
                USING ERRCODE = 'check_violation', CONSTRAINT = 'extension_number_reserved',
                      DETAIL = reason, HINT = home;
        END IF;
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER extension_number_insert BEFORE INSERT ON extension
    FOR EACH ROW EXECUTE FUNCTION extension_number_check();
CREATE TRIGGER extension_number_update BEFORE UPDATE OF number ON extension
    FOR EACH ROW WHEN (OLD.number IS DISTINCT FROM NEW.number) EXECUTE FUNCTION extension_number_check();

-- The outgoing-call decision for a caller's extension. This slice (1D
-- step 1) decides what the number is; permission levels and lines come
-- with trunks (steps 2 and 3), so everything but emergency numbers is
-- refused with 'no_lines' for now. reason: 'emergency' (always allowed),
-- 'invalid', 'unknown_caller', 'no_lines'.
CREATE FUNCTION numbering_route(caller uuid, dialled text,
    OUT category text, OUT number_type text, OUT region text, OUT e164 text, OUT dial text, OUT label text,
    OUT allowed boolean, OUT reason text)
LANGUAGE plpgsql STABLE AS $$
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
        reason := 'no_lines';
    END IF;
END $$;

-- What Asterisk calls (through func_odbc) for a call from one of its
-- endpoints: the caller is the device's SIP username, so only a live device
-- or browser line can call out (ADR-048). Runs with its owner's rights, so
-- linx_asterisk needs no access to the tables behind it.
CREATE FUNCTION asterisk.linx_route_outbound(endpoint text, dialled text,
    OUT category text, OUT number_type text, OUT region text, OUT e164 text, OUT dial text, OUT label text,
    OUT allowed boolean, OUT reason text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT r.* FROM numbering_route(
        coalesce((SELECT l.extension_id FROM device_live l WHERE l.sip_username = endpoint), '00000000-0000-0000-0000-000000000000'::uuid),
        dialled) r
$$;
REVOKE ALL ON FUNCTION asterisk.linx_route_outbound(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION asterisk.linx_route_outbound(text, text) TO linx_asterisk;
