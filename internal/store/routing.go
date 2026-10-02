package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/routing"
)

// RingGroups returns every ring group of the tenant with its members in
// order (migration 0032), by name. A member "can ring" when one of its
// extension's phones or browsers is connected and its person isn't on Do
// not disturb (the dialplan's own linx_ring_targets).
func (s *Store) RingGroups(ctx context.Context, tenant uuid.UUID) ([]routing.RingGroup, error) {
	rows, err := s.pool.Query(ctx, `SELECT g.id, g.tenant_id, g.name, coalesce(g.number, ''), g.strategy, g.ring_seconds, g.turn_seconds,
			g.no_answer_kind, coalesce(g.no_answer_extension_id, vb.extension_id), coalesce(g.no_answer_ring_group_id, vb.ring_group_id),
			coalesce(g.no_answer_message, ''), g.version, g.created_at, g.updated_at
		FROM ring_group g LEFT JOIN voicemail_box vb ON vb.id = g.no_answer_voicemail_id
		WHERE g.tenant_id = $1 ORDER BY lower(g.name), g.id`, tenant)
	if err != nil {
		return nil, err
	}
	out := []routing.RingGroup{}
	index := map[uuid.UUID]int{}
	for rows.Next() {
		var g routing.RingGroup
		if err := rows.Scan(&g.ID, &g.TenantID, &g.Name, &g.Number, &g.Strategy, &g.RingSeconds, &g.TurnSeconds,
			&g.NoAnswer.Kind, &g.NoAnswer.ExtensionID, &g.NoAnswer.RingGroupID, &g.NoAnswer.Message,
			&g.Version, &g.CreatedAt, &g.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		g.Members = []routing.Member{}
		index[g.ID] = len(out)
		out = append(out, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT m.ring_group_id, e.id, e.number, e.display_name,
			EXISTS (SELECT 1 FROM asterisk.linx_ring_targets r JOIN device d ON d.sip_username = r.aor
				WHERE r.number = e.number AND d.online)
		FROM ring_group_member m
		JOIN ring_group g ON g.id = m.ring_group_id
		JOIN extension e ON e.id = m.extension_id AND e.deleted_at IS NULL
		WHERE g.tenant_id = $1 ORDER BY m.ring_group_id, m.position`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var group uuid.UUID
		var m routing.Member
		if err := rows.Scan(&group, &m.ExtensionID, &m.Number, &m.DisplayName, &m.CanRing); err != nil {
			return nil, err
		}
		if i, ok := index[group]; ok {
			out[i].Members = append(out[i].Members, m)
		}
	}
	return out, rows.Err()
}

// Extensions returns the tenant's live extensions with these ids.
func (s *Store) Extensions(ctx context.Context, tenant uuid.UUID, ids []uuid.UUID) ([]routing.ExtensionRef, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, number, display_name FROM extension
		WHERE tenant_id = $1 AND id = ANY($2) AND deleted_at IS NULL`, tenant, ids)
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

// destColumns are d's kind, extension, ring group, voicemail box and
// message columns (migration 0034: a box's id is its owner's, in a column
// of its own).
func destColumns(d routing.Destination) (string, *uuid.UUID, *uuid.UUID, *uuid.UUID, *string) {
	if d.Kind == routing.KindVoicemail {
		box := d.ExtensionID
		if box == nil {
			box = d.RingGroupID
		}
		return d.Kind, nil, nil, box, nil
	}
	return d.Kind, d.ExtensionID, d.RingGroupID, nil, emptyStrToNil(d.Message)
}

// CreateRingGroup adds g; its voicemail box comes with it (migration
// 0034's trigger), so g can send its unanswered calls there.
func (s *Store) CreateRingGroup(ctx context.Context, g routing.RingGroup, audit auth.AuditEntry) error {
	kind, ext, grp, box, msg := destColumns(g.NoAnswer)
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ring_group (id, tenant_id, name, number, strategy, ring_seconds, turn_seconds,
				no_answer_kind, no_answer_extension_id, no_answer_ring_group_id, no_answer_voicemail_id, no_answer_message,
				version, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
			g.ID, g.TenantID, g.Name, emptyStrToNil(g.Number), g.Strategy, g.RingSeconds, g.TurnSeconds,
			kind, ext, grp, box, msg, g.Version, g.CreatedAt, g.UpdatedAt)
		if err != nil {
			return ringGroupError(err, g)
		}
		if err := setMembers(ctx, tx, g); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) UpdateRingGroup(ctx context.Context, g routing.RingGroup, audit auth.AuditEntry) error {
	kind, ext, grp, box, msg := destColumns(g.NoAnswer)
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE ring_group SET name = $4, number = $5, strategy = $6, ring_seconds = $7, turn_seconds = $8,
				no_answer_kind = $9, no_answer_extension_id = $10, no_answer_ring_group_id = $11, no_answer_voicemail_id = $12,
				no_answer_message = $13, version = version + 1, updated_at = $14
			WHERE id = $1 AND tenant_id = $2 AND version = $3`,
			g.ID, g.TenantID, g.Version, g.Name, emptyStrToNil(g.Number), g.Strategy, g.RingSeconds, g.TurnSeconds,
			kind, ext, grp, box, msg, g.UpdatedAt)
		if err != nil {
			return ringGroupError(err, g)
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ring_group WHERE id = $1 AND tenant_id = $2)`,
				g.ID, g.TenantID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return routing.ErrVersionChanged
			}
			return routing.ErrNotFound
		}
		if _, err := tx.Exec(ctx, `DELETE FROM ring_group_member WHERE ring_group_id = $1`, g.ID); err != nil {
			return err
		}
		if err := setMembers(ctx, tx, g); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

// setMembers adds g's members in order, refusing an extension that isn't
// the tenant's or was removed.
func setMembers(ctx context.Context, tx pgx.Tx, g routing.RingGroup) error {
	ids := make([]uuid.UUID, len(g.Members))
	for i, m := range g.Members {
		ids[i] = m.ExtensionID
	}
	tag, err := tx.Exec(ctx, `INSERT INTO ring_group_member (ring_group_id, extension_id, position)
		SELECT $1, m.id, m.position FROM unnest($2::uuid[]) WITH ORDINALITY AS m (id, position)
		JOIN extension e ON e.id = m.id AND e.tenant_id = $3 AND e.deleted_at IS NULL`, g.ID, ids, g.TenantID)
	if err != nil {
		return fmt.Errorf("saving ring group members: %w", err)
	}
	if int(tag.RowsAffected()) != len(ids) {
		return routing.ErrExtensionNotFound
	}
	return nil
}

// DeleteRingGroup removes the group with its voicemail box and messages;
// ErrInUse while something else sends calls to either.
func (s *Store) DeleteRingGroup(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var used bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ring_group WHERE no_answer_voicemail_id = $1 AND id <> $1)
				OR EXISTS (SELECT 1 FROM incoming_rule WHERE $1 IN (no_answer_voicemail_id, closed_voicemail_id, holiday_voicemail_id))`,
			id).Scan(&used); err != nil {
			return err
		}
		if used {
			return routing.ErrInUse
		}
		tag, err := tx.Exec(ctx, `DELETE FROM ring_group WHERE id = $1 AND tenant_id = $2`, id, tenant)
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

func ringGroupError(err error, g routing.RingGroup) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch {
	case pgErr.ConstraintName == "ring_group_number_reserved":
		return &routing.ReservedNumberError{Number: g.Number, Reason: pgErr.Detail, Country: pgErr.Hint}
	case pgErr.ConstraintName == "ring_group_tenant_id_name_key":
		return routing.ErrDuplicateName
	case pgErr.Code == "23505":
		return routing.ErrNumberTaken
	case pgErr.Code == "23503":
		if pgErr.ConstraintName == "ring_group_no_answer_extension_id_fkey" {
			return routing.ErrExtensionNotFound
		}
		return routing.ErrNotFound
	}
	return err
}
