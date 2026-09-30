package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/sso"
)

// Company sign-in (ADR-052, migration 0023): providers and the links
// between their accounts and people.

var (
	_ sso.Store         = (*Store)(nil)
	_ auth.CompanyStore = (*Store)(nil)
)

const ssoProviderColumns = `id, tenant_id, kind, name, issuer, client_id, client_secret_enc, enabled, shown,
	position, version, created_at, updated_at`

func scanSSOProvider(row pgx.Row) (sso.Provider, error) {
	var p sso.Provider
	err := row.Scan(&p.ID, &p.TenantID, &p.Kind, &p.Name, &p.Issuer, &p.ClientID, &p.ClientSecretEnc, &p.Enabled, &p.Shown,
		&p.Position, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, auth.ErrNotFound
	}
	return p, err
}

func (s *Store) CreateSSOProvider(ctx context.Context, p sso.Provider, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO sso_provider (`+ssoProviderColumns+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			p.ID, p.TenantID, p.Kind, p.Name, p.Issuer, p.ClientID, p.ClientSecretEnc, p.Enabled, p.Shown,
			p.Position, p.Version, p.CreatedAt, p.UpdatedAt)
		if err != nil {
			if IsUniqueViolation(err) {
				return auth.ErrDuplicate
			}
			return fmt.Errorf("adding company sign-in provider: %w", err)
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) SSOProvider(ctx context.Context, tenant, id uuid.UUID) (sso.Provider, error) {
	return scanSSOProvider(s.pool.QueryRow(ctx, `SELECT `+ssoProviderColumns+` FROM sso_provider
		WHERE id = $1 AND tenant_id = $2`, id, tenant))
}

func (s *Store) ListSSOProviders(ctx context.Context, tenant uuid.UUID) ([]sso.Provider, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+ssoProviderColumns+` FROM sso_provider
		WHERE tenant_id = $1 ORDER BY position, name COLLATE "unicode"`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []sso.Provider{}
	for rows.Next() {
		p, err := scanSSOProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) UpdateSSOProvider(ctx context.Context, p sso.Provider, audit auth.AuditEntry) (sso.Provider, error) {
	var out sso.Provider
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanSSOProvider(tx.QueryRow(ctx, `UPDATE sso_provider SET name = $3, issuer = $4, client_id = $5,
				client_secret_enc = $6, enabled = $7, shown = $8, position = $9, updated_at = $10, version = version + 1
			WHERE id = $1 AND tenant_id = $2 AND version = $11
			RETURNING `+ssoProviderColumns,
			p.ID, p.TenantID, p.Name, p.Issuer, p.ClientID, p.ClientSecretEnc, p.Enabled, p.Shown, p.Position, p.UpdatedAt, p.Version))
		if errors.Is(err, auth.ErrNotFound) {
			var exists bool
			if qerr := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sso_provider WHERE id = $1 AND tenant_id = $2)`,
				p.ID, p.TenantID).Scan(&exists); qerr != nil {
				return qerr
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

func (s *Store) DeleteSSOProvider(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM sso_provider WHERE id = $1 AND tenant_id = $2`, id, tenant)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return auth.ErrNotFound
		}
		return insertAudit(ctx, tx, audit)
	})
}

const companyLinkColumns = `l.id, l.tenant_id, l.user_id, l.provider_id, p.name, l.subject, l.email, l.created_at, l.last_used_at`

func scanCompanyLink(row pgx.Row) (auth.CompanyLinkInfo, error) {
	var l auth.CompanyLinkInfo
	err := row.Scan(&l.ID, &l.TenantID, &l.UserID, &l.ProviderID, &l.ProviderName, &l.Subject, &l.Email, &l.CreatedAt, &l.LastUsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, auth.ErrNotFound
	}
	return l, err
}

func (s *Store) CompanyLinkBySubject(ctx context.Context, tenant, provider uuid.UUID, subject string) (auth.CompanyLinkInfo, error) {
	return scanCompanyLink(s.pool.QueryRow(ctx, `SELECT `+companyLinkColumns+`
		FROM user_sso_link l JOIN sso_provider p ON p.id = l.provider_id
		WHERE l.tenant_id = $1 AND l.provider_id = $2 AND l.subject = $3`, tenant, provider, subject))
}

func (s *Store) CompanyLinks(ctx context.Context, tenant, user uuid.UUID) ([]auth.CompanyLinkInfo, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+companyLinkColumns+`
		FROM user_sso_link l JOIN sso_provider p ON p.id = l.provider_id
		WHERE l.tenant_id = $1 AND l.user_id = $2 ORDER BY p.position, p.name COLLATE "unicode"`, tenant, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []auth.CompanyLinkInfo{}
	for rows.Next() {
		l, err := scanCompanyLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) AddCompanyLink(ctx context.Context, l auth.CompanyLinkInfo, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO user_sso_link
			(id, tenant_id, user_id, provider_id, subject, email, created_at, last_used_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			l.ID, l.TenantID, l.UserID, l.ProviderID, l.Subject, l.Email, l.CreatedAt, l.LastUsedAt)
		if err != nil {
			if IsUniqueViolation(err) {
				return auth.ErrDuplicate
			}
			return fmt.Errorf("linking company account: %w", err)
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) UseCompanyLink(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE user_sso_link SET last_used_at = $2 WHERE id = $1`, id, at)
	return err
}

func (s *Store) RemoveCompanyLink(ctx context.Context, tenant, user, id uuid.UUID, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM user_sso_link WHERE id = $1 AND tenant_id = $2 AND user_id = $3`, id, tenant, user)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return auth.ErrNotFound
		}
		return insertAudit(ctx, tx, audit)
	})
}

func (s *Store) CompanySignInRequired(ctx context.Context, tenant uuid.UUID) (bool, error) {
	var required bool
	err := s.pool.QueryRow(ctx, `SELECT company_sign_in_required FROM pbx_setting`).Scan(&required)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return required, err
}
