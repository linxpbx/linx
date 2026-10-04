-- A phone is not a browser (owner, 2026-10-04).
--
-- The Team list showed a person as offline the moment their app stopped
-- running, which on an iPhone is a few seconds after they put it in their
-- pocket: iOS suspends the app and its line closes (docs/PHASE2.md §7).
-- But that person is perfectly reachable — ringing them wakes the phone
-- with a push and it rings within about two seconds. A closed browser
-- genuinely cannot take a call; a phone in a pocket can, and the list has
-- to say so, or nobody can tell who to ring.
--
-- So "reachable" replaces "signed in" for an app phone: one view says which
-- app phones a push can actually reach, and both the dialplan's wake step
-- and the Team list read it, so the two can never drift apart.

-- The app phones a push can reach right now: the phone is still set up and
-- its person still has that extension (device_live), it has told Linx where
-- Apple can reach it, and this server has an Apple key with a wait set —
-- without which the dialplan pushes to nothing at all.
CREATE VIEW device_wakeable AS
SELECT l.id, l.extension_id, l.sip_username
FROM device_live l
JOIN device_identity i ON i.device_id = l.id
WHERE l.kind = 'ios'
  AND (i.voip_token <> '' OR (i.call_alerts AND i.alert_token <> ''))
  AND EXISTS (SELECT 1 FROM push_settings p WHERE p.enabled AND p.wait_ms > 0);

COMMENT ON VIEW device_wakeable IS
    'App phones a push can reach: still set up, a push token sent, and an Apple key in place. The wake step rings them; the Team list counts their person as reachable.';

-- Migration 0043's lookup, now reading the view rather than repeating it.
-- Nothing about the answer changes.
CREATE OR REPLACE FUNCTION asterisk.linx_wake(targets text) RETURNS TABLE (aors text, wait_ms integer)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT
        coalesce(string_agg(w.sip_username, '&' ORDER BY w.sip_username), ''),
        coalesce((SELECT p.wait_ms FROM push_settings p WHERE p.enabled LIMIT 1), 0)
    FROM device_wakeable w
    WHERE w.sip_username IN (
        SELECT m[1] FROM regexp_matches(targets, '(d_[A-Za-z0-9]{8})', 'g') AS m)
$$;
