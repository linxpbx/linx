package db

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AsteriskRolePasswordPathFromEnv is the Docker secret holding the
// linx_asterisk Postgres role's password (docs/PBX.md §3), overridable for
// tests.
func AsteriskRolePasswordPathFromEnv(getenv func(string) string) string {
	return envOr(getenv, "LINX_ASTERISK_DB_PASSWORD_FILE", "/run/secrets/linx_asterisk_db_password")
}

// EnsureAsteriskRole sets the linx_asterisk role's login password to the one
// in the Docker secret at path, so it always matches what Asterisk's own
// ODBC config uses (internal/asteriskconf). Migration 0005 only creates the
// role (NOLOGIN, no usable password baked into the schema); this is what
// lets it log in, run at every startup so a rotated secret takes effect
// without a migration.
func EnsureAsteriskRole(ctx context.Context, pool *pgxpool.Pool, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("asterisk database password: %w", err)
	}
	password := strings.TrimSpace(string(b))
	if password == "" {
		return fmt.Errorf("asterisk database password: %s is empty", path)
	}

	// format(..., %L) safely quotes the password as a SQL string literal;
	// ALTER ROLE's PASSWORD clause is a lexical string constant, not an
	// expression, so it can't take a bind parameter directly.
	var stmt string
	if err := pool.QueryRow(ctx, "SELECT format('ALTER ROLE linx_asterisk WITH LOGIN PASSWORD %L', $1::text)", password).Scan(&stmt); err != nil {
		return fmt.Errorf("asterisk database password: %w", err)
	}
	if _, err := pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("asterisk database password: %w", err)
	}
	return nil
}
