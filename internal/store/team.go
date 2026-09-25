package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/webhook"
)

// The Team list (docs/ui/WEB_SCREENS_PHASE1C.md §6) and people's chosen
// status (migration 0014).

// TeamMembers lists the tenant's live extensions, each with the name of the
// first enabled person on it (or the extension's own name), whether any
// phone on it Asterisk accepts is signed in, and that person's status.
func (s *Store) TeamMembers(ctx context.Context, tenant uuid.UUID) ([]pbx.TeamMember, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT e.number, coalesce(u.name, e.display_name),
			EXISTS (SELECT 1 FROM device_live l JOIN device d ON d.id = l.id
				WHERE l.extension_id = e.id AND d.online),
			coalesce(u.presence, '')
		FROM extension e
		LEFT JOIN LATERAL (
			SELECT name, presence FROM app_user
			WHERE extension_id = e.id AND disabled_at IS NULL
			ORDER BY created_at, id LIMIT 1) u ON true
		WHERE e.tenant_id = $1 AND e.enabled AND e.deleted_at IS NULL
		ORDER BY e.number`, tenant)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (pbx.TeamMember, error) {
		var m pbx.TeamMember
		err := r.Scan(&m.Extension, &m.Name, &m.Online, &m.Presence)
		return m, err
	})
}

// SetPresence records a person's chosen status and, if it changed, queues
// presence.changed.
func (s *Store) SetPresence(ctx context.Context, tenant, user uuid.UUID, presence string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var was string
		var ext *string
		err := tx.QueryRow(ctx, `SELECT u.presence, e.number FROM app_user u
			LEFT JOIN extension e ON e.id = u.extension_id AND e.deleted_at IS NULL
			WHERE u.tenant_id = $1 AND u.id = $2 FOR UPDATE OF u`, tenant, user).Scan(&was, &ext)
		if err != nil {
			if err == pgx.ErrNoRows {
				return pbx.ErrNotFound
			}
			return err
		}
		if was == presence {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE app_user SET presence = $3 WHERE tenant_id = $1 AND id = $2`, tenant, user, presence); err != nil {
			return err
		}
		ev, err := webhook.NewEvent(tenant, "presence.changed",
			map[string]any{"user_id": user, "extension": ext, "presence": presence, "previous": was}, time.Now().UTC())
		if err != nil {
			return err
		}
		return insertEvent(ctx, tx, ev, nil)
	})
}
