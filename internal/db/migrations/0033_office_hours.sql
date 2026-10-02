-- Office hours, holidays and "When someone calls" (ADR-068, docs/PHASE1F.md
-- §7, Phase 1F step 12). The database decides "open or closed right now",
-- so the dialplan and the call simulator use the same function and can't
-- disagree; Asterisk still only calls asterisk.linx_route.

-- The server's time zone (setup's, the control plane writes it at every
-- start from TZ, like sip_domain). Office hours are in it.
ALTER TABLE pbx_setting ADD COLUMN time_zone text NOT NULL DEFAULT 'UTC' CHECK (length(time_zone) BETWEEN 1 AND 64);

-- A schedule: "Office hours" (one made at setup for a business), and any
-- more the admin adds ("Support hours").
CREATE TABLE schedule (
    id         uuid PRIMARY KEY,
    tenant_id  uuid NOT NULL REFERENCES tenant (id),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    version    integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, name)
);

-- When it's open: spans of one weekday (0 Sunday … 6 Saturday, like
-- extract(dow)), several a day for a lunch break. A span never crosses
-- midnight ("closes" may be 24:00); the API refuses overlaps.
CREATE TABLE schedule_span (
    schedule_id uuid NOT NULL REFERENCES schedule (id) ON DELETE CASCADE,
    weekday     smallint NOT NULL CHECK (weekday BETWEEN 0 AND 6),
    opens       time NOT NULL,
    closes      time NOT NULL,
    CHECK (opens < closes),
    PRIMARY KEY (schedule_id, weekday, opens)
);

-- Closed all day: one date or a range ("Eid al-Fitr", 20-22 March), and
-- for fixed ones every year on the same dates.
CREATE TABLE schedule_holiday (
    schedule_id uuid NOT NULL REFERENCES schedule (id) ON DELETE CASCADE,
    position    smallint NOT NULL CHECK (position BETWEEN 1 AND 200),
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    first_day   date NOT NULL,
    last_day    date NOT NULL,
    every_year  boolean NOT NULL DEFAULT false,
    CHECK (last_day >= first_day AND last_day - first_day < 366),
    PRIMARY KEY (schedule_id, position)
);

-- The holiday on local date d, if any.
CREATE FUNCTION schedule_holiday_on(sid uuid, d date) RETURNS text
LANGUAGE sql STABLE SET search_path = public, pg_temp AS $$
    SELECT h.name FROM schedule_holiday h
    WHERE h.schedule_id = sid AND (
        d BETWEEN h.first_day AND h.last_day
        OR (h.every_year AND EXISTS (
            SELECT 1 FROM unnest(ARRAY[
                    extract(year FROM d)::integer - extract(year FROM h.first_day)::integer - 1,
                    extract(year FROM d)::integer - extract(year FROM h.first_day)::integer]) AS k
            WHERE k >= 1
              AND d BETWEEN (h.first_day + make_interval(years => k))::date
                        AND (h.last_day + make_interval(years => k))::date)))
    ORDER BY h.first_day, h.position
    LIMIT 1
$$;

-- Open at local time t of local date d (holidays aside).
CREATE FUNCTION schedule_open_local(sid uuid, d date, t time) RETURNS boolean
LANGUAGE sql STABLE SET search_path = public, pg_temp AS $$
    SELECT schedule_holiday_on(sid, d) IS NULL AND EXISTS (
        SELECT 1 FROM schedule_span s
        WHERE s.schedule_id = sid AND s.weekday = extract(dow FROM d) AND t >= s.opens AND t < s.closes)
$$;

-- Whether schedule sid is open at moment at, in the server's time zone,
-- and the holiday's name when a holiday closes it. The one answer calls,
-- the simulator and the Office hours page all use.
CREATE FUNCTION schedule_open_at(sid uuid, at timestamptz, OUT open boolean, OUT holiday text)
LANGUAGE plpgsql STABLE SET search_path = public, pg_temp AS $$
DECLARE
    local timestamp := at AT TIME ZONE (SELECT time_zone FROM pbx_setting);
BEGIN
    holiday := schedule_holiday_on(sid, local::date);
    open := holiday IS NULL AND schedule_open_local(sid, local::date, local::time);
END $$;

-- When schedule sid next opens or closes after at, looking up to 31 days
-- ahead (NULL: not in that time, e.g. no hours at all). Only the Office
-- hours page asks ("open, closes at 17:00").
CREATE FUNCTION schedule_changes_at(sid uuid, at timestamptz) RETURNS timestamptz
LANGUAGE plpgsql STABLE SET search_path = public, pg_temp AS $$
DECLARE
    tz    text := (SELECT time_zone FROM pbx_setting);
    local timestamp := at AT TIME ZONE tz;
    now_open boolean := schedule_open_local(sid, local::date, local::time);
    day   date;
    c     time;
BEGIN
    FOR i IN 0..31 LOOP
        day := local::date + i;
        FOR c IN
            SELECT x FROM (
                SELECT '00:00'::time AS x
                UNION SELECT s.opens FROM schedule_span s WHERE s.schedule_id = sid AND s.weekday = extract(dow FROM day)
                UNION SELECT s.closes FROM schedule_span s WHERE s.schedule_id = sid AND s.weekday = extract(dow FROM day)
                    AND s.closes <> '24:00'::time
            ) moments ORDER BY x
        LOOP
            IF day + c > local AND schedule_open_local(sid, day, c) <> now_open THEN
                RETURN (day + c) AT TIME ZONE tz;
            END IF;
        END LOOP;
    END LOOP;
    RETURN NULL;
END $$;

-- A message can now also be "We're closed" (a ring group's unanswered
-- calls too).
ALTER TABLE ring_group DROP CONSTRAINT ring_group_no_answer_message_check;
ALTER TABLE ring_group ADD CONSTRAINT ring_group_no_answer_message_check
    CHECK (no_answer_message IN ('not-available', 'closed'));

-- A number (or a line's calls for none of its numbers) can ring a ring
-- group: next to the extension it already could ring, never both.
ALTER TABLE trunk_did ADD COLUMN ring_group_id uuid REFERENCES ring_group (id) ON DELETE RESTRICT;
ALTER TABLE trunk_did ADD CONSTRAINT trunk_did_rings_one CHECK (extension_id IS NULL OR ring_group_id IS NULL);
ALTER TABLE trunk ADD COLUMN rings_ring_group_id uuid REFERENCES ring_group (id) ON DELETE RESTRICT;
ALTER TABLE trunk ADD CONSTRAINT trunk_rings_one CHECK (rings_extension_id IS NULL OR rings_ring_group_id IS NULL);

-- "When someone calls" for one number (did_id) or a line's other calls
-- (trunk_id): what happens when nobody answers the person it rings, and
-- outside office hours. Who it rings during office hours stays on the
-- number or line (above). No row: it rings the same all the time, a
-- person for 30 seconds, then "not available" (as before this step).
CREATE TABLE incoming_rule (
    tenant_id  uuid NOT NULL REFERENCES tenant (id),
    did_id     uuid UNIQUE REFERENCES trunk_did (id) ON DELETE CASCADE,
    trunk_id   uuid UNIQUE REFERENCES trunk (id) ON DELETE CASCADE,
    -- When a person is rung: how long, and where the call goes then (a
    -- ring group decides that itself).
    no_answer_seconds       smallint NOT NULL DEFAULT 25 CHECK (no_answer_seconds BETWEEN 5 AND 300),
    no_answer_kind          text NOT NULL DEFAULT 'message' CHECK (no_answer_kind IN ('extension', 'ring_group', 'message')),
    no_answer_extension_id  uuid REFERENCES extension (id),
    no_answer_ring_group_id uuid REFERENCES ring_group (id) ON DELETE RESTRICT,
    no_answer_message       text CHECK (no_answer_message IN ('not-available', 'closed')),
    -- NULL: the same all the time (no office hours).
    schedule_id             uuid REFERENCES schedule (id) ON DELETE RESTRICT,
    -- Outside office hours, and on holidays unless holiday_kind says
    -- otherwise.
    closed_kind             text NOT NULL DEFAULT 'message' CHECK (closed_kind IN ('extension', 'ring_group', 'message')),
    closed_extension_id     uuid REFERENCES extension (id),
    closed_ring_group_id    uuid REFERENCES ring_group (id) ON DELETE RESTRICT,
    closed_message          text DEFAULT 'closed' CHECK (closed_message IN ('not-available', 'closed')),
    holiday_kind            text CHECK (holiday_kind IN ('extension', 'ring_group', 'message')),
    holiday_extension_id    uuid REFERENCES extension (id),
    holiday_ring_group_id   uuid REFERENCES ring_group (id) ON DELETE RESTRICT,
    holiday_message         text CHECK (holiday_message IN ('not-available', 'closed')),
    updated_at timestamptz NOT NULL,
    CHECK ((did_id IS NULL) <> (trunk_id IS NULL)),
    CHECK ((no_answer_kind = 'extension') = (no_answer_extension_id IS NOT NULL)),
    CHECK ((no_answer_kind = 'ring_group') = (no_answer_ring_group_id IS NOT NULL)),
    CHECK ((no_answer_kind = 'message') = (no_answer_message IS NOT NULL)),
    CHECK ((closed_kind = 'extension') = (closed_extension_id IS NOT NULL)),
    CHECK ((closed_kind = 'ring_group') = (closed_ring_group_id IS NOT NULL)),
    CHECK ((closed_kind = 'message') = (closed_message IS NOT NULL)),
    CHECK ((holiday_kind IS NOT DISTINCT FROM 'extension') = (holiday_extension_id IS NOT NULL)),
    CHECK ((holiday_kind IS NOT DISTINCT FROM 'ring_group') = (holiday_ring_group_id IS NOT NULL)),
    CHECK ((holiday_kind IS NOT DISTINCT FROM 'message') = (holiday_message IS NOT NULL))
);

CREATE INDEX incoming_rule_tenant_idx ON incoming_rule (tenant_id);

-- A destination's columns as linx_route's short text: an extension that's
-- gone or off becomes the "not available" message.
CREATE FUNCTION routing_dest(kind text, ext uuid, grp uuid, message text) RETURNS text
LANGUAGE sql STABLE SET search_path = public, pg_temp AS $$
    SELECT CASE kind
        WHEN 'ring_group' THEN 'g:' || grp::text
        WHEN 'extension' THEN coalesce((SELECT 'e:' || e.number FROM extension e
            WHERE e.id = ext AND e.enabled AND e.deleted_at IS NULL), 'm:not-available')
        ELSE 'm:' || message
    END
$$;

-- The office-hours schedule a business gets at setup (the settings save
-- that makes it a business calls this; migration below for existing
-- ones): Monday to Friday, 08:00-17:00. Nothing if it has one already.
CREATE FUNCTION schedule_add_default(tenant uuid, at timestamptz) RETURNS void
LANGUAGE plpgsql SET search_path = public, pg_temp AS $$
DECLARE
    sid uuid := gen_random_uuid();
BEGIN
    IF EXISTS (SELECT 1 FROM schedule s WHERE s.tenant_id = tenant) THEN
        RETURN;
    END IF;
    INSERT INTO schedule (id, tenant_id, name, created_at, updated_at) VALUES (sid, tenant, 'Office hours', at, at);
    INSERT INTO schedule_span (schedule_id, weekday, opens, closes)
        SELECT sid, d, '08:00', '17:00' FROM generate_series(1, 5) d;
END $$;

SELECT schedule_add_default(t.id, now()) FROM tenant t, pbx_setting s WHERE s.site_kind = 'business';

-- One step of a call's way through Linx (migration 0032), now at a given
-- moment, so the simulator can ask "Friday at 20:00". Two more places:
--   d:<id>  a call to one of your numbers (trunk_did)
--   l:<id>  a line's call for none of its numbers (trunk)
-- Each looks at the number's rule and schedule at that moment, and goes
-- on (action 'next', not counted) to whoever it rings, or rings its person
-- itself when the rule says how long. label is then 'always', 'open',
-- 'closed' or 'holiday' (the simulator says it; the dialplan only uses
-- label when it dials). Messages: "not available", "not in use", "We're
-- closed".
CREATE FUNCTION linx_route_at(dest text, caller text, at timestamptz)
RETURNS TABLE (action text, targets text, seconds integer, next text, counts integer, label text)
LANGUAGE plpgsql STABLE SET search_path = public, pg_temp AS $$
DECLARE
    kind text := split_part(dest, ':', 1);
    arg  text := split_part(dest, ':', 2);
    turn text := split_part(dest, ':', 3);
    g    ring_group;
    r    incoming_rule;
    t    text;
    n    integer;
    pos  integer;
    no_answer text;
    rings_ext uuid;
    rings_grp uuid;
    state text := 'always';
    open boolean;
    holiday text;
BEGIN
    IF kind IN ('d', 'l') THEN
        IF arg !~ '^[0-9a-f-]{36}$' THEN
            RETURN;
        END IF;
        IF kind = 'd' THEN
            SELECT d.extension_id, d.ring_group_id INTO rings_ext, rings_grp
            FROM trunk_did d JOIN trunk k ON k.id = d.trunk_id WHERE d.id = arg::uuid AND k.enabled;
            IF NOT FOUND THEN
                RETURN;
            END IF;
            SELECT * INTO r FROM incoming_rule i WHERE i.did_id = arg::uuid;
        ELSE
            SELECT k.rings_extension_id, k.rings_ring_group_id INTO rings_ext, rings_grp
            FROM trunk k WHERE k.id = arg::uuid AND k.enabled;
            IF NOT FOUND THEN
                RETURN;
            END IF;
            SELECT * INTO r FROM incoming_rule i WHERE i.trunk_id = arg::uuid;
        END IF;
        IF r.schedule_id IS NOT NULL THEN
            SELECT o.open, o.holiday INTO open, holiday FROM schedule_open_at(r.schedule_id, at) o;
            IF holiday IS NOT NULL THEN
                RETURN QUERY SELECT 'next', '', 0, CASE WHEN r.holiday_kind IS NULL
                    THEN routing_dest(r.closed_kind, r.closed_extension_id, r.closed_ring_group_id, r.closed_message)
                    ELSE routing_dest(r.holiday_kind, r.holiday_extension_id, r.holiday_ring_group_id, r.holiday_message) END,
                    0, 'holiday';
                RETURN;
            ELSIF NOT open THEN
                RETURN QUERY SELECT 'next', '', 0,
                    routing_dest(r.closed_kind, r.closed_extension_id, r.closed_ring_group_id, r.closed_message), 0, 'closed';
                RETURN;
            END IF;
            state := 'open';
        END IF;
        IF rings_grp IS NOT NULL THEN
            RETURN QUERY SELECT 'next', '', 0, 'g:' || rings_grp::text, 0, state;
            RETURN;
        END IF;
        SELECT e.number INTO t FROM extension e WHERE e.id = rings_ext AND e.enabled AND e.deleted_at IS NULL;
        IF t IS NULL THEN
            RETURN QUERY SELECT 'next', '', 0, 'm:not-in-use', 0, state;
            RETURN;
        END IF;
        IF r.did_id IS NULL AND r.trunk_id IS NULL THEN
            -- No rule: the person, as an extension rings (30 s, then "not available").
            RETURN QUERY SELECT 'next', '', 0, 'e:' || t, 0, state;
            RETURN;
        END IF;
        arg := t;
        no_answer := routing_dest(r.no_answer_kind, r.no_answer_extension_id, r.no_answer_ring_group_id, r.no_answer_message);
        SELECT coalesce(string_agg('PJSIP/' || x.aor, '&' ORDER BY x.aor), '') INTO t
            FROM asterisk.linx_ring_targets x WHERE x.number = arg;
        RETURN QUERY SELECT CASE WHEN t = '' THEN 'next' ELSE 'dial' END, t, r.no_answer_seconds::integer, no_answer, 1, arg;
        RETURN;
    END IF;

    IF kind = 'n' THEN
        IF arg !~ '^[0-9]{2,6}$' THEN
            RETURN;
        END IF;
        IF EXISTS (SELECT 1 FROM extension e WHERE e.number = arg AND e.enabled AND e.deleted_at IS NULL) THEN
            kind := 'e';
        ELSE
            SELECT x.id::text INTO arg FROM ring_group x WHERE x.number = arg;
            IF NOT FOUND THEN
                RETURN;
            END IF;
            kind := 'g';
            turn := '';
        END IF;
    END IF;

    IF kind = 'e' THEN
        SELECT count(*), coalesce(string_agg('PJSIP/' || x.aor, '&' ORDER BY x.aor), '')
            INTO n, t FROM asterisk.linx_ring_targets x WHERE x.number = arg;
        IF n = 0 THEN
            RETURN;
        END IF;
        RETURN QUERY SELECT CASE WHEN t = '' THEN 'next' ELSE 'dial' END, t, 30, 'm:not-available', 1, arg;
        RETURN;
    END IF;

    IF kind = 'm' THEN
        IF arg IN ('not-available', 'not-in-use', 'closed') THEN
            RETURN QUERY SELECT 'message', arg, 0, '', 0, '';
        END IF;
        RETURN;
    END IF;

    IF kind <> 'g' OR arg !~ '^[0-9a-f-]{36}$' OR turn !~ '^[0-9]{0,2}$' THEN
        RETURN;
    END IF;
    SELECT * INTO g FROM ring_group x WHERE x.id = arg::uuid;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    no_answer := routing_dest(g.no_answer_kind, g.no_answer_extension_id, g.no_answer_ring_group_id, g.no_answer_message);

    IF g.strategy = 'all' THEN
        SELECT string_agg('PJSIP/' || x.aor, '&' ORDER BY x.aor) INTO t
        FROM ring_group_member m
        JOIN extension e ON e.id = m.extension_id
        JOIN asterisk.linx_ring_targets x ON x.number = e.number AND x.aor IS NOT NULL
        WHERE m.ring_group_id = g.id AND e.number <> caller;
        RETURN QUERY SELECT CASE WHEN t IS NULL THEN 'next' ELSE 'dial' END, coalesce(t, ''),
            g.ring_seconds::integer, no_answer, 1, coalesce(g.number, '');
        RETURN;
    END IF;

    -- One after another: the next member after this turn who can ring.
    SELECT m.position, string_agg('PJSIP/' || x.aor, '&' ORDER BY x.aor) INTO pos, t
    FROM ring_group_member m
    JOIN extension e ON e.id = m.extension_id
    JOIN asterisk.linx_ring_targets x ON x.number = e.number AND x.aor IS NOT NULL
    WHERE m.ring_group_id = g.id AND m.position > coalesce(nullif(turn, '')::integer, 0) AND e.number <> caller
    GROUP BY m.position ORDER BY m.position LIMIT 1;
    IF pos IS NULL THEN
        RETURN QUERY SELECT 'next', '', 0, no_answer, 1, coalesce(g.number, '');
    ELSE
        RETURN QUERY SELECT 'dial', t, g.turn_seconds::integer, 'g:' || g.id::text || ':' || pos::text, 0, coalesce(g.number, '');
    END IF;
END $$;
REVOKE ALL ON FUNCTION linx_route_at(text, text, timestamptz) FROM PUBLIC;

-- What the dialplan asks: the same, now.
CREATE OR REPLACE FUNCTION asterisk.linx_route(dest text, caller text)
RETURNS TABLE (action text, targets text, seconds integer, next text, counts integer, label text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT * FROM linx_route_at(dest, caller, now())
$$;

-- A call from a trunk to one of its numbers now starts at that number
-- (d:<id>), which knows its rule; no row still means the trunk doesn't own
-- the number (ADR-048). Same signature, so the dialplan's lookup is
-- unchanged; only what it does with the answer is (DEST is the answer).
CREATE OR REPLACE FUNCTION asterisk.linx_inbound(endpoint text, dialled text) RETURNS SETOF text
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    WITH n AS (
        SELECT (numbering_classify(s.country, dialled)).e164 AS e164,
               (numbering_classify(s.country, '+' || ltrim(dialled, '+'))).e164 AS plus, s.country
        FROM pbx_setting s
    )
    SELECT 'd:' || d.id::text
    FROM trunk_did d JOIN trunk t ON t.id = d.trunk_id, n
    WHERE 'trunk-' || t.id::text = endpoint AND t.enabled AND dialled ~ '^\+?[0-9]{2,20}$'
      AND (d.number = dialled
           OR (numbering_classify(n.country, d.number)).e164 IN (n.e164, n.plus))
    ORDER BY d.number = dialled DESC, d.number
    LIMIT 1
$$;

-- A line's call for none of its numbers starts at the line (l:<id>) when
-- the line sends such calls anywhere; no row: "not in use", as before.
CREATE OR REPLACE FUNCTION asterisk.linx_line_rings(endpoint text) RETURNS SETOF text
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT 'l:' || t.id::text
    FROM trunk t
    WHERE 'trunk-' || t.id::text = endpoint AND t.enabled
      AND (t.rings_ring_group_id IS NOT NULL
           OR EXISTS (SELECT 1 FROM incoming_rule i WHERE i.trunk_id = t.id)
           OR EXISTS (SELECT 1 FROM extension e WHERE e.id = t.rings_extension_id AND e.enabled AND e.deleted_at IS NULL))
$$;
