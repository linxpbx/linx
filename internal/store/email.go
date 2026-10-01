package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/email"
)

// Email (ADR-066, migration 0030): the setting and the queue.

var _ email.Store = (*Store)(nil)

func (s *Store) EmailSettings(ctx context.Context, tenant uuid.UUID) (email.Config, error) {
	c := email.Config{TenantID: tenant}
	err := s.pool.QueryRow(ctx, `SELECT enabled, preset, host, port, security, username, from_address, from_name,
			password_enc, hourly_limit, arrived_at, version, updated_at
		FROM email_settings WHERE tenant_id = $1`, tenant).
		Scan(&c.Enabled, &c.Preset, &c.Host, &c.Port, &c.Security, &c.Username, &c.FromAddress, &c.FromName,
			&c.PasswordEnc, &c.HourlyLimit, &c.ArrivedAt, &c.Version, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, auth.ErrNotFound
	}
	return c, err
}

func (s *Store) SaveEmailSettings(ctx context.Context, c email.Config, version int, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO email_settings (tenant_id, enabled, preset, host, port, security, username,
				from_address, from_name, password_enc, hourly_limit, arrived_at, version, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT (tenant_id) DO UPDATE SET enabled = $2, preset = $3, host = $4, port = $5, security = $6,
				username = $7, from_address = $8, from_name = $9, password_enc = $10, hourly_limit = $11,
				arrived_at = $12, version = $13, updated_at = $14
			WHERE email_settings.version = $15`,
			c.TenantID, c.Enabled, c.Preset, c.Host, c.Port, c.Security, c.Username, c.FromAddress, c.FromName,
			c.PasswordEnc, c.HourlyLimit, c.ArrivedAt, c.Version, c.UpdatedAt, version)
		if err != nil {
			return fmt.Errorf("saving the email setting: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return auth.ErrVersionChanged
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) EnqueueEmail(ctx context.Context, m email.Message, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO email_outbox (id, tenant_id, kind, to_addresses, content_enc, status,
				next_attempt_at, created_at)
			VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7)`,
			m.ID, m.TenantID, m.Kind, m.To, m.ContentEnc, m.NextAttemptAt, m.CreatedAt); err != nil {
			return fmt.Errorf("queueing an email: %w", err)
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) ClaimEmails(ctx context.Context, now, leaseUntil time.Time, limit int) ([]email.Message, error) {
	// Each tenant gets at most what its hourly limit has left; one worker
	// runs, and the lease keeps a crashed attempt from being lost.
	rows, err := s.pool.Query(ctx, `WITH room AS (
			-- Sent in the last hour, and being sent right now.
			SELECT st.tenant_id, st.hourly_limit - (SELECT count(*) FROM email_outbox o
				WHERE o.tenant_id = st.tenant_id AND (o.sent_at > $1::timestamptz - interval '1 hour'
					OR (o.status = 'pending' AND o.lease_until >= $1))) AS left_this_hour
			FROM email_settings st WHERE st.enabled
		), due AS (
			SELECT o.id, row_number() OVER (PARTITION BY o.tenant_id ORDER BY o.next_attempt_at, o.id) AS n, r.left_this_hour
			FROM email_outbox o JOIN room r USING (tenant_id)
			WHERE o.status = 'pending' AND o.next_attempt_at <= $1 AND (o.lease_until IS NULL OR o.lease_until < $1)
		)
		UPDATE email_outbox o SET lease_until = $2
		FROM (SELECT id FROM due WHERE n <= left_this_hour ORDER BY n LIMIT $3) picked
		WHERE o.id = picked.id
		RETURNING o.id, o.tenant_id, o.kind, o.to_addresses, o.content_enc, o.status, o.attempts, o.next_attempt_at,
			o.last_error, o.created_at`, now, leaseUntil, limit)
	if err != nil {
		return nil, fmt.Errorf("claiming emails: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (email.Message, error) {
		var m email.Message
		err := r.Scan(&m.ID, &m.TenantID, &m.Kind, &m.To, &m.ContentEnc, &m.Status, &m.Attempts, &m.NextAttemptAt,
			&m.LastError, &m.CreatedAt)
		return m, err
	})
}

func (s *Store) FinishEmail(ctx context.Context, m email.Message, status, lastError string, next *time.Time, now time.Time) error {
	if r := []rune(lastError); len(r) > 1000 {
		lastError = string(r[:1000])
	}
	var err error
	switch status {
	case email.StatusPending:
		_, err = s.pool.Exec(ctx, `UPDATE email_outbox SET attempts = attempts + 1, last_error = $2,
				next_attempt_at = $3, lease_until = NULL WHERE id = $1`, m.ID, lastError, next)
	case email.StatusSent, email.StatusFailed:
		// The content goes: what's left is who, what kind and when.
		_, err = s.pool.Exec(ctx, `UPDATE email_outbox SET attempts = attempts + 1, status = $2::text, last_error = $3,
				content_enc = NULL, next_attempt_at = NULL, lease_until = NULL, finished_at = $4::timestamptz,
				sent_at = CASE WHEN $2::text = 'sent' THEN $4::timestamptz END
			WHERE id = $1`, m.ID, status, lastError, now)
	default:
		return fmt.Errorf("email status %q", status)
	}
	return err
}

func (s *Store) NextEmailDue(ctx context.Context) (*time.Time, error) {
	var next *time.Time
	err := s.pool.QueryRow(ctx, `SELECT min(greatest(o.next_attempt_at, coalesce(o.lease_until, o.next_attempt_at)))
		FROM email_outbox o JOIN email_settings st USING (tenant_id)
		WHERE o.status = 'pending' AND st.enabled`).Scan(&next)
	return next, err
}

func (s *Store) EmailStatus(ctx context.Context, tenant uuid.UUID, now time.Time) (email.Status, error) {
	var st email.Status
	// The last error is the newest one still waiting, or a failure in the
	// last day that no email has gone out since.
	err := s.pool.QueryRow(ctx, `WITH sums AS (
			SELECT max(sent_at) AS last_sent,
				count(*) FILTER (WHERE sent_at > $2::timestamptz - interval '1 hour') AS hour,
				count(*) FILTER (WHERE status = 'pending') AS waiting
			FROM email_outbox WHERE tenant_id = $1
		)
		SELECT last_sent, hour, waiting, coalesce((SELECT o.last_error FROM email_outbox o
				WHERE o.tenant_id = $1 AND o.last_error <> '' AND (o.status = 'pending' OR
					(o.status = 'failed' AND o.finished_at > greatest($2::timestamptz - interval '1 day', coalesce(sums.last_sent, '-infinity'))))
				ORDER BY coalesce(o.finished_at, o.created_at) DESC LIMIT 1), '')
		FROM sums`, tenant, now).
		Scan(&st.LastSentAt, &st.SentLastHour, &st.Waiting, &st.LastError)
	return st, err
}

func (s *Store) CleanupEmails(ctx context.Context, cutoff time.Time) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM email_outbox WHERE status <> 'pending' AND finished_at < $1`, cutoff)
	return err
}
