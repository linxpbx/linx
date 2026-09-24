-- Revoking a device is permanent (Phase 1B security review, docs/THREAT_MODEL.md).
-- Before this, revoking only cleared enabled, so PATCH {"enabled": true}
-- brought a revoked device back with its old password: a lost phone that
-- still holds it could sign in again. revoked_at marks the device for good,
-- and the check below makes the database itself refuse to turn a revoked
-- device back on. The realtime views already require enabled, so they need
-- no change.
ALTER TABLE device ADD COLUMN revoked_at timestamptz;

-- Devices already revoked: those of deleted extensions, and those an
-- audited DELETE /devices/{id} turned off.
UPDATE device d SET revoked_at = e.deleted_at
FROM extension e
WHERE e.id = d.extension_id AND e.deleted_at IS NOT NULL AND NOT d.enabled;

UPDATE device d SET revoked_at = d.updated_at
WHERE d.revoked_at IS NULL AND NOT d.enabled AND EXISTS (
    SELECT 1 FROM audit_log a
    WHERE a.action = 'device.revoke' AND a.target = 'device:' || d.id::text AND a.result = 'ok');

ALTER TABLE device ADD CONSTRAINT device_revoked_is_disabled CHECK (revoked_at IS NULL OR NOT enabled);
