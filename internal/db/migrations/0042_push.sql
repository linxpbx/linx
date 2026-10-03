-- Ringing a sleeping iPhone (ADR-074, docs/PHASE2.md §5, Phase 2 step 5).
--
-- An app that isn't connected can't be rung by SIP, so Linx asks Apple to
-- wake it: a VoIP push with the call's id and the caller's number, nothing
-- else. The call is held ringing for a few seconds meanwhile, so it is
-- still there when the phone arrives. The same gateway sends the quiet
-- notifications for a missed call and a new voicemail (owner, 2026-10-03).

-- Where to reach a phone's app, and whether its person allowed the quiet
-- notifications. A push token is not a secret by itself: nothing can be
-- sent with one without the Apple key below, and it is useless on any
-- other server. The app sends them after it is set up, and again whenever
-- Apple gives it new ones.
ALTER TABLE device_identity
    ADD COLUMN voip_token       text NOT NULL DEFAULT '' CHECK (voip_token ~ '^[0-9a-f]{0,200}$'),
    ADD COLUMN alert_token      text NOT NULL DEFAULT '' CHECK (alert_token ~ '^[0-9a-f]{0,200}$'),
    -- Which Apple to send to. A build signed for development is only
    -- reachable on the sandbox, so a token from one is useless on the
    -- other and the app says which it has.
    ADD COLUMN push_environment text NOT NULL DEFAULT ''
        CHECK (push_environment IN ('', 'sandbox', 'production')),
    ADD COLUMN push_updated_at  timestamptz,
    -- When Apple last refused a token as dead (410 Unregistered): the
    -- token is cleared then, and the app sends a new one next time it
    -- runs.
    ADD COLUMN push_dead_at     timestamptz;

-- The Apple key that lets this server send those pushes (docs/PHASE2.md §6
-- (a), the owner's own key on the owner's own server). There is no Linx
-- relay and nothing third-party in the middle.
CREATE TABLE push_settings (
    tenant_id   uuid PRIMARY KEY REFERENCES tenant (id),
    enabled     boolean NOT NULL DEFAULT false,
    -- From Apple: the ten characters of the team, the key's own id, and
    -- the app this key may push to.
    team_id     text NOT NULL DEFAULT '' CHECK (team_id ~ '^[A-Z0-9]{0,10}$'),
    key_id      text NOT NULL DEFAULT '' CHECK (key_id ~ '^[A-Z0-9]{0,10}$'),
    bundle_id   text NOT NULL DEFAULT '' CHECK (length(bundle_id) <= 155),
    environment text NOT NULL DEFAULT 'production' CHECK (environment IN ('production', 'sandbox')),
    -- The .p8 signing key, sealed with ADR-030's key, row id
    -- "push_settings:<tenant_id>". It never leaves the server, and the API
    -- never reads it back out.
    key_enc     bytea,
    -- How long a call waits for a woken phone before it rings the devices
    -- that are already there; 0 turns the waiting off.
    wait_ms     integer NOT NULL DEFAULT 6000 CHECK (wait_ms BETWEEN 0 AND 15000),
    version     integer NOT NULL DEFAULT 1,
    updated_at  timestamptz NOT NULL,
    CHECK (NOT enabled OR (team_id <> '' AND key_id <> '' AND bundle_id <> '' AND key_enc IS NOT NULL))
);

-- What the dialplan asks before it rings a step's phones (the LINX_WAKE
-- function, docs/PBX.md §4): of the devices about to be dialled, which are
-- app phones that can be woken, and how long to wait for them.
--
-- Only phones that can actually be woken are named — the app sent a push
-- token, the phone is still set up, and the Apple key is in place — so a
-- server without push never waits for anything. Like the other dialplan
-- functions: SECURITY DEFINER, and all linx_asterisk gains is this answer.
CREATE FUNCTION asterisk.linx_wake(targets text) RETURNS TABLE (aors text, wait_ms integer)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT
        coalesce(string_agg(l.sip_username, '&' ORDER BY l.sip_username), ''),
        coalesce((SELECT p.wait_ms FROM push_settings p WHERE p.enabled LIMIT 1), 0)
    FROM device_live l
    JOIN device_identity i ON i.device_id = l.id
    WHERE l.kind = 'ios'
      AND i.voip_token <> ''
      AND EXISTS (SELECT 1 FROM push_settings p WHERE p.enabled AND p.wait_ms > 0)
      AND l.sip_username IN (
          SELECT m[1] FROM regexp_matches(targets, '(d_[A-Za-z0-9]{8})', 'g') AS m)
$$;
REVOKE ALL ON FUNCTION asterisk.linx_wake(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION asterisk.linx_wake(text) TO linx_asterisk;
