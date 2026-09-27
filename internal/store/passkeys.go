package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/auth"
)

var _ auth.PasskeyStore = (*Store)(nil)

const passkeyColumns = `id, tenant_id, user_id, credential_id, public_key, sign_count, aaguid, backup_eligible,
	backup_state, transports, attestation_format, name, created_at, last_used_at`

func scanPasskey(row pgx.Row) (auth.Passkey, error) {
	var p auth.Passkey
	var count int64
	err := row.Scan(&p.ID, &p.TenantID, &p.UserID, &p.CredentialID, &p.PublicKey, &count, &p.AAGUID, &p.BackupEligible,
		&p.BackupState, &p.Transports, &p.AttestationFormat, &p.Name, &p.CreatedAt, &p.LastUsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, auth.ErrNotFound
	}
	p.SignCount = uint32(count)
	return p, err
}

func (s *Store) Passkeys(ctx context.Context, tenant, user uuid.UUID) ([]auth.Passkey, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+passkeyColumns+` FROM user_passkey
		WHERE tenant_id = $1 AND user_id = $2 ORDER BY created_at, id`, tenant, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []auth.Passkey{}
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PasskeyByCredentialID(ctx context.Context, credentialID []byte) (auth.Passkey, error) {
	return scanPasskey(s.pool.QueryRow(ctx, `SELECT `+passkeyColumns+` FROM user_passkey WHERE credential_id = $1`, credentialID))
}

// AddPasskey locks the person's row first, so two registrations racing
// can't both squeeze in under auth.MaxPasskeys.
func (s *Store) AddPasskey(ctx context.Context, p auth.Passkey, recoveryHashes [][]byte, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM user_passkey WHERE user_id = u.id)
			FROM app_user u WHERE u.id = $1 AND u.tenant_id = $2 FOR UPDATE`, p.UserID, p.TenantID).Scan(&n); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		if n >= auth.MaxPasskeys {
			return auth.ErrLimit
		}
		_, err := tx.Exec(ctx, `INSERT INTO user_passkey (`+passkeyColumns+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NULL)`,
			p.ID, p.TenantID, p.UserID, p.CredentialID, p.PublicKey, int64(p.SignCount), p.AAGUID, p.BackupEligible,
			p.BackupState, p.Transports, p.AttestationFormat, p.Name, p.CreatedAt)
		if err != nil {
			if IsUniqueViolation(err) {
				return auth.ErrDuplicate
			}
			return fmt.Errorf("adding passkey: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE app_user SET password_only_accepted_at = NULL,
				recovery_code_hashes = COALESCE($3, recovery_code_hashes), updated_at = $4, version = version + 1
			WHERE id = $1 AND tenant_id = $2`, p.UserID, p.TenantID, recoveryHashes, p.CreatedAt); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) UsePasskey(ctx context.Context, id uuid.UUID, signCount uint32, backupState bool, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE user_passkey SET sign_count = $2, backup_state = $3, last_used_at = $4 WHERE id = $1`,
		id, int64(signCount), backupState, at)
	return err
}

func (s *Store) RenamePasskey(ctx context.Context, tenant, user, id uuid.UUID, name string, audit auth.AuditEntry) (auth.Passkey, error) {
	var out auth.Passkey
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanPasskey(tx.QueryRow(ctx, `UPDATE user_passkey SET name = $4
			WHERE id = $1 AND tenant_id = $2 AND user_id = $3 RETURNING `+passkeyColumns, id, tenant, user, name))
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

func (s *Store) DeletePasskey(ctx context.Context, tenant, user, id uuid.UUID, passwordOnly bool, at time.Time, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM user_passkey WHERE id = $1 AND tenant_id = $2 AND user_id = $3`, id, tenant, user)
		if err != nil {
			return fmt.Errorf("removing passkey: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return auth.ErrNotFound
		}
		// With no second step left, recovery codes have nothing to stand in
		// for.
		if _, err := tx.Exec(ctx, `UPDATE app_user SET
				recovery_code_hashes = CASE WHEN mfa_enabled OR EXISTS (SELECT 1 FROM user_passkey WHERE user_id = $1)
					THEN recovery_code_hashes ELSE '{}' END,
				password_only_accepted_at = CASE WHEN $3::boolean THEN $4::timestamptz ELSE password_only_accepted_at END,
				updated_at = $4, version = version + 1
			WHERE id = $1 AND tenant_id = $2`, user, tenant, passwordOnly, at); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}
