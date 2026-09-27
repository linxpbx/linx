package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLockID is the Postgres advisory lock two control-plane instances
// starting at once take before applying migrations, so they can't race
// (ADR-026). Arbitrary but stable: "LINX" as ASCII bytes.
const migrationLockID int64 = 0x4c494e58

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads and orders the embedded SQL files, named
// <version>_<name>.sql, e.g. 0001_tenant_audit_log.sql.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	migrations := make([]migration, 0, len(entries))
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		numPart, rest, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migrations/%s: name must start with a number, e.g. 0001_name.sql", e.Name())
		}
		v, err := strconv.Atoi(numPart)
		if err != nil {
			return nil, fmt.Errorf("migrations/%s: %w", e.Name(), err)
		}
		if other, dup := seen[v]; dup {
			return nil, fmt.Errorf("migrations/%s: version %d already used by %s", e.Name(), v, other)
		}
		seen[v] = e.Name()
		b, err := migrationFiles.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, migration{version: v, name: strings.TrimSuffix(rest, ".sql"), sql: string(b)})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	return migrations, nil
}

// Migrate applies every migration that hasn't run yet, forward-only, each in
// its own transaction, and returns the resulting schema version (0 if none
// have ever run). Safe to call from multiple instances at once.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return 0, fmt.Errorf("migrate: %w", err)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("migrate: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return 0, fmt.Errorf("migrate: acquiring lock: %w", err)
	}
	defer conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", migrationLockID)

	if _, err := conn.Exec(ctx, schemaMigrationsTable); err != nil {
		return 0, fmt.Errorf("migrate: %w", err)
	}

	applied := map[int]bool{}
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return 0, fmt.Errorf("migrate: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return 0, fmt.Errorf("migrate: %w", err)
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("migrate: %w", err)
	}

	current := 0
	for _, m := range migrations {
		if applied[m.version] {
			current = m.version
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return current, err
		}
		current = m.version
	}
	return current, nil
}

// applyMigration runs one migration's SQL and records it in schema_migrations,
// in a single transaction, so a failure never leaves a partial change applied.
func applyMigration(ctx context.Context, conn *pgxpool.Conn, m migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migrate %04d_%s: %w", m.version, m.name, err)
	}
	defer tx.Rollback(ctx) // no-op after a successful Commit

	if _, err := tx.Exec(ctx, m.sql); err != nil {
		return fmt.Errorf("migrate %04d_%s: %w", m.version, m.name, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name) VALUES ($1, $2)", m.version, m.name); err != nil {
		return fmt.Errorf("migrate %04d_%s: %w", m.version, m.name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migrate %04d_%s: %w", m.version, m.name, err)
	}
	return nil
}

// schemaMigrationsTable is Migrate's own bookkeeping table.
const schemaMigrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    integer PRIMARY KEY,
		name       text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`

// createAsteriskRole is 0005's role creation: roles belong to the whole
// Postgres server, not one database, so a second database migrated on the
// same server (ReferenceScript's) finds it already there.
const createAsteriskRole = "CREATE ROLE linx_asterisk NOLOGIN;"

// ReferenceScript is every migration up to and including version, as one
// SQL script that builds exactly the schema Migrate would have made at that
// version, in an empty database on a server where Linx already runs. A
// restore compares a backup's schema against it (docs/BACKUP.md §8 step 6:
// only data comes from a backup, never code).
func ReferenceScript(version int) (string, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(schemaMigrationsTable + ";\n")
	found := false
	for _, m := range migrations {
		if m.version > version {
			break
		}
		sql := m.sql
		if strings.Contains(sql, createAsteriskRole) {
			sql = strings.Replace(sql, createAsteriskRole,
				"DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'linx_asterisk') THEN CREATE ROLE linx_asterisk NOLOGIN; END IF; END $$;", 1)
		}
		fmt.Fprintf(&b, "-- %04d_%s\n%s\n;\nINSERT INTO schema_migrations (version, name) VALUES (%d, '%s');\n", m.version, m.name, sql, m.version, m.name)
		found = found || m.version == version
	}
	if !found {
		return "", fmt.Errorf("this Linx has no database version %d", version)
	}
	return b.String(), nil
}
