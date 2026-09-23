package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/alert"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/webhook"
)

var _ alert.Store = (*Store)(nil)

const channelColumns = `id, tenant_id, kind, name, config_enc, min_severity,
	quiet_hours_start, quiet_hours_end, quiet_hours_tz, quiet_hours_bypass_critical,
	enabled, version, created_by, created_at, updated_at`

func scanChannel(row pgx.Row) (alert.Channel, error) {
	var c alert.Channel
	var start, end *int
	var tz *string
	var bypass bool
	err := row.Scan(&c.ID, &c.TenantID, &c.Kind, &c.Name, &c.ConfigEnc, &c.MinSeverity,
		&start, &end, &tz, &bypass, &c.Enabled, &c.Version, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, alert.ErrNotFound
	}
	if err != nil {
		return c, err
	}
	if start != nil {
		c.QuietHours = &alert.QuietHours{Start: *start, End: *end, Timezone: *tz, BypassCritical: bypass}
	}
	return c, nil
}

func quietHoursArgs(c alert.Channel) (start, end *int, tz *string, bypass bool) {
	if c.QuietHours == nil {
		return nil, nil, nil, false
	}
	return &c.QuietHours.Start, &c.QuietHours.End, &c.QuietHours.Timezone, c.QuietHours.BypassCritical
}

func (s *Store) CreateChannel(ctx context.Context, c alert.Channel, audit auth.AuditEntry) error {
	start, end, tz, bypass := quietHoursArgs(c)
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO alert_channel (id, tenant_id, kind, name, config_enc, min_severity,
			quiet_hours_start, quiet_hours_end, quiet_hours_tz, quiet_hours_bypass_critical,
			enabled, version, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
			c.ID, c.TenantID, c.Kind, c.Name, c.ConfigEnc, c.MinSeverity, start, end, tz, bypass,
			c.Enabled, c.Version, c.CreatedBy, c.CreatedAt, c.UpdatedAt)
		if err != nil {
			return fmt.Errorf("creating alert channel: %w", err)
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) Channel(ctx context.Context, tenant, id uuid.UUID) (alert.Channel, error) {
	return scanChannel(s.pool.QueryRow(ctx,
		`SELECT `+channelColumns+` FROM alert_channel WHERE id = $1 AND tenant_id = $2`, id, tenant))
}

func (s *Store) ListChannels(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]alert.Channel, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+channelColumns+` FROM alert_channel
		WHERE tenant_id = $1 AND ($2::uuid IS NULL OR id < $2) ORDER BY id DESC LIMIT $3`, tenant, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []alert.Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) EnabledChannels(ctx context.Context, tenant uuid.UUID) ([]alert.Channel, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+channelColumns+` FROM alert_channel WHERE tenant_id = $1 AND enabled`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []alert.Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) UpdateChannel(ctx context.Context, c alert.Channel, audit auth.AuditEntry) (alert.Channel, error) {
	start, end, tz, bypass := quietHoursArgs(c)
	var out alert.Channel
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanChannel(tx.QueryRow(ctx, `UPDATE alert_channel SET
				name = $4, config_enc = $5, min_severity = $6,
				quiet_hours_start = $7, quiet_hours_end = $8, quiet_hours_tz = $9, quiet_hours_bypass_critical = $10,
				enabled = $11, version = version + 1, updated_at = $12
			WHERE id = $1 AND tenant_id = $2 AND version = $3
			RETURNING `+channelColumns,
			c.ID, c.TenantID, c.Version, c.Name, c.ConfigEnc, c.MinSeverity, start, end, tz, bypass,
			c.Enabled, c.UpdatedAt))
		if errors.Is(err, alert.ErrNotFound) {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM alert_channel WHERE id = $1 AND tenant_id = $2)`,
				c.ID, c.TenantID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return alert.ErrVersionChanged
			}
			return alert.ErrNotFound
		}
		if err != nil {
			return err
		}
		if !out.Enabled {
			if err := cancelChannelDeliveries(ctx, tx, out.ID, c.UpdatedAt); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

func cancelChannelDeliveries(ctx context.Context, db execer, channel uuid.UUID, at time.Time) error {
	_, err := db.Exec(ctx, `UPDATE alert_delivery SET status = 'cancelled', next_attempt_at = NULL, finished_at = $2
		WHERE channel_id = $1 AND status IN ('pending', 'held')`, channel, at)
	return err
}

func (s *Store) DeleteChannel(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM alert_channel WHERE id = $1 AND tenant_id = $2`, id, tenant)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return alert.ErrNotFound
		}
		return insertAudit(ctx, tx, audit)
	})
}

const alertColumns = `id, tenant_id, key, severity, title, message, link, status,
	first_seen_at, last_seen_at, stable_since, notified_at, last_reminder_at, resolved_at, resolved_notified_at`

func scanAlert(row pgx.Row) (alert.Alert, error) {
	var a alert.Alert
	var link *string
	err := row.Scan(&a.ID, &a.TenantID, &a.Key, &a.Severity, &a.Title, &a.Message, &link, &a.Status,
		&a.FirstSeenAt, &a.LastSeenAt, &a.StableSince, &a.NotifiedAt, &a.LastReminderAt, &a.ResolvedAt, &a.ResolvedNotifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, alert.ErrNotFound
	}
	if link != nil {
		a.Link = *link
	}
	return a, err
}

// Fire upserts the open alert for (tenant, key): a fresh row if none is
// currently open (justOpened, resetting stable_since), or a bump of
// last_seen_at (and description, in case it changed) if one already is.
// The partial unique index alert_open_key_idx makes this an atomic upsert.
func (s *Store) Fire(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string, now time.Time) (alert.Alert, bool, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return alert.Alert{}, false, err
	}
	var linkArg *string
	if link != "" {
		linkArg = &link
	}
	row := s.pool.QueryRow(ctx, `INSERT INTO alert (id, tenant_id, key, severity, title, message, link, status,
			first_seen_at, last_seen_at, stable_since)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'open', $8, $8, $8)
		ON CONFLICT (tenant_id, key) WHERE status = 'open' DO UPDATE SET
			last_seen_at = $8, severity = $4, title = $5, message = $6, link = $7
		RETURNING `+alertColumns+`, (xmax = 0) AS inserted`,
		id, tenant, key, severity, title, message, linkArg, now)

	var a alert.Alert
	var linkOut *string
	var inserted bool
	err = row.Scan(&a.ID, &a.TenantID, &a.Key, &a.Severity, &a.Title, &a.Message, &linkOut, &a.Status,
		&a.FirstSeenAt, &a.LastSeenAt, &a.StableSince, &a.NotifiedAt, &a.LastReminderAt, &a.ResolvedAt, &a.ResolvedNotifiedAt, &inserted)
	if err != nil {
		return alert.Alert{}, false, fmt.Errorf("firing alert: %w", err)
	}
	if linkOut != nil {
		a.Link = *linkOut
	}
	return a, inserted, nil
}

func (s *Store) Resolve(ctx context.Context, tenant uuid.UUID, key string, now time.Time) (alert.Alert, bool, error) {
	a, err := scanAlert(s.pool.QueryRow(ctx, `UPDATE alert SET status = 'resolved', resolved_at = $3
		WHERE tenant_id = $1 AND key = $2 AND status = 'open' RETURNING `+alertColumns, tenant, key, now))
	if errors.Is(err, alert.ErrNotFound) {
		return alert.Alert{}, false, nil
	}
	return a, err == nil, err
}

func (s *Store) ListAlerts(ctx context.Context, tenant uuid.UUID, status string, before *uuid.UUID, limit int) ([]alert.Alert, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+alertColumns+` FROM alert
		WHERE tenant_id = $1 AND ($2 = '' OR status = $2) AND ($3::uuid IS NULL OR id < $3)
		ORDER BY id DESC LIMIT $4`, tenant, status, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []alert.Alert{}
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) DueToNotify(ctx context.Context, cutoff time.Time, limit int) ([]alert.Alert, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+alertColumns+` FROM alert
		WHERE status = 'open' AND notified_at IS NULL AND stable_since <= $1
		ORDER BY stable_since LIMIT $2`, cutoff, limit)
	return collectAlerts(rows, err)
}

func (s *Store) DueForReminder(ctx context.Context, cutoff time.Time, limit int) ([]alert.Alert, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+alertColumns+` FROM alert
		WHERE status = 'open' AND notified_at IS NOT NULL AND COALESCE(last_reminder_at, notified_at) <= $1
		ORDER BY COALESCE(last_reminder_at, notified_at) LIMIT $2`, cutoff, limit)
	return collectAlerts(rows, err)
}

func (s *Store) DueForResolvedNotice(ctx context.Context, limit int) ([]alert.Alert, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+alertColumns+` FROM alert
		WHERE status = 'resolved' AND notified_at IS NOT NULL AND resolved_notified_at IS NULL
		ORDER BY resolved_at LIMIT $1`, limit)
	return collectAlerts(rows, err)
}

func collectAlerts(rows pgx.Rows, err error) ([]alert.Alert, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []alert.Alert{}
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// alertNotifyColumn is the alert column Notify bumps for each kind, so a
// later scan doesn't pick the same alert up again for the same reason.
var alertNotifyColumn = map[string]string{
	alert.DeliveryFired:    "notified_at",
	alert.DeliveryReminder: "last_reminder_at",
	alert.DeliveryResolved: "resolved_notified_at",
}

func (s *Store) Notify(ctx context.Context, a alert.Alert, kind string, due, held []uuid.UUID, now time.Time, maxAttempts int, ev *alert.WebhookEvent) error {
	column, ok := alertNotifyColumn[kind]
	if !ok {
		return fmt.Errorf("notify: unknown kind %q", kind)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, channel := range due {
			if err := insertAlertDelivery(ctx, tx, a, channel, kind, alert.DeliveryPending, &now, maxAttempts, now); err != nil {
				return err
			}
		}
		for _, channel := range held {
			if err := insertAlertDelivery(ctx, tx, a, channel, kind, alert.DeliveryHeld, nil, maxAttempts, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE alert SET `+column+` = $2 WHERE id = $1`, a.ID, now); err != nil {
			return err
		}
		if ev == nil {
			return nil
		}
		event, err := webhook.NewEvent(a.TenantID, ev.Type, ev.Data, now)
		if err != nil {
			return err
		}
		return InsertEvent(ctx, tx, event)
	})
}

func insertAlertDelivery(ctx context.Context, db execer, a alert.Alert, channel uuid.UUID, kind, status string, nextAttemptAt *time.Time, maxAttempts int, now time.Time) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO alert_delivery (id, tenant_id, channel_id, alert_ids, kind, status,
		attempts, max_attempts, next_attempt_at, created_at) VALUES ($1, $2, $3, $4, $5, $6, 0, $7, $8, $9)`,
		id, a.TenantID, channel, []uuid.UUID{a.ID}, kind, status, maxAttempts, nextAttemptAt, now)
	if err != nil {
		return fmt.Errorf("queueing alert delivery: %w", err)
	}
	return nil
}

func (s *Store) HeldChannels(ctx context.Context) ([]alert.Channel, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+channelColumns+` FROM alert_channel
		WHERE EXISTS (SELECT 1 FROM alert_delivery d WHERE d.channel_id = alert_channel.id AND d.status = 'held')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []alert.Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// FlushHeld merges channel's held deliveries, if any, into one pending
// digest due now.
func (s *Store) FlushHeld(ctx context.Context, channel uuid.UUID, now time.Time, maxAttempts int) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, tenant_id, alert_ids FROM alert_delivery
			WHERE channel_id = $1 AND status = 'held' FOR UPDATE`, channel)
		if err != nil {
			return err
		}
		var deliveryIDs []uuid.UUID
		var tenant uuid.UUID
		seen := map[uuid.UUID]bool{}
		var alertIDs []uuid.UUID
		for rows.Next() {
			var did uuid.UUID
			var ids []uuid.UUID
			if err := rows.Scan(&did, &tenant, &ids); err != nil {
				rows.Close()
				return err
			}
			deliveryIDs = append(deliveryIDs, did)
			for _, id := range ids {
				if !seen[id] {
					seen[id] = true
					alertIDs = append(alertIDs, id)
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(deliveryIDs) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM alert_delivery WHERE id = ANY ($1)`, deliveryIDs); err != nil {
			return err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		kind := alert.DeliveryDigest
		if len(alertIDs) == 1 {
			// A single held alert doesn't need summarising as a digest,
			// but it's still sent as one message the same way; kind
			// controls only the wording (e.g. a webhook channel's event
			// type), so DeliveryDigest is fine either way. Kept explicit
			// for clarity rather than reusing the original fired/reminder
			// kind, which FlushHeld no longer knows for certain (several
			// held rows of different kinds may have merged).
			kind = alert.DeliveryDigest
		}
		_, err = tx.Exec(ctx, `INSERT INTO alert_delivery (id, tenant_id, channel_id, alert_ids, kind, status,
			attempts, max_attempts, next_attempt_at, created_at) VALUES ($1, $2, $3, $4, $5, 'pending', 0, $6, $7, $7)`,
			id, tenant, channel, alertIDs, kind, maxAttempts, now)
		return err
	})
}

// ClaimDeliveries leases up to limit due deliveries of enabled channels,
// with the alerts each one covers.
func (s *Store) ClaimAlertDeliveries(ctx context.Context, now, leaseUntil time.Time, limit int) ([]alert.Job, error) {
	rows, err := s.pool.Query(ctx, `WITH due AS (
			SELECT d.id FROM alert_delivery d JOIN alert_channel c ON c.id = d.channel_id
			WHERE d.status = 'pending' AND d.next_attempt_at <= $1 AND c.enabled
			ORDER BY d.next_attempt_at LIMIT $3
			FOR UPDATE OF d SKIP LOCKED
		), claimed AS (
			UPDATE alert_delivery d SET next_attempt_at = $2 FROM due WHERE d.id = due.id
			RETURNING d.*
		)
		SELECT cl.id, cl.tenant_id, cl.channel_id, cl.alert_ids, cl.kind, cl.status, cl.attempts, cl.max_attempts,
			cl.next_attempt_at, cl.created_at, cl.finished_at, ch.kind, ch.config_enc
		FROM claimed cl JOIN alert_channel ch ON ch.id = cl.channel_id`, now, leaseUntil, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []alert.Job
	allAlertIDs := map[uuid.UUID]bool{}
	for rows.Next() {
		var j alert.Job
		var attempts, maxAttempts int16
		if err := rows.Scan(&j.ID, &j.TenantID, &j.ChannelID, &j.AlertIDs, &j.Kind, &j.Status, &attempts,
			&maxAttempts, &j.NextAttemptAt, &j.CreatedAt, &j.FinishedAt, &j.ChannelKind, &j.ConfigEnc); err != nil {
			return nil, err
		}
		j.Attempts, j.MaxAttempts = int(attempts), int(maxAttempts)
		for _, id := range j.AlertIDs {
			allAlertIDs[id] = true
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, len(allAlertIDs))
	for id := range allAlertIDs {
		ids = append(ids, id)
	}
	arows, err := s.pool.Query(ctx, `SELECT `+alertColumns+` FROM alert WHERE id = ANY ($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	byID := map[uuid.UUID]alert.Alert{}
	for arows.Next() {
		a, err := scanAlert(arows)
		if err != nil {
			return nil, err
		}
		byID[a.ID] = a
	}
	if err := arows.Err(); err != nil {
		return nil, err
	}
	for i := range jobs {
		for _, id := range jobs[i].AlertIDs {
			if a, ok := byID[id]; ok {
				jobs[i].Alerts = append(jobs[i].Alerts, a)
			}
		}
	}
	return jobs, nil
}

// errAlertDeliveryGone marks a poisoned transaction so pgx.BeginFunc rolls
// it back (a Postgres transaction can't continue, let alone commit, once a
// statement in it has failed); RecordAlertAttempt then turns this specific
// error back into success, the same way store.RecordAttempt does for webhooks.
var errAlertDeliveryGone = errors.New("alert delivery no longer exists")

func (s *Store) RecordAlertAttempt(ctx context.Context, o alert.Outcome) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		a := o.Attempt
		if _, err := tx.Exec(ctx, `INSERT INTO alert_attempt (id, delivery_id, at, status_code, duration_ms,
			response_excerpt, error) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			id, o.DeliveryID, a.At, a.StatusCode, a.DurationMS, a.ResponseExcerpt, a.Error); err != nil {
			if isForeignKeyViolation(err) {
				return errAlertDeliveryGone // the channel was deleted meanwhile
			}
			return fmt.Errorf("logging alert attempt: %w", err)
		}
		var finished *time.Time
		if o.Status != alert.DeliveryPending {
			finished = &a.At
		}
		_, err = tx.Exec(ctx, `UPDATE alert_delivery SET status = $2, attempts = $3, next_attempt_at = $4,
			finished_at = $5 WHERE id = $1 AND status = 'pending'`,
			o.DeliveryID, o.Status, o.Attempts, o.NextAttemptAt, finished)
		return err
	})
	if errors.Is(err, errAlertDeliveryGone) {
		return nil
	}
	return err
}

// Cleanup keeps the alert delivery log to its retention. Alert rows
// (open/resolved history) aren't pruned here.
func (s *Store) CleanupAlerts(ctx context.Context, cutoff time.Time) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM alert_delivery
		WHERE status IN ('succeeded', 'failed', 'cancelled') AND created_at < $1`, cutoff)
	return err
}
