package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/trunk"
	"linxpbx.com/linx/internal/webhook"
)

var _ trunk.Store = (*Store)(nil)

const trunkColumns = `id, tenant_id, name, kind, template, host, port, transport, media_encryption, cert_trust,
	pinned_certificate, username, password_enc, dial_format, codecs, caller_id_number, max_calls,
	wireguard_profile_id, outbound_priority, unencrypted_confirmed_by, unencrypted_confirmed_at,
	enabled, version, created_at, updated_at, status, status_detail, status_since`

func scanTrunk(row pgx.Row) (trunk.Trunk, error) {
	var t trunk.Trunk
	var pinnedCert, callerID, confirmedBy *string
	err := row.Scan(&t.ID, &t.TenantID, &t.Name, &t.Kind, &t.Template, &t.Host, &t.Port, &t.Transport,
		&t.MediaEncryption, &t.CertTrust, &pinnedCert, &t.Username, &t.PasswordEnc, &t.DialFormat, &t.Codecs,
		&callerID, &t.MaxCalls, &t.WireGuardProfileID, &t.OutboundPriority, &confirmedBy, &t.UnencryptedConfirmedAt,
		&t.Enabled, &t.Version, &t.CreatedAt, &t.UpdatedAt, &t.Status, &t.StatusDetail, &t.StatusSince)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, trunk.ErrNotFound
	}
	t.PinnedCertificate, t.CallerIDNumber, t.UnencryptedConfirmedBy = deref(pinnedCert), deref(callerID), deref(confirmedBy)
	return t, err
}

func trunkEvent(t trunk.Trunk, eventType string, at time.Time) (webhook.Event, error) {
	return webhook.NewEvent(t.TenantID, eventType, map[string]any{
		"id": t.ID, "name": t.Name, "kind": t.Kind, "host": t.Host, "enabled": t.Enabled,
	}, at)
}

// SetTrunkStatus records a trunk's new state and, in the same transaction,
// fires trunk.status_changed. Nothing changes (and false comes back) if the
// trunk is gone or already has that status. It doesn't bump version: the
// state isn't one of the trunk's settings.
func (s *Store) SetTrunkStatus(ctx context.Context, tenant, id uuid.UUID, status, detail string, at time.Time) (bool, error) {
	changed := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var previous string
		err := tx.QueryRow(ctx, `SELECT status FROM trunk WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, tenant).Scan(&previous)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if previous == status {
			_, err := tx.Exec(ctx, `UPDATE trunk SET status_detail = $2 WHERE id = $1 AND status_detail <> $2`, id, detail)
			return err
		}
		t, err := scanTrunk(tx.QueryRow(ctx, `UPDATE trunk SET status = $2, status_detail = $3, status_since = $4
			WHERE id = $1 RETURNING `+trunkColumns, id, status, detail, at))
		if err != nil {
			return err
		}
		changed = true
		ev, err := webhook.NewEvent(t.TenantID, "trunk.status_changed", map[string]any{
			"id": t.ID, "name": t.Name, "status": status, "previous_status": previous, "detail": detail,
		}, at)
		if err != nil {
			return err
		}
		return insertEvent(ctx, tx, ev, nil)
	})
	return changed, err
}

// AllTrunks returns every trunk of every tenant, for the trunk monitor.
func (s *Store) AllTrunks(ctx context.Context) ([]trunk.Trunk, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+trunkColumns+` FROM trunk ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []trunk.Trunk
	for rows.Next() {
		t, err := scanTrunk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) CreateTrunk(ctx context.Context, t trunk.Trunk, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO trunk (id, tenant_id, name, kind, template, host, port, transport,
				media_encryption, cert_trust, pinned_certificate, username, password_enc, dial_format, codecs,
				caller_id_number, max_calls, wireguard_profile_id, unencrypted_confirmed_by, unencrypted_confirmed_at,
				enabled, version, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)`,
			t.ID, t.TenantID, t.Name, t.Kind, t.Template, t.Host, t.Port, t.Transport, t.MediaEncryption, t.CertTrust,
			emptyStrToNil(t.PinnedCertificate), t.Username, t.PasswordEnc, t.DialFormat, t.Codecs,
			emptyStrToNil(t.CallerIDNumber), t.MaxCalls, t.WireGuardProfileID,
			emptyStrToNil(t.UnencryptedConfirmedBy), t.UnencryptedConfirmedAt, t.Enabled, t.Version, t.CreatedAt, t.UpdatedAt)
		if err != nil {
			if IsUniqueViolation(err) {
				return trunk.ErrDuplicate
			}
			if isForeignKeyViolation(err) {
				return trunk.ErrNotFound
			}
			return fmt.Errorf("creating trunk: %w", err)
		}
		ev, err := trunkEvent(t, "trunk.created", t.CreatedAt)
		if err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, ev, nil); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) Trunk(ctx context.Context, tenant, id uuid.UUID) (trunk.Trunk, error) {
	return scanTrunk(s.pool.QueryRow(ctx, `SELECT `+trunkColumns+` FROM trunk WHERE id = $1 AND tenant_id = $2`, id, tenant))
}

func (s *Store) ListTrunks(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]trunk.Trunk, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+trunkColumns+` FROM trunk
		WHERE tenant_id = $1 AND ($2::uuid IS NULL OR id < $2) ORDER BY id DESC LIMIT $3`, tenant, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []trunk.Trunk{}
	for rows.Next() {
		t, err := scanTrunk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) UpdateTrunk(ctx context.Context, t trunk.Trunk, audit auth.AuditEntry) (trunk.Trunk, error) {
	var out trunk.Trunk
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanTrunk(tx.QueryRow(ctx, `UPDATE trunk SET
				name = $4, kind = $5, template = $6, host = $7, port = $8, transport = $9, media_encryption = $10,
				cert_trust = $11, pinned_certificate = $12, username = $13, password_enc = $14, dial_format = $15,
				codecs = $16, caller_id_number = $17, max_calls = $18, wireguard_profile_id = $19,
				unencrypted_confirmed_by = $20, unencrypted_confirmed_at = $21, enabled = $22, version = version + 1,
				updated_at = $23
			WHERE id = $1 AND tenant_id = $2 AND version = $3 RETURNING `+trunkColumns,
			t.ID, t.TenantID, t.Version, t.Name, t.Kind, t.Template, t.Host, t.Port, t.Transport, t.MediaEncryption,
			t.CertTrust, emptyStrToNil(t.PinnedCertificate), t.Username, t.PasswordEnc, t.DialFormat, t.Codecs,
			emptyStrToNil(t.CallerIDNumber), t.MaxCalls, t.WireGuardProfileID, emptyStrToNil(t.UnencryptedConfirmedBy),
			t.UnencryptedConfirmedAt, t.Enabled, t.UpdatedAt))
		if errors.Is(err, trunk.ErrNotFound) {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM trunk WHERE id = $1 AND tenant_id = $2)`,
				t.ID, t.TenantID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return trunk.ErrVersionChanged
			}
			return trunk.ErrNotFound
		}
		if err != nil {
			if IsUniqueViolation(err) {
				return trunk.ErrDuplicate
			}
			if isForeignKeyViolation(err) {
				return trunk.ErrNotFound
			}
			return err
		}
		ev, err := trunkEvent(out, "trunk.updated", t.UpdatedAt)
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

func (s *Store) DeleteTrunk(ctx context.Context, tenant, id uuid.UUID, at time.Time, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := scanTrunk(tx.QueryRow(ctx, `DELETE FROM trunk WHERE id = $1 AND tenant_id = $2 RETURNING `+trunkColumns, id, tenant))
		if err != nil {
			return err
		}
		ev, err := trunkEvent(t, "trunk.deleted", at)
		if err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, ev, nil); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

// SetOutboundOrder sets order[i]'s outbound_priority to i+1 and clears
// every other trunk's, in one transaction (docs/TRUNKS.md §5).
func (s *Store) SetOutboundOrder(ctx context.Context, tenant uuid.UUID, order []uuid.UUID, at time.Time, audit auth.AuditEntry) ([]trunk.Trunk, error) {
	var out []trunk.Trunk
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE trunk SET outbound_priority = NULL, version = version + 1, updated_at = $2
			WHERE tenant_id = $1 AND outbound_priority IS NOT NULL`, tenant, at); err != nil {
			return fmt.Errorf("clearing outbound order: %w", err)
		}
		for i, id := range order {
			tag, err := tx.Exec(ctx, `UPDATE trunk SET outbound_priority = $3, version = version + 1, updated_at = $4
				WHERE id = $1 AND tenant_id = $2`, id, tenant, i+1, at)
			if err != nil {
				return fmt.Errorf("setting outbound order: %w", err)
			}
			if tag.RowsAffected() == 0 {
				return trunk.ErrNotFound
			}
		}
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+trunkColumns+` FROM trunk WHERE tenant_id = $1 ORDER BY id DESC`, tenant)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = []trunk.Trunk{}
		for rows.Next() {
			t, err := scanTrunk(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

const didColumns = `id, tenant_id, trunk_id, number, label, extension_id, version, created_at, updated_at`

func scanDID(row pgx.Row) (trunk.DID, error) {
	var d trunk.DID
	err := row.Scan(&d.ID, &d.TenantID, &d.TrunkID, &d.Number, &d.Label, &d.ExtensionID, &d.Version, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, trunk.ErrNotFound
	}
	return d, err
}

func (s *Store) CreateDID(ctx context.Context, d trunk.DID, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO trunk_did (id, tenant_id, trunk_id, number, label, extension_id, version, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			d.ID, d.TenantID, d.TrunkID, d.Number, d.Label, d.ExtensionID, d.Version, d.CreatedAt, d.UpdatedAt)
		if err != nil {
			if IsUniqueViolation(err) {
				return trunk.ErrDuplicate
			}
			if isForeignKeyViolation(err) {
				return trunk.ErrNotFound
			}
			return fmt.Errorf("creating DID: %w", err)
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) DID(ctx context.Context, tenant, id uuid.UUID) (trunk.DID, error) {
	return scanDID(s.pool.QueryRow(ctx, `SELECT `+didColumns+` FROM trunk_did WHERE id = $1 AND tenant_id = $2`, id, tenant))
}

func (s *Store) ListDIDsByTrunk(ctx context.Context, tenant, trunkID uuid.UUID, before *uuid.UUID, limit int) ([]trunk.DID, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+didColumns+` FROM trunk_did
		WHERE tenant_id = $1 AND trunk_id = $2 AND ($3::uuid IS NULL OR id < $3) ORDER BY id DESC LIMIT $4`,
		tenant, trunkID, before, limit)
	if err != nil {
		return nil, err
	}
	return collectDIDs(rows)
}

func (s *Store) ListDIDs(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]trunk.DID, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+didColumns+` FROM trunk_did
		WHERE tenant_id = $1 AND ($2::uuid IS NULL OR id < $2) ORDER BY id DESC LIMIT $3`, tenant, before, limit)
	if err != nil {
		return nil, err
	}
	return collectDIDs(rows)
}

func collectDIDs(rows pgx.Rows) ([]trunk.DID, error) {
	defer rows.Close()
	out := []trunk.DID{}
	for rows.Next() {
		d, err := scanDID(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) UpdateDID(ctx context.Context, d trunk.DID, audit auth.AuditEntry) (trunk.DID, error) {
	var out trunk.DID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanDID(tx.QueryRow(ctx, `UPDATE trunk_did SET label = $4, extension_id = $5, version = version + 1, updated_at = $6
			WHERE id = $1 AND tenant_id = $2 AND version = $3 RETURNING `+didColumns,
			d.ID, d.TenantID, d.Version, d.Label, d.ExtensionID, d.UpdatedAt))
		if errors.Is(err, trunk.ErrNotFound) {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM trunk_did WHERE id = $1 AND tenant_id = $2)`,
				d.ID, d.TenantID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return trunk.ErrVersionChanged
			}
			return trunk.ErrNotFound
		}
		if err != nil {
			if isForeignKeyViolation(err) {
				return trunk.ErrNotFound
			}
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

func (s *Store) DeleteDID(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM trunk_did WHERE id = $1 AND tenant_id = $2`, id, tenant)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return trunk.ErrNotFound
		}
		return insertAudit(ctx, tx, audit)
	})
}

const wireguardProfileColumns = `id, tenant_id, name, address, private_key_enc, public_key, peer_public_key,
	peer_endpoint_host, peer_endpoint_port, preshared_key_enc, persistent_keepalive, version, created_at, updated_at,
	status, status_detail, status_since, last_handshake_at`

func scanWireGuardProfile(row pgx.Row) (trunk.WireGuardProfile, error) {
	var w trunk.WireGuardProfile
	err := row.Scan(&w.ID, &w.TenantID, &w.Name, &w.Address, &w.PrivateKeyEnc, &w.PublicKey, &w.PeerPublicKey,
		&w.PeerEndpointHost, &w.PeerEndpointPort, &w.PresharedKeyEnc, &w.PersistentKeepalive, &w.Version, &w.CreatedAt, &w.UpdatedAt,
		&w.Status, &w.StatusDetail, &w.StatusSince, &w.LastHandshakeAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, trunk.ErrNotFound
	}
	return w, err
}

// AllWireGuardProfiles returns every tenant's profiles, for the trunk
// monitor.
func (s *Store) AllWireGuardProfiles(ctx context.Context) ([]trunk.WireGuardProfile, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+wireguardProfileColumns+` FROM wireguard_profile ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []trunk.WireGuardProfile
	for rows.Next() {
		w, err := scanWireGuardProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// SetWireGuardStatus records a tunnel's state. status_since moves only when
// the status does; version doesn't (it isn't a setting).
func (s *Store) SetWireGuardStatus(ctx context.Context, id uuid.UUID, status, detail string, lastHandshake *time.Time, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE wireguard_profile SET
			status_since = CASE WHEN status <> $2 THEN $5 ELSE status_since END,
			status = $2, status_detail = $3, last_handshake_at = $4
		WHERE id = $1`, id, status, detail, lastHandshake, at)
	return err
}

func (s *Store) CreateWireGuardProfile(ctx context.Context, w trunk.WireGuardProfile, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO wireguard_profile (id, tenant_id, name, address, private_key_enc, public_key,
				peer_public_key, peer_endpoint_host, peer_endpoint_port, preshared_key_enc, persistent_keepalive,
				version, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			w.ID, w.TenantID, w.Name, w.Address, w.PrivateKeyEnc, w.PublicKey, w.PeerPublicKey, w.PeerEndpointHost,
			w.PeerEndpointPort, w.PresharedKeyEnc, w.PersistentKeepalive, w.Version, w.CreatedAt, w.UpdatedAt)
		if err != nil {
			if IsUniqueViolation(err) {
				return trunk.ErrDuplicate
			}
			return fmt.Errorf("creating wireguard profile: %w", err)
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) WireGuardProfile(ctx context.Context, tenant, id uuid.UUID) (trunk.WireGuardProfile, error) {
	return scanWireGuardProfile(s.pool.QueryRow(ctx, `SELECT `+wireguardProfileColumns+` FROM wireguard_profile WHERE id = $1 AND tenant_id = $2`, id, tenant))
}

func (s *Store) ListWireGuardProfiles(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]trunk.WireGuardProfile, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+wireguardProfileColumns+` FROM wireguard_profile
		WHERE tenant_id = $1 AND ($2::uuid IS NULL OR id < $2) ORDER BY id DESC LIMIT $3`, tenant, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []trunk.WireGuardProfile{}
	for rows.Next() {
		w, err := scanWireGuardProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) UpdateWireGuardProfile(ctx context.Context, w trunk.WireGuardProfile, audit auth.AuditEntry) (trunk.WireGuardProfile, error) {
	var out trunk.WireGuardProfile
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanWireGuardProfile(tx.QueryRow(ctx, `UPDATE wireguard_profile SET
				name = $4, peer_endpoint_host = $5, peer_endpoint_port = $6, persistent_keepalive = $7,
				version = version + 1, updated_at = $8
			WHERE id = $1 AND tenant_id = $2 AND version = $3 RETURNING `+wireguardProfileColumns,
			w.ID, w.TenantID, w.Version, w.Name, w.PeerEndpointHost, w.PeerEndpointPort, w.PersistentKeepalive, w.UpdatedAt))
		if errors.Is(err, trunk.ErrNotFound) {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM wireguard_profile WHERE id = $1 AND tenant_id = $2)`,
				w.ID, w.TenantID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return trunk.ErrVersionChanged
			}
			return trunk.ErrNotFound
		}
		if err != nil {
			if IsUniqueViolation(err) {
				return trunk.ErrDuplicate
			}
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

func (s *Store) DeleteWireGuardProfile(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM wireguard_profile WHERE id = $1 AND tenant_id = $2`, id, tenant)
		if err != nil {
			if isRestrictViolation(err) {
				return trunk.ErrInUse
			}
			return err
		}
		if tag.RowsAffected() == 0 {
			return trunk.ErrNotFound
		}
		return insertAudit(ctx, tx, audit)
	})
}

const callPermissionLevelColumns = `id, tenant_id, name, allowed_categories, withhold_caller_id, version, created_at, updated_at`

func scanCallPermissionLevel(row pgx.Row) (trunk.CallPermissionLevel, error) {
	var l trunk.CallPermissionLevel
	err := row.Scan(&l.ID, &l.TenantID, &l.Name, &l.AllowedCategories, &l.WithholdCallerID, &l.Version, &l.CreatedAt, &l.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, trunk.ErrNotFound
	}
	return l, err
}

func (s *Store) CreateCallPermissionLevel(ctx context.Context, l trunk.CallPermissionLevel, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO call_permission_level (id, tenant_id, name, allowed_categories, withhold_caller_id, version, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			l.ID, l.TenantID, l.Name, l.AllowedCategories, l.WithholdCallerID, l.Version, l.CreatedAt, l.UpdatedAt)
		if err != nil {
			if IsUniqueViolation(err) {
				return trunk.ErrDuplicate
			}
			return fmt.Errorf("creating call permission level: %w", err)
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) CallPermissionLevel(ctx context.Context, tenant, id uuid.UUID) (trunk.CallPermissionLevel, error) {
	return scanCallPermissionLevel(s.pool.QueryRow(ctx, `SELECT `+callPermissionLevelColumns+` FROM call_permission_level WHERE id = $1 AND tenant_id = $2`, id, tenant))
}

func (s *Store) ListCallPermissionLevels(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]trunk.CallPermissionLevel, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+callPermissionLevelColumns+` FROM call_permission_level
		WHERE tenant_id = $1 AND ($2::uuid IS NULL OR id < $2) ORDER BY id DESC LIMIT $3`, tenant, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []trunk.CallPermissionLevel{}
	for rows.Next() {
		l, err := scanCallPermissionLevel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) UpdateCallPermissionLevel(ctx context.Context, l trunk.CallPermissionLevel, audit auth.AuditEntry) (trunk.CallPermissionLevel, error) {
	var out trunk.CallPermissionLevel
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanCallPermissionLevel(tx.QueryRow(ctx, `UPDATE call_permission_level SET
				name = $4, allowed_categories = $5, withhold_caller_id = $6, version = version + 1, updated_at = $7
			WHERE id = $1 AND tenant_id = $2 AND version = $3 RETURNING `+callPermissionLevelColumns,
			l.ID, l.TenantID, l.Version, l.Name, l.AllowedCategories, l.WithholdCallerID, l.UpdatedAt))
		if errors.Is(err, trunk.ErrNotFound) {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM call_permission_level WHERE id = $1 AND tenant_id = $2)`,
				l.ID, l.TenantID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return trunk.ErrVersionChanged
			}
			return trunk.ErrNotFound
		}
		if err != nil {
			if IsUniqueViolation(err) {
				return trunk.ErrDuplicate
			}
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

func (s *Store) DeleteCallPermissionLevel(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM call_permission_level WHERE id = $1 AND tenant_id = $2`, id, tenant)
		if err != nil {
			if isRestrictViolation(err) {
				return trunk.ErrInUse
			}
			return err
		}
		if tag.RowsAffected() == 0 {
			return trunk.ErrNotFound
		}
		return insertAudit(ctx, tx, audit)
	})
}
