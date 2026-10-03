package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/enroll"
	"linxpbx.com/linx/internal/pbx"
)

var _ enroll.Store = (*Store)(nil)

// Setting up an iPhone or iPad (docs/PHASE2.md §4, migration 0041): the
// one-time tickets, what a set-up phone is, and the single-use proofs it
// signs.

// The device columns again, qualified, for the joins below.
const deviceColumnsQualified = `d.id, d.tenant_id, d.extension_id, d.name, d.kind, d.sip_username, d.digest_hash,
	d.enabled, d.revoked_at, d.user_session_id, d.online, d.last_registered_at, d.last_registered_from, d.version,
	d.created_at, d.updated_at`

const enrollmentColumns = `e.id, e.tenant_id, e.user_id, e.extension_id, e.kind, e.device_name, e.delivery,
	e.created_by, e.code_hash, e.token_jti, e.created_at, e.expires_at, e.used_at, e.canceled_at, e.device_id, e.attempts`

func scanEnrollment(row pgx.Row) (enroll.Ticket, error) {
	var t enroll.Ticket
	err := row.Scan(&t.ID, &t.TenantID, &t.UserID, &t.ExtensionID, &t.Kind, &t.DeviceName, &t.Delivery,
		&t.CreatedBy, &t.CodeHash, &t.TokenJTI, &t.CreatedAt, &t.ExpiresAt, &t.UsedAt, &t.CanceledAt, &t.DeviceID,
		&t.Attempts, &t.PersonName, &t.Number)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, pbx.ErrNotFound
	}
	return t, err
}

// EnrollmentTarget is the person a ticket may be made for: enabled, with an
// extension of their own.
func (s *Store) EnrollmentTarget(ctx context.Context, tenant, user uuid.UUID) (enroll.Target, error) {
	var t enroll.Target
	err := s.pool.QueryRow(ctx, `SELECT u.id, e.id, u.name, e.number
		FROM app_user u JOIN extension e ON e.id = u.extension_id
		WHERE u.tenant_id = $1 AND u.id = $2 AND u.disabled_at IS NULL
		  AND e.enabled AND e.deleted_at IS NULL`, tenant, user).
		Scan(&t.UserID, &t.ExtensionID, &t.PersonName, &t.Number)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, pbx.ErrNotFound
	}
	return t, err
}

func (s *Store) CreateEnrollment(ctx context.Context, t enroll.Ticket, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO device_enrollment (id, tenant_id, user_id, extension_id, kind, device_name,
			code_hash, token_jti, created_by, delivery, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			t.ID, t.TenantID, t.UserID, t.ExtensionID, t.Kind, t.DeviceName,
			t.CodeHash, t.TokenJTI, t.CreatedBy, t.Delivery, t.CreatedAt, t.ExpiresAt)
		if err != nil {
			if IsUniqueViolation(err) {
				return pbx.ErrDuplicate
			}
			return fmt.Errorf("creating enrollment: %w", err)
		}
		return insertAudit(ctx, tx, audit)
	})
}

// Enrollments lists the tickets that are still open, newest first.
func (s *Store) Enrollments(ctx context.Context, tenant uuid.UUID, user *uuid.UUID) ([]enroll.Ticket, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+enrollmentColumns+`, u.name, x.number
		FROM device_enrollment e
		JOIN app_user u ON u.id = e.user_id
		JOIN extension x ON x.id = e.extension_id
		WHERE e.tenant_id = $1 AND ($2::uuid IS NULL OR e.user_id = $2)
		  AND e.used_at IS NULL AND e.canceled_at IS NULL AND e.expires_at > now()
		ORDER BY e.created_at DESC LIMIT 200`, tenant, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []enroll.Ticket
	for rows.Next() {
		t, err := scanEnrollment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) Enrollment(ctx context.Context, id uuid.UUID) (enroll.Ticket, error) {
	return scanEnrollment(s.pool.QueryRow(ctx, `SELECT `+enrollmentColumns+`, u.name, x.number
		FROM device_enrollment e
		JOIN app_user u ON u.id = e.user_id
		JOIN extension x ON x.id = e.extension_id
		WHERE e.id = $1`, id))
}

func (s *Store) EnrollmentByCodeHash(ctx context.Context, hash []byte) (enroll.Ticket, error) {
	return scanEnrollment(s.pool.QueryRow(ctx, `SELECT `+enrollmentColumns+`, u.name, x.number
		FROM device_enrollment e
		JOIN app_user u ON u.id = e.user_id
		JOIN extension x ON x.id = e.extension_id
		WHERE e.code_hash = $1 AND e.used_at IS NULL AND e.canceled_at IS NULL`, hash))
}

func (s *Store) CancelEnrollment(ctx context.Context, tenant, id uuid.UUID, at time.Time, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE device_enrollment SET canceled_at = $3
			WHERE id = $1 AND tenant_id = $2 AND used_at IS NULL AND canceled_at IS NULL`, id, tenant, at); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) FailEnrollmentAttempt(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE device_enrollment SET attempts = attempts + 1
		WHERE id = $1 AND used_at IS NULL AND canceled_at IS NULL`, id)
	return err
}

// RedeemEnrollment creates the phone and its identity and closes the ticket
// in one transaction, so a ticket can only ever make one phone.
func (s *Store) RedeemEnrollment(ctx context.Context, ticket uuid.UUID, d pbx.Device, id enroll.Identity, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var open bool
		err := tx.QueryRow(ctx, `SELECT true FROM device_enrollment
			WHERE id = $1 AND used_at IS NULL AND canceled_at IS NULL AND expires_at > $2 FOR UPDATE`,
			ticket, d.CreatedAt).Scan(&open)
		if errors.Is(err, pgx.ErrNoRows) {
			return pbx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO device (id, tenant_id, extension_id, name, kind, sip_username, digest_hash,
			enabled, version, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			d.ID, d.TenantID, d.ExtensionID, d.Name, d.Kind, d.SIPUsername, d.DigestHash, d.Enabled, d.Version,
			d.CreatedAt, d.UpdatedAt); err != nil {
			if IsUniqueViolation(err) {
				return pbx.ErrDuplicate
			}
			return fmt.Errorf("creating phone: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO device_identity (device_id, tenant_id, user_id, public_key, cert_serial,
			cert_fingerprint, cert_not_after, enrolled_at, last_seen_at, expires_at, app_version, os_version)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			id.DeviceID, id.TenantID, id.UserID, id.PublicKey, id.CertSerial, id.CertFingerprint, id.CertNotAfter,
			id.EnrolledAt, id.LastSeenAt, id.ExpiresAt, id.AppVersion, id.OSVersion); err != nil {
			if IsUniqueViolation(err) {
				return pbx.ErrDuplicate
			}
			return fmt.Errorf("creating phone identity: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE device_enrollment SET used_at = $2, device_id = $3 WHERE id = $1`,
			ticket, d.CreatedAt, d.ID); err != nil {
			return err
		}
		for _, t := range []string{"device.created", "device.enrolled"} {
			ev, err := deviceEvent(d, t, d.CreatedAt)
			if err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, ev, nil); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, audit)
	})
}

const identityColumns = `i.device_id, i.tenant_id, i.user_id, i.public_key, i.cert_serial, i.cert_fingerprint,
	i.cert_not_after, i.enrolled_at, i.last_seen_at, i.expires_at, i.expired_at, i.app_version, i.os_version`

func scanIdentity(row pgx.Row) (enroll.Identity, pbx.Device, error) {
	var i enroll.Identity
	var d pbx.Device
	err := row.Scan(&i.DeviceID, &i.TenantID, &i.UserID, &i.PublicKey, &i.CertSerial, &i.CertFingerprint,
		&i.CertNotAfter, &i.EnrolledAt, &i.LastSeenAt, &i.ExpiresAt, &i.ExpiredAt, &i.AppVersion, &i.OSVersion,
		&d.ID, &d.TenantID, &d.ExtensionID, &d.Name, &d.Kind, &d.SIPUsername, &d.DigestHash, &d.Enabled, &d.RevokedAt,
		&d.UserSessionID, &d.Online, &d.LastRegisteredAt, &d.LastRegisteredFrom, &d.Version, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return i, d, pbx.ErrNotFound
	}
	return i, d, err
}

func (s *Store) IdentityByFingerprint(ctx context.Context, fingerprint []byte) (enroll.Identity, pbx.Device, error) {
	return scanIdentity(s.pool.QueryRow(ctx, `SELECT `+identityColumns+`, `+deviceColumnsQualified+`
		FROM device_identity i JOIN device d ON d.id = i.device_id
		WHERE i.cert_fingerprint = $1`, fingerprint))
}

func (s *Store) IdentityByDevice(ctx context.Context, device uuid.UUID) (enroll.Identity, pbx.Device, error) {
	return scanIdentity(s.pool.QueryRow(ctx, `SELECT `+identityColumns+`, `+deviceColumnsQualified+`
		FROM device_identity i JOIN device d ON d.id = i.device_id
		WHERE i.device_id = $1`, device))
}

// TouchIdentity records that the phone was in touch, with its renewed
// certificate when it asked for one.
func (s *Store) TouchIdentity(ctx context.Context, device uuid.UUID, cert *enroll.CertUpdate, app, os string, seen, expires time.Time) error {
	if cert != nil {
		_, err := s.pool.Exec(ctx, `UPDATE device_identity SET last_seen_at = $2, expires_at = $3, expired_at = NULL,
			cert_serial = $4, cert_fingerprint = $5, cert_not_after = $6, app_version = $7, os_version = $8
			WHERE device_id = $1`, device, seen, expires, cert.Serial, cert.Fingerprint, cert.NotAfter, app, os)
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE device_identity SET last_seen_at = $2, expires_at = $3,
		app_version = $4, os_version = $5 WHERE device_id = $1`, device, seen, expires, app, os)
	return err
}

func (s *Store) UseProof(ctx context.Context, jti, device uuid.UUID, at time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO device_proof (jti, device_id, used_at) VALUES ($1, $2, $3)`, jti, device, at)
	if err != nil && IsUniqueViolation(err) {
		return pbx.ErrDuplicate
	}
	return err
}

// DevicePrincipalFor is the same rule Asterisk's device_live view uses: the
// phone is on, not revoked, not expired, and its person is still there with
// that extension.
func (s *Store) DevicePrincipalFor(ctx context.Context, device uuid.UUID, now time.Time) (uuid.UUID, uuid.UUID, error) {
	var tenant, user uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT d.tenant_id, i.user_id
		FROM device d
		JOIN device_identity i ON i.device_id = d.id
		JOIN app_user u ON u.id = i.user_id
		JOIN extension e ON e.id = d.extension_id
		WHERE d.id = $1 AND d.enabled AND d.revoked_at IS NULL
		  AND i.expired_at IS NULL AND i.expires_at > $2
		  AND u.disabled_at IS NULL AND u.extension_id = d.extension_id
		  AND e.enabled AND e.deleted_at IS NULL`, device, now).Scan(&tenant, &user)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, pbx.ErrNotFound
	}
	return tenant, user, err
}

// ExpireIdentities marks the phones that have gone six months without being
// in touch, each with a device.expired event.
func (s *Store) ExpireIdentities(ctx context.Context, now time.Time) ([]pbx.Device, error) {
	var expired []pbx.Device
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE device_identity SET expired_at = $1
			WHERE expired_at IS NULL AND expires_at <= $1 RETURNING device_id`, now)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (uuid.UUID, error) {
			var id uuid.UUID
			err := r.Scan(&id)
			return id, err
		})
		if err != nil {
			return err
		}
		for _, id := range ids {
			d, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM device WHERE id = $1`, id))
			if err != nil {
				return err
			}
			ev, err := deviceEvent(d, "device.expired", now)
			if err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, ev, nil); err != nil {
				return err
			}
			expired = append(expired, d)
		}
		return nil
	})
	return expired, err
}

// expirePhonesTx makes every phone of this person need setting up again,
// with a device.expired event each. A password change does this (owner,
// 2026-10-03): the person asks for a new QR code or emailed link and sets
// the phone up again, exactly as they sign in again in the browser.
func expirePhonesTx(ctx context.Context, tx pgx.Tx, user uuid.UUID, at time.Time) error {
	rows, err := tx.Query(ctx, `UPDATE device_identity SET expired_at = $2, expires_at = $2
		WHERE user_id = $1 AND expired_at IS NULL RETURNING device_id`, user, at)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (uuid.UUID, error) {
		var id uuid.UUID
		err := r.Scan(&id)
		return id, err
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		d, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM device WHERE id = $1`, id))
		if err != nil {
			return err
		}
		ev, err := deviceEvent(d, "device.expired", at)
		if err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, ev, nil); err != nil {
			return err
		}
	}
	return nil
}

// PhonesForUser is this person's own set-up phones, newest first.
func (s *Store) PhonesForUser(ctx context.Context, tenant, user uuid.UUID) ([]pbx.Device, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+deviceColumnsQualified+`, `+phoneColumns+`
		FROM device d JOIN device_identity i ON i.device_id = d.id
		WHERE d.tenant_id = $1 AND i.user_id = $2 AND d.revoked_at IS NULL
		ORDER BY d.id DESC LIMIT 50`, tenant, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pbx.Device{}
	for rows.Next() {
		d, err := scanDeviceWithPhone(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) DeleteUsedProofs(ctx context.Context, before time.Time) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM device_proof WHERE used_at < $1`, before)
	return err
}

// IssuePhoneLine gives a set-up phone a fresh SIP password for the device it
// already has (docs/PHASE2.md §4): the app asks every time it starts,
// because it keeps the password in memory only. The row is locked while it
// happens, so a revoke landing at the same moment wins.
func (s *Store) IssuePhoneLine(ctx context.Context, tenant, device uuid.UUID, digest func(username string) string,
	at time.Time, audit auth.AuditEntry,
) (pbx.Device, pbx.Extension, error) {
	var d pbx.Device
	var ext pbx.Extension
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM device
			WHERE id = $1 AND tenant_id = $2 AND kind = $3 AND enabled AND revoked_at IS NULL
			FOR UPDATE`, device, tenant, pbx.KindIOS))
		if err != nil {
			return err
		}
		ext, err = scanExtension(tx.QueryRow(ctx, `SELECT `+extensionColumns+` FROM extension
			WHERE id = $1 AND tenant_id = $2 AND enabled AND deleted_at IS NULL`, cur.ExtensionID, tenant))
		if err != nil {
			return err
		}
		d, err = scanDevice(tx.QueryRow(ctx, `UPDATE device SET digest_hash = $2, version = version + 1, updated_at = $3
			WHERE id = $1 RETURNING `+deviceColumns, cur.ID, digest(cur.SIPUsername), at))
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
	return d, ext, err
}
