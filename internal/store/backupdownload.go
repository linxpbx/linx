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

var _ backupschedule.DownloadStore = (*Store)(nil)

const downloadColumns = `id, tenant_id, status, error, size, snapshot_id, snapshot_time, requested_by, requested_at, updated_at, expires_at`

func scanDownload(row pgx.Row) (backupschedule.Download, error) {
	var d backupschedule.Download
	err := row.Scan(&d.ID, &d.TenantID, &d.Status, &d.Error, &d.Size, &d.SnapshotID, &d.SnapshotTime,
		&d.RequestedBy, &d.RequestedAt, &d.UpdatedAt, &d.ExpiresAt)
	return d, err
}

func (s *Store) Download(ctx context.Context, tenant uuid.UUID) (backupschedule.Download, bool, error) {
	d, err := scanDownload(s.pool.QueryRow(ctx, `SELECT `+downloadColumns+` FROM backup_download WHERE tenant_id = $1`, tenant))
	if errors.Is(err, pgx.ErrNoRows) {
		return backupschedule.Download{}, false, nil
	}
	return d, err == nil, err
}

func (s *Store) PutDownload(ctx context.Context, d backupschedule.Download, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO backup_download
				(tenant_id, id, status, error, size, snapshot_id, snapshot_time, password_enc, requested_by, requested_at, updated_at, expires_at)
			VALUES ($1, $2, $3, '', 0, '', NULL, NULL, $4, $5, $6, NULL)
			ON CONFLICT (tenant_id) DO UPDATE SET
				id = EXCLUDED.id, status = EXCLUDED.status, error = '', size = 0, snapshot_id = '', snapshot_time = NULL,
				password_enc = NULL, requested_by = EXCLUDED.requested_by, requested_at = EXCLUDED.requested_at,
				updated_at = EXCLUDED.updated_at, expires_at = NULL`,
			d.TenantID, d.ID, d.Status, d.RequestedBy, d.RequestedAt, d.UpdatedAt); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) TakeDownload(ctx context.Context, now time.Time) (backupschedule.Download, bool, error) {
	d, err := scanDownload(s.pool.QueryRow(ctx, `UPDATE backup_download SET status = 'preparing', updated_at = $1
		WHERE status = 'pending' RETURNING `+downloadColumns, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return backupschedule.Download{}, false, nil
	}
	return d, err == nil, err
}

func (s *Store) FinishDownload(ctx context.Context, d backupschedule.Download, passwordEnc []byte) error {
	_, err := s.pool.Exec(ctx, `UPDATE backup_download SET status = $2, error = $3, size = $4, snapshot_id = $5,
			snapshot_time = $6, password_enc = $7, updated_at = $8, expires_at = $9
		WHERE id = $1 AND status = 'preparing'`,
		d.ID, d.Status, d.Error, d.Size, d.SnapshotID, d.SnapshotTime, passwordEnc, d.UpdatedAt, d.ExpiresAt)
	return err
}

func (s *Store) DownloadPassword(ctx context.Context, id uuid.UUID) ([]byte, error) {
	var sealed []byte
	err := s.pool.QueryRow(ctx, `SELECT password_enc FROM backup_download WHERE id = $1`, id).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return sealed, err
}

func (s *Store) ExpireDownloads(ctx context.Context, now time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE backup_download SET password_enc = NULL
		WHERE password_enc IS NOT NULL AND expires_at <= $1::timestamptz`, now)
	return err
}

func (s *Store) WriteAudit(ctx context.Context, audit auth.AuditEntry) error {
	return insertAudit(ctx, s.pool, audit)
}
