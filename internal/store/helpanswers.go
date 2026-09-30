package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/helpanswers"
)

// Help's written answers (docs/HELP.md §4, migration 0029): the setting and
// the daily question counts.

var _ helpanswers.Store = (*Store)(nil)

func (s *Store) HelpAnswers(ctx context.Context, tenant uuid.UUID) (helpanswers.Config, error) {
	c := helpanswers.Config{TenantID: tenant}
	err := s.pool.QueryRow(ctx, `SELECT enabled, provider, base_url, model, api_key_enc, person_daily_limit,
			server_daily_limit, version, updated_at
		FROM help_answers WHERE tenant_id = $1`, tenant).
		Scan(&c.Enabled, &c.Provider, &c.BaseURL, &c.Model, &c.APIKeyEnc, &c.PersonDailyLimit,
			&c.ServerDailyLimit, &c.Version, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, auth.ErrNotFound
	}
	return c, err
}

func (s *Store) SaveHelpAnswers(ctx context.Context, c helpanswers.Config, version int, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Version 0 is "none saved yet": the insert's conflict then means
		// someone saved first.
		tag, err := tx.Exec(ctx, `INSERT INTO help_answers (tenant_id, enabled, provider, base_url, model, api_key_enc,
				person_daily_limit, server_daily_limit, version, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (tenant_id) DO UPDATE SET enabled = $2, provider = $3, base_url = $4, model = $5,
				api_key_enc = $6, person_daily_limit = $7, server_daily_limit = $8, version = $9, updated_at = $10
			WHERE help_answers.version = $11`,
			c.TenantID, c.Enabled, c.Provider, c.BaseURL, c.Model, c.APIKeyEnc, c.PersonDailyLimit,
			c.ServerDailyLimit, c.Version, c.UpdatedAt, version)
		if err != nil {
			return fmt.Errorf("saving help answers: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return auth.ErrVersionChanged
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) TakeHelpAnswer(ctx context.Context, tenant, user uuid.UUID, day time.Time, personMax, serverMax int) (string, error) {
	var which string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// One question counted at a time per tenant, so two at once can't
		// both take the server's last one.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('help_answer_usage:' || $1::text))`, tenant); err != nil {
			return err
		}
		var server, person int
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(count), 0), coalesce(sum(count) FILTER (WHERE user_id = $3), 0)
			FROM help_answer_usage WHERE tenant_id = $1 AND day = $2::date`, tenant, dayOf(day), user).Scan(&server, &person); err != nil {
			return err
		}
		switch {
		case person >= personMax:
			which = "person"
			return nil
		case server >= serverMax:
			which = "server"
			return nil
		}
		if server == 0 {
			// A new day: forget the counts from over a week ago.
			if _, err := tx.Exec(ctx, `DELETE FROM help_answer_usage WHERE tenant_id = $1 AND day < $2::date - 7`, tenant, dayOf(day)); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO help_answer_usage (tenant_id, day, user_id, count) VALUES ($1, $2::date, $3, 1)
			ON CONFLICT (tenant_id, day, user_id) DO UPDATE SET count = help_answer_usage.count + 1`, tenant, dayOf(day), user)
		return err
	})
	return which, err
}

func (s *Store) HelpAnswersUsedToday(ctx context.Context, tenant uuid.UUID, day time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT coalesce(sum(count), 0) FROM help_answer_usage WHERE tenant_id = $1 AND day = $2::date`,
		tenant, dayOf(day)).Scan(&n)
	return n, err
}

// dayOf is t's UTC date, as Postgres reads a date, whatever the session's
// time zone.
func dayOf(t time.Time) string { return t.UTC().Format(time.DateOnly) }
