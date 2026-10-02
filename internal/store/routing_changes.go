package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/routing"
)

// Undo for call routing (ADR-071, migration 0038). Every routing change
// keeps the routing as it was just before it, in its own transaction, so
// the snapshot and the change can't disagree; a per-tenant lock puts
// routing changes in a line. Putting a version back restores those rows
// and is itself a change.

// KeepRoutingChanges is how many changes are kept.
const KeepRoutingChanges = 50

// routingSnapshot is the routing's rows as one JSON object. Lines and
// numbers themselves aren't routing (only who they ring and the order
// outgoing calls try lines), nor are calling levels' existence (only what
// each allows).
const routingSnapshot = `SELECT jsonb_build_object(
	'ring_group', coalesce((SELECT jsonb_agg(to_jsonb(g) ORDER BY g.id) FROM ring_group g WHERE g.tenant_id = $1), '[]'),
	'ring_group_member', coalesce((SELECT jsonb_agg(to_jsonb(m) ORDER BY m.ring_group_id, m.position)
		FROM ring_group_member m JOIN ring_group g ON g.id = m.ring_group_id WHERE g.tenant_id = $1), '[]'),
	'schedule', coalesce((SELECT jsonb_agg(to_jsonb(sc) ORDER BY sc.id) FROM schedule sc WHERE sc.tenant_id = $1), '[]'),
	'schedule_span', coalesce((SELECT jsonb_agg(to_jsonb(sp) ORDER BY sp.schedule_id, sp.weekday, sp.opens)
		FROM schedule_span sp JOIN schedule sc ON sc.id = sp.schedule_id WHERE sc.tenant_id = $1), '[]'),
	'schedule_holiday', coalesce((SELECT jsonb_agg(to_jsonb(h) ORDER BY h.schedule_id, h.position)
		FROM schedule_holiday h JOIN schedule sc ON sc.id = h.schedule_id WHERE sc.tenant_id = $1), '[]'),
	'incoming_rule', coalesce((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.did_id, r.trunk_id) FROM incoming_rule r WHERE r.tenant_id = $1), '[]'),
	'trunk_did', coalesce((SELECT jsonb_agg(jsonb_build_object('id', d.id, 'extension_id', d.extension_id, 'ring_group_id', d.ring_group_id) ORDER BY d.id)
		FROM trunk_did d WHERE d.tenant_id = $1), '[]'),
	'trunk', coalesce((SELECT jsonb_agg(jsonb_build_object('id', t.id, 'rings_extension_id', t.rings_extension_id,
			'rings_ring_group_id', t.rings_ring_group_id, 'outbound_priority', t.outbound_priority) ORDER BY t.id)
		FROM trunk t WHERE t.tenant_id = $1), '[]'),
	'call_permission_level', coalesce((SELECT jsonb_agg(jsonb_build_object('id', l.id, 'name', l.name,
			'allowed_categories', l.allowed_categories, 'withhold_caller_id', l.withhold_caller_id) ORDER BY l.id)
		FROM call_permission_level l WHERE l.tenant_id = $1), '[]'))`

func lockRouting(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('linx-routing:' || $1::text, 0))`, tenant)
	return err
}

// RoutingWords puts the routing st reads into sentences
// (routing.Service.Words); the control plane sets it, and without it
// changes are kept with no words.
type RoutingWords func(ctx context.Context, st routing.StateStore, tenant uuid.UUID) ([]routing.Item, error)

// SetRoutingWords sets how routing changes are put into words.
func (s *Store) SetRoutingWords(fn RoutingWords) { s.routingWords = fn }

func (s *Store) wordsIn(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) (any, error) {
	if s.routingWords == nil {
		return nil, nil
	}
	words, err := s.routingWords(ctx, &Store{pool: tx}, tenant)
	if err != nil {
		return nil, fmt.Errorf("routing in words: %w", err)
	}
	return words, nil
}

// changeRouting runs change in tx as a routing change, which it returns:
// it keeps the routing as it was (its rows, to put back, and in words),
// runs change, keeps the words after, and drops the oldest changes beyond
// KeepRoutingChanges. A per-tenant lock puts routing changes in a line.
func (s *Store) changeRouting(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, audit auth.AuditEntry, putBackOf *uuid.UUID, putBackAt *time.Time, change func() error) (uuid.UUID, error) {
	if err := lockRouting(ctx, tx, tenant); err != nil {
		return uuid.Nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}
	before, err := s.wordsIn(ctx, tx, tenant)
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO routing_change (id, tenant_id, at, actor, action, target, before, before_words, put_back_of, put_back_at)
		VALUES ($2, $1, clock_timestamp(), $3, $4, $5, (`+routingSnapshot+`), $6, $7, $8)`,
		tenant, id, audit.Actor, audit.Action, audit.Target, before, putBackOf, putBackAt); err != nil {
		return uuid.Nil, fmt.Errorf("keeping the routing before a change: %w", err)
	}
	if err := change(); err != nil {
		return uuid.Nil, err
	}
	after, err := s.wordsIn(ctx, tx, tenant)
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE routing_change SET after_words = $2 WHERE id = $1`, id, after); err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM routing_change WHERE tenant_id = $1 AND id NOT IN (
		SELECT id FROM routing_change WHERE tenant_id = $1 ORDER BY at DESC, id DESC LIMIT $2)`, tenant, KeepRoutingChanges); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// routingTx runs fn in a transaction as a routing change, and tells the
// request which change it made (routing.NoteChange).
func (s *Store) routingTx(ctx context.Context, tenant uuid.UUID, audit auth.AuditEntry, fn func(pgx.Tx) error) error {
	var change uuid.UUID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		change, err = s.changeRouting(ctx, tx, tenant, audit, nil, nil, func() error { return fn(tx) })
		return err
	})
	if err == nil {
		routing.NoteChange(ctx, change)
	}
	return err
}

// routingTxIf is routingTx for a save that changes routing only
// sometimes (a line's or number's other settings): changed says, in the
// transaction, whether this one does.
func (s *Store) routingTxIf(ctx context.Context, tenant uuid.UUID, audit auth.AuditEntry, changed func(pgx.Tx) (bool, error), fn func(pgx.Tx) error) error {
	var change uuid.UUID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockRouting(ctx, tx, tenant); err != nil {
			return err
		}
		yes, err := changed(tx)
		if err != nil || !yes {
			if err != nil {
				return err
			}
			return fn(tx)
		}
		change, err = s.changeRouting(ctx, tx, tenant, audit, nil, nil, func() error { return fn(tx) })
		return err
	})
	if err == nil && change != uuid.Nil {
		routing.NoteChange(ctx, change)
	}
	return err
}

// RoutingChanges returns the kept changes, newest first, with who made
// each by name ("" for an API key, an app or the server).
func (s *Store) RoutingChanges(ctx context.Context, tenant uuid.UUID) ([]routing.Change, error) {
	rows, err := s.pool.Query(ctx, `SELECT c.id, c.at, c.actor, coalesce(u.name, ''), c.action, c.target, c.put_back_of, c.put_back_at,
			c.before_words, c.after_words
		FROM routing_change c
		LEFT JOIN app_user u ON c.actor = 'user:' || u.id::text
		WHERE c.tenant_id = $1 ORDER BY c.at DESC, c.id DESC`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []routing.Change{}
	for rows.Next() {
		var c routing.Change
		if err := rows.Scan(&c.ID, &c.At, &c.Actor, &c.ActorName, &c.Action, &c.Target, &c.PutBackOf, &c.PutBackAt,
			&c.BeforeWords, &c.AfterWords); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// WithRoutingBefore runs fn on the routing as it was just before change
// id: the version is put back in a transaction that is always rolled
// back, so fn reads it with the usual queries. It returns the newest
// change, as it was then. routing.ErrNotFound when the change isn't
// kept; a *routing.PutBackError when it can't be put back as things are
// now.
func (s *Store) WithRoutingBefore(ctx context.Context, tenant, id uuid.UUID, fn func(routing.StateStore) error) (uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := lockRouting(ctx, tx, tenant); err != nil {
		return uuid.Nil, err
	}
	newest, err := newestRoutingChange(ctx, tx, tenant)
	if err != nil {
		return uuid.Nil, err
	}
	before, err := routingBefore(ctx, tx, tenant, id)
	if err != nil {
		return uuid.Nil, err
	}
	if err := restoreRouting(ctx, tx, tenant, before); err != nil {
		return uuid.Nil, err
	}
	return newest, fn(&Store{pool: tx})
}

func newestRoutingChange(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM routing_change WHERE tenant_id = $1 ORDER BY at DESC, id DESC LIMIT 1`, tenant).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, routing.ErrNotFound
	}
	return id, err
}

func routingBefore(ctx context.Context, tx pgx.Tx, tenant, id uuid.UUID) ([]byte, error) {
	var before []byte
	err := tx.QueryRow(ctx, `SELECT before FROM routing_change WHERE id = $1 AND tenant_id = $2`, id, tenant).Scan(&before)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, routing.ErrNotFound
	}
	return before, err
}

// PutRoutingBack puts back the routing as it was just before change id,
// as a new change (which it returns), only while newest is still the
// newest change (routing.ErrVersionChanged otherwise): what the caller
// checked (and "Saved. Undo", which passes id itself) must be what's
// replaced, never someone else's later change.
func (s *Store) PutRoutingBack(ctx context.Context, tenant, id, newest uuid.UUID, audit auth.AuditEntry) (uuid.UUID, error) {
	var change uuid.UUID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockRouting(ctx, tx, tenant); err != nil {
			return err
		}
		var at time.Time
		err := tx.QueryRow(ctx, `SELECT at FROM routing_change WHERE id = $1 AND tenant_id = $2`, id, tenant).Scan(&at)
		if errors.Is(err, pgx.ErrNoRows) {
			return routing.ErrNotFound
		}
		if err != nil {
			return err
		}
		if now, err := newestRoutingChange(ctx, tx, tenant); err != nil || now != newest {
			if err != nil {
				return err
			}
			return routing.ErrVersionChanged
		}
		before, err := routingBefore(ctx, tx, tenant, id)
		if err != nil {
			return err
		}
		change, err = s.changeRouting(ctx, tx, tenant, audit, &id, &at, func() error {
			return restoreRouting(ctx, tx, tenant, before)
		})
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
	if err != nil {
		return uuid.Nil, err
	}
	routing.NoteChange(ctx, change)
	return change, nil
}

// restoreRouting makes the routing tables say what snap says. What the
// version had and is gone now comes back (a ring group with an empty
// voicemail box: its messages went with it); what was added since goes
// (a ring group with its box and messages). A person removed since is
// left out of groups, and calls that went to them get "not available",
// as when they were removed. Lines and numbers added since keep who they
// ring unless it's a group or schedule that goes, and keep their place
// after the version's lines for outgoing calls.
func restoreRouting(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, snap []byte) error {
	steps := []string{
		// Groups and schedules the version had come back, under a
		// placeholder name; every group and schedule then has a
		// placeholder name, no number and sends its unanswered calls to
		// the message, so the version's names and numbers can be set in
		// any order (two swapped) and what goes can be removed.
		`INSERT INTO ring_group (id, tenant_id, name, strategy, ring_seconds, turn_seconds, no_answer_kind, no_answer_message, created_at, updated_at)
			SELECT s.id, $1, s.id::text, s.strategy, s.ring_seconds, s.turn_seconds, 'message', 'not-available', s.created_at, now()
			FROM jsonb_populate_recordset(NULL::ring_group, $2::jsonb->'ring_group') s
			WHERE NOT EXISTS (SELECT 1 FROM ring_group g WHERE g.id = s.id)`,
		`UPDATE ring_group SET name = id::text, number = NULL, no_answer_kind = 'message', no_answer_extension_id = NULL,
			no_answer_ring_group_id = NULL, no_answer_voicemail_id = NULL, no_answer_message = 'not-available'
			WHERE tenant_id = $1`,
		`INSERT INTO schedule (id, tenant_id, name, created_at, updated_at)
			SELECT s.id, $1, s.id::text, s.created_at, now()
			FROM jsonb_populate_recordset(NULL::schedule, $2::jsonb->'schedule') s
			WHERE NOT EXISTS (SELECT 1 FROM schedule sc WHERE sc.id = s.id)`,
		`UPDATE schedule SET name = id::text WHERE tenant_id = $1`,
		`DELETE FROM schedule_span WHERE schedule_id IN (SELECT id FROM schedule WHERE tenant_id = $1)`,
		`INSERT INTO schedule_span SELECT s.* FROM jsonb_populate_recordset(NULL::schedule_span, $2::jsonb->'schedule_span') s
			WHERE s.schedule_id IN (SELECT id FROM schedule WHERE tenant_id = $1)`,
		`DELETE FROM schedule_holiday WHERE schedule_id IN (SELECT id FROM schedule WHERE tenant_id = $1)`,
		`INSERT INTO schedule_holiday SELECT s.* FROM jsonb_populate_recordset(NULL::schedule_holiday, $2::jsonb->'schedule_holiday') s
			WHERE s.schedule_id IN (SELECT id FROM schedule WHERE tenant_id = $1)`,
		`UPDATE schedule sc SET name = s.name, version = sc.version + 1, updated_at = now()
			FROM jsonb_populate_recordset(NULL::schedule, $2::jsonb->'schedule') s WHERE sc.id = s.id AND sc.tenant_id = $1`,

		// Who numbers and lines ring.
		`UPDATE trunk_did d SET extension_id = (SELECT e.id FROM extension e WHERE e.id = s.extension_id AND e.deleted_at IS NULL),
				ring_group_id = s.ring_group_id, version = d.version + 1, updated_at = now()
			FROM jsonb_populate_recordset(NULL::trunk_did, $2::jsonb->'trunk_did') s
			WHERE d.id = s.id AND d.tenant_id = $1
				AND (d.extension_id IS DISTINCT FROM s.extension_id OR d.ring_group_id IS DISTINCT FROM s.ring_group_id)`,
		`UPDATE trunk t SET rings_extension_id = (SELECT e.id FROM extension e WHERE e.id = s.rings_extension_id AND e.deleted_at IS NULL),
				rings_ring_group_id = s.rings_ring_group_id, version = t.version + 1, updated_at = now()
			FROM jsonb_populate_recordset(NULL::trunk, $2::jsonb->'trunk') s
			WHERE t.id = s.id AND t.tenant_id = $1
				AND (t.rings_extension_id IS DISTINCT FROM s.rings_extension_id OR t.rings_ring_group_id IS DISTINCT FROM s.rings_ring_group_id)`,
		`UPDATE trunk_did SET ring_group_id = NULL, version = version + 1, updated_at = now()
			WHERE tenant_id = $1 AND ring_group_id IS NOT NULL
				AND ring_group_id NOT IN (SELECT s.id FROM jsonb_populate_recordset(NULL::ring_group, $2::jsonb->'ring_group') s)`,
		`UPDATE trunk SET rings_ring_group_id = NULL, version = version + 1, updated_at = now()
			WHERE tenant_id = $1 AND rings_ring_group_id IS NOT NULL
				AND rings_ring_group_id NOT IN (SELECT s.id FROM jsonb_populate_recordset(NULL::ring_group, $2::jsonb->'ring_group') s)`,

		// "When someone calls" of the version's numbers and lines.
		`DELETE FROM incoming_rule WHERE tenant_id = $1 AND (
			did_id IN (SELECT s.id FROM jsonb_populate_recordset(NULL::trunk_did, $2::jsonb->'trunk_did') s)
			OR trunk_id IN (SELECT s.id FROM jsonb_populate_recordset(NULL::trunk, $2::jsonb->'trunk') s))`,
		`INSERT INTO incoming_rule SELECT s.* FROM jsonb_populate_recordset(NULL::incoming_rule, $2::jsonb->'incoming_rule') s
			WHERE s.did_id IN (SELECT id FROM trunk_did WHERE tenant_id = $1) OR s.trunk_id IN (SELECT id FROM trunk WHERE tenant_id = $1)`,
		`UPDATE incoming_rule SET schedule_id = NULL WHERE tenant_id = $1 AND schedule_id IS NOT NULL
			AND schedule_id NOT IN (SELECT s.id FROM jsonb_populate_recordset(NULL::schedule, $2::jsonb->'schedule') s)`,

		// The groups as the version had them, and their people.
		`UPDATE ring_group g SET name = s.name, number = s.number, strategy = s.strategy, ring_seconds = s.ring_seconds,
				turn_seconds = s.turn_seconds, no_answer_kind = s.no_answer_kind, no_answer_extension_id = s.no_answer_extension_id,
				no_answer_ring_group_id = s.no_answer_ring_group_id, no_answer_voicemail_id = s.no_answer_voicemail_id,
				no_answer_message = s.no_answer_message, version = g.version + 1, updated_at = now()
			FROM jsonb_populate_recordset(NULL::ring_group, $2::jsonb->'ring_group') s WHERE g.id = s.id AND g.tenant_id = $1`,
		`UPDATE ring_group SET no_answer_kind = 'message', no_answer_extension_id = NULL, no_answer_message = 'not-available'
			WHERE tenant_id = $1 AND no_answer_extension_id IN (SELECT id FROM extension WHERE deleted_at IS NOT NULL)`,
		`DELETE FROM ring_group_member WHERE ring_group_id IN (SELECT id FROM ring_group WHERE tenant_id = $1)`,
		`INSERT INTO ring_group_member (ring_group_id, extension_id, position)
			SELECT s.ring_group_id, s.extension_id, row_number() OVER (PARTITION BY s.ring_group_id ORDER BY s.position)
			FROM jsonb_populate_recordset(NULL::ring_group_member, $2::jsonb->'ring_group_member') s
			JOIN extension e ON e.id = s.extension_id AND e.deleted_at IS NULL
			JOIN ring_group g ON g.id = s.ring_group_id AND g.tenant_id = $1`,
	}
	// Rules (the version's and newer ones) that send calls to a person
	// removed since, or to a group that goes, get the message.
	for _, part := range []string{"no_answer", "closed", "holiday"} {
		steps = append(steps, `UPDATE incoming_rule SET `+part+`_kind = 'message', `+part+`_extension_id = NULL,
				`+part+`_ring_group_id = NULL, `+part+`_voicemail_id = NULL, `+part+`_message = 'not-available'
			WHERE tenant_id = $1 AND (`+part+`_extension_id IN (SELECT id FROM extension WHERE deleted_at IS NOT NULL)
				OR `+part+`_ring_group_id NOT IN (SELECT s.id FROM jsonb_populate_recordset(NULL::ring_group, $2::jsonb->'ring_group') s)
				OR `+part+`_voicemail_id IN (SELECT b.id FROM voicemail_box b WHERE b.ring_group_id IS NOT NULL
					AND b.ring_group_id NOT IN (SELECT s.id FROM jsonb_populate_recordset(NULL::ring_group, $2::jsonb->'ring_group') s)))`)
	}
	steps = append(steps,
		// What the version didn't have goes.
		`DELETE FROM ring_group WHERE tenant_id = $1
			AND id NOT IN (SELECT s.id FROM jsonb_populate_recordset(NULL::ring_group, $2::jsonb->'ring_group') s)`,
		`DELETE FROM schedule WHERE tenant_id = $1
			AND id NOT IN (SELECT s.id FROM jsonb_populate_recordset(NULL::schedule, $2::jsonb->'schedule') s)`,

		// What each calling level that's still here allows.
		`UPDATE call_permission_level SET name = id::text WHERE tenant_id = $1
			AND id IN (SELECT s.id FROM jsonb_populate_recordset(NULL::call_permission_level, $2::jsonb->'call_permission_level') s)`,
		`UPDATE call_permission_level l SET name = s.name, allowed_categories = s.allowed_categories,
				withhold_caller_id = s.withhold_caller_id, version = l.version + 1, updated_at = now()
			FROM jsonb_populate_recordset(NULL::call_permission_level, $2::jsonb->'call_permission_level') s WHERE l.id = s.id AND l.tenant_id = $1`,
	)
	for _, sql := range steps {
		args := []any{tenant}
		if strings.Contains(sql, "$2") {
			args = append(args, snap)
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return putBackError(err)
		}
	}
	return restoreOutboundOrder(ctx, tx, tenant, snap)
}

// restoreOutboundOrder puts the version's order of lines back; a line
// added since keeps its place after them.
func restoreOutboundOrder(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, snap []byte) error {
	rows, err := tx.Query(ctx, `SELECT t.id FROM trunk t
		LEFT JOIN jsonb_populate_recordset(NULL::trunk, $2::jsonb->'trunk') s ON s.id = t.id
		WHERE t.tenant_id = $1 AND CASE WHEN s.id IS NULL THEN t.outbound_priority IS NOT NULL ELSE s.outbound_priority IS NOT NULL END
		ORDER BY s.id IS NULL, coalesce(s.outbound_priority, t.outbound_priority)`, tenant, snap)
	if err != nil {
		return err
	}
	order, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	var same bool
	if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(id ORDER BY outbound_priority), '{}') = $2::uuid[]
		FROM trunk WHERE tenant_id = $1 AND outbound_priority IS NOT NULL`, tenant, order).Scan(&same); err != nil {
		return err
	}
	if same {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE trunk SET outbound_priority = NULL, version = version + 1, updated_at = now()
		WHERE tenant_id = $1 AND outbound_priority IS NOT NULL`, tenant); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE trunk t SET outbound_priority = o.p, version = t.version + 1, updated_at = now()
		FROM unnest($2::uuid[]) WITH ORDINALITY AS o (id, p) WHERE t.id = o.id AND t.tenant_id = $1`, tenant, order)
	return err
}

// putBackError turns what the database refused into words.
func putBackError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch {
	case pgErr.ConstraintName == "number_used_by_extension":
		return &routing.PutBackError{Detail: "A ring group in that version had a number that is now someone's extension. Give one of them another number first."}
	case pgErr.ConstraintName == "ring_group_number_reserved":
		return &routing.PutBackError{Detail: "A ring group's number in that version now looks like an outside number."}
	case pgErr.ConstraintName == "call_permission_level_tenant_id_name_key":
		return &routing.PutBackError{Detail: "A calling level's name in that version is now another level's."}
	case pgErr.Code == "23505" || pgErr.Code == "23503" || pgErr.Code == "23514":
		return &routing.PutBackError{Detail: "That version doesn't fit with how things are now (" + pgErr.Message + ")."}
	}
	return err
}

// OutgoingRouting is the outgoing part of routing: the lines outgoing
// calls try, in order, and what each calling level allows.
func (s *Store) OutgoingRouting(ctx context.Context, tenant uuid.UUID) (routing.Outgoing, error) {
	out := routing.Outgoing{Lines: []string{}, Levels: []routing.Level{}}
	rows, err := s.pool.Query(ctx, `SELECT name FROM trunk WHERE tenant_id = $1 AND outbound_priority IS NOT NULL ORDER BY outbound_priority`, tenant)
	if err != nil {
		return out, err
	}
	if out.Lines, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return out, err
	}
	rows, err = s.pool.Query(ctx, `SELECT id, name, allowed_categories, withhold_caller_id FROM call_permission_level
		WHERE tenant_id = $1 ORDER BY name`, tenant)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var l routing.Level
		if err := rows.Scan(&l.ID, &l.Name, &l.Categories, &l.WithholdCallerID); err != nil {
			return out, err
		}
		out.Levels = append(out.Levels, l)
	}
	return out, rows.Err()
}
