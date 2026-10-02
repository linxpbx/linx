-- Ring groups and where a call can go (ADR-068, docs/PHASE1F.md §6,
-- Phase 1F step 11). The control plane owns these tables; Asterisk only
-- calls asterisk.linx_route, one step of a call at a time.

-- A ring group rings several extensions for one call: all at once, or one
-- after another. Its own number, if it has one, comes from the numbering
-- plan's groups range (checked by the API) and is never an extension's
-- (checked here, both ways).
CREATE TABLE ring_group (
    id           uuid PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenant (id),
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    number       text CHECK (number IS NULL OR number ~ '^[0-9]{2,6}$'),
    strategy     text NOT NULL DEFAULT 'all' CHECK (strategy IN ('all', 'in_turn')),
    -- All at once: how long everyone rings before "if nobody answers".
    ring_seconds smallint NOT NULL DEFAULT 25 CHECK (ring_seconds BETWEEN 5 AND 300),
    -- One after another: how long each person rings.
    turn_seconds smallint NOT NULL DEFAULT 15 CHECK (turn_seconds BETWEEN 5 AND 120),
    -- If nobody answers: one destination (a person, another group, or a
    -- message and hang up; a voicemail box comes with Phase 1F step 13).
    -- A group it points at can't be removed while it does (the API asks
    -- where those calls go instead); an extension being removed sends them
    -- to the message instead (internal/store DeleteExtension).
    no_answer_kind          text NOT NULL DEFAULT 'message' CHECK (no_answer_kind IN ('extension', 'ring_group', 'message')),
    no_answer_extension_id  uuid REFERENCES extension (id),
    no_answer_ring_group_id uuid REFERENCES ring_group (id) ON DELETE RESTRICT,
    no_answer_message       text CHECK (no_answer_message IN ('not-available')),
    version      integer NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    CHECK ((no_answer_kind = 'extension') = (no_answer_extension_id IS NOT NULL)),
    CHECK ((no_answer_kind = 'ring_group') = (no_answer_ring_group_id IS NOT NULL)),
    CHECK ((no_answer_kind = 'message') = (no_answer_message IS NOT NULL)),
    CHECK (no_answer_ring_group_id IS DISTINCT FROM id),
    UNIQUE (tenant_id, name)
);

CREATE UNIQUE INDEX ring_group_tenant_number_idx ON ring_group (tenant_id, number) WHERE number IS NOT NULL;
CREATE INDEX ring_group_tenant_idx ON ring_group (tenant_id, id DESC);

-- The people in a group, in the order "one after another" rings them.
CREATE TABLE ring_group_member (
    ring_group_id uuid NOT NULL REFERENCES ring_group (id) ON DELETE CASCADE,
    extension_id  uuid NOT NULL REFERENCES extension (id),
    position      smallint NOT NULL CHECK (position BETWEEN 1 AND 50),
    PRIMARY KEY (ring_group_id, extension_id),
    UNIQUE (ring_group_id, position)
);

CREATE INDEX ring_group_member_extension_idx ON ring_group_member (extension_id);

-- An extension and a ring group never share a number. Both sides take the
-- same per-tenant lock first, so two saves at once can't both pass.
CREATE FUNCTION extension_number_shared_check() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.deleted_at IS NOT NULL THEN
        RETURN NEW;
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('linx-number:' || NEW.tenant_id::text, 0));
    IF EXISTS (SELECT 1 FROM ring_group g WHERE g.tenant_id = NEW.tenant_id AND g.number = NEW.number) THEN
        RAISE EXCEPTION 'number % is a ring group''s', NEW.number
            USING ERRCODE = 'unique_violation', CONSTRAINT = 'number_used_by_ring_group';
    END IF;
    RETURN NEW;
END $$;

CREATE FUNCTION ring_group_number_check() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    home text := (SELECT country FROM pbx_setting);
    reason text;
BEGIN
    IF NEW.number IS NULL THEN
        RETURN NEW;
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('linx-number:' || NEW.tenant_id::text, 0));
    IF EXISTS (SELECT 1 FROM extension e WHERE e.tenant_id = NEW.tenant_id AND e.number = NEW.number AND e.deleted_at IS NULL) THEN
        RAISE EXCEPTION 'number % is an extension''s', NEW.number
            USING ERRCODE = 'unique_violation', CONSTRAINT = 'number_used_by_extension';
    END IF;
    -- The same numbers an extension can't take (emergency, a dialling
    -- prefix), so a phone dialling it never reaches the outside instead.
    reason := numbering_extension_clash(home, NEW.number);
    IF reason IS NOT NULL THEN
        RAISE EXCEPTION 'ring group number % is reserved (%)', NEW.number, reason
            USING ERRCODE = 'check_violation', CONSTRAINT = 'ring_group_number_reserved',
                  DETAIL = reason, HINT = home;
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER extension_number_shared BEFORE INSERT OR UPDATE OF number, deleted_at ON extension
    FOR EACH ROW EXECUTE FUNCTION extension_number_shared_check();
CREATE TRIGGER ring_group_number_shared BEFORE INSERT OR UPDATE OF number ON ring_group
    FOR EACH ROW EXECUTE FUNCTION ring_group_number_check();

-- One step of a call's way through Linx (ADR-068). The dialplan carries
-- where the call goes next as a short text:
--   n:<number>  a number dialled from a phone: an extension's or a ring group's
--   e:<number>  an extension
--   g:<id>      a ring group; g:<id>:<position> after that member's turn
--   m:<name>    a message, then hang up (linx-messages,<name>)
-- caller is the calling extension's number ('' for a call from a line): a
-- group never rings the person calling it.
--
-- One row: action 'dial' (ring targets, a Dial() string, for seconds),
-- 'next' (nobody can ring here: go straight on) or 'message' (targets is
-- its name); next is where the call goes if nobody answers; counts is 1
-- when next is a new place (the dialplan stops a call after 10 of those,
-- so a loop the API missed still ends); label is the number being rung,
-- for the control plane's call events. No row: no such place.
--
-- Device usernames match ^d_[A-Za-z0-9]{8}$ (migration 0005) and message
-- names come from a fixed list, so nothing here can smuggle dial options
-- or another context into the dialplan.
CREATE FUNCTION asterisk.linx_route(dest text, caller text)
RETURNS TABLE (action text, targets text, seconds integer, next text, counts integer, label text)
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
    kind text := split_part(dest, ':', 1);
    arg  text := split_part(dest, ':', 2);
    turn text := split_part(dest, ':', 3);
    g    ring_group;
    t    text;
    n    integer;
    pos  integer;
    no_answer text;
BEGIN
    IF kind = 'n' THEN
        IF arg !~ '^[0-9]{2,6}$' THEN
            RETURN;
        END IF;
        IF EXISTS (SELECT 1 FROM extension e WHERE e.number = arg AND e.enabled AND e.deleted_at IS NULL) THEN
            kind := 'e';
        ELSE
            SELECT r.id::text INTO arg FROM ring_group r WHERE r.number = arg;
            IF NOT FOUND THEN
                RETURN;
            END IF;
            kind := 'g';
            turn := '';
        END IF;
    END IF;

    IF kind = 'e' THEN
        SELECT count(*), coalesce(string_agg('PJSIP/' || r.aor, '&' ORDER BY r.aor), '')
            INTO n, t FROM asterisk.linx_ring_targets r WHERE r.number = arg;
        IF n = 0 THEN
            RETURN;
        END IF;
        RETURN QUERY SELECT CASE WHEN t = '' THEN 'next' ELSE 'dial' END, t, 30, 'm:not-available', 1, arg;
        RETURN;
    END IF;

    IF kind = 'm' THEN
        IF arg IN ('not-available') THEN
            RETURN QUERY SELECT 'message', arg, 0, '', 0, '';
        END IF;
        RETURN;
    END IF;

    IF kind <> 'g' OR arg !~ '^[0-9a-f-]{36}$' OR turn !~ '^[0-9]{0,2}$' THEN
        RETURN;
    END IF;
    SELECT * INTO g FROM ring_group r WHERE r.id = arg::uuid;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    no_answer := CASE g.no_answer_kind
        WHEN 'ring_group' THEN 'g:' || g.no_answer_ring_group_id::text
        WHEN 'extension' THEN coalesce((SELECT 'e:' || e.number FROM extension e
            WHERE e.id = g.no_answer_extension_id AND e.enabled AND e.deleted_at IS NULL), 'm:not-available')
        ELSE 'm:' || g.no_answer_message
    END;

    IF g.strategy = 'all' THEN
        SELECT string_agg('PJSIP/' || r.aor, '&' ORDER BY r.aor) INTO t
        FROM ring_group_member m
        JOIN extension e ON e.id = m.extension_id
        JOIN asterisk.linx_ring_targets r ON r.number = e.number AND r.aor IS NOT NULL
        WHERE m.ring_group_id = g.id AND e.number <> caller;
        RETURN QUERY SELECT CASE WHEN t IS NULL THEN 'next' ELSE 'dial' END, coalesce(t, ''),
            g.ring_seconds::integer, no_answer, 1, coalesce(g.number, '');
        RETURN;
    END IF;

    -- One after another: the next member after this turn who can ring.
    SELECT m.position, string_agg('PJSIP/' || r.aor, '&' ORDER BY r.aor) INTO pos, t
    FROM ring_group_member m
    JOIN extension e ON e.id = m.extension_id
    JOIN asterisk.linx_ring_targets r ON r.number = e.number AND r.aor IS NOT NULL
    WHERE m.ring_group_id = g.id AND m.position > coalesce(nullif(turn, '')::integer, 0) AND e.number <> caller
    GROUP BY m.position ORDER BY m.position LIMIT 1;
    IF pos IS NULL THEN
        RETURN QUERY SELECT 'next', '', 0, no_answer, 1, coalesce(g.number, '');
    ELSE
        RETURN QUERY SELECT 'dial', t, g.turn_seconds::integer, 'g:' || g.id::text || ':' || pos::text, 0, coalesce(g.number, '');
    END IF;
END $$;

REVOKE ALL ON FUNCTION asterisk.linx_route(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION asterisk.linx_route(text, text) TO linx_asterisk;
