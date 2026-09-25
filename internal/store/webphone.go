package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
)

// The browser's phone line (docs/WEB.md §5): one web device per signed-in
// session, which Asterisk accepts only while the session is live (migration
// 0013's device_live view).

func (s *Store) IssueWebDevice(ctx context.Context, d pbx.Device, digest func(username string) string, audit auth.AuditEntry) (pbx.Device, error) {
	if d.Kind != pbx.KindWeb || d.UserSessionID == nil {
		return pbx.Device{}, errors.New("IssueWebDevice: not a web device with a session")
	}
	var out pbx.Device
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM device
			WHERE user_session_id = $1 AND revoked_at IS NULL FOR UPDATE`, *d.UserSessionID))
		switch {
		case err == nil && cur.ExtensionID == d.ExtensionID && cur.Enabled:
			// The same line again (the page reloaded): a new password only.
			// The username stays, so nothing else needs to know.
			out, err = scanDevice(tx.QueryRow(ctx, `UPDATE device SET digest_hash = $2, version = version + 1, updated_at = $3
				WHERE id = $1 RETURNING `+deviceColumns, cur.ID, digest(cur.SIPUsername), d.UpdatedAt))
			if err != nil {
				return err
			}
			return insertAudit(ctx, tx, audit)
		case err == nil:
			// The person moved to another extension (or an admin turned
			// this line off): this one goes for good, a new one replaces it.
			if err := revokeWebDeviceTx(ctx, tx, cur.ID, d.UpdatedAt); err != nil {
				return err
			}
		case !errors.Is(err, pbx.ErrNotFound):
			return err
		}
		d.DigestHash = digest(d.SIPUsername)
		_, err = tx.Exec(ctx, `INSERT INTO device (id, tenant_id, extension_id, name, kind, sip_username, digest_hash,
			enabled, user_session_id, version, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			d.ID, d.TenantID, d.ExtensionID, d.Name, d.Kind, d.SIPUsername, d.DigestHash, d.Enabled, d.UserSessionID,
			d.Version, d.CreatedAt, d.UpdatedAt)
		if err != nil {
			if IsUniqueViolation(err) {
				return pbx.ErrDuplicate
			}
			return fmt.Errorf("creating web device: %w", err)
		}
		out = d
		ev, err := deviceEvent(d, "device.created", d.CreatedAt)
		if err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, ev, nil); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

func revokeWebDeviceTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, at time.Time) error {
	d, err := scanDevice(tx.QueryRow(ctx, `UPDATE device SET enabled = false, online = false, revoked_at = $2, version = version + 1, updated_at = $2
		WHERE id = $1 RETURNING `+deviceColumns, id, at))
	if err != nil {
		return err
	}
	ev, err := deviceEvent(d, "device.revoked", at)
	if err != nil {
		return err
	}
	return insertEvent(ctx, tx, ev, nil)
}

func (s *Store) WebDeviceForSession(ctx context.Context, session uuid.UUID) (pbx.Device, error) {
	return scanDevice(s.pool.QueryRow(ctx, `SELECT `+deviceColumns+` FROM device
		WHERE user_session_id = $1 AND revoked_at IS NULL`, session))
}

func (s *Store) RevokeDeadWebDevices(ctx context.Context, at time.Time) ([]pbx.Device, error) {
	var revoked []pbx.Device
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE device d SET enabled = false, online = false, revoked_at = $1, version = version + 1, updated_at = $1
			WHERE d.kind = 'web' AND d.revoked_at IS NULL
			  AND NOT EXISTS (SELECT 1 FROM device_live l WHERE l.id = d.id)
			RETURNING `+deviceColumns, at)
		if err != nil {
			return err
		}
		revoked, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (pbx.Device, error) { return scanDevice(r) })
		if err != nil {
			return err
		}
		for _, d := range revoked {
			ev, err := deviceEvent(d, "device.revoked", at)
			if err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, ev, nil); err != nil {
				return err
			}
		}
		return nil
	})
	return revoked, err
}
