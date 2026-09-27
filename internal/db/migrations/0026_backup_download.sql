-- Download a backup file from System → Backups, and restore from one
-- uploaded in the setup wizard (docs/BACKUP.md §8 step 5). The files
-- themselves are in the control plane's transfer folder (compose volume
-- backup-transfer), never in the database.

-- One download per tenant at a time. linx-backup-agent makes the file (a
-- tar of the server's own, still-encrypted backup folder) and hands it and
-- the backup's password over; only the admin who asked may fetch either.
CREATE TABLE backup_download (
    tenant_id     uuid PRIMARY KEY REFERENCES tenant (id),
    id            uuid NOT NULL,
    status        text NOT NULL CHECK (status IN ('pending', 'preparing', 'ready', 'failed')),
    error         text NOT NULL DEFAULT '',
    size          bigint NOT NULL DEFAULT 0,
    snapshot_id   text NOT NULL DEFAULT '',
    snapshot_time timestamptz,
    -- The backup's password, sealed with ADR-030's key (row id
    -- "backup_download:<id>"); cleared when the download expires.
    password_enc  bytea,
    requested_by  text NOT NULL,
    requested_at  timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL,
    -- A ready file is kept this long, then removed with its password.
    expires_at    timestamptz
);

-- upload: a backup file uploaded from the browser; location is its id.
ALTER TABLE backup_restore_request DROP CONSTRAINT backup_restore_request_source_check;
ALTER TABLE backup_restore_request ADD CONSTRAINT backup_restore_request_source_check
    CHECK (source IN ('folder', 'destination', 'upload'));
