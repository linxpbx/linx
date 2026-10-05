package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"linxpbx.com/linx/internal/auth"
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

// VoicemailBoxes returns the tenant's boxes (migration 0034), each marked
// as user's own or a ring group's user is in, with its message counts. A
// message's bytes are its length (8 a millisecond), so nothing reads the
// audio. Own box first, then groups, then people, by name.
func (s *Store) VoicemailBoxes(ctx context.Context, tenant, user uuid.UUID) ([]voicemail.BoxView, error) {
	rows, err := s.pool.Query(ctx, `WITH me AS (SELECT extension_id FROM app_user WHERE id = $2 AND tenant_id = $1)
		SELECT b.id, b.tenant_id, b.extension_id, b.ring_group_id,
			coalesce(e.display_name || ' (' || e.number || ')', g.name, ''), b.enabled, b.email,
			coalesce(e.deleted_at IS NOT NULL, false),
			b.extension_id IS NOT NULL AND b.extension_id = (SELECT extension_id FROM me),
			b.ring_group_id IS NOT NULL AND EXISTS (SELECT 1 FROM ring_group_member m
				WHERE m.ring_group_id = b.ring_group_id AND m.extension_id = (SELECT extension_id FROM me)),
			count(v.id), count(v.id) FILTER (WHERE v.heard_at IS NULL), coalesce(sum(v.duration_ms), 0) * 8
		FROM voicemail_box b
		LEFT JOIN extension e ON e.id = b.extension_id
		LEFT JOIN ring_group g ON g.id = b.ring_group_id
		LEFT JOIN voicemail_message v ON v.box_id = b.id
		WHERE b.tenant_id = $1
		GROUP BY b.id, e.id, g.id
		ORDER BY 9 DESC, b.ring_group_id IS NULL, lower(coalesce(e.display_name, g.name)), b.id`, tenant, user)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (voicemail.BoxView, error) {
		var b voicemail.BoxView
		err := r.Scan(&b.ID, &b.TenantID, &b.ExtensionID, &b.RingGroupID, &b.Owner, &b.Enabled, &b.Email,
			&b.Removed, &b.Mine, &b.Member, &b.Count, &b.New, &b.Bytes)
		return b, err
	})
}

// VoicemailList returns the messages in boxes, newest first, without
// their audio; who heard each, and whether it was user.
func (s *Store) VoicemailList(ctx context.Context, tenant uuid.UUID, boxes []uuid.UUID, user uuid.UUID, limit int) ([]voicemail.Listed, error) {
	rows, err := s.pool.Query(ctx, `SELECT v.id, v.tenant_id, v.box_id, v.source, v.caller_number, v.caller_name,
			v.caller_extension_id, v.received_at, v.duration_ms, v.created_at, v.heard_at, coalesce(u.name, ''), coalesce(v.heard_by = $3, false)
		FROM voicemail_message v LEFT JOIN app_user u ON u.id = v.heard_by
		WHERE v.tenant_id = $1 AND v.box_id = ANY($2)
		ORDER BY v.received_at DESC, v.id DESC LIMIT $4`, tenant, boxes, user, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (voicemail.Listed, error) {
		var l voicemail.Listed
		var ms int64
		err := r.Scan(&l.ID, &l.TenantID, &l.BoxID, &l.Source, &l.CallerNumber, &l.CallerName, &l.CallerExtensionID,
			&l.ReceivedAt, &ms, &l.CreatedAt, &l.HeardAt, &l.HeardBy, &l.HeardByMe)
		l.Duration = msDuration(ms)
		return l, err
	})
}

// VoicemailInfo is VoicemailMessage without the audio.
func (s *Store) VoicemailInfo(ctx context.Context, tenant, id uuid.UUID) (voicemail.Message, error) {
	var m voicemail.Message
	var ms int64
	err := s.pool.QueryRow(ctx, `SELECT id, tenant_id, box_id, source, caller_number, caller_name, caller_extension_id,
			received_at, duration_ms, created_at
		FROM voicemail_message WHERE id = $1 AND tenant_id = $2`, id, tenant).Scan(
		&m.ID, &m.TenantID, &m.BoxID, &m.Source, &m.CallerNumber, &m.CallerName, &m.CallerExtensionID,
		&m.ReceivedAt, &ms, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, voicemail.ErrNotFound
	}
	m.Duration = msDuration(ms)
	return m, err
}

// MarkVoicemail marks a message heard by heardBy at at, or new (nil).
func (s *Store) MarkVoicemail(ctx context.Context, tenant, id uuid.UUID, heardBy *uuid.UUID, at time.Time) error {
	var tag pgconn.CommandTag
	var err error
	if heardBy == nil {
		tag, err = s.pool.Exec(ctx, `UPDATE voicemail_message SET heard_at = NULL, heard_by = NULL
			WHERE id = $1 AND tenant_id = $2`, id, tenant)
	} else {
		// Heard once is heard: the first person keeps the credit.
		tag, err = s.pool.Exec(ctx, `UPDATE voicemail_message SET heard_at = coalesce(heard_at, $3),
				heard_by = CASE WHEN heard_at IS NULL THEN $4 ELSE heard_by END
			WHERE id = $1 AND tenant_id = $2`, id, tenant, at, *heardBy)
	}
	if err == nil && tag.RowsAffected() == 0 {
		return voicemail.ErrNotFound
	}
	return err
}

// DeleteVoicemail deletes a message, audited.
func (s *Store) DeleteVoicemail(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM voicemail_message WHERE id = $1 AND tenant_id = $2`, id, tenant)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return voicemail.ErrNotFound
		}
		return insertAudit(ctx, tx, audit)
	})
}

// VoicemailGreetings is box's two greetings: recorded or not, in use.
func (s *Store) VoicemailGreetings(ctx context.Context, box uuid.UUID) (map[string]voicemail.GreetingInfo, error) {
	out := map[string]voicemail.GreetingInfo{voicemail.GreetingUnavailable: {}, voicemail.GreetingClosed: {}}
	rows, err := s.pool.Query(ctx, `SELECT kind, in_use, recorded_at, length(audio) FROM voicemail_greeting WHERE box_id = $1`, box)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var n int64
		g := voicemail.GreetingInfo{Recorded: true}
		if err := rows.Scan(&kind, &g.InUse, &g.RecordedAt, &n); err != nil {
			return nil, err
		}
		g.Duration = time.Duration(n) * time.Second / (2 * voicemail.GreetingRate)
		out[kind] = g
	}
	return out, rows.Err()
}

// UpdateVoicemailBox changes a box's switches and which greetings it
// plays, audited.
func (s *Store) UpdateVoicemailBox(ctx context.Context, tenant, id uuid.UUID, p voicemail.BoxPatch, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE voicemail_box SET enabled = coalesce($3, enabled), email = coalesce($4, email)
			WHERE id = $1 AND tenant_id = $2`, id, tenant, p.Enabled, p.Email)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return voicemail.ErrNotFound
		}
		for kind, own := range p.UseOwn {
			if _, err := tx.Exec(ctx, `UPDATE voicemail_greeting SET in_use = $3 WHERE box_id = $1 AND kind = $2`, id, kind, own); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, audit)
	})
}

// SetGreeting keeps box's new recording of kind (slin16), in use, audited.
func (s *Store) SetGreeting(ctx context.Context, tenant, box uuid.UUID, kind string, audio []byte, at time.Time, by uuid.UUID, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO voicemail_greeting (box_id, kind, tenant_id, in_use, audio, recorded_at, recorded_by)
			SELECT $1, $2, $3, true, $4, $5, $6 FROM voicemail_box WHERE id = $1 AND tenant_id = $3
			ON CONFLICT (box_id, kind) DO UPDATE SET in_use = true, audio = EXCLUDED.audio,
				recorded_at = EXCLUDED.recorded_at, recorded_by = EXCLUDED.recorded_by`, box, kind, tenant, audio, at, by)
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

// DeleteGreeting forgets box's recording of kind, audited.
func (s *Store) DeleteGreeting(ctx context.Context, tenant, box uuid.UUID, kind string, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM voicemail_greeting WHERE box_id = $1 AND kind = $2 AND tenant_id = $3`, box, kind, tenant); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

// GreetingAudio is box's recording of kind (slin16).
func (s *Store) GreetingAudio(ctx context.Context, box uuid.UUID, kind string) ([]byte, error) {
	var audio []byte
	err := s.pool.QueryRow(ctx, `SELECT audio FROM voicemail_greeting WHERE box_id = $1 AND kind = $2`, box, kind).Scan(&audio)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, voicemail.ErrNotFound
	}
	return audio, err
}

// GreetingsInUse lists every greeting in use, on every tenant, for
// Asterisk's folder. A box that's off plays nothing, but its file stays
// so turning it back on needs no copy.
func (s *Store) GreetingsInUse(ctx context.Context) ([]voicemail.GreetingFile, error) {
	rows, err := s.pool.Query(ctx, `SELECT box_id, kind, recorded_at FROM voicemail_greeting WHERE in_use`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (voicemail.GreetingFile, error) {
		var g voicemail.GreetingFile
		err := r.Scan(&g.BoxID, &g.Kind, &g.RecordedAt)
		return g, err
	})
}

// VoicemailKeepDays is how long messages are kept (migration 0035).
func (s *Store) VoicemailKeepDays(ctx context.Context) (int, error) {
	var days int
	err := s.pool.QueryRow(ctx, `SELECT voicemail_keep_days FROM pbx_setting`).Scan(&days)
	return days, err
}

// SetVoicemailKeepDays changes it, audited.
func (s *Store) SetVoicemailKeepDays(ctx context.Context, days int, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE pbx_setting SET voicemail_keep_days = $1`, days); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

// VoicemailUsage is the tenant's messages and the space they take, with
// its greetings'.
func (s *Store) VoicemailUsage(ctx context.Context, tenant uuid.UUID) (voicemail.Usage, error) {
	var u voicemail.Usage
	err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM voicemail_message WHERE tenant_id = $1),
			(SELECT coalesce(sum(duration_ms), 0) * 8 FROM voicemail_message WHERE tenant_id = $1)
			+ (SELECT coalesce(sum(length(audio)), 0) FROM voicemail_greeting WHERE tenant_id = $1)`, tenant).Scan(&u.Count, &u.Bytes)
	return u, err
}

// ExpireVoicemail deletes messages received more than the kept days
// before now, returning the tenants that lost any.
func (s *Store) ExpireVoicemail(ctx context.Context, now time.Time) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `WITH gone AS (DELETE FROM voicemail_message
			WHERE received_at < $1::timestamptz - make_interval(days => (SELECT voicemail_keep_days FROM pbx_setting))
			RETURNING tenant_id)
		SELECT DISTINCT tenant_id FROM gone`, now)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

// Listening from a phone, and the message-waiting light (Phase 2 step 9b).

// HeardVoicemail marks a message heard, by somebody or by nobody in
// particular. *97 needs this rather than MarkVoicemail, whose nil means
// the opposite — "make it new again", which is what the web app's **Mark
// as new** does — and an extension in a corridor has no person to name.
func (s *Store) HeardVoicemail(ctx context.Context, tenant, id uuid.UUID, by *uuid.UUID, at time.Time) error {
	// Heard once is heard: the first person keeps the credit.
	tag, err := s.pool.Exec(ctx, `UPDATE voicemail_message SET heard_at = coalesce(heard_at, $3),
			heard_by = CASE WHEN heard_at IS NULL THEN $4 ELSE heard_by END
		WHERE id = $1 AND tenant_id = $2`, id, tenant, at, by)
	if err == nil && tag.RowsAffected() == 0 {
		return voicemail.ErrNotFound
	}
	return err
}

// BoxForEndpoint is the voicemail box of the extension whose live device
// has this SIP username, and the person it belongs to. *97 asks this
// about the channel that dialled it, so a phone can only ever reach its
// own extension's messages.
//
// An extension may have no person (a phone in a corridor): there is still
// a box, and the phone may still hear it. Nobody's name goes against the
// messages it marks heard, which is what "heard by nobody in particular"
// looks like in the web app already.
func (s *Store) BoxForEndpoint(ctx context.Context, sipUsername string) (voicemail.Box, *uuid.UUID, error) {
	var b voicemail.Box
	var owner *uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT b.id, b.tenant_id, b.extension_id, b.ring_group_id,
			coalesce(e.display_name || ' (' || e.number || ')', ''), b.enabled, b.email, u.id
		FROM device_live l
		JOIN extension e ON e.id = l.extension_id
		JOIN voicemail_box b ON b.extension_id = l.extension_id
		LEFT JOIN app_user u ON u.extension_id = e.id AND u.disabled_at IS NULL
		WHERE l.sip_username = $1`, sipUsername).
		Scan(&b.ID, &b.TenantID, &b.ExtensionID, &b.RingGroupID, &b.Owner, &b.Enabled, &b.Email, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, nil, voicemail.ErrNotFound
	}
	return b, owner, err
}

// UnheardVoicemail is a box's messages nobody has heard, oldest first and
// without their audio: *97 plays them in the order they arrived.
func (s *Store) UnheardVoicemail(ctx context.Context, box uuid.UUID) ([]voicemail.Message, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, tenant_id, box_id, caller_number, caller_name, received_at, duration_ms
		FROM voicemail_message WHERE box_id = $1 AND heard_at IS NULL
		ORDER BY received_at LIMIT $2`, box, voicemail.MaxList)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []voicemail.Message
	for rows.Next() {
		var m voicemail.Message
		var ms int
		if err := rows.Scan(&m.ID, &m.TenantID, &m.BoxID, &m.CallerNumber, &m.CallerName, &m.ReceivedAt, &ms); err != nil {
			return nil, err
		}
		m.Duration = time.Duration(ms) * time.Millisecond
		out = append(out, m)
	}
	return out, rows.Err()
}

// VoicemailCounts is every person's box with what it holds, for the
// message-waiting light. A ring group's box isn't counted: no phone
// belongs to one, so there is no light to turn on. A box with nothing in
// it is still named, because that is how a light goes out.
func (s *Store) VoicemailCounts(ctx context.Context) (map[uuid.UUID]voicemail.Counts, error) {
	rows, err := s.pool.Query(ctx, `SELECT b.id,
			count(m.id) FILTER (WHERE m.heard_at IS NULL),
			count(m.id) FILTER (WHERE m.heard_at IS NOT NULL)
		FROM voicemail_box b
		LEFT JOIN voicemail_message m ON m.box_id = b.id
		WHERE b.extension_id IS NOT NULL AND b.enabled
		GROUP BY b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]voicemail.Counts{}
	for rows.Next() {
		var id uuid.UUID
		var c voicemail.Counts
		if err := rows.Scan(&id, &c.New, &c.Old); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}
