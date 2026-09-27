package backup

import (
	"bytes"
	"context"
	"errors"
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
// (docs/BACKUP.md §4), all through docker exec. The live database is never
// touched until the dump has loaded in full into a database of its own
// (Load) and been checked (Check); Swap then trades the two by renaming, and
// the replaced one stays as Database+"_before_restore" until the next
// restore.
//
// A dump is SQL, and a backup can come from anywhere (a file uploaded in the
// browser), so it's never run by the database's superuser (User,
// compose.yaml's POSTGRES_USER): Load runs it as LoaderRole, which can do
// nothing outside the database it loads into, and Check refuses it unless
// its structure is exactly what this Linx's own migrations make, so only
// data ever comes from a backup, never functions, triggers or anything else
// that would later run as the superuser (docs/BACKUP.md §8 step 6).
type Postgres struct {
	Container string // "linx-postgres"
	User      string // "linx", the superuser
	Database  string // "linx"
	Run       Runner
	RunInput  RunInput
	// Reference returns the SQL that builds this Linx's schema as it was at
	// a migration version (db.ReferenceScript).
	Reference func(version int) (string, error)
	// Sleep waits between Swap's attempts (time.Sleep when nil).
	Sleep func(time.Duration)
}

// LoaderRole is the role a dump is loaded as: no superuser, no role or
// database creation, no password (so it can't sign in over the network),
// allowed to sign in (over the container's own socket) only while Load
// runs.
const LoaderRole = "linx_restore_loader"

// LoadedName, ReferenceName and BeforeName are the database a dump is
// loaded into, the one Check builds from this Linx's migrations to compare
// it with, and the name the live one keeps after Swap.
func (p Postgres) LoadedName() string    { return p.Database + "_restore" }
func (p Postgres) ReferenceName() string { return p.Database + "_reference" }
func (p Postgres) BeforeName() string    { return p.Database + "_before_restore" }

// psql runs one SQL statement against database db as the superuser and
// returns its unaligned, tuples-only output. Every identifier in sql comes
// from this package's own constants, never from a request. Never point it
// at LoadedName before Check has vouched for it.
func (p Postgres) psql(ctx context.Context, db, sql string) (string, error) {
	return p.psqlAs(ctx, p.User, db, sql)
}

func (p Postgres) psqlAs(ctx context.Context, user, db, sql string) (string, error) {
	var out bytes.Buffer
	err := p.Run(ctx, &out, nil, "docker", "exec", p.Container, "psql", "--username", user, "--dbname", db,
		"--no-psqlrc", "-v", "ON_ERROR_STOP=1", "--tuples-only", "--no-align", "--command", sql)
	return strings.TrimSpace(out.String()), err
}

// Load creates LoadedName (dropping any left over from an earlier attempt),
// owned by LoaderRole, and restores the dump into it in one transaction as
// LoaderRole. The live database is untouched.
func (p Postgres) Load(ctx context.Context, dump io.Reader) error {
	if err := p.DropLoaded(ctx); err != nil {
		return err
	}
	for _, sql := range []string{
		`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '` + LoaderRole + `') THEN
			CREATE ROLE ` + LoaderRole + `; END IF; END $$`,
		`ALTER ROLE ` + LoaderRole + ` LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD NULL`,
		`ALTER ROLE ` + LoaderRole + ` RESET ALL`,
		`CREATE DATABASE "` + p.LoadedName() + `" OWNER ` + LoaderRole,
	} {
		if _, err := p.psql(ctx, "postgres", sql); err != nil {
			return fmt.Errorf("preparing a database to restore into: %w", err)
		}
	}
	// --no-owner: everything belongs to LoaderRole until Check hands it to
	// the superuser. Privileges are kept (the asterisk role's grants on its
	// views, ADR-032); roles themselves aren't in a dump, and this server's
	// migrations already created linx_asterisk.
	err := p.RunInput(ctx, dump, nil, "docker", "exec", "-i", p.Container, "pg_restore", "--username", LoaderRole,
		"--dbname", p.LoadedName(), "--no-owner", "--exit-on-error", "--single-transaction")
	_, lockErr := p.psql(ctx, "postgres", `ALTER ROLE `+LoaderRole+` NOLOGIN`)
	if err == nil && lockErr != nil {
		err = lockErr
	}
	if err != nil {
		_ = p.DropLoaded(ctx)
		return fmt.Errorf("loading the backup's database: %w", err)
	}
	return nil
}

// ErrNotLinxSchema is Check's refusal of a database whose structure isn't
// what this Linx's migrations make.
var ErrNotLinxSchema = errors.New("this backup's database isn't one Linx made: its structure differs from what this version of Linx builds, so it wasn't restored")

// Check vouches for the loaded database before anything else touches it as
// the superuser: its settings are cleared (a database's own settings, such
// as search_path, apply to every connection to it), its schema version is
// read as LoaderRole and refused if newer than the live one's, and its
// schema is compared, as LoaderRole, with ReferenceName built from this
// Linx's own migrations at that version. Only when they're the same is it
// handed to the superuser. live and loaded are the two schema versions.
func (p Postgres) Check(ctx context.Context) (live, loaded int, err error) {
	defer func() {
		if err != nil {
			_ = p.DropLoaded(ctx)
		}
		_, _ = p.psql(ctx, "postgres", `DROP DATABASE IF EXISTS "`+p.ReferenceName()+`" WITH (FORCE)`)
		_, _ = p.psql(ctx, "postgres", `ALTER ROLE `+LoaderRole+` NOLOGIN`)
	}()
	loadedDB := `"` + p.LoadedName() + `"`
	for _, sql := range []string{
		`ALTER DATABASE ` + loadedDB + ` OWNER TO "` + p.User + `"`,
		`ALTER DATABASE ` + loadedDB + ` RESET ALL`,
		`ALTER ROLE ALL IN DATABASE ` + loadedDB + ` RESET ALL`,
		`ALTER ROLE ` + LoaderRole + ` RESET ALL`,
		`ALTER ROLE ` + LoaderRole + ` LOGIN`,
	} {
		if _, err := p.psql(ctx, "postgres", sql); err != nil {
			return 0, 0, fmt.Errorf("checking the backup's database: %w", err)
		}
	}
	const version = `SELECT coalesce(max(version), 0) FROM public.schema_migrations`
	s, err := p.psql(ctx, p.Database, version)
	if err == nil {
		live, err = strconv.Atoi(s)
	}
	if err != nil {
		return 0, 0, fmt.Errorf("reading this server's schema version: %w", err)
	}
	if s, err = p.psqlAs(ctx, LoaderRole, p.LoadedName(), version); err != nil {
		return live, 0, fmt.Errorf("the backup's database has no schema version: %w", err)
	}
	if loaded, err = strconv.Atoi(s); err != nil || loaded < 1 {
		return live, 0, fmt.Errorf("the backup's database has no schema version (%q)", s)
	}
	if loaded > live {
		return live, loaded, fmt.Errorf("this backup is from a newer version of Linx than this server runs (database version %d, this server %d): update Linx here first", loaded, live)
	}

	script, err := p.Reference(loaded)
	if err != nil {
		return live, loaded, err
	}
	// Built from the migrations, then put through the same dump and
	// reload a backup went through: Postgres writes some expressions back
	// slightly differently after a reload ("(a AND b) AND c" becomes
	// "a AND b AND c"), so only a reloaded reference compares like for like.
	if err := p.buildReference(ctx, script); err != nil {
		return live, loaded, fmt.Errorf("building this version's database structure to compare with: %w", err)
	}
	want, err := p.schema(ctx, p.User, p.ReferenceName())
	if err != nil {
		return live, loaded, err
	}
	got, err := p.schema(ctx, LoaderRole, p.LoadedName())
	if err != nil {
		return live, loaded, fmt.Errorf("reading the backup's database structure: %w", err)
	}
	if got != want {
		return live, loaded, fmt.Errorf("%w (first difference: %s)", ErrNotLinxSchema, firstDifference(got, want))
	}

	// Vouched for: the superuser takes everything over.
	for _, sql := range []string{
		`REASSIGN OWNED BY ` + LoaderRole + ` TO "` + p.User + `"`,
		`DROP OWNED BY ` + LoaderRole,
	} {
		if _, err := p.psql(ctx, p.LoadedName(), sql); err != nil {
			return live, loaded, fmt.Errorf("taking over the backup's database: %w", err)
		}
	}
	return live, loaded, nil
}

// buildReference builds ReferenceName from script via a scratch database
// and a dump and reload.
func (p Postgres) buildReference(ctx context.Context, script string) error {
	scratch, ref := p.ReferenceName()+"_build", p.ReferenceName()
	defer func() { _, _ = p.psql(ctx, "postgres", `DROP DATABASE IF EXISTS "`+scratch+`" WITH (FORCE)`) }()
	for _, sql := range []string{
		`DROP DATABASE IF EXISTS "` + scratch + `" WITH (FORCE)`,
		`DROP DATABASE IF EXISTS "` + ref + `" WITH (FORCE)`,
		`CREATE DATABASE "` + scratch + `"`,
		`CREATE DATABASE "` + ref + `"`,
	} {
		if _, err := p.psql(ctx, "postgres", sql); err != nil {
			return err
		}
	}
	if err := p.RunInput(ctx, strings.NewReader(script), nil, "docker", "exec", "-i", p.Container, "psql", "--username", p.User,
		"--dbname", scratch, "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "--single-transaction", "--quiet", "--file", "-"); err != nil {
		return err
	}
	var dump bytes.Buffer
	if err := p.Run(ctx, &dump, nil, "docker", "exec", p.Container, "pg_dump", "--username", p.User, "--dbname", scratch,
		"--schema-only", "--format", "custom"); err != nil {
		return err
	}
	return p.RunInput(ctx, &dump, nil, "docker", "exec", "-i", p.Container, "pg_restore", "--username", p.User,
		"--dbname", ref, "--no-owner", "--exit-on-error", "--single-transaction")
}

// schema is pg_dump's schema-only text of db, read as user, without the
// lines that differ between two dumps of the same schema (comments, blank
// lines, and PostgreSQL 18's per-run \restrict keys).
func (p Postgres) schema(ctx context.Context, user, db string) (string, error) {
	var out bytes.Buffer
	if err := p.Run(ctx, &out, nil, "docker", "exec", p.Container, "pg_dump", "--username", user, "--dbname", db,
		"--schema-only", "--no-owner"); err != nil {
		return "", err
	}
	var keep []string
	for _, line := range strings.Split(out.String(), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "--") || strings.HasPrefix(t, `\restrict`) || strings.HasPrefix(t, `\unrestrict`) {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n"), nil
}

// firstDifference names the first line where got and want differ, briefly.
func firstDifference(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < max(len(g), len(w)); i++ {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			cut := func(s string) string {
				if len(s) > 160 {
					return s[:160] + "…"
				}
				return s
			}
			return fmt.Sprintf("the backup has %q where Linx has %q", cut(strings.TrimSpace(gl)), cut(strings.TrimSpace(wl)))
		}
	}
	return "none"
}

// DropLoaded removes LoadedName, if it's there.
func (p Postgres) DropLoaded(ctx context.Context) error {
	if _, err := p.psql(ctx, "postgres", `DROP DATABASE IF EXISTS "`+p.LoadedName()+`" WITH (FORCE)`); err != nil {
		return fmt.Errorf("clearing an earlier restore attempt: %w", err)
	}
	return nil
}

// Prepare readies the loaded database to go live: every sign-in session
// ends (people sign in again; a browser session from before the backup
// must not come back to life), with its browser phone line, the way signing
// out ends them (marked ended, not deleted: a line belongs to its session),
// and any backup or restore request the backup happened to hold is dropped.
func (p Postgres) Prepare(ctx context.Context) error {
	// A backup from an older Linx may not have every table yet (the
	// control plane's migrations add them when it starts).
	for _, sql := range []string{
		`DO $$ BEGIN IF to_regclass('user_session') IS NOT NULL THEN
			UPDATE user_session SET revoked_at = now() WHERE revoked_at IS NULL; END IF; END $$`,
		`DO $$ BEGIN IF EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_name = 'device' AND column_name = 'user_session_id') THEN
			UPDATE device SET enabled = false, online = false, revoked_at = now(), version = version + 1, updated_at = now()
				WHERE kind = 'web' AND revoked_at IS NULL; END IF; END $$`,
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
