// Package store is the control plane's PostgreSQL access (ADR-026): plain
// SQL through pgx, no ORM.
package store

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"linxpbx.com/linx/internal/auth"
)

// Store implements auth.Store and the API's credential management.
type Store struct {
	pool *pgxpool.Pool
}

// New wraps a connection pool.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ auth.Store = (*Store)(nil)

// tenantLockID serialises creating the default tenant across instances.
const tenantLockID = 0x6c696e7874656e // "linxten"

// DefaultTenant returns the first tenant, creating it ("Default") if there
// is none yet. Linx runs a single tenant until multi-tenancy is added.
func (s *Store) DefaultTenant(ctx context.Context) (uuid.UUID, error) {
	var id uuid.UUID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", tenantLockID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, "SELECT id FROM tenant ORDER BY id LIMIT 1").Scan(&id)
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if id, err = uuid.NewV7(); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO tenant (id, name) VALUES ($1, 'Default')", id)
		return err
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("default tenant: %w", err)
	}
	return id, nil
}

// table maps a credential kind to its table. Only these two strings are
// ever put into SQL text.
func table(kind string) (string, error) {
	switch kind {
	case auth.TypeAPIKey:
		return "api_key", nil
	case auth.TypeOAuthClient:
		return "oauth_client", nil
	}
	return "", fmt.Errorf("unknown credential kind %q", kind)
}

const credentialColumns = `id, public_id, tenant_id, name, secret_hash, role, scopes, allowed_ips,
	created_by, created_at, expires_at, last_used_at, last_used_ip, revoked_at`

func scanCredential(kind string, row pgx.Row) (auth.Credential, error) {
	c := auth.Credential{Kind: kind}
	err := row.Scan(&c.ID, &c.PublicID, &c.TenantID, &c.Name, &c.SecretHash, &c.Role, &c.Scopes, &c.AllowedIPs,
		&c.CreatedBy, &c.CreatedAt, &c.ExpiresAt, &c.LastUsedAt, &c.LastUsedIP, &c.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, auth.ErrNotFound
	}
	return c, err
}

// CreateCredential stores a new key or client and its audit entry in one
// transaction.
func (s *Store) CreateCredential(ctx context.Context, c auth.Credential, audit auth.AuditEntry) error {
	t, err := table(c.Kind)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO `+t+` (id, public_id, tenant_id, name, secret_hash, role, scopes,
			allowed_ips, created_by, created_at, expires_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			c.ID, c.PublicID, c.TenantID, c.Name, c.SecretHash, c.Role, c.Scopes, emptyToNil(c.AllowedIPs),
			c.CreatedBy, c.CreatedAt, c.ExpiresAt)
		if err != nil {
			return fmt.Errorf("creating %s: %w", t, err)
		}
		return insertAudit(ctx, tx, audit)
	})
}

// IsUniqueViolation reports whether err is a unique-constraint failure
// (e.g. a public id collision, which the caller can retry).
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// CredentialByPublicID finds a key or client by the public part of its secret.
func (s *Store) CredentialByPublicID(ctx context.Context, kind, publicID string) (auth.Credential, error) {
	t, err := table(kind)
	if err != nil {
		return auth.Credential{}, err
	}
	return scanCredential(kind, s.pool.QueryRow(ctx,
		`SELECT `+credentialColumns+` FROM `+t+` WHERE public_id = $1`, publicID))
}

// CredentialByID finds a key or client in any tenant.
func (s *Store) CredentialByID(ctx context.Context, kind string, id uuid.UUID) (auth.Credential, error) {
	t, err := table(kind)
	if err != nil {
		return auth.Credential{}, err
	}
	return scanCredential(kind, s.pool.QueryRow(ctx,
		`SELECT `+credentialColumns+` FROM `+t+` WHERE id = $1`, id))
}

// TenantCredential finds a key or client within one tenant.
func (s *Store) TenantCredential(ctx context.Context, kind string, tenant, id uuid.UUID) (auth.Credential, error) {
	t, err := table(kind)
	if err != nil {
		return auth.Credential{}, err
	}
	return scanCredential(kind, s.pool.QueryRow(ctx,
		`SELECT `+credentialColumns+` FROM `+t+` WHERE id = $1 AND tenant_id = $2`, id, tenant))
}

// ListCredentials returns up to limit keys or clients of a tenant, newest
// first, starting after the id before (nil for the first page).
func (s *Store) ListCredentials(ctx context.Context, kind string, tenant uuid.UUID, before *uuid.UUID, limit int) ([]auth.Credential, error) {
	t, err := table(kind)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+credentialColumns+` FROM `+t+`
		WHERE tenant_id = $1 AND ($2::uuid IS NULL OR id < $2) ORDER BY id DESC LIMIT $3`, tenant, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []auth.Credential{}
	for rows.Next() {
		c, err := scanCredential(kind, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RevokeCredential revokes a key or client of a tenant (a no-op if it
// already is) and writes the audit entry in the same transaction. It returns
// the credential as it now stands.
func (s *Store) RevokeCredential(ctx context.Context, kind string, tenant, id uuid.UUID, at time.Time, audit auth.AuditEntry) (auth.Credential, error) {
	t, err := table(kind)
	if err != nil {
		return auth.Credential{}, err
	}
	var c auth.Credential
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		c, err = scanCredential(kind, tx.QueryRow(ctx, `UPDATE `+t+` SET revoked_at = COALESCE(revoked_at, $3)
			WHERE id = $1 AND tenant_id = $2 RETURNING `+credentialColumns, id, tenant, at))
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
	return c, err
}

// TouchCredential records when and from where a key or client was last used.
func (s *Store) TouchCredential(ctx context.Context, kind string, id uuid.UUID, ip netip.Addr, at time.Time) error {
	t, err := table(kind)
	if err != nil {
		return err
	}
	var addr *netip.Addr
	if ip.IsValid() {
		addr = &ip
	}
	_, err = s.pool.Exec(ctx, `UPDATE `+t+` SET last_used_at = $2, last_used_ip = $3 WHERE id = $1`, id, at, addr)
	return err
}

// TokenRevoked reports whether a JWT id is on the revocation list.
func (s *Store) TokenRevoked(ctx context.Context, jti string) (bool, error) {
	var revoked bool
	err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM token_revocation WHERE jti = $1)", jti).Scan(&revoked)
	return revoked, err
}

// Audit writes one audit_log row.
func (s *Store) Audit(ctx context.Context, e auth.AuditEntry) error {
	return insertAudit(ctx, s.pool, e)
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func insertAudit(ctx context.Context, db execer, e auth.AuditEntry) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	var ip *netip.Addr
	if e.IP.IsValid() {
		ip = &e.IP
	}
	var target *string
	if e.Target != "" {
		target = &e.Target
	}
	_, err = db.Exec(ctx, `INSERT INTO audit_log (id, tenant_id, actor, ip, action, target, result, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, id, e.TenantID, e.Actor, ip, e.Action, target, e.Result, e.Detail)
	if err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	return nil
}

func emptyToNil(p []netip.Prefix) []netip.Prefix {
	if len(p) == 0 {
		return nil
	}
	return p
}
