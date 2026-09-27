package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/backupschedule"
	"linxpbx.com/linx/internal/webhook"
)

var _ backupschedule.Store = (*Store)(nil)

const scheduleColumns = `backup_frequency, backup_time_of_day, backup_day_of_week, backup_day_of_month, backup_requested_at, backup_requested_by`

func scanSchedule(row pgx.Row) (backupschedule.Schedule, error) {
	var out backupschedule.Schedule
	err := row.Scan(&out.Frequency, &out.TimeOfDay, &out.DayOfWeek, &out.DayOfMonth, &out.RequestedAt, &out.RequestedBy)
	return out, err
}

func (s *Store) Schedule(ctx context.Context) (backupschedule.Schedule, error) {
	return scanSchedule(s.pool.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM pbx_setting`))
}

func (s *Store) UpdateSchedule(ctx context.Context, in backupschedule.Schedule, audit auth.AuditEntry) (backupschedule.Schedule, error) {
	var out backupschedule.Schedule
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanSchedule(tx.QueryRow(ctx, `UPDATE pbx_setting SET
				backup_frequency = $1, backup_time_of_day = $2, backup_day_of_week = $3, backup_day_of_month = $4
			RETURNING `+scheduleColumns,
			in.Frequency, in.TimeOfDay, in.DayOfWeek, in.DayOfMonth))
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

func (s *Store) RequestRun(ctx context.Context, at time.Time, by string, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE pbx_setting SET backup_requested_at = $1, backup_requested_by = $2`, at, by); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) LastRunStartedAt(ctx context.Context) (*time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx, `SELECT started_at FROM backup_run ORDER BY started_at DESC LIMIT 1`).Scan(&t)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return t, err
}

func (s *Store) InsertRun(ctx context.Context, r backupschedule.Run) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		destinations := r.Destinations
		if destinations == nil {
			// pgx sends a nil slice as SQL NULL, not JSON "[]"; the column
			// is NOT NULL (a failed run with no destination even attempted
			// is exactly the empty-but-not-nil case, the same reasoning as
			// admin_networks' orEmpty).
			destinations = []backupschedule.Destination{}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO backup_run
				(id, tenant_id, trigger, started_at, finished_at, status, destinations, error)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			r.ID, r.TenantID, r.Trigger, r.StartedAt, r.FinishedAt, r.Status, destinations, r.Error); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE pbx_setting SET backup_requested_at = NULL, backup_requested_by = ''`); err != nil {
			return err
		}
		eventType := "backup.completed"
		if r.Status != backupschedule.StatusSuccess {
			eventType = "backup.failed"
		}
		ev, err := webhook.NewEvent(r.TenantID, eventType, map[string]any{
			"id": r.ID, "trigger": r.Trigger, "status": r.Status, "started_at": r.StartedAt, "finished_at": r.FinishedAt,
		}, r.FinishedAt)
		if err != nil {
			return err
		}
		return insertEvent(ctx, tx, ev, nil)
	})
}

func (s *Store) ListRuns(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]backupschedule.Run, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, trigger, started_at, finished_at, status, destinations, error FROM backup_run
		WHERE tenant_id = $1 AND ($2::uuid IS NULL OR id < $2)
		ORDER BY id DESC LIMIT $3`, tenant, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []backupschedule.Run{}
	for rows.Next() {
		var r backupschedule.Run
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Trigger, &r.StartedAt, &r.FinishedAt, &r.Status, &r.Destinations, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
