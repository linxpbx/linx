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
	"linxpbx.com/linx/internal/webhook"
)

var _ pbx.Store = (*Store)(nil)

const extensionColumns = `id, tenant_id, number, display_name, email, enabled, version, created_at, updated_at, deleted_at`

func scanExtension(row pgx.Row) (pbx.Extension, error) {
	var e pbx.Extension
	var email *string
	err := row.Scan(&e.ID, &e.TenantID, &e.Number, &e.DisplayName, &email, &e.Enabled, &e.Version,
		&e.CreatedAt, &e.UpdatedAt, &e.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, pbx.ErrNotFound
	}
	if email != nil {
		e.Email = *email
	}
	return e, err
}

func extensionEvent(e pbx.Extension, eventType string, at time.Time) (webhook.Event, error) {
	return webhook.NewEvent(e.TenantID, eventType, map[string]any{
		"id": e.ID, "number": e.Number, "display_name": e.DisplayName, "enabled": e.Enabled,
	}, at)
}

func (s *Store) CreateExtension(ctx context.Context, e pbx.Extension, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO extension (id, tenant_id, number, display_name, email, enabled, version, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			e.ID, e.TenantID, e.Number, e.DisplayName, emptyStrToNil(e.Email), e.Enabled, e.Version, e.CreatedAt, e.UpdatedAt)
		if err != nil {
			if IsUniqueViolation(err) {
				return pbx.ErrDuplicate
			}
			return fmt.Errorf("creating extension: %w", err)
		}
		ev, err := extensionEvent(e, "extension.created", e.CreatedAt)
		if err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, ev, nil); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) Extension(ctx context.Context, tenant, id uuid.UUID) (pbx.Extension, error) {
	return scanExtension(s.pool.QueryRow(ctx,
		`SELECT `+extensionColumns+` FROM extension WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL`, id, tenant))
}

func (s *Store) ListExtensions(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]pbx.Extension, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+extensionColumns+` FROM extension
		WHERE tenant_id = $1 AND deleted_at IS NULL AND ($2::uuid IS NULL OR id < $2) ORDER BY id DESC LIMIT $3`,
		tenant, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pbx.Extension{}
	for rows.Next() {
		e, err := scanExtension(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) UpdateExtension(ctx context.Context, e pbx.Extension, audit auth.AuditEntry) (pbx.Extension, error) {
	var out pbx.Extension
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanExtension(tx.QueryRow(ctx, `UPDATE extension SET
				number = $4, display_name = $5, email = $6, enabled = $7, version = version + 1, updated_at = $8
			WHERE id = $1 AND tenant_id = $2 AND version = $3 AND deleted_at IS NULL
			RETURNING `+extensionColumns,
			e.ID, e.TenantID, e.Version, e.Number, e.DisplayName, emptyStrToNil(e.Email), e.Enabled, e.UpdatedAt))
		if errors.Is(err, pbx.ErrNotFound) {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM extension WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL)`,
				e.ID, e.TenantID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return pbx.ErrVersionChanged
			}
			return pbx.ErrNotFound
		}
		if err != nil {
			if IsUniqueViolation(err) {
				return pbx.ErrDuplicate
			}
			return err
		}
		ev, err := extensionEvent(out, "extension.updated", e.UpdatedAt)
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

// revokeDevicesOf disables every still-enabled device of extension (turning
// the extension off, or deleting it, must stop them ringing at once) and
// fires one device.revoked event per device it revoked.
func revokeDevicesOf(ctx context.Context, tx pgx.Tx, extension uuid.UUID, at time.Time) error {
	rows, err := tx.Query(ctx, `UPDATE device SET enabled = false, version = version + 1, updated_at = $2
		WHERE extension_id = $1 AND enabled = true
		RETURNING `+deviceColumns, extension, at)
	if err != nil {
		return err
	}
	var revoked []pbx.Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			rows.Close()
			return err
		}
		revoked = append(revoked, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
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
}

func (s *Store) DeleteExtension(ctx context.Context, tenant, id uuid.UUID, at time.Time, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		e, err := scanExtension(tx.QueryRow(ctx, `UPDATE extension SET deleted_at = $3, enabled = false, updated_at = $3
			WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL RETURNING `+extensionColumns, id, tenant, at))
		if err != nil {
			return err
		}
		if err := revokeDevicesOf(ctx, tx, id, at); err != nil {
			return err
		}
		ev, err := extensionEvent(e, "extension.deleted", at)
		if err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, ev, nil); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

const deviceColumns = `id, tenant_id, extension_id, name, kind, sip_username, digest_hash, enabled,
	last_registered_at, last_registered_from, version, created_at, updated_at`

func scanDevice(row pgx.Row) (pbx.Device, error) {
	var d pbx.Device
	err := row.Scan(&d.ID, &d.TenantID, &d.ExtensionID, &d.Name, &d.Kind, &d.SIPUsername, &d.DigestHash, &d.Enabled,
		&d.LastRegisteredAt, &d.LastRegisteredFrom, &d.Version, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, pbx.ErrNotFound
	}
	return d, err
}

func deviceEvent(d pbx.Device, eventType string, at time.Time) (webhook.Event, error) {
	return webhook.NewEvent(d.TenantID, eventType, map[string]any{
		"id": d.ID, "extension_id": d.ExtensionID, "name": d.Name, "kind": d.Kind,
		"sip_username": d.SIPUsername, "enabled": d.Enabled,
	}, at)
}

func (s *Store) CreateDevice(ctx context.Context, d pbx.Device, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO device (id, tenant_id, extension_id, name, kind, sip_username, digest_hash,
			enabled, version, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			d.ID, d.TenantID, d.ExtensionID, d.Name, d.Kind, d.SIPUsername, d.DigestHash, d.Enabled, d.Version, d.CreatedAt, d.UpdatedAt)
		if err != nil {
			if IsUniqueViolation(err) {
				return pbx.ErrDuplicate
			}
			return fmt.Errorf("creating device: %w", err)
		}
		ev, err := deviceEvent(d, "device.created", d.CreatedAt)
		if err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, ev, nil); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) Device(ctx context.Context, tenant, id uuid.UUID) (pbx.Device, error) {
	return scanDevice(s.pool.QueryRow(ctx, `SELECT `+deviceColumns+` FROM device WHERE id = $1 AND tenant_id = $2`, id, tenant))
}

func (s *Store) ListDevicesByExtension(ctx context.Context, tenant, extension uuid.UUID, before *uuid.UUID, limit int) ([]pbx.Device, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+deviceColumns+` FROM device
		WHERE tenant_id = $1 AND extension_id = $2 AND ($3::uuid IS NULL OR id < $3) ORDER BY id DESC LIMIT $4`,
		tenant, extension, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pbx.Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) UpdateDevice(ctx context.Context, d pbx.Device, audit auth.AuditEntry) (pbx.Device, error) {
	var out pbx.Device
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanDevice(tx.QueryRow(ctx, `UPDATE device SET
				name = $4, digest_hash = $5, enabled = $6, version = version + 1, updated_at = $7
			WHERE id = $1 AND tenant_id = $2 AND version = $3
			RETURNING `+deviceColumns,
			d.ID, d.TenantID, d.Version, d.Name, d.DigestHash, d.Enabled, d.UpdatedAt))
		if errors.Is(err, pbx.ErrNotFound) {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM device WHERE id = $1 AND tenant_id = $2)`,
				d.ID, d.TenantID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return pbx.ErrVersionChanged
			}
			return pbx.ErrNotFound
		}
		if err != nil {
			return err
		}
		ev, err := deviceEvent(out, "device.updated", d.UpdatedAt)
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

// RevokeDevice locks the device row, so a concurrent revoke or extension
// deletion can't race it, then disables it only if it wasn't already.
func (s *Store) RevokeDevice(ctx context.Context, tenant, id uuid.UUID, at time.Time, audit auth.AuditEntry) (pbx.Device, error) {
	var out pbx.Device
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM device WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, tenant))
		if err != nil {
			return err
		}
		out = cur
		if cur.Enabled {
			out, err = scanDevice(tx.QueryRow(ctx, `UPDATE device SET enabled = false, version = version + 1, updated_at = $3
				WHERE id = $1 AND tenant_id = $2 RETURNING `+deviceColumns, id, tenant, at))
			if err != nil {
				return err
			}
			ev, err := deviceEvent(out, "device.revoked", at)
			if err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, ev, nil); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

func emptyStrToNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
