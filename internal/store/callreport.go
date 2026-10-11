package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/callreport"
)

// SaveCallReport keeps one phone's report of a call and, in the same
// transaction, drops that phone's reports older than keepAfter or beyond
// its newest keep (callreport.Service).
func (s *Store) SaveCallReport(ctx context.Context, r callreport.Report, keepAfter time.Time, keep int) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO phone_call_report
				(id, tenant_id, device_id, call_id, direction, peer, started_at, ended_at, report, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			r.ID, r.TenantID, r.DeviceID, r.CallID, r.Direction, r.Peer, r.StartedAt, r.EndedAt, r.Text, r.CreatedAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM phone_call_report WHERE device_id = $1 AND (created_at < $2 OR id NOT IN
			(SELECT id FROM phone_call_report WHERE device_id = $1 ORDER BY created_at DESC LIMIT $3))`,
			r.DeviceID, keepAfter, keep)
		return err
	})
}

// CallReports is one phone's reports, newest first.
func (s *Store) CallReports(ctx context.Context, tenant, device uuid.UUID) ([]callreport.Report, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, tenant_id, device_id, call_id, direction, peer, started_at, ended_at,
			report, created_at
		FROM phone_call_report WHERE tenant_id = $1 AND device_id = $2 ORDER BY created_at DESC`, tenant, device)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []callreport.Report{}
	for rows.Next() {
		var r callreport.Report
		if err := rows.Scan(&r.ID, &r.TenantID, &r.DeviceID, &r.CallID, &r.Direction, &r.Peer, &r.StartedAt,
			&r.EndedAt, &r.Text, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
