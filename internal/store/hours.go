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

var _ routing.RulesStore = (*Store)(nil)

// TimeZone is the server's time zone (migration 0033, written at start).
func (s *Store) TimeZone(ctx context.Context) (string, error) {
	var tz string
	err := s.pool.QueryRow(ctx, `SELECT time_zone FROM pbx_setting`).Scan(&tz)
	return tz, err
}

// Schedules returns every schedule of the tenant, by name, with its spans
// and holidays in order.
func (s *Store) OfficeHours(ctx context.Context, tenant uuid.UUID) ([]routing.Schedule, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, tenant_id, name, version, created_at, updated_at
		FROM schedule WHERE tenant_id = $1 ORDER BY lower(name), id`, tenant)
	if err != nil {
		return nil, err
	}
	out := []routing.Schedule{}
	index := map[uuid.UUID]int{}
	for rows.Next() {
		var sc routing.Schedule
		if err := rows.Scan(&sc.ID, &sc.TenantID, &sc.Name, &sc.Version, &sc.CreatedAt, &sc.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		sc.Spans, sc.Holidays = []routing.Span{}, []routing.Holiday{}
		index[sc.ID] = len(out)
		out = append(out, sc)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT p.schedule_id, p.weekday, to_char(p.opens, 'HH24:MI'),
			CASE WHEN p.closes = '24:00'::time THEN '24:00' ELSE to_char(p.closes, 'HH24:MI') END
		FROM schedule_span p JOIN schedule sc ON sc.id = p.schedule_id
		WHERE sc.tenant_id = $1 ORDER BY p.schedule_id, p.weekday, p.opens`, tenant)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uuid.UUID
		var sp routing.Span
		if err := rows.Scan(&id, &sp.Weekday, &sp.Opens, &sp.Closes); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].Spans = append(out[i].Spans, sp)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT h.schedule_id, h.name, to_char(h.first_day, 'YYYY-MM-DD'), to_char(h.last_day, 'YYYY-MM-DD'), h.every_year
		FROM schedule_holiday h JOIN schedule sc ON sc.id = h.schedule_id
		WHERE sc.tenant_id = $1 ORDER BY h.schedule_id, h.position`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var h routing.Holiday
		if err := rows.Scan(&id, &h.Name, &h.FirstDay, &h.LastDay, &h.EveryYear); err != nil {
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].Holidays = append(out[i].Holidays, h)
		}
	}
	return out, rows.Err()
}

// ScheduleStates asks the database (schedule_open_at, schedule_changes_at)
// about every schedule of the tenant at at.
func (s *Store) OfficeHoursStates(ctx context.Context, tenant uuid.UUID, at time.Time) (map[uuid.UUID]routing.ScheduleState, error) {
	rows, err := s.pool.Query(ctx, `SELECT sc.id, o.open, coalesce(o.holiday, ''), schedule_changes_at(sc.id, $2)
		FROM schedule sc, schedule_open_at(sc.id, $2) o WHERE sc.tenant_id = $1`, tenant, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]routing.ScheduleState{}
	for rows.Next() {
		var id uuid.UUID
		var st routing.ScheduleState
		if err := rows.Scan(&id, &st.Open, &st.Holiday, &st.ChangesAt); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

func (s *Store) CreateOfficeHours(ctx context.Context, sc routing.Schedule, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO schedule (id, tenant_id, name, version, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6)`, sc.ID, sc.TenantID, sc.Name, sc.Version, sc.CreatedAt, sc.UpdatedAt)
		if err != nil {
			if IsUniqueViolation(err) {
				return routing.ErrDuplicateName
			}
			return err
		}
		if err := setScheduleParts(ctx, tx, sc); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) UpdateOfficeHours(ctx context.Context, sc routing.Schedule, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE schedule SET name = $4, version = version + 1, updated_at = $5
			WHERE id = $1 AND tenant_id = $2 AND version = $3`, sc.ID, sc.TenantID, sc.Version, sc.Name, sc.UpdatedAt)
		if err != nil {
			if IsUniqueViolation(err) {
				return routing.ErrDuplicateName
			}
			return err
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schedule WHERE id = $1 AND tenant_id = $2)`,
				sc.ID, sc.TenantID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return routing.ErrVersionChanged
			}
			return routing.ErrNotFound
		}
		if _, err := tx.Exec(ctx, `DELETE FROM schedule_span WHERE schedule_id = $1`, sc.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM schedule_holiday WHERE schedule_id = $1`, sc.ID); err != nil {
			return err
		}
		if err := setScheduleParts(ctx, tx, sc); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

func setScheduleParts(ctx context.Context, tx pgx.Tx, sc routing.Schedule) error {
	for _, sp := range sc.Spans {
		if _, err := tx.Exec(ctx, `INSERT INTO schedule_span (schedule_id, weekday, opens, closes) VALUES ($1, $2, $3::time, $4::time)`,
			sc.ID, sp.Weekday, sp.Opens, sp.Closes); err != nil {
			return fmt.Errorf("saving opening times: %w", err)
		}
	}
	for i, h := range sc.Holidays {
		if _, err := tx.Exec(ctx, `INSERT INTO schedule_holiday (schedule_id, position, name, first_day, last_day, every_year)
			VALUES ($1, $2, $3, $4::date, $5::date, $6)`, sc.ID, i+1, h.Name, h.FirstDay, h.LastDay, h.EveryYear); err != nil {
			return fmt.Errorf("saving holidays: %w", err)
		}
	}
	return nil
}

func (s *Store) DeleteOfficeHours(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM schedule WHERE id = $1 AND tenant_id = $2`, id, tenant)
		if err != nil {
			if isRestrictViolation(err) {
				return routing.ErrInUse
			}
			return err
		}
		if tag.RowsAffected() == 0 {
			return routing.ErrNotFound
		}
		return insertAudit(ctx, tx, audit)
	})
}

// ruleColumns are incoming_rule's, in scanRule's order.
// A voicemail box comes back as its owner (migration 0034: the box's id is
// the extension's or ring group's).
const ruleColumns = `r.no_answer_seconds, r.no_answer_kind,
	coalesce(r.no_answer_extension_id, (SELECT b.extension_id FROM voicemail_box b WHERE b.id = r.no_answer_voicemail_id)),
	coalesce(r.no_answer_ring_group_id, (SELECT b.ring_group_id FROM voicemail_box b WHERE b.id = r.no_answer_voicemail_id)),
	r.no_answer_message, r.schedule_id, r.closed_kind,
	coalesce(r.closed_extension_id, (SELECT b.extension_id FROM voicemail_box b WHERE b.id = r.closed_voicemail_id)),
	coalesce(r.closed_ring_group_id, (SELECT b.ring_group_id FROM voicemail_box b WHERE b.id = r.closed_voicemail_id)),
	r.closed_message, r.holiday_kind,
	coalesce(r.holiday_extension_id, (SELECT b.extension_id FROM voicemail_box b WHERE b.id = r.holiday_voicemail_id)),
	coalesce(r.holiday_ring_group_id, (SELECT b.ring_group_id FROM voicemail_box b WHERE b.id = r.holiday_voicemail_id)),
	r.holiday_message`

// IncomingList returns every number of the tenant (by number), then every
// line (by name), with who it rings and its rule.
func (s *Store) IncomingList(ctx context.Context, tenant uuid.UUID) ([]routing.Incoming, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT 'number', d.id, d.number, d.label, t.id, t.name, t.kind, d.extension_id, d.ring_group_id, d.version,
			r.did_id IS NOT NULL, `+ruleColumns+`
		FROM trunk_did d JOIN trunk t ON t.id = d.trunk_id LEFT JOIN incoming_rule r ON r.did_id = d.id
		WHERE d.tenant_id = $1
		UNION ALL
		SELECT 'line', t.id, '', '', t.id, t.name, t.kind, t.rings_extension_id, t.rings_ring_group_id, t.version,
			r.trunk_id IS NOT NULL, `+ruleColumns+`
		FROM trunk t LEFT JOIN incoming_rule r ON r.trunk_id = t.id
		WHERE t.tenant_id = $1
		ORDER BY 1 DESC, 3, 6`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []routing.Incoming{}
	for rows.Next() {
		in := routing.Incoming{TenantID: tenant}
		var ext, grp *uuid.UUID
		var hasRule bool
		var r ruleRow
		if err := rows.Scan(&in.Kind, &in.ID, &in.Number, &in.Label, &in.LineID, &in.LineName, &in.LineKind, &ext, &grp, &in.Version,
			&hasRule, &r.seconds, &r.kind[0], &r.ext[0], &r.grp[0], &r.msg[0], &r.schedule,
			&r.kind[1], &r.ext[1], &r.grp[1], &r.msg[1], &r.kind[2], &r.ext[2], &r.grp[2], &r.msg[2]); err != nil {
			return nil, err
		}
		switch {
		case ext != nil:
			in.Rings = &routing.Destination{Kind: routing.KindExtension, ExtensionID: ext}
		case grp != nil:
			in.Rings = &routing.Destination{Kind: routing.KindRingGroup, RingGroupID: grp}
		}
		if hasRule {
			in.Rule = r.rule()
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

type ruleRow struct {
	seconds  *int
	schedule *uuid.UUID
	kind     [3]*string
	ext, grp [3]*uuid.UUID
	msg      [3]*string
}

func (r ruleRow) dest(i int) *routing.Destination {
	if r.kind[i] == nil {
		return nil
	}
	return &routing.Destination{Kind: *r.kind[i], ExtensionID: r.ext[i], RingGroupID: r.grp[i], Message: deref(r.msg[i])}
}

func (r ruleRow) rule() *routing.Rule {
	out := &routing.Rule{ScheduleID: r.schedule, Holiday: r.dest(2)}
	if r.seconds != nil {
		out.NoAnswerSeconds = *r.seconds
	}
	if d := r.dest(0); d != nil {
		out.NoAnswer = *d
	}
	if d := r.dest(1); d != nil {
		out.Closed = *d
	}
	return out
}

// SetIncoming saves who in rings (on the number or line, bumping its
// version) and its rule, in one transaction.
func (s *Store) SetIncoming(ctx context.Context, in routing.Incoming, audit auth.AuditEntry) error {
	var ext, grp *uuid.UUID
	if in.Rings != nil {
		ext, grp = in.Rings.ExtensionID, in.Rings.RingGroupID
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var tag pgconn.CommandTag
		var err error
		if in.Kind == routing.IncomingLine {
			tag, err = tx.Exec(ctx, `UPDATE trunk SET rings_extension_id = $4, rings_ring_group_id = $5, version = version + 1, updated_at = now()
				WHERE id = $1 AND tenant_id = $2 AND version = $3`, in.ID, in.TenantID, in.Version, ext, grp)
		} else {
			tag, err = tx.Exec(ctx, `UPDATE trunk_did SET extension_id = $4, ring_group_id = $5, version = version + 1, updated_at = now()
				WHERE id = $1 AND tenant_id = $2 AND version = $3`, in.ID, in.TenantID, in.Version, ext, grp)
		}
		if err != nil {
			return ruleError(err)
		}
		if tag.RowsAffected() == 0 {
			return routing.ErrVersionChanged
		}
		did, line := &in.ID, (*uuid.UUID)(nil)
		if in.Kind == routing.IncomingLine {
			did, line = nil, &in.ID
		}
		if _, err := tx.Exec(ctx, `DELETE FROM incoming_rule WHERE did_id = $1 OR trunk_id = $1`, in.ID); err != nil {
			return err
		}
		if r := in.Rule; r != nil {
			nk, ne, ng, nv, nm := destColumns(r.NoAnswer)
			ck, ce, cg, cv, cm := destColumns(r.Closed)
			hk, he, hg, hv, hm := (*string)(nil), (*uuid.UUID)(nil), (*uuid.UUID)(nil), (*uuid.UUID)(nil), (*string)(nil)
			if h := r.Holiday; h != nil {
				var k string
				k, he, hg, hv, hm = destColumns(*h)
				hk = &k
			}
			_, err := tx.Exec(ctx, `INSERT INTO incoming_rule (tenant_id, did_id, trunk_id, no_answer_seconds,
					no_answer_kind, no_answer_extension_id, no_answer_ring_group_id, no_answer_voicemail_id, no_answer_message, schedule_id,
					closed_kind, closed_extension_id, closed_ring_group_id, closed_voicemail_id, closed_message,
					holiday_kind, holiday_extension_id, holiday_ring_group_id, holiday_voicemail_id, holiday_message, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, now())`,
				in.TenantID, did, line, r.NoAnswerSeconds,
				nk, ne, ng, nv, nm, r.ScheduleID,
				ck, ce, cg, cv, cm,
				hk, he, hg, hv, hm)
			if err != nil {
				return ruleError(err)
			}
		}
		return insertAudit(ctx, tx, audit)
	})
}

func ruleError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		if strings.HasSuffix(pgErr.ConstraintName, "extension_id_fkey") {
			return routing.ErrExtensionNotFound
		}
		return routing.ErrNotFound
	}
	return err
}

// RouteStep asks linx_route_at, the function behind the dialplan's
// asterisk.linx_route, at at.
func (s *Store) RouteStep(ctx context.Context, dest, caller string, at time.Time) (*routing.Step, error) {
	var st routing.Step
	err := s.pool.QueryRow(ctx, `SELECT * FROM linx_route_at($1, $2, $3)`, dest, caller, at).
		Scan(&st.Action, &st.Targets, &st.Seconds, &st.Next, &st.Counts, &st.Label)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// RingTargets says whose these phones and browsers are.
func (s *Store) RingTargets(ctx context.Context, usernames []string) ([]routing.RingTarget, error) {
	rows, err := s.pool.Query(ctx, `SELECT d.sip_username, e.number, e.display_name
		FROM device d JOIN extension e ON e.id = d.extension_id
		WHERE d.sip_username = ANY($1) ORDER BY e.number, d.sip_username`, usernames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []routing.RingTarget{}
	for rows.Next() {
		var t routing.RingTarget
		if err := rows.Scan(&t.Username, &t.Number, &t.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ExtensionsByNumber returns the tenant's live extensions with these
// numbers.
func (s *Store) ExtensionsByNumber(ctx context.Context, tenant uuid.UUID, numbers []string) ([]routing.ExtensionRef, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, number, display_name FROM extension
		WHERE tenant_id = $1 AND number = ANY($2) AND deleted_at IS NULL`, tenant, numbers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []routing.ExtensionRef{}
	for rows.Next() {
		var e routing.ExtensionRef
		if err := rows.Scan(&e.ID, &e.Number, &e.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
