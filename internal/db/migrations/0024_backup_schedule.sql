-- Backup schedule and history (docs/BACKUP.md §5, §8 step 3). The schedule
-- itself is tenant-wide config, so it lives on pbx_setting like every other
-- admin-portal setting (0021); each run's outcome is its own history table,
-- like outside_call, since it grows over time and isn't "settings".

ALTER TABLE pbx_setting
    -- Off by default (docs/BACKUP.md §5). time_of_day is minutes since
    -- midnight, server-local time (there's no per-tenant timezone setting
    -- yet); day_of_week 0=Sunday..6=Saturday (weekly only); day_of_month
    -- 1..28 (monthly only, so every month has that day).
    ADD COLUMN backup_frequency   text NOT NULL DEFAULT 'off'
        CHECK (backup_frequency IN ('off', 'daily', 'weekly', 'monthly')),
    ADD COLUMN backup_time_of_day integer NOT NULL DEFAULT 180 CHECK (backup_time_of_day BETWEEN 0 AND 1439),
    ADD COLUMN backup_day_of_week  smallint NOT NULL DEFAULT 0 CHECK (backup_day_of_week BETWEEN 0 AND 6),
    ADD COLUMN backup_day_of_month smallint NOT NULL DEFAULT 1 CHECK (backup_day_of_month BETWEEN 1 AND 28),
    -- Set by POST /backups ("back up now"); cleared once linx-backup-agent's
    -- next tick picks it up and reports back. Never cleared any other way,
    -- so a request is never silently dropped by an unrelated write.
    ADD COLUMN backup_requested_at timestamptz,
    ADD COLUMN backup_requested_by text NOT NULL DEFAULT '';

-- One row per attempt (scheduled or "back up now"), written by
-- linx-backup-agent's report through `service backup report`
-- (services/control-plane/backup_cmd.go) — never by an authenticated admin
-- request, so there's no audit_log row for these, the same reasoning as a
-- trunk's status column (0019): it's what happened, not something someone
-- set.
CREATE TABLE backup_run (
    id           uuid PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenant (id),
    trigger      text NOT NULL CHECK (trigger IN ('manual', 'scheduled')),
    started_at   timestamptz NOT NULL,
    finished_at  timestamptz NOT NULL,
    -- success: every destination worked. partial: at least one did, at
    -- least one didn't. failure: none did (including the dump itself
    -- failing before any destination was tried).
    status       text NOT NULL CHECK (status IN ('success', 'partial', 'failure')),
    -- Per destination: [{"name": "...", "ok": true, "snapshot_id": "...", "error": "..."}].
    destinations jsonb NOT NULL DEFAULT '[]',
    error        text NOT NULL DEFAULT ''  -- set when status is failure and no destination ran at all (e.g. the dump failed)
);
CREATE INDEX backup_run_tenant_started_idx ON backup_run (tenant_id, started_at DESC);
