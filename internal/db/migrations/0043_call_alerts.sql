-- Ringing a phone that may not use CallKit (ADR-078, docs/PHASE2.md §5,
-- Phase 2 step 6).
--
-- Apple does not allow CallKit in mainland China, and an app that takes a
-- VoIP push without reporting a call to CallKit is killed by the system
-- (docs/PHASE2.md §14 item 1). So an app there sends no VoIP token at all
-- and says instead that it can only be rung with an ordinary, time-
-- sensitive notification — the person taps it and the call is still held
-- ringing for them. The app rings with its own full-screen screen when it
-- is already open (ADR-042).
ALTER TABLE device_identity
    ADD COLUMN call_alerts boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN device_identity.call_alerts IS
    'This phone cannot be woken with a VoIP push (CallKit is not available where it is), so a call is announced with a time-sensitive notification instead.';

-- The dialplan's lookup gains those phones: a call now waits for a phone
-- that has either a VoIP token or, failing that, a notification token and
-- the flag above. Everything else about it is unchanged from 0042 —
-- SECURITY DEFINER, and all linx_asterisk gains is this one answer.
CREATE OR REPLACE FUNCTION asterisk.linx_wake(targets text) RETURNS TABLE (aors text, wait_ms integer)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT
        coalesce(string_agg(l.sip_username, '&' ORDER BY l.sip_username), ''),
        coalesce((SELECT p.wait_ms FROM push_settings p WHERE p.enabled LIMIT 1), 0)
    FROM device_live l
    JOIN device_identity i ON i.device_id = l.id
    WHERE l.kind = 'ios'
      AND (i.voip_token <> '' OR (i.call_alerts AND i.alert_token <> ''))
      AND EXISTS (SELECT 1 FROM push_settings p WHERE p.enabled AND p.wait_ms > 0)
      AND l.sip_username IN (
          SELECT m[1] FROM regexp_matches(targets, '(d_[A-Za-z0-9]{8})', 'g') AS m)
$$;
