package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/callquality"
)

// DeviceForQuality is the phone Asterisk calls sipUsername, if it is one of
// Linx's and not revoked (internal/callquality).
func (s *Store) DeviceForQuality(ctx context.Context, sipUsername string) (tenant, device uuid.UUID, name string, err error) {
	err = s.pool.QueryRow(ctx, `SELECT tenant_id, id, name FROM device
		WHERE sip_username = $1 AND revoked_at IS NULL`, sipUsername).Scan(&tenant, &device, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errNotLinxPhone
	}
	return tenant, device, name, err
}

var errNotLinxPhone = errors.New("not one of Linx's phones")

// SaveCallQuality keeps one record and forgets every phone's records older
// than before.
func (s *Store) SaveCallQuality(ctx context.Context, r callquality.Record, before time.Time) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO call_quality
				(id, tenant_id, device_id, kind, call_id, started_at, ended_at, verdict, reasons,
				 in_packets, out_packets, in_lost, out_lost, jitter_ms, rtt_ms)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
			r.ID, r.TenantID, r.DeviceID, r.Kind, r.CallID, r.StartedAt, r.EndedAt, r.Verdict, nonNil(r.Reasons),
			r.Sound.InPackets, r.Sound.OutPackets, r.Sound.InLost, r.Sound.OutLost, r.Sound.JitterMs,
			r.Sound.RTTMs); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM call_quality WHERE ended_at < $1`, before)
		return err
	})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// BadCallsSince counts a phone's bad records since a moment.
func (s *Store) BadCallsSince(ctx context.Context, device uuid.UUID, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM call_quality
		WHERE device_id = $1 AND verdict = 'bad' AND ended_at >= $2`, device, since).Scan(&n)
	return n, err
}

// CallQualityItem is one record as Calls → Call quality shows it: the
// phone, whose it is, and what the phone itself reported of the same call.
type CallQualityItem struct {
	callquality.Record
	DeviceName string
	Extension  string
	Person     string
	// PhoneReport is the phone's own report of the same call, if it sent
	// one, and PhoneVerdict the verdict line in it.
	PhoneReport  *uuid.UUID
	PhoneVerdict string
}

// CallQuality is a company's records, newest first, at most limit.
func (s *Store) CallQuality(ctx context.Context, tenant uuid.UUID, limit int) ([]CallQualityItem, error) {
	rows, err := s.pool.Query(ctx, `SELECT q.id, q.tenant_id, q.device_id, q.kind, q.call_id, q.started_at, q.ended_at,
			q.verdict, q.reasons, q.in_packets, q.out_packets, q.in_lost, q.out_lost, q.jitter_ms, q.rtt_ms,
			d.name, e.number, e.display_name,
			r.id, coalesce(substring(r.report from E'\nverdict: ([^\n]*)'), '')
		FROM call_quality q
		JOIN device d ON d.id = q.device_id
		JOIN extension e ON e.id = d.extension_id
		LEFT JOIN LATERAL (SELECT id, report FROM phone_call_report
			WHERE device_id = q.device_id AND q.call_id <> '' AND call_id = q.call_id
			ORDER BY created_at DESC LIMIT 1) r ON true
		WHERE q.tenant_id = $1
		ORDER BY q.ended_at DESC LIMIT $2`, tenant, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CallQualityItem{}
	for rows.Next() {
		var i CallQualityItem
		var inLost, outLost, jitter, rtt float32
		if err := rows.Scan(&i.ID, &i.TenantID, &i.DeviceID, &i.Kind, &i.CallID, &i.StartedAt, &i.EndedAt,
			&i.Verdict, &i.Reasons, &i.Sound.InPackets, &i.Sound.OutPackets, &inLost, &outLost, &jitter, &rtt,
			&i.DeviceName, &i.Extension, &i.Person, &i.PhoneReport, &i.PhoneVerdict); err != nil {
			return nil, err
		}
		i.Sound.InLost, i.Sound.OutLost, i.Sound.JitterMs, i.Sound.RTTMs =
			float64(inLost), float64(outLost), float64(jitter), float64(rtt)
		out = append(out, i)
	}
	return out, rows.Err()
}
