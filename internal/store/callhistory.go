package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/callhistory"
	"linxpbx.com/linx/internal/voicemail"
)

// CallHistory is the store as call history sees it (ADR-070, migration
// 0037). Its own type: a few of its lookups share names with the store's.
type CallHistory struct{ *Store }

var _ callhistory.Store = CallHistory{}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return callhistory.ErrNotFound
	}
	return err
}

// DeviceOwner is the extension behind a device's PJSIP endpoint.
func (s CallHistory) DeviceOwner(ctx context.Context, endpoint string) (callhistory.Person, error) {
	var p callhistory.Person
	err := s.pool.QueryRow(ctx, `SELECT e.id, e.tenant_id, e.number, e.display_name
		FROM device d JOIN extension e ON e.id = d.extension_id WHERE d.sip_username = $1`, endpoint).
		Scan(&p.ExtensionID, &p.TenantID, &p.Number, &p.Name)
	return p, notFound(err)
}

// ExtensionByNumber is the tenant's extension numbered number.
func (s CallHistory) ExtensionByNumber(ctx context.Context, tenant uuid.UUID, number string) (callhistory.Person, error) {
	var p callhistory.Person
	err := s.pool.QueryRow(ctx, `SELECT id, tenant_id, number, display_name FROM extension
		WHERE tenant_id = $1 AND number = $2 AND deleted_at IS NULL`, tenant, number).
		Scan(&p.ExtensionID, &p.TenantID, &p.Number, &p.Name)
	return p, notFound(err)
}

// RingGroup is ring group id.
func (s CallHistory) RingGroup(ctx context.Context, id uuid.UUID) (callhistory.Group, error) {
	var g callhistory.Group
	err := s.pool.QueryRow(ctx, `SELECT id, tenant_id, name, strategy = 'in_turn' FROM ring_group WHERE id = $1`, id).
		Scan(&g.ID, &g.TenantID, &g.Name, &g.InTurn)
	return g, notFound(err)
}

// RingGroupByNumber is the tenant's ring group numbered number.
func (s CallHistory) RingGroupByNumber(ctx context.Context, tenant uuid.UUID, number string) (callhistory.Group, error) {
	var g callhistory.Group
	err := s.pool.QueryRow(ctx, `SELECT id, tenant_id, name, strategy = 'in_turn' FROM ring_group
		WHERE tenant_id = $1 AND number = $2`, tenant, number).Scan(&g.ID, &g.TenantID, &g.Name, &g.InTurn)
	return g, notFound(err)
}

// Trunk is phone line id.
func (s CallHistory) Trunk(ctx context.Context, id uuid.UUID) (callhistory.Line, error) {
	var l callhistory.Line
	err := s.pool.QueryRow(ctx, `SELECT id, tenant_id, name FROM trunk WHERE id = $1`, id).Scan(&l.ID, &l.TenantID, &l.Name)
	return l, notFound(err)
}

// VoicemailBox is box id's owner in words.
func (s CallHistory) VoicemailBox(ctx context.Context, id uuid.UUID) (callhistory.Box, error) {
	b, err := s.Store.VoicemailBox(ctx, id)
	if errors.Is(err, voicemail.ErrNotFound) {
		return callhistory.Box{}, callhistory.ErrNotFound
	}
	if err != nil {
		return callhistory.Box{}, err
	}
	return callhistory.Box{Name: b.Owner, ExtensionID: b.ExtensionID}, nil
}

const cdrColumns = `id, started, coalesce(answered, 'epoch'), ended, clid, src, dst, dcontext, channel, dstchannel,
	duration, billsec, disposition, uniqueid, linkedid, linx_dialled, linx_did, linx_step`

// UnreadCDR returns the rows of the calls with a row past the read mark:
// the next batch rows after it, and every other row of those calls.
func (s CallHistory) UnreadCDR(ctx context.Context, batch int) (map[string][]callhistory.Row, int64, error) {
	var readTo int64
	var ids []string
	rows, err := s.pool.Query(ctx, `SELECT c.id, c.linkedid FROM asterisk.cdr c
		WHERE c.id > (SELECT call_history_read_id FROM pbx_setting) ORDER BY c.id LIMIT $1`, batch)
	if err != nil {
		return nil, 0, err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var id int64
		var linked string
		if err := rows.Scan(&id, &linked); err != nil {
			rows.Close()
			return nil, 0, err
		}
		readTo = id
		if !seen[linked] {
			seen[linked] = true
			ids = append(ids, linked)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return nil, 0, err
	}
	rows, err = s.pool.Query(ctx, `SELECT `+cdrColumns+` FROM asterisk.cdr WHERE linkedid = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return nil, 0, err
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (callhistory.Row, error) {
		var c callhistory.Row
		err := r.Scan(&c.ID, &c.Started, &c.Answered, &c.Ended, &c.CLID, &c.Src, &c.Dst, &c.DContext, &c.Channel, &c.DstChannel,
			&c.Duration, &c.BillSec, &c.Disposition, &c.UniqueID, &c.LinkedID, &c.Dialled, &c.DID, &c.Step)
		c.Started, c.Answered, c.Ended = c.Started.UTC(), c.Answered.UTC(), c.Ended.UTC()
		return c, err
	})
	if err != nil {
		return nil, 0, err
	}
	out := map[string][]callhistory.Row{}
	for _, c := range all {
		out[c.LinkedID] = append(out[c.LinkedID], c)
	}
	return out, readTo, nil
}

// cut keeps s within a column's length.
func cut(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// SaveCalls keeps calls, each replacing the one with its linkedid (the id
// stays), with who they belong to, and moves the read mark to readTo.
func (s CallHistory) SaveCalls(ctx context.Context, calls []callhistory.Call, readTo int64, at time.Time) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, c := range calls {
			steps, err := json.Marshal(c.Steps)
			if err != nil {
				return err
			}
			if c.Steps == nil {
				steps = []byte("[]")
			}
			id := uuid.Must(uuid.NewV7())
			err = tx.QueryRow(ctx, `INSERT INTO call_record (id, tenant_id, linkedid, direction, started_at, answered_at, ended_at,
					talk_seconds, result, rang_unanswered, from_number, from_name, from_extension_id, to_number, to_name, to_extension_id,
					trunk_id, trunk_name, ring_group_id, ring_group_name, answered_by_extension_id, answered_by_name,
					voicemail_source, voicemail_box_name, steps, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26)
				ON CONFLICT (linkedid) DO UPDATE SET tenant_id = EXCLUDED.tenant_id, direction = EXCLUDED.direction,
					started_at = EXCLUDED.started_at, answered_at = EXCLUDED.answered_at, ended_at = EXCLUDED.ended_at,
					talk_seconds = EXCLUDED.talk_seconds, result = EXCLUDED.result, rang_unanswered = EXCLUDED.rang_unanswered,
					from_number = EXCLUDED.from_number, from_name = EXCLUDED.from_name, from_extension_id = EXCLUDED.from_extension_id,
					to_number = EXCLUDED.to_number, to_name = EXCLUDED.to_name, to_extension_id = EXCLUDED.to_extension_id,
					trunk_id = EXCLUDED.trunk_id, trunk_name = EXCLUDED.trunk_name, ring_group_id = EXCLUDED.ring_group_id,
					ring_group_name = EXCLUDED.ring_group_name, answered_by_extension_id = EXCLUDED.answered_by_extension_id,
					answered_by_name = EXCLUDED.answered_by_name, voicemail_source = EXCLUDED.voicemail_source,
					voicemail_box_name = EXCLUDED.voicemail_box_name, steps = EXCLUDED.steps, updated_at = EXCLUDED.updated_at
				RETURNING id`,
				id, c.TenantID, c.LinkedID, c.Direction, c.StartedAt, c.AnsweredAt, c.EndedAt, max(c.TalkSeconds, 0), c.Result, c.RangUnanswered,
				cut(c.FromNumber, 40), cut(c.FromName, 100), c.FromExtensionID, cut(c.ToNumber, 40), cut(c.ToName, 100), c.ToExtensionID,
				c.TrunkID, cut(c.TrunkName, 100), c.RingGroupID, cut(c.RingGroupName, 100), c.AnsweredByExtensionID, cut(c.AnsweredByName, 100),
				c.VoicemailSource, cut(c.VoicemailBoxName, 100), steps, at).Scan(&id)
			if err != nil {
				return fmt.Errorf("call %s: %w", c.LinkedID, err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM call_record_party WHERE call_id = $1`, id); err != nil {
				return err
			}
			for _, p := range c.Parties {
				if _, err := tx.Exec(ctx, `INSERT INTO call_record_party (call_id, extension_id, started_at, missed)
					SELECT $1, $2, $3, $4 WHERE EXISTS (SELECT 1 FROM extension WHERE id = $2)`, id, p.ExtensionID, c.StartedAt, p.Missed); err != nil {
					return err
				}
			}
		}
		_, err := tx.Exec(ctx, `UPDATE pbx_setting SET call_history_read_id = greatest(call_history_read_id, $1)`, readTo)
		return err
	})
}

// ExpireCallHistory deletes calls that started more than the kept days
// before now, and Asterisk's rows already read once they're a day old.
func (s CallHistory) ExpireCallHistory(ctx context.Context, now time.Time) ([]uuid.UUID, error) {
	var tenants []uuid.UUID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH gone AS (DELETE FROM call_record
				WHERE started_at < $1::timestamptz - make_interval(days => (SELECT call_history_keep_days FROM pbx_setting))
				RETURNING tenant_id)
			SELECT DISTINCT tenant_id FROM gone`, now)
		if err != nil {
			return err
		}
		if tenants, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID]); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM asterisk.cdr WHERE id <= (SELECT call_history_read_id FROM pbx_setting)
			AND ended < ($1::timestamptz AT TIME ZONE 'UTC') - interval '1 day'`, now)
		return err
	})
	return tenants, err
}

// digits keeps a number search to its digits, without leading zeros, so
// "050 123 4567" finds +971501234567.
func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return strings.TrimLeft(b.String(), "0")
}

// ListCalls lists the calls f picks, newest first.
func (s CallHistory) ListCalls(ctx context.Context, f callhistory.Filter) ([]callhistory.Listed, error) {
	args := []any{f.TenantID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	missed := `EXISTS (SELECT 1 FROM call_record_party x WHERE x.call_id = c.id AND x.missed)`
	join := ""
	if f.Party != nil {
		join = ` JOIN call_record_party p ON p.call_id = c.id AND p.extension_id = ` + arg(*f.Party)
		missed = `p.missed`
	}
	where := []string{"c.tenant_id = $1"}
	if f.Missed {
		where = append(where, missed)
	}
	if n := digits(f.Number); n != "" {
		a := arg("%" + n + "%")
		where = append(where, fmt.Sprintf("(c.from_number LIKE %s OR c.to_number LIKE %s)", a, a))
	}
	if f.From != nil {
		where = append(where, "c.started_at >= "+arg(*f.From))
	}
	if f.To != nil {
		where = append(where, "c.started_at < "+arg(*f.To))
	}
	if f.Before != nil {
		where = append(where, fmt.Sprintf("(c.started_at, c.id) < (%s, %s)", arg(f.Before.StartedAt), arg(f.Before.ID)))
	}
	limit := arg(f.Limit)
	rows, err := s.pool.Query(ctx, `SELECT c.id, c.tenant_id, c.linkedid, c.direction, c.started_at, c.answered_at, c.ended_at,
			c.talk_seconds, c.result, c.rang_unanswered, c.from_number, c.from_name, c.from_extension_id, c.to_number, c.to_name,
			c.to_extension_id, c.trunk_id, c.trunk_name, c.ring_group_id, c.ring_group_name, c.answered_by_extension_id,
			c.answered_by_name, c.voicemail_source, c.voicemail_box_name, c.steps, `+missed+`, v.id, v.box_id, coalesce(v.duration_ms, 0)
		FROM call_record c`+join+`
		LEFT JOIN voicemail_message v ON c.voicemail_source <> '' AND v.source = c.voicemail_source
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY c.started_at DESC, c.id DESC LIMIT `+limit, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (callhistory.Listed, error) {
		var l callhistory.Listed
		var steps []byte
		var ms int64
		err := r.Scan(&l.ID, &l.TenantID, &l.LinkedID, &l.Direction, &l.StartedAt, &l.AnsweredAt, &l.EndedAt, &l.TalkSeconds, &l.Result,
			&l.RangUnanswered, &l.FromNumber, &l.FromName, &l.FromExtensionID, &l.ToNumber, &l.ToName, &l.ToExtensionID, &l.TrunkID,
			&l.TrunkName, &l.RingGroupID, &l.RingGroupName, &l.AnsweredByExtensionID, &l.AnsweredByName, &l.VoicemailSource,
			&l.VoicemailBoxName, &steps, &l.Missed, &l.VoicemailID, &l.VoicemailBoxID, &ms)
		if err != nil {
			return l, err
		}
		l.VoicemailDuration = msDuration(ms)
		return l, json.Unmarshal(steps, &l.Steps)
	})
}

// UserExtension is a person's extension (nil: none).
func (s CallHistory) UserExtension(ctx context.Context, user uuid.UUID) (*uuid.UUID, error) {
	var ext *uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT extension_id FROM app_user WHERE id = $1`, user).Scan(&ext)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return ext, err
}

// MissedCount is how many calls the person missed since they last opened
// Call history.
func (s CallHistory) MissedCount(ctx context.Context, user uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM call_record_party p JOIN app_user u ON u.extension_id = p.extension_id
		WHERE u.id = $1 AND p.missed AND p.started_at > coalesce(u.calls_seen_at, '-infinity')`, user).Scan(&n)
	return n, err
}

// SetCallsSeen records that the person opened Call history at.
func (s CallHistory) SetCallsSeen(ctx context.Context, user uuid.UUID, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE app_user SET calls_seen_at = $2 WHERE id = $1`, user, at)
	return err
}

// CallHistoryKeepDays is how long calls are kept.
func (s CallHistory) CallHistoryKeepDays(ctx context.Context) (int, error) {
	var days int
	err := s.pool.QueryRow(ctx, `SELECT call_history_keep_days FROM pbx_setting`).Scan(&days)
	return days, err
}

// SetCallHistoryKeepDays changes it, audited.
func (s CallHistory) SetCallHistoryKeepDays(ctx context.Context, days int, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE pbx_setting SET call_history_keep_days = $1`, days); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

// CallHistoryUsage is the tenant's calls and the space all call history
// takes on disk.
func (s CallHistory) CallHistoryUsage(ctx context.Context, tenant uuid.UUID) (callhistory.Usage, error) {
	var u callhistory.Usage
	err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM call_record WHERE tenant_id = $1),
			pg_total_relation_size('call_record') + pg_total_relation_size('call_record_party') + pg_total_relation_size('asterisk.cdr')`,
		tenant).Scan(&u.Calls, &u.Bytes)
	return u, err
}
