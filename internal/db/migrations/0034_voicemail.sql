-- Voicemail (ADR-069, docs/PHASE1F.md §8, Phase 1F step 13). Asterisk
-- records a message into a folder only it and the control plane share;
-- the control plane checks it and keeps it here, so backups include
-- voicemail with no change. Asterisk still only reads: asterisk.linx_route
-- says when a call goes to a box.

-- One box per person (extension) and per ring group, made with it. A box's
-- id is its owner's, so "voicemail for Sara" needs no lookup. On by
-- default; a person's emails them new messages unless they turn it off. A
-- removed person's box stays with its messages until they expire; a ring
-- group's goes with the group.
CREATE TABLE voicemail_box (
    id            uuid PRIMARY KEY,
    tenant_id     uuid NOT NULL REFERENCES tenant (id),
    extension_id  uuid UNIQUE REFERENCES extension (id),
    ring_group_id uuid UNIQUE REFERENCES ring_group (id) ON DELETE CASCADE,
    enabled       boolean NOT NULL DEFAULT true,
    email         boolean NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL,
    CHECK ((extension_id IS NULL) <> (ring_group_id IS NULL)),
    CHECK (id = coalesce(extension_id, ring_group_id))
);

CREATE INDEX voicemail_box_tenant_idx ON voicemail_box (tenant_id);

CREATE FUNCTION voicemail_box_add() RETURNS trigger
LANGUAGE plpgsql SET search_path = public, pg_temp AS $$
BEGIN
    IF TG_TABLE_NAME = 'extension' THEN
        INSERT INTO voicemail_box (id, tenant_id, extension_id, created_at) VALUES (NEW.id, NEW.tenant_id, NEW.id, now());
    ELSE
        -- Nobody owns a group's address: its messages aren't emailed
        -- (only a box owner's own address gets the audio, §12).
        INSERT INTO voicemail_box (id, tenant_id, ring_group_id, email, created_at) VALUES (NEW.id, NEW.tenant_id, NEW.id, false, now());
    END IF;
    RETURN NULL;
END $$;

CREATE TRIGGER extension_voicemail_box AFTER INSERT ON extension
    FOR EACH ROW EXECUTE FUNCTION voicemail_box_add();
CREATE TRIGGER ring_group_voicemail_box AFTER INSERT ON ring_group
    FOR EACH ROW EXECUTE FUNCTION voicemail_box_add();

INSERT INTO voicemail_box (id, tenant_id, extension_id, created_at)
    SELECT e.id, e.tenant_id, e.id, now() FROM extension e;
INSERT INTO voicemail_box (id, tenant_id, ring_group_id, email, created_at)
    SELECT g.id, g.tenant_id, g.id, false, now() FROM ring_group g;

-- One message. source is the recording's name in the shared folder
-- (Asterisk's call id), so a message the control plane stored but couldn't
-- delete yet (a restart) isn't stored twice. The audio is 8 kHz G.711
-- mu-law as Asterisk recorded it (8 KB a second); the web app and emails get a
-- WAV made from it. It doesn't compress, so Postgres isn't asked to try.
-- caller_extension_id: the calling person, for a call from a phone (their
-- name is shown from it); otherwise the line's caller number and name,
-- already cleaned by the dialplan, shown as text only.
CREATE TABLE voicemail_message (
    id                  uuid PRIMARY KEY,
    tenant_id           uuid NOT NULL REFERENCES tenant (id),
    box_id              uuid NOT NULL REFERENCES voicemail_box (id) ON DELETE CASCADE,
    source              text NOT NULL UNIQUE CHECK (source ~ '^[0-9]{1,12}\.[0-9]{1,10}$'),
    caller_number       text NOT NULL DEFAULT '' CHECK (caller_number ~ '^\+?[0-9]{0,20}$'),
    caller_name         text NOT NULL DEFAULT '' CHECK (length(caller_name) <= 100),
    caller_extension_id uuid REFERENCES extension (id),
    received_at         timestamptz NOT NULL,
    duration_ms         integer NOT NULL CHECK (duration_ms BETWEEN 1 AND 200000),
    audio               bytea NOT NULL,
    heard_at            timestamptz,
    heard_by            uuid REFERENCES app_user (id) ON DELETE SET NULL,
    created_at          timestamptz NOT NULL
);
ALTER TABLE voicemail_message ALTER COLUMN audio SET STORAGE EXTERNAL;

CREATE INDEX voicemail_message_box_idx ON voicemail_message (box_id, received_at DESC);
CREATE INDEX voicemail_message_tenant_idx ON voicemail_message (tenant_id, received_at);

-- A voicemail box is now a place a call can go (ADR-068's list):
-- "voicemail for Sara", "voicemail for Sales". Its own column, so the
-- extension and ring group columns keep meaning "ring them".
ALTER TABLE ring_group ADD COLUMN no_answer_voicemail_id uuid REFERENCES voicemail_box (id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE ring_group DROP CONSTRAINT ring_group_no_answer_kind_check;
ALTER TABLE ring_group ADD CONSTRAINT ring_group_no_answer_kind_check
    CHECK (no_answer_kind IN ('extension', 'ring_group', 'voicemail', 'message'));
ALTER TABLE ring_group ADD CHECK ((no_answer_kind = 'voicemail') = (no_answer_voicemail_id IS NOT NULL));

ALTER TABLE incoming_rule ADD COLUMN no_answer_voicemail_id uuid REFERENCES voicemail_box (id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE incoming_rule ADD COLUMN closed_voicemail_id uuid REFERENCES voicemail_box (id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE incoming_rule ADD COLUMN holiday_voicemail_id uuid REFERENCES voicemail_box (id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE incoming_rule DROP CONSTRAINT incoming_rule_no_answer_kind_check;
ALTER TABLE incoming_rule DROP CONSTRAINT incoming_rule_closed_kind_check;
ALTER TABLE incoming_rule DROP CONSTRAINT incoming_rule_holiday_kind_check;
ALTER TABLE incoming_rule ADD CONSTRAINT incoming_rule_no_answer_kind_check
    CHECK (no_answer_kind IN ('extension', 'ring_group', 'voicemail', 'message'));
ALTER TABLE incoming_rule ADD CONSTRAINT incoming_rule_closed_kind_check
    CHECK (closed_kind IN ('extension', 'ring_group', 'voicemail', 'message'));
ALTER TABLE incoming_rule ADD CONSTRAINT incoming_rule_holiday_kind_check
    CHECK (holiday_kind IN ('extension', 'ring_group', 'voicemail', 'message'));
ALTER TABLE incoming_rule ADD CHECK ((no_answer_kind = 'voicemail') = (no_answer_voicemail_id IS NOT NULL));
ALTER TABLE incoming_rule ADD CHECK ((closed_kind = 'voicemail') = (closed_voicemail_id IS NOT NULL));
ALTER TABLE incoming_rule ADD CHECK ((holiday_kind IS NOT DISTINCT FROM 'voicemail') = (holiday_voicemail_id IS NOT NULL));

CREATE INDEX ring_group_no_answer_voicemail_idx ON ring_group (no_answer_voicemail_id) WHERE no_answer_voicemail_id IS NOT NULL;

-- A box as linx_route's short text: v:<box id>, or the "not available"
-- message when the box is off or its person is gone or off.
CREATE FUNCTION voicemail_dest(box uuid) RETURNS text
LANGUAGE sql STABLE SET search_path = public, pg_temp AS $$
    SELECT coalesce((SELECT 'v:' || b.id::text FROM voicemail_box b
        LEFT JOIN extension e ON e.id = b.extension_id
        WHERE b.id = box AND b.enabled
          AND (b.ring_group_id IS NOT NULL OR (e.enabled AND e.deleted_at IS NULL))), 'm:not-available')
$$;

-- A destination's columns as linx_route's short text (migration 0033's,
-- with a box).
DROP FUNCTION routing_dest(text, uuid, uuid, text);
CREATE FUNCTION routing_dest(kind text, ext uuid, grp uuid, vm uuid, message text) RETURNS text
LANGUAGE sql STABLE SET search_path = public, pg_temp AS $$
    SELECT CASE kind
        WHEN 'ring_group' THEN 'g:' || grp::text
        WHEN 'voicemail' THEN voicemail_dest(vm)
        WHEN 'extension' THEN coalesce((SELECT 'e:' || e.number FROM extension e
            WHERE e.id = ext AND e.enabled AND e.deleted_at IS NULL), 'm:not-available')
        ELSE 'm:' || message
    END
$$;

-- Outside office hours and on holidays a box plays the "we're closed"
-- greeting (v:<id>:closed).
CREATE FUNCTION closed_dest(dest text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN dest LIKE 'v:%' THEN dest || ':closed' ELSE dest END
$$;

-- One step of a call's way through Linx (migrations 0032 and 0033), with
-- one more place:
--   v:<id>[:closed]  a voicemail box: action 'voicemail', targets the box,
--                    label the greeting ('unavailable' or 'closed')
-- and a person nobody answers (or who can't ring) now goes to their own
-- box when it's on, instead of "not available".
CREATE OR REPLACE FUNCTION linx_route_at(dest text, caller text, at timestamptz)
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
                RETURN QUERY SELECT 'next', '', 0, closed_dest(CASE WHEN r.holiday_kind IS NULL
                    THEN routing_dest(r.closed_kind, r.closed_extension_id, r.closed_ring_group_id, r.closed_voicemail_id, r.closed_message)
                    ELSE routing_dest(r.holiday_kind, r.holiday_extension_id, r.holiday_ring_group_id, r.holiday_voicemail_id, r.holiday_message) END),
                    0, 'holiday';
                RETURN;
            ELSIF NOT open THEN
                RETURN QUERY SELECT 'next', '', 0, closed_dest(
                    routing_dest(r.closed_kind, r.closed_extension_id, r.closed_ring_group_id, r.closed_voicemail_id, r.closed_message)), 0, 'closed';
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
            -- No rule: the person, as an extension rings (30 s, then their
            -- voicemail).
            RETURN QUERY SELECT 'next', '', 0, 'e:' || t, 0, state;
            RETURN;
        END IF;
        arg := t;
        no_answer := routing_dest(r.no_answer_kind, r.no_answer_extension_id, r.no_answer_ring_group_id, r.no_answer_voicemail_id, r.no_answer_message);
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
        SELECT voicemail_dest(e.id) INTO no_answer FROM extension e
            WHERE e.number = arg AND e.enabled AND e.deleted_at IS NULL;
        RETURN QUERY SELECT CASE WHEN t = '' THEN 'next' ELSE 'dial' END, t, 30,
            coalesce(no_answer, 'm:not-available'), 1, arg;
        RETURN;
    END IF;

    IF kind = 'v' THEN
        IF arg !~ '^[0-9a-f-]{36}$' OR turn NOT IN ('', 'closed') THEN
            RETURN;
        END IF;
        t := voicemail_dest(arg::uuid);
        IF t LIKE 'm:%' THEN
            RETURN QUERY SELECT 'next', '', 0, t, 0, '';
        ELSE
            RETURN QUERY SELECT 'voicemail', arg, 0, '', 0, CASE WHEN turn = 'closed' THEN 'closed' ELSE 'unavailable' END;
        END IF;
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
    no_answer := routing_dest(g.no_answer_kind, g.no_answer_extension_id, g.no_answer_ring_group_id, g.no_answer_voicemail_id, g.no_answer_message);

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
