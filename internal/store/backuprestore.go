package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/backupschedule"
)

var _ backupschedule.RestoreStore = (*Store)(nil)

const restoreColumns = `id, tenant_id, source, location, snapshot, status, error, requested_by, requested_at, updated_at`

func scanRestore(row pgx.Row, extra ...any) (backupschedule.Restore, error) {
	var r backupschedule.Restore
	dest := append([]any{&r.ID, &r.TenantID, &r.Source, &r.Location, &r.Snapshot, &r.Status, &r.Error,
		&r.RequestedBy, &r.RequestedAt, &r.UpdatedAt}, extra...)
	err := row.Scan(dest...)
	return r, err
}

func (s *Store) SetupCompleted(ctx context.Context) (bool, error) {
	var done bool
	err := s.pool.QueryRow(ctx, `SELECT setup_completed_at IS NOT NULL FROM pbx_setting`).Scan(&done)
	return done, err
}

func (s *Store) Restore(ctx context.Context, tenant uuid.UUID) (backupschedule.Restore, bool, error) {
	r, err := scanRestore(s.pool.QueryRow(ctx, `SELECT `+restoreColumns+` FROM backup_restore_request WHERE tenant_id = $1`, tenant))
	if errors.Is(err, pgx.ErrNoRows) {
		return backupschedule.Restore{}, false, nil
	}
	return r, err == nil, err
}

func (s *Store) PutRestore(ctx context.Context, r backupschedule.Restore, passwordEnc []byte, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO backup_restore_request
				(tenant_id, id, source, location, snapshot, password_enc, status, error, requested_by, requested_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, '', $8, $9, $10)
			ON CONFLICT (tenant_id) DO UPDATE SET
				id = EXCLUDED.id, source = EXCLUDED.source, location = EXCLUDED.location, snapshot = EXCLUDED.snapshot,
				password_enc = EXCLUDED.password_enc, status = EXCLUDED.status, error = '',
				requested_by = EXCLUDED.requested_by, requested_at = EXCLUDED.requested_at, updated_at = EXCLUDED.updated_at`,
			r.TenantID, r.ID, r.Source, r.Location, r.Snapshot, passwordEnc, r.Status, r.RequestedBy, r.RequestedAt, r.UpdatedAt); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) TakeRestore(ctx context.Context, now time.Time) (backupschedule.Restore, []byte, bool, error) {
	var sealed []byte
	// The old password comes back from the row as it was before this
	// UPDATE cleared it (a self-join on the same row's pre-update value).
	r, err := scanRestore(s.pool.QueryRow(ctx, `UPDATE backup_restore_request AS b
			SET status = 'running', password_enc = NULL, updated_at = $1
		FROM backup_restore_request AS old
		WHERE old.tenant_id = b.tenant_id AND b.status = 'pending' AND b.password_enc IS NOT NULL
		RETURNING b.id, b.tenant_id, b.source, b.location, b.snapshot, b.status, b.error, b.requested_by, b.requested_at, b.updated_at,
			old.password_enc`, now), &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return backupschedule.Restore{}, nil, false, nil
	}
	if err != nil {
		return backupschedule.Restore{}, nil, false, err
	}
	return r, sealed, true, nil
}

func (s *Store) FailRestore(ctx context.Context, id uuid.UUID, message string, now time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE backup_restore_request SET status = 'failed', password_enc = NULL, error = $2, updated_at = $3
		WHERE id = $1`, id, message, now)
	return err
}

func (s *Store) RecordRestored(ctx context.Context, audit auth.AuditEntry) error {
	return insertAudit(ctx, s.pool, audit)
}
