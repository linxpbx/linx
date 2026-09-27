package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// RunInput runs a command with stdin (pg_restore reads the dump from it),
// writing its standard output to stdout (nil discards it). Its error
// carries the command's stderr, like Runner's.
type RunInput func(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error

// Postgres restores a database dump into Linx's Postgres container
// (docs/BACKUP.md §4), all through docker exec as the database's own
// superuser (compose.yaml's POSTGRES_USER), the same trust as linx backup's
// pg_dump. The live database is never touched until the dump has loaded in
// full into a database of its own (Load) and been checked; Swap then trades
// the two by renaming, and the replaced one stays as Database+"_before_restore"
// until the next restore.
type Postgres struct {
	Container string // "linx-postgres"
	User      string // "linx"
	Database  string // "linx"
	Run       Runner
	RunInput  RunInput
	// Sleep waits between Swap's attempts (time.Sleep when nil).
	Sleep func(time.Duration)
}

// LoadedName and BeforeName are the database a dump is loaded into, and the
// name the live one keeps after Swap.
func (p Postgres) LoadedName() string { return p.Database + "_restore" }
func (p Postgres) BeforeName() string { return p.Database + "_before_restore" }

// psql runs one SQL statement against database db and returns its
// unaligned, tuples-only output. Every identifier in sql comes from this
// package's own constants, never from a request.
func (p Postgres) psql(ctx context.Context, db, sql string) (string, error) {
	var out bytes.Buffer
	err := p.Run(ctx, &out, nil, "docker", "exec", p.Container, "psql", "--username", p.User, "--dbname", db,
		"--no-psqlrc", "-v", "ON_ERROR_STOP=1", "--tuples-only", "--no-align", "--command", sql)
	return strings.TrimSpace(out.String()), err
}

// Load creates LoadedName (dropping any left over from an earlier attempt)
// and restores the dump into it in one transaction. The live database is
// untouched.
func (p Postgres) Load(ctx context.Context, dump io.Reader) error {
	if err := p.DropLoaded(ctx); err != nil {
		return err
	}
	if _, err := p.psql(ctx, "postgres", `CREATE DATABASE "`+p.LoadedName()+`"`); err != nil {
		return fmt.Errorf("creating a database to restore into: %w", err)
	}
	// --no-owner: everything belongs to the user restoring it, which is
	// the same superuser that owned it when it was dumped. Privileges are
	// kept (the asterisk role's grants on its views, ADR-032); roles
	// themselves aren't in a dump, and this server's migrations already
	// created linx_asterisk.
	err := p.RunInput(ctx, dump, nil, "docker", "exec", "-i", p.Container, "pg_restore", "--username", p.User,
		"--dbname", p.LoadedName(), "--no-owner", "--exit-on-error", "--single-transaction")
	if err != nil {
		_ = p.DropLoaded(ctx)
		return fmt.Errorf("loading the backup's database: %w", err)
	}
	return nil
}

// DropLoaded removes LoadedName, if it's there.
func (p Postgres) DropLoaded(ctx context.Context) error {
	if _, err := p.psql(ctx, "postgres", `DROP DATABASE IF EXISTS "`+p.LoadedName()+`" WITH (FORCE)`); err != nil {
		return fmt.Errorf("clearing an earlier restore attempt: %w", err)
	}
	return nil
}

// SchemaVersions returns the live database's and the loaded one's newest
// migration (schema_migrations), so a backup from a newer Linx than this
// server runs is refused before anything changes.
func (p Postgres) SchemaVersions(ctx context.Context) (live, loaded int, err error) {
	const q = `SELECT coalesce(max(version), 0) FROM schema_migrations`
	for _, v := range []struct {
		db  string
		out *int
	}{{p.Database, &live}, {p.LoadedName(), &loaded}} {
		s, err := p.psql(ctx, v.db, q)
		if err != nil {
			return 0, 0, fmt.Errorf("reading %s's schema version: %w", v.db, err)
		}
		if *v.out, err = strconv.Atoi(s); err != nil {
			return 0, 0, fmt.Errorf("reading %s's schema version: %q", v.db, s)
		}
	}
	return live, loaded, nil
}

// Prepare readies the loaded database to go live: every sign-in session
// ends (people sign in again; a browser session from before the backup
// must not come back to life), and any backup or restore request the
// backup happened to hold is dropped.
func (p Postgres) Prepare(ctx context.Context) error {
	// A backup from an older Linx may not have every table yet (the
	// control plane's migrations add them when it starts).
	for _, sql := range []string{
		`DO $$ BEGIN IF to_regclass('user_session') IS NOT NULL THEN DELETE FROM user_session; END IF; END $$`,
		`DO $$ BEGIN IF EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_name = 'pbx_setting' AND column_name = 'backup_requested_at') THEN
			UPDATE pbx_setting SET backup_requested_at = NULL, backup_requested_by = ''; END IF; END $$`,
		`DO $$ BEGIN IF to_regclass('backup_restore_request') IS NOT NULL THEN DELETE FROM backup_restore_request; END IF; END $$`,
	} {
		if _, err := p.psql(ctx, p.LoadedName(), sql); err != nil {
			return fmt.Errorf("preparing the restored database: %w", err)
		}
	}
	return nil
}

// Swap puts the loaded database live: the live one is closed to new
// connections, its open ones ended, and it's renamed BeforeName; then
// LoadedName becomes Database. If the second rename fails, the first is
// undone. Stop the control plane first: Asterisk's read-only connections
// are simply ended and reconnect to the new database.
func (p Postgres) Swap(ctx context.Context) error {
	sleep := p.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	live, before, loaded := `"`+p.Database+`"`, `"`+p.BeforeName()+`"`, `"`+p.LoadedName()+`"`
	if _, err := p.psql(ctx, "postgres", `DROP DATABASE IF EXISTS `+before+` WITH (FORCE)`); err != nil {
		return fmt.Errorf("removing the database kept from the last restore: %w", err)
	}
	if _, err := p.psql(ctx, "postgres", `ALTER DATABASE `+live+` ALLOW_CONNECTIONS false`); err != nil {
		return fmt.Errorf("closing the database to new connections: %w", err)
	}
	reopen := func() { _, _ = p.psql(ctx, "postgres", `ALTER DATABASE `+live+` ALLOW_CONNECTIONS true`) }
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			sleep(time.Second)
		}
		if _, err = p.psql(ctx, "postgres", `SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity
			WHERE datname = '`+p.Database+`' AND pid <> pg_backend_pid()`); err != nil {
			continue
		}
		if _, err = p.psql(ctx, "postgres", `ALTER DATABASE `+live+` RENAME TO `+before); err == nil {
			break
		}
	}
	if err != nil {
		reopen()
		return fmt.Errorf("setting the current database aside: %w", err)
	}
	if _, err := p.psql(ctx, "postgres", `ALTER DATABASE `+loaded+` RENAME TO `+live); err != nil {
		if _, undoErr := p.psql(ctx, "postgres", `ALTER DATABASE `+before+` RENAME TO `+live); undoErr != nil {
			return fmt.Errorf("putting the restored database in place: %w (and putting the old one back failed too: %v)", err, undoErr)
		}
		reopen()
		return fmt.Errorf("putting the restored database in place: %w", err)
	}
	if _, err := p.psql(ctx, "postgres", `ALTER DATABASE `+before+` ALLOW_CONNECTIONS true`); err != nil {
		return fmt.Errorf("reopening the database kept from before the restore: %w", err)
	}
	return nil
}
