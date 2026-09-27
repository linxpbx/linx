-- Restore from a backup, asked for in the setup wizard (docs/BACKUP.md §4,
-- §8 step 4). The browser only asks; linx-backup-agent (a host program,
-- root) does the restoring and reports back, the same bridge as "back up
-- now" (0024). One request per tenant at a time.
CREATE TABLE backup_restore_request (
    tenant_id    uuid PRIMARY KEY REFERENCES tenant (id),
    id           uuid NOT NULL,
    -- folder: a restic repository in a folder on the server (location is
    -- its absolute path). destination: one set up on this server with
    -- `linx backup destination add` (location is its name).
    source       text NOT NULL CHECK (source IN ('folder', 'destination')),
    location     text NOT NULL,
    snapshot     text NOT NULL,
    -- The repository password, sealed with ADR-030's key (row id
    -- "backup_restore:<id>"). Cleared the moment the agent takes the
    -- request, so it's in the database for about a minute at most.
    password_enc bytea,
    status       text NOT NULL CHECK (status IN ('pending', 'running', 'failed')),
    error        text NOT NULL DEFAULT '',
    requested_by text NOT NULL,
    requested_at timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL
);
