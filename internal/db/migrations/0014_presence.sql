-- A person's chosen status (docs/ui/WEB_SCREENS_PHASE1C.md §2, "Set
-- status"): Available, Away or Do not disturb. The Team list shows it
-- whenever the person isn't on a call or ringing. "Do not disturb" also
-- stops their extension ringing: callers hear "not available", exactly as
-- if no phone were signed in.
ALTER TABLE app_user ADD COLUMN presence text NOT NULL DEFAULT 'available'
    CHECK (presence IN ('available', 'away', 'dnd'));

-- Same columns as migration 0013's. An extension rings nothing while every
-- enabled person on it has chosen Do not disturb (an extension with nobody
-- on it, like a desk phone's, rings as before).
CREATE OR REPLACE VIEW asterisk.linx_ring_targets AS
SELECT
    e.number       AS number,
    l.sip_username AS aor
FROM extension e
LEFT JOIN device_live l ON l.extension_id = e.id
    AND NOT (
        EXISTS (SELECT 1 FROM app_user u WHERE u.extension_id = e.id AND u.disabled_at IS NULL)
        AND NOT EXISTS (SELECT 1 FROM app_user u WHERE u.extension_id = e.id AND u.disabled_at IS NULL AND u.presence <> 'dnd'))
WHERE e.enabled AND e.deleted_at IS NULL;
