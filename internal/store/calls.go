package store

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/webhook"
)

// DeviceBySIPUsername returns the device Asterisk calls username (its PJSIP
// endpoint) and its extension's number.
func (s *Store) DeviceBySIPUsername(ctx context.Context, username string) (pbx.Device, string, error) {
	var number string
	var d pbx.Device
	err := s.pool.QueryRow(ctx, `SELECT d.id, d.tenant_id, d.extension_id, d.name, d.kind, d.sip_username, d.digest_hash,
			d.enabled, d.online, d.last_registered_at, d.last_registered_from, d.version, d.created_at, d.updated_at, e.number
		FROM device d JOIN extension e ON e.id = d.extension_id WHERE d.sip_username = $1`, username).Scan(
		&d.ID, &d.TenantID, &d.ExtensionID, &d.Name, &d.Kind, &d.SIPUsername, &d.DigestHash, &d.Enabled, &d.Online,
		&d.LastRegisteredAt, &d.LastRegisteredFrom, &d.Version, &d.CreatedAt, &d.UpdatedAt, &number)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, "", pbx.ErrNotFound
	}
	return d, number, err
}

// SetDeviceOnline records that a device signed in (online, from its
// address) or out. A change fires device.registered or device.unregistered
// in the same transaction; a sign-in while already online (e.g. from a new
// address) only updates last_registered_*. It returns whether the online
// state changed.
func (s *Store) SetDeviceOnline(ctx context.Context, username string, online bool, from *netip.Addr, at time.Time) (bool, error) {
	var changed bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var was bool
		var id uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id, online FROM device WHERE sip_username = $1 FOR UPDATE`, username).Scan(&id, &was)
		if errors.Is(err, pgx.ErrNoRows) {
			return pbx.ErrNotFound
		}
		if err != nil {
			return err
		}
		d, err := scanDevice(tx.QueryRow(ctx, `UPDATE device SET online = $2,
				last_registered_at = CASE WHEN $2 THEN $3 ELSE last_registered_at END,
				last_registered_from = CASE WHEN $2 THEN $4 ELSE last_registered_from END
			WHERE id = $1 RETURNING `+deviceColumns, id, online, at, from))
		if err != nil {
			return err
		}
		if was == online {
			return nil
		}
		changed = true
		data := map[string]any{"id": d.ID, "extension_id": d.ExtensionID, "name": d.Name, "sip_username": d.SIPUsername}
		eventType := "device.unregistered"
		if online {
			eventType = "device.registered"
			if from != nil {
				data["address"] = from.String()
			}
		}
		ev, err := webhook.NewEvent(d.TenantID, eventType, data, at)
		if err != nil {
			return err
		}
		return insertEvent(ctx, tx, ev, nil)
	})
	return changed, err
}

// InsertCallEvent queues a call webhook event (call.started, call.answered,
// call.ended). Calls live in the tracker's memory, not the database (call
// history arrives with CDR in a later slice), so the event is the only write.
func (s *Store) InsertCallEvent(ctx context.Context, tenant uuid.UUID, eventType string, data any, at time.Time) error {
	ev, err := webhook.NewEvent(tenant, eventType, data, at)
	if err != nil {
		return err
	}
	return insertEvent(ctx, s.pool, ev, nil)
}
