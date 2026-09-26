package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/trunk"
)

var (
	_ pbx.CallStore        = (*Store)(nil)
	_ trunk.CallAlertStore = (*Store)(nil)
	_ trunk.MonitorStore   = (*Store)(nil)
)

// TrunkTenant returns the tenant of the trunk with id.
func (s *Store) TrunkTenant(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	var tenant uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT tenant_id FROM trunk WHERE id = $1`, id).Scan(&tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, pbx.ErrNotFound
	}
	return tenant, err
}

// InternationalAlertLimits returns the "unusual calling abroad" limits.
func (s *Store) InternationalAlertLimits(ctx context.Context) (minutes, calls int, err error) {
	err = s.pool.QueryRow(ctx, `SELECT international_alert_minutes, international_alert_calls FROM pbx_setting`).Scan(&minutes, &calls)
	return minutes, calls, err
}

// SetInternationalAlertLimits changes them, with an audit entry.
func (s *Store) SetInternationalAlertLimits(ctx context.Context, minutes, calls int, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE pbx_setting SET international_alert_minutes = $1, international_alert_calls = $2`, minutes, calls); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

// RecordOutsideCall keeps a call that went out on a trunk (once: a second
// record of the same call is ignored).
func (s *Store) RecordOutsideCall(ctx context.Context, c pbx.OutsideCall) error {
	var ended *time.Time
	if !c.EndedAt.IsZero() {
		ended = &c.EndedAt
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO outside_call (id, tenant_id, trunk_id, extension, number, region, category,
			started_at, answered_at, ended_at, talk_seconds)
		SELECT $1, $2, t.id, $4, $5, $6, $7, $8, $9, $10, $11
		FROM (SELECT $3::uuid AS want) w LEFT JOIN trunk t ON t.id = w.want
		ON CONFLICT (id) DO NOTHING`,
		c.ID, c.Tenant, c.TrunkID, c.Extension, c.Number, c.Result.Region, string(c.Result.Category),
		c.StartedAt, c.AnsweredAt, ended, c.TalkSeconds)
	return err
}

// CallsAbroadSince counts calls that went out to international or premium
// numbers outside home since since, and their talk time.
func (s *Store) CallsAbroadSince(ctx context.Context, tenant uuid.UUID, home string, since time.Time) (calls, talkSeconds int, err error) {
	err = s.pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(talk_seconds), 0) FROM outside_call
		WHERE tenant_id = $1 AND category IN ('international', 'premium') AND region <> '' AND region <> $2
			AND coalesce(ended_at, started_at) >= $3`, tenant, home, since).Scan(&calls, &talkSeconds)
	return calls, talkSeconds, err
}

// FirstCallToRegion records region as called, reporting whether it never
// had been.
func (s *Store) FirstCallToRegion(ctx context.Context, tenant uuid.UUID, region, extension string, at time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO called_region (tenant_id, region, first_called_at, first_called_by)
		VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, tenant, region, at, extension)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// CleanupOutsideCalls removes calls that started before before.
func (s *Store) CleanupOutsideCalls(ctx context.Context, before time.Time) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM outside_call WHERE started_at < $1`, before)
	return err
}
