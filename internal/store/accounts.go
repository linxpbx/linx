package store

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/auth"
)

var (
	_ auth.UserStore    = (*Store)(nil)
	_ auth.SessionStore = (*Store)(nil)
)

const userColumns = `id, tenant_id, email, name, role, extension_id, password_hash, password_updated_at,
	mfa_secret_enc, mfa_pending_secret_enc, mfa_enabled, recovery_code_hashes, failed_attempts, locked_until,
	failure_window_start, failure_window_count, disabled_at, version, created_at, updated_at, presence`

func scanUser(row pgx.Row) (auth.User, error) {
	var u auth.User
	err := row.Scan(&u.ID, &u.TenantID, &u.Email, &u.Name, &u.Role, &u.ExtensionID, &u.PasswordHash, &u.PasswordUpdatedAt,
		&u.MFASecretEnc, &u.MFAPendingSecretEnc, &u.MFAEnabled, &u.RecoveryCodeHashes, &u.FailedAttempts, &u.LockedUntil,
		&u.FailureWindowStart, &u.FailureWindowCount, &u.DisabledAt, &u.Version, &u.CreatedAt, &u.UpdatedAt, &u.Presence)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, auth.ErrNotFound
	}
	return u, err
}

func (s *Store) CreateUser(ctx context.Context, u auth.User, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO app_user
			(id, tenant_id, email, name, role, extension_id, password_hash, password_updated_at, version, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			u.ID, u.TenantID, u.Email, u.Name, u.Role, u.ExtensionID, u.PasswordHash, u.PasswordUpdatedAt,
			u.Version, u.CreatedAt, u.UpdatedAt)
		if err != nil {
			if IsUniqueViolation(err) {
				return auth.ErrDuplicate
			}
			return fmt.Errorf("creating person: %w", err)
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) User(ctx context.Context, tenant, id uuid.UUID) (auth.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM app_user WHERE id = $1 AND tenant_id = $2`, id, tenant))
}

func (s *Store) UserByEmail(ctx context.Context, tenant uuid.UUID, email string) (auth.User, error) {
	return scanUser(s.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM app_user WHERE tenant_id = $1 AND lower(email) = lower($2)`, tenant, email))
}

func (s *Store) ListUsers(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]auth.User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userColumns+` FROM app_user
		WHERE tenant_id = $1 AND ($2::uuid IS NULL OR id < $2) ORDER BY id DESC LIMIT $3`, tenant, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []auth.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) UpdateUser(ctx context.Context, u auth.User, audit auth.AuditEntry) (auth.User, error) {
	var out auth.User
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanUser(tx.QueryRow(ctx, `UPDATE app_user SET
				name = $4, role = $5, extension_id = $6, password_hash = $7, password_updated_at = $8,
				mfa_secret_enc = $9, mfa_pending_secret_enc = $10, mfa_enabled = $11, recovery_code_hashes = $12,
				failed_attempts = $13, locked_until = $14, failure_window_start = $15, failure_window_count = $16,
				disabled_at = $17, version = version + 1, updated_at = $18
			WHERE id = $1 AND tenant_id = $2 AND version = $3
			RETURNING `+userColumns,
			u.ID, u.TenantID, u.Version, u.Name, u.Role, u.ExtensionID, u.PasswordHash, u.PasswordUpdatedAt,
			u.MFASecretEnc, u.MFAPendingSecretEnc, u.MFAEnabled, u.RecoveryCodeHashes, u.FailedAttempts, u.LockedUntil,
			u.FailureWindowStart, u.FailureWindowCount, u.DisabledAt, u.UpdatedAt))
		if errors.Is(err, auth.ErrNotFound) {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_user WHERE id = $1 AND tenant_id = $2)`,
				u.ID, u.TenantID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return auth.ErrVersionChanged
			}
			return auth.ErrNotFound
		}
		if err != nil {
			if IsUniqueViolation(err) {
				return auth.ErrDuplicate
			}
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

// DisableUser stops a person signing in and revokes every session of
// theirs, in one transaction. Disabling a disabled person succeeds and
// changes nothing beyond the audit entry.
func (s *Store) DisableUser(ctx context.Context, tenant, id uuid.UUID, at time.Time, audit auth.AuditEntry) (auth.User, error) {
	var out auth.User
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanUser(tx.QueryRow(ctx, `UPDATE app_user SET
				disabled_at = COALESCE(disabled_at, $3),
				version = CASE WHEN disabled_at IS NULL THEN version + 1 ELSE version END,
				updated_at = CASE WHEN disabled_at IS NULL THEN $3 ELSE updated_at END
			WHERE id = $1 AND tenant_id = $2
			RETURNING `+userColumns, id, tenant, at))
		if err != nil {
			return err
		}
		if err := revokeUserSessionsTx(ctx, tx, id, at); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

func (s *Store) SetPassword(ctx context.Context, tenant, user uuid.UUID, passwordHash string, at time.Time, revokeSessions bool, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE app_user SET password_hash = $3, password_updated_at = $4, updated_at = $4, version = version + 1
			WHERE id = $1 AND tenant_id = $2`, user, tenant, passwordHash, at)
		if err != nil {
			return fmt.Errorf("setting password: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return auth.ErrNotFound
		}
		if revokeSessions {
			if err := revokeUserSessionsTx(ctx, tx, user, at); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, audit)
	})
}

// SetMFASecret stores sealedSecret as the *pending* enrollment secret only:
// it never touches mfa_secret_enc or mfa_enabled, so starting (or
// restarting) enrollment can't weaken an already-confirmed secret.
func (s *Store) SetMFASecret(ctx context.Context, tenant, user uuid.UUID, sealedSecret []byte, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE app_user SET mfa_pending_secret_enc = $3, updated_at = $4, version = version + 1
		WHERE id = $1 AND tenant_id = $2`, user, tenant, sealedSecret, at)
	if err != nil {
		return fmt.Errorf("starting MFA enrollment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return auth.ErrNotFound
	}
	return nil
}

// ConfirmMFA promotes the pending secret to the confirmed one and clears
// the pending column, once a code from it has been checked.
func (s *Store) ConfirmMFA(ctx context.Context, tenant, user uuid.UUID, recoveryHashes [][]byte, at time.Time, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE app_user SET
				mfa_secret_enc = mfa_pending_secret_enc, mfa_pending_secret_enc = NULL,
				mfa_enabled = true, recovery_code_hashes = $3, updated_at = $4, version = version + 1
			WHERE id = $1 AND tenant_id = $2 AND mfa_pending_secret_enc IS NOT NULL`, user, tenant, recoveryHashes, at)
		if err != nil {
			return fmt.Errorf("confirming MFA: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return auth.ErrNotFound
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) ConsumeRecoveryCode(ctx context.Context, tenant, user uuid.UUID, hash []byte) error {
	_, err := s.pool.Exec(ctx, `UPDATE app_user SET recovery_code_hashes = array_remove(recovery_code_hashes, $3)
		WHERE id = $1 AND tenant_id = $2`, user, tenant, hash)
	return err
}

func (s *Store) RecordLoginSuccess(ctx context.Context, tenant, user uuid.UUID, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE app_user SET failed_attempts = 0, locked_until = NULL,
		failure_window_start = NULL, failure_window_count = 0, updated_at = $3
		WHERE id = $1 AND tenant_id = $2`, user, tenant, at)
	return err
}

// RecordLoginFailure applies lockout (5 failures, then a doubling wait up
// to 1 hour) and a rolling hour's failure count for the guessing-password
// alert, in one statement so concurrent failures for the same account
// still serialize correctly (docs/WEB.md §4).
func (s *Store) RecordLoginFailure(ctx context.Context, tenant, user uuid.UUID, at time.Time) (*time.Time, bool, error) {
	row := s.pool.QueryRow(ctx, `UPDATE app_user SET
			failed_attempts = failed_attempts + 1,
			locked_until = CASE WHEN failed_attempts + 1 >= 5
				THEN $3::timestamptz + make_interval(secs => LEAST(3600, 60 * power(2, GREATEST(0, failed_attempts + 1 - 5))))
				ELSE NULL END,
			failure_window_start = CASE WHEN failure_window_start IS NULL OR $3::timestamptz - failure_window_start > interval '1 hour'
				THEN $3::timestamptz ELSE failure_window_start END,
			failure_window_count = CASE WHEN failure_window_start IS NULL OR $3::timestamptz - failure_window_start > interval '1 hour'
				THEN 1 ELSE failure_window_count + 1 END,
			updated_at = $3::timestamptz
		WHERE id = $1 AND tenant_id = $2
		RETURNING locked_until, failure_window_count`, user, tenant, at)
	var lockedUntil *time.Time
	var windowCount int
	if err := row.Scan(&lockedUntil, &windowCount); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, auth.ErrNotFound
		}
		return nil, false, err
	}
	return lockedUntil, windowCount == 20, nil
}

const setupLinkColumns = `id, tenant_id, user_id, token_hash, created_at, expires_at, used_at`

func scanSetupLink(row pgx.Row) (auth.SetupLink, error) {
	var l auth.SetupLink
	err := row.Scan(&l.ID, &l.TenantID, &l.UserID, &l.TokenHash, &l.CreatedAt, &l.ExpiresAt, &l.UsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, auth.ErrNotFound
	}
	return l, err
}

func (s *Store) CreateSetupLink(ctx context.Context, l auth.SetupLink) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO user_setup_link (id, tenant_id, user_id, token_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, l.ID, l.TenantID, l.UserID, l.TokenHash, l.CreatedAt, l.ExpiresAt)
	return err
}

func (s *Store) SetupLinkByTokenHash(ctx context.Context, hash []byte) (auth.SetupLink, error) {
	return scanSetupLink(s.pool.QueryRow(ctx, `SELECT `+setupLinkColumns+` FROM user_setup_link WHERE token_hash = $1`, hash))
}

func (s *Store) ConsumeSetupLink(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE user_setup_link SET used_at = $2 WHERE id = $1`, id, at)
	return err
}

const sessionColumns = `id, tenant_id, user_id, role, token_hash, csrf_hash, mfa_verified,
	created_at, expires_at, idle_expires_at, last_seen_at, last_seen_ip, user_agent, revoked_at`

func scanSession(row pgx.Row) (auth.UserSession, error) {
	var s auth.UserSession
	err := row.Scan(&s.ID, &s.TenantID, &s.UserID, &s.Role, &s.TokenHash, &s.CSRFHash, &s.MFAVerified,
		&s.CreatedAt, &s.ExpiresAt, &s.IdleExpiresAt, &s.LastSeenAt, &s.LastSeenIP, &s.UserAgent, &s.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, auth.ErrNotFound
	}
	return s, err
}

func (s *Store) CreateSession(ctx context.Context, sess auth.UserSession) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO user_session
		(id, tenant_id, user_id, role, token_hash, csrf_hash, mfa_verified, created_at, expires_at, idle_expires_at, last_seen_at, last_seen_ip, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		sess.ID, sess.TenantID, sess.UserID, sess.Role, sess.TokenHash, sess.CSRFHash, sess.MFAVerified,
		sess.CreatedAt, sess.ExpiresAt, sess.IdleExpiresAt, sess.LastSeenAt, sess.LastSeenIP, sess.UserAgent)
	return err
}

func (s *Store) SessionByTokenHash(ctx context.Context, hash []byte) (auth.UserSession, error) {
	return scanSession(s.pool.QueryRow(ctx, `SELECT `+sessionColumns+` FROM user_session WHERE token_hash = $1`, hash))
}

func (s *Store) TouchSession(ctx context.Context, id uuid.UUID, lastSeen, idleExpires time.Time, ip netip.Addr) error {
	var ipArg *netip.Addr
	if ip.IsValid() {
		ipArg = &ip
	}
	_, err := s.pool.Exec(ctx, `UPDATE user_session SET last_seen_at = $2, idle_expires_at = $3, last_seen_ip = $4
		WHERE id = $1 AND revoked_at IS NULL`, id, lastSeen, idleExpires, ipArg)
	return err
}

func (s *Store) RevokeSession(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE user_session SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, id, at)
	return err
}

func (s *Store) RevokeUserSessions(ctx context.Context, user uuid.UUID, at time.Time) error {
	return revokeUserSessionsTx(ctx, s.pool, user, at)
}

// execer already declares Exec; revokeUserSessionsTx works against either
// the pool or a transaction.
func revokeUserSessionsTx(ctx context.Context, db execer, user uuid.UUID, at time.Time) error {
	_, err := db.Exec(ctx, `UPDATE user_session SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`, user, at)
	return err
}

func (s *Store) PromoteSession(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE user_session SET mfa_verified = true WHERE id = $1 AND revoked_at IS NULL`, id)
	return err
}
