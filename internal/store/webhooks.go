package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/webhook"
)

var _ webhook.Store = (*Store)(nil)

// InsertEvent adds an event to the outbox. Call it inside the transaction
// that makes the change the event describes (docs/API.md §4 "Outbox").
func InsertEvent(ctx context.Context, tx pgx.Tx, ev webhook.Event) error {
	return insertEvent(ctx, tx, ev, nil)
}

func insertEvent(ctx context.Context, db execer, ev webhook.Event, dispatchedAt *time.Time) error {
	_, err := db.Exec(ctx, `INSERT INTO event_outbox (id, tenant_id, type, body, created_at, dispatched_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, ev.ID, ev.TenantID, ev.Type, ev.Body, ev.CreatedAt, dispatchedAt)
	if err != nil {
		return fmt.Errorf("inserting event: %w", err)
	}
	return nil
}

const endpointColumns = `id, tenant_id, url, description, event_types, enabled, disabled_reason, disabled_at,
	secret_enc, previous_secret_enc, previous_secret_expires_at, failing_since, last_success_at,
	version, created_by, created_at, updated_at`

func scanEndpoint(row pgx.Row) (webhook.Endpoint, error) {
	var e webhook.Endpoint
	err := row.Scan(&e.ID, &e.TenantID, &e.URL, &e.Description, &e.EventTypes, &e.Enabled, &e.DisabledReason,
		&e.DisabledAt, &e.SecretEnc, &e.PreviousSecretEnc, &e.PreviousSecretExpiresAt, &e.FailingSince,
		&e.LastSuccessAt, &e.Version, &e.CreatedBy, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, webhook.ErrNotFound
	}
	return e, err
}

// eventTypes is e's subscription for the NOT NULL column: empty (every
// event) when nil.
func eventTypes(e webhook.Endpoint) []string {
	if e.EventTypes == nil {
		return []string{}
	}
	return e.EventTypes
}

func (s *Store) CreateEndpoint(ctx context.Context, e webhook.Endpoint, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO webhook_endpoint (id, tenant_id, url, description, event_types, enabled,
			disabled_reason, disabled_at, secret_enc, version, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			e.ID, e.TenantID, e.URL, e.Description, eventTypes(e), e.Enabled, e.DisabledReason, e.DisabledAt,
			e.SecretEnc, e.Version, e.CreatedBy, e.CreatedAt, e.UpdatedAt)
		if err != nil {
			return fmt.Errorf("creating webhook endpoint: %w", err)
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) Endpoint(ctx context.Context, tenant, id uuid.UUID) (webhook.Endpoint, error) {
	return scanEndpoint(s.pool.QueryRow(ctx,
		`SELECT `+endpointColumns+` FROM webhook_endpoint WHERE id = $1 AND tenant_id = $2`, id, tenant))
}

func (s *Store) ListEndpoints(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]webhook.Endpoint, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+endpointColumns+` FROM webhook_endpoint
		WHERE tenant_id = $1 AND ($2::uuid IS NULL OR id < $2) ORDER BY id DESC LIMIT $3`, tenant, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []webhook.Endpoint{}
	for rows.Next() {
		e, err := scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpdateEndpoint writes the fields an admin changes (and the secrets); the
// worker's health fields are left alone except that turning an endpoint
// back on clears its failure run.
func (s *Store) UpdateEndpoint(ctx context.Context, e webhook.Endpoint, audit auth.AuditEntry) (webhook.Endpoint, error) {
	var out webhook.Endpoint
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanEndpoint(tx.QueryRow(ctx, `UPDATE webhook_endpoint SET
				url = $4, description = $5, event_types = $6,
				failing_since = CASE WHEN $7 AND NOT enabled THEN NULL ELSE failing_since END,
				enabled = $7, disabled_reason = $8, disabled_at = $9,
				secret_enc = $10, previous_secret_enc = $11, previous_secret_expires_at = $12,
				version = version + 1, updated_at = $13
			WHERE id = $1 AND tenant_id = $2 AND version = $3
			RETURNING `+endpointColumns,
			e.ID, e.TenantID, e.Version, e.URL, e.Description, eventTypes(e), e.Enabled, e.DisabledReason,
			e.DisabledAt, e.SecretEnc, e.PreviousSecretEnc, e.PreviousSecretExpiresAt, e.UpdatedAt))
		if errors.Is(err, webhook.ErrNotFound) {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM webhook_endpoint WHERE id = $1 AND tenant_id = $2)`,
				e.ID, e.TenantID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return webhook.ErrVersionChanged
			}
			return webhook.ErrNotFound
		}
		if err != nil {
			return err
		}
		if !out.Enabled {
			if err := cancelPending(ctx, tx, out.ID, e.UpdatedAt); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

func cancelPending(ctx context.Context, db execer, endpoint uuid.UUID, at time.Time) error {
	_, err := db.Exec(ctx, `UPDATE webhook_delivery SET status = 'cancelled', next_attempt_at = NULL, finished_at = $2
		WHERE endpoint_id = $1 AND status = 'pending'`, endpoint, at)
	return err
}

func (s *Store) DeleteEndpoint(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM webhook_endpoint WHERE id = $1 AND tenant_id = $2`, id, tenant)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return webhook.ErrNotFound
		}
		return insertAudit(ctx, tx, audit)
	})
}

func insertDelivery(ctx context.Context, db execer, d webhook.Delivery) error {
	_, err := db.Exec(ctx, `INSERT INTO webhook_delivery (id, tenant_id, endpoint_id, event_id, event_type, status,
		attempts, max_attempts, next_attempt_at, replay_of, created_at) VALUES ($1, $2, $3, $4, $5, $6, 0, $7, $8, $9, $10)`,
		d.ID, d.TenantID, d.EndpointID, d.EventID, d.EventType, d.Status, d.MaxAttempts, d.NextAttemptAt, d.ReplayOf, d.CreatedAt)
	if err != nil {
		return fmt.Errorf("inserting webhook delivery: %w", err)
	}
	return nil
}

// CreateTestDelivery stores a test event, already dispatched so the worker
// doesn't fan it out, with its single delivery.
func (s *Store) CreateTestDelivery(ctx context.Context, ev webhook.Event, d webhook.Delivery, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := insertEvent(ctx, tx, ev, &ev.CreatedAt); err != nil {
			return err
		}
		if err := insertDelivery(ctx, tx, d); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

const deliveryColumns = `id, tenant_id, endpoint_id, event_id, event_type, status, attempts, max_attempts,
	next_attempt_at, replay_of, created_at, finished_at`

func scanDelivery(row pgx.Row) (webhook.Delivery, error) {
	var d webhook.Delivery
	var attempts, maxAttempts int16
	err := row.Scan(&d.ID, &d.TenantID, &d.EndpointID, &d.EventID, &d.EventType, &d.Status, &attempts, &maxAttempts,
		&d.NextAttemptAt, &d.ReplayOf, &d.CreatedAt, &d.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, webhook.ErrNotFound
	}
	d.Attempts, d.MaxAttempts = int(attempts), int(maxAttempts)
	return d, err
}

// Delivery returns a delivery with its attempts.
func (s *Store) Delivery(ctx context.Context, tenant, id uuid.UUID) (webhook.Delivery, error) {
	d, err := scanDelivery(s.pool.QueryRow(ctx,
		`SELECT `+deliveryColumns+` FROM webhook_delivery WHERE id = $1 AND tenant_id = $2`, id, tenant))
	if err != nil {
		return d, err
	}
	rows, err := s.pool.Query(ctx, `SELECT at, status_code, duration_ms, response_excerpt, error
		FROM webhook_attempt WHERE delivery_id = $1 ORDER BY at, id`, id)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	d.Log = []webhook.Attempt{}
	for rows.Next() {
		var a webhook.Attempt
		var code *int16
		if err := rows.Scan(&a.At, &code, &a.DurationMS, &a.ResponseExcerpt, &a.Error); err != nil {
			return d, err
		}
		if code != nil {
			c := int(*code)
			a.StatusCode = &c
		}
		d.Log = append(d.Log, a)
	}
	return d, rows.Err()
}

func (s *Store) ListDeliveries(ctx context.Context, tenant, endpoint uuid.UUID, status string, before *uuid.UUID, limit int) ([]webhook.Delivery, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+deliveryColumns+` FROM webhook_delivery
		WHERE tenant_id = $1 AND endpoint_id = $2 AND ($3 = '' OR status = $3) AND ($4::uuid IS NULL OR id < $4)
		ORDER BY id DESC LIMIT $5`, tenant, endpoint, status, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []webhook.Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) ReplayDelivery(ctx context.Context, _ webhook.Delivery, d webhook.Delivery, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := insertDelivery(ctx, tx, d); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) ReplayFailed(ctx context.Context, e webhook.Endpoint, since, now time.Time, maxAttempts, limit int, audit auth.AuditEntry) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The newest failed or cancelled delivery of each event, unless the
		// event is already pending or delivered to this endpoint.
		rows, err := tx.Query(ctx, `SELECT DISTINCT ON (d.event_id) d.id, d.event_id, d.event_type
			FROM webhook_delivery d
			WHERE d.endpoint_id = $1 AND d.tenant_id = $2 AND d.status IN ('failed', 'cancelled')
				AND d.created_at >= $3 AND d.event_type <> $4
				AND NOT EXISTS (SELECT 1 FROM webhook_delivery o WHERE o.endpoint_id = d.endpoint_id
					AND o.event_id = d.event_id AND o.status IN ('pending', 'succeeded'))
			ORDER BY d.event_id, d.id DESC LIMIT $5`, e.ID, e.TenantID, since, webhook.TestEventType, limit)
		if err != nil {
			return err
		}
		var batch pgx.Batch
		for rows.Next() {
			var orig, event uuid.UUID
			var eventType string
			if err := rows.Scan(&orig, &event, &eventType); err != nil {
				rows.Close()
				return err
			}
			id, err := uuid.NewV7()
			if err != nil {
				rows.Close()
				return err
			}
			batch.Queue(`INSERT INTO webhook_delivery (id, tenant_id, endpoint_id, event_id, event_type, status,
				attempts, max_attempts, next_attempt_at, replay_of, created_at)
				VALUES ($1, $2, $3, $4, $5, 'pending', 0, $6, $7, $8, $7)`,
				id, e.TenantID, e.ID, event, eventType, maxAttempts, now, orig)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		n = batch.Len()
		if n > 0 {
			if err := tx.SendBatch(ctx, &batch).Close(); err != nil {
				return fmt.Errorf("queueing replays: %w", err)
			}
		}
		audit.Detail = map[string]any{"since": since, "queued": n}
		return insertAudit(ctx, tx, audit)
	})
	return n, err
}

// DispatchEvents turns up to limit undispatched events into one pending
// delivery per enabled, subscribed endpoint of the event's tenant.
func (s *Store) DispatchEvents(ctx context.Context, now time.Time, maxAttempts, limit int) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, tenant_id, type FROM event_outbox WHERE dispatched_at IS NULL
			ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		type ev struct {
			id, tenant uuid.UUID
			typ        string
		}
		var events []ev
		for rows.Next() {
			var e ev
			if err := rows.Scan(&e.id, &e.tenant, &e.typ); err != nil {
				rows.Close()
				return err
			}
			events = append(events, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(events) == 0 {
			return err
		}
		ids := make([]uuid.UUID, 0, len(events))
		for _, e := range events {
			ids = append(ids, e.id)
			rows, err := tx.Query(ctx, `SELECT id FROM webhook_endpoint WHERE tenant_id = $1 AND enabled
				AND (cardinality(event_types) = 0 OR $2 = ANY (event_types))`, e.tenant, e.typ)
			if err != nil {
				return err
			}
			endpoints, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
			if err != nil {
				return err
			}
			for _, ep := range endpoints {
				id, err := uuid.NewV7()
				if err != nil {
					return err
				}
				if err := insertDelivery(ctx, tx, webhook.Delivery{
					ID: id, TenantID: e.tenant, EndpointID: ep, EventID: e.id, EventType: e.typ,
					Status: webhook.StatusPending, MaxAttempts: maxAttempts, NextAttemptAt: &now, CreatedAt: now,
				}); err != nil {
					return err
				}
			}
		}
		n = len(events)
		_, err = tx.Exec(ctx, `UPDATE event_outbox SET dispatched_at = $2 WHERE id = ANY ($1)`, ids, now)
		return err
	})
	return n, err
}

// ClaimDeliveries leases up to limit due deliveries of enabled endpoints.
func (s *Store) ClaimDeliveries(ctx context.Context, now, leaseUntil time.Time, limit int) ([]webhook.Job, error) {
	rows, err := s.pool.Query(ctx, `WITH due AS (
			SELECT d.id FROM webhook_delivery d JOIN webhook_endpoint e ON e.id = d.endpoint_id
			WHERE d.status = 'pending' AND d.next_attempt_at <= $1 AND e.enabled
			ORDER BY d.next_attempt_at LIMIT $3
			FOR UPDATE OF d SKIP LOCKED
		), claimed AS (
			UPDATE webhook_delivery d SET next_attempt_at = $2 FROM due WHERE d.id = due.id
			RETURNING d.*
		)
		SELECT c.id, c.tenant_id, c.endpoint_id, c.event_id, c.event_type, c.status, c.attempts, c.max_attempts,
			c.next_attempt_at, c.replay_of, c.created_at, c.finished_at,
			e.url, e.secret_enc, e.previous_secret_enc, e.previous_secret_expires_at, ev.body
		FROM claimed c
		JOIN webhook_endpoint e ON e.id = c.endpoint_id
		JOIN event_outbox ev ON ev.id = c.event_id`, now, leaseUntil, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []webhook.Job
	for rows.Next() {
		var j webhook.Job
		var attempts, maxAttempts int16
		if err := rows.Scan(&j.ID, &j.TenantID, &j.EndpointID, &j.EventID, &j.EventType, &j.Status, &attempts,
			&maxAttempts, &j.NextAttemptAt, &j.ReplayOf, &j.CreatedAt, &j.FinishedAt,
			&j.URL, &j.SecretEnc, &j.PreviousSecretEnc, &j.PreviousSecretExpiresAt, &j.Body); err != nil {
			return nil, err
		}
		j.Attempts, j.MaxAttempts = int(attempts), int(maxAttempts)
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// RecordAttempt logs the attempt, moves the delivery on, and tracks the
// endpoint's health (not for test messages).
func (s *Store) RecordAttempt(ctx context.Context, o webhook.Outcome, failingCutoff time.Time) (string, error) {
	disabled := ""
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		a := o.Attempt
		if _, err := tx.Exec(ctx, `INSERT INTO webhook_attempt (id, delivery_id, at, status_code, duration_ms,
			response_excerpt, error) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			id, o.DeliveryID, a.At, a.StatusCode, a.DurationMS, a.ResponseExcerpt, a.Error); err != nil {
			if isForeignKeyViolation(err) {
				return errGone // the endpoint was deleted meanwhile
			}
			return fmt.Errorf("logging webhook attempt: %w", err)
		}
		var finished *time.Time
		if o.Status != webhook.StatusPending {
			finished = &a.At
		}
		// A delivery cancelled meanwhile stays cancelled.
		if _, err := tx.Exec(ctx, `UPDATE webhook_delivery SET status = $2, attempts = $3, next_attempt_at = $4,
			finished_at = $5 WHERE id = $1 AND status = 'pending'`,
			o.DeliveryID, o.Status, o.Attempts, o.NextAttemptAt, finished); err != nil {
			return err
		}
		if o.EventType == webhook.TestEventType {
			return nil
		}

		if o.Succeeded {
			_, err := tx.Exec(ctx, `UPDATE webhook_endpoint SET failing_since = NULL, last_success_at = $2
				WHERE id = $1`, o.EndpointID, a.At)
			return err
		}
		var failingSince *time.Time
		var enabled bool
		err = tx.QueryRow(ctx, `UPDATE webhook_endpoint SET failing_since = COALESCE(failing_since, $2)
			WHERE id = $1 RETURNING failing_since, enabled`, o.EndpointID, a.At).Scan(&failingSince, &enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil || !enabled {
			return err
		}
		reason := ""
		switch {
		case o.Gone:
			reason = webhook.DisabledGone
		case failingSince != nil && !failingSince.After(failingCutoff):
			reason = webhook.DisabledFailing
		default:
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE webhook_endpoint SET enabled = false, disabled_reason = $2,
			disabled_at = $3, version = version + 1, updated_at = $3 WHERE id = $1`, o.EndpointID, reason, a.At); err != nil {
			return err
		}
		if err := cancelPending(ctx, tx, o.EndpointID, a.At); err != nil {
			return err
		}
		disabled = reason
		return insertAudit(ctx, tx, auth.AuditEntry{
			TenantID: &o.TenantID, Actor: "system", Action: "webhook.disable",
			Target: "webhook:" + o.EndpointID.String(), Result: auth.ResultOK,
			Detail: map[string]any{"reason": reason, "failing_since": failingSince},
		})
	})
	if errors.Is(err, errGone) {
		return "", nil
	}
	return disabled, err
}

var errGone = errors.New("delivery no longer exists")

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// Cleanup keeps the delivery log to its retention and forgets previous
// secrets once their overlap has ended.
func (s *Store) Cleanup(ctx context.Context, cutoff, now time.Time) error {
	// Nothing is legitimately still pending after 30 days (retries end
	// after ≈ 27 h); a leftover would be a crashed test to a turned-off endpoint.
	if _, err := s.pool.Exec(ctx, `DELETE FROM webhook_delivery WHERE created_at < $1`, cutoff); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM event_outbox e WHERE e.created_at < $1 AND e.dispatched_at IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM webhook_delivery d WHERE d.event_id = e.id)`, cutoff); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE webhook_endpoint SET previous_secret_enc = NULL, previous_secret_expires_at = NULL
		WHERE previous_secret_expires_at < $1`, now)
	return err
}

func (s *Store) ListAllowlist(ctx context.Context) ([]webhook.AllowlistEntry, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, cidr::text, host, description, created_by, created_at
		FROM outbound_allowlist ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []webhook.AllowlistEntry{}
	for rows.Next() {
		var e webhook.AllowlistEntry
		if err := rows.Scan(&e.ID, &e.CIDR, &e.Host, &e.Description, &e.CreatedBy, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) CreateAllowlistEntry(ctx context.Context, e webhook.AllowlistEntry, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO outbound_allowlist (id, cidr, host, description, created_by, created_at)
			VALUES ($1, $2::cidr, $3, $4, $5, $6)`, e.ID, e.CIDR, e.Host, e.Description, e.CreatedBy, e.CreatedAt)
		if IsUniqueViolation(err) {
			return webhook.ErrDuplicate
		}
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) DeleteAllowlistEntry(ctx context.Context, id uuid.UUID, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM outbound_allowlist WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return webhook.ErrNotFound
		}
		return insertAudit(ctx, tx, audit)
	})
}
