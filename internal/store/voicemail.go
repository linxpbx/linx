package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/voicemail"
	"linxpbx.com/linx/internal/webhook"
)

// VoicemailBox returns box id (migration 0034) with its owner in words.
func (s *Store) VoicemailBox(ctx context.Context, id uuid.UUID) (voicemail.Box, error) {
	var b voicemail.Box
	err := s.pool.QueryRow(ctx, `SELECT b.id, b.tenant_id, b.extension_id, b.ring_group_id,
			coalesce(e.display_name || ' (' || e.number || ')', g.name, ''), b.enabled, b.email
		FROM voicemail_box b
		LEFT JOIN extension e ON e.id = b.extension_id
		LEFT JOIN ring_group g ON g.id = b.ring_group_id
		WHERE b.id = $1`, id).Scan(&b.ID, &b.TenantID, &b.ExtensionID, &b.RingGroupID, &b.Owner, &b.Enabled, &b.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, voicemail.ErrNotFound
	}
	return b, err
}

// CallerExtension is the tenant's live extension numbered number, with
// its person's name (nil when there's none).
func (s *Store) CallerExtension(ctx context.Context, tenant uuid.UUID, number string) (*uuid.UUID, string, error) {
	var id uuid.UUID
	var name string
	err := s.pool.QueryRow(ctx, `SELECT id, display_name FROM extension
		WHERE tenant_id = $1 AND number = $2 AND deleted_at IS NULL`, tenant, number).Scan(&id, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	return &id, name, nil
}

// AddVoicemail stores m and queues voicemail.created with event as its
// data, in one transaction; false (and no event) when m.Source was stored
// already.
func (s *Store) AddVoicemail(ctx context.Context, m voicemail.Message, event map[string]any) (bool, error) {
	ev, err := webhook.NewEvent(m.TenantID, "voicemail.created", event, m.CreatedAt)
	if err != nil {
		return false, err
	}
	added := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO voicemail_message (id, tenant_id, box_id, source, caller_number, caller_name,
				caller_extension_id, received_at, duration_ms, audio, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT (source) DO NOTHING`,
			m.ID, m.TenantID, m.BoxID, m.Source, m.CallerNumber, m.CallerName, m.CallerExtensionID,
			m.ReceivedAt, m.Duration.Milliseconds(), m.Audio, m.CreatedAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		added = true
		return insertEvent(ctx, tx, ev, nil)
	})
	return added, err
}

// VoicemailEmailTo returns the addresses of the box owner's own active
// accounts (a person's box; a ring group's has no owner's address).
func (s *Store) VoicemailEmailTo(ctx context.Context, box voicemail.Box) ([]string, error) {
	if box.ExtensionID == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT email FROM app_user
		WHERE tenant_id = $1 AND extension_id = $2 AND disabled_at IS NULL ORDER BY created_at LIMIT 10`,
		box.TenantID, *box.ExtensionID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// VoicemailMessage returns one of the tenant's messages with its audio.
func (s *Store) VoicemailMessage(ctx context.Context, tenant, id uuid.UUID) (voicemail.Message, error) {
	var m voicemail.Message
	var ms int64
	err := s.pool.QueryRow(ctx, `SELECT id, tenant_id, box_id, source, caller_number, caller_name, caller_extension_id,
			received_at, duration_ms, audio, created_at
		FROM voicemail_message WHERE id = $1 AND tenant_id = $2`, id, tenant).Scan(
		&m.ID, &m.TenantID, &m.BoxID, &m.Source, &m.CallerNumber, &m.CallerName, &m.CallerExtensionID,
		&m.ReceivedAt, &ms, &m.Audio, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, voicemail.ErrNotFound
	}
	m.Duration = msDuration(ms)
	return m, err
}

func msDuration(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }
