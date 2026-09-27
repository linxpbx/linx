package backup_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/backup"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
)

func execRun(ctx context.Context, w io.Writer, _ []string, name string, args ...string) error {
	return execRunInput(ctx, nil, w, name, args...)
}

func execRunInput(ctx context.Context, stdin io.Reader, w io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, w, &stderr
	if err := cmd.Run(); err != nil {
		return errors.New(strings.TrimSpace(stderr.String()) + ": " + err.Error())
	}
	return nil
}

// TestPostgresRestoreDocker restores a real pg_dump of a migrated Linx
// database over a live one that has an open connection, the way linx
// restore does (docs/BACKUP.md §4). make test-docker.
func TestPostgresRestoreDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const container = "linx-backup-restore-test"
	pool := dbtest.Start(t, ctx, container)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec1 := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// Data only: a backup is refused unless its structure is exactly what
	// Linx's migrations make, so the marker is a value, not a table.
	exec1(`INSERT INTO tenant (id, name) VALUES (gen_random_uuid(), 'backup')`)
	exec1(`UPDATE pbx_setting SET backup_requested_at = now(), backup_requested_by = 'user:x'`)
	// Someone signed in, with a browser phone line: the line belongs to the
	// session, which the restore ends (found on a real server: deleting the
	// session was refused).
	exec1(`INSERT INTO extension (id, tenant_id, number, display_name, created_at, updated_at)
		SELECT '00000000-0000-7000-8000-000000000001', id, '101', 'Rana', now(), now() FROM tenant`)
	exec1(`INSERT INTO app_user (id, tenant_id, email, name, role, password_hash, password_updated_at, created_at, updated_at)
		SELECT '00000000-0000-7000-8000-000000000002', id, 'rana@example.com', 'Rana', 'user', '', now(), now(), now() FROM tenant`)
	exec1(`INSERT INTO user_session (id, tenant_id, user_id, role, token_hash, csrf_hash, created_at, expires_at, idle_expires_at, last_seen_at)
		SELECT '00000000-0000-7000-8000-000000000003', id, '00000000-0000-7000-8000-000000000002', 'user',
			decode(repeat('ab', 32), 'hex'), decode(repeat('cd', 32), 'hex'), now(), now() + interval '1 day', now() + interval '1 day', now() FROM tenant`)
	exec1(`INSERT INTO device (id, tenant_id, extension_id, name, kind, sip_username, digest_hash, user_session_id, created_at, updated_at)
		SELECT '00000000-0000-7000-8000-000000000004', id, '00000000-0000-7000-8000-000000000001', 'Browser', 'web', 'd_Web00001',
			repeat('0', 32), '00000000-0000-7000-8000-000000000003', now(), now() FROM tenant`)

	pgDump := func() []byte {
		t.Helper()
		var dump bytes.Buffer
		if err := execRun(ctx, &dump, nil, "docker", "exec", container, "pg_dump", "--username", "linx", "--dbname", "linx", "--format", "custom"); err != nil {
			t.Fatal(err)
		}
		return dump.Bytes()
	}
	dump := pgDump()
	exec1(`UPDATE tenant SET name = 'live'`)

	p := backup.Postgres{Container: container, User: "linx", Database: "linx", Run: execRun, RunInput: execRunInput,
		Sleep: func(time.Duration) {}, Reference: db.ReferenceScript}
	restore := func() {
		t.Helper()
		if err := p.Load(ctx, bytes.NewReader(dump)); err != nil {
			t.Fatal(err)
		}
		live, loaded, err := p.Check(ctx)
		if err != nil || live != loaded || live < 26 {
			t.Fatalf("Check() = %d, %d, %v", live, loaded, err)
		}
		if err := p.Prepare(ctx); err != nil {
			t.Fatal(err)
		}
		// The pool still holds connections to the live database: Swap
		// has to end them.
		if err := pool.Ping(ctx); err != nil {
			t.Fatal(err)
		}
		if err := p.Swap(ctx); err != nil {
			t.Fatal(err)
		}
	}
	restore()

	query := func(dbName, sql string) string {
		t.Helper()
		var out bytes.Buffer
		if err := execRun(ctx, &out, nil, "docker", "exec", container, "psql", "--username", "linx", "--dbname", dbName,
			"--tuples-only", "--no-align", "--command", sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return strings.TrimSpace(out.String())
	}
	if got := query("linx", `SELECT name FROM tenant`); got != "backup" {
		t.Errorf("live database's marker = %q, want the backup's", got)
	}
	if got := query("linx_before_restore", `SELECT name FROM tenant`); got != "live" {
		t.Errorf("kept database's marker = %q, want the one from before the restore", got)
	}
	if got := query("linx", `SELECT count(*) FROM user_session WHERE revoked_at IS NULL`); got != "0" {
		t.Errorf("%s sign-in sessions came back to life with the restore", got)
	}
	if got := query("linx", `SELECT enabled::text || ' ' || (revoked_at IS NOT NULL)::text FROM device WHERE kind = 'web'`); got != "false true" {
		t.Errorf("the browser phone line wasn't ended with its session: %q", got)
	}
	if got := query("linx", `SELECT backup_requested_at IS NULL FROM pbx_setting`); got != "t" {
		t.Errorf("a pending \"back up now\" came back with the restore")
	}
	// The views' grants to Asterisk's role (ADR-032) survive.
	if got := query("linx", `SELECT has_table_privilege('linx_asterisk', 'asterisk.ps_endpoints', 'SELECT')`); got != "t" {
		t.Errorf("linx_asterisk lost SELECT on asterisk.ps_endpoints")
	}
	if got := query("postgres", `SELECT count(*) FROM pg_database WHERE datname = 'linx_restore'`); got != "0" {
		t.Errorf("linx_restore left behind")
	}
	// The pool's old connections were ended; new ones reach the restored
	// database.
	pool.Reset()
	var v string
	if err := pool.QueryRow(ctx, `SELECT name FROM tenant`).Scan(&v); err != nil || v != "backup" {
		t.Errorf("pool after restore: %q, %v", v, err)
	}

	// Again: the database kept from the first restore makes way.
	restore()
	if got := query("linx_before_restore", `SELECT name FROM tenant`); got != "backup" {
		t.Errorf("after a second restore, kept marker = %q", got)
	}

	// Everything restored belongs to the superuser, nothing to the loader.
	if got := query("linx", `SELECT count(*) FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner WHERE r.rolname = '`+backup.LoaderRole+`'`); got != "0" {
		t.Errorf("%s objects still owned by the loader", got)
	}
	if got := query("postgres", `SELECT rolcanlogin::text || rolsuper::text FROM pg_roles WHERE rolname = '`+backup.LoaderRole+`'`); got != "falsefalse" {
		t.Errorf("loader role can log in or is a superuser: %s", got)
	}

	// A backup from a newer Linx is refused.
	if err := p.Load(ctx, bytes.NewReader(dump)); err != nil {
		t.Fatal(err)
	}
	query(p.LoadedName(), `INSERT INTO schema_migrations (version, name) VALUES (999, 'future')`)
	if live, loaded, err := p.Check(ctx); err == nil || loaded != 999 || live >= loaded {
		t.Errorf("Check() = %d, %d, %v; want the newer backup refused", live, loaded, err)
	}
	if got := query("postgres", `SELECT count(*) FROM pg_database WHERE datname IN ('linx_restore', 'linx_reference')`); got != "0" {
		t.Errorf("a refused backup left %s databases behind", got)
	}

	// A backup carrying anything Linx's migrations don't make — here a
	// function and a trigger that would later run as the superuser — is
	// refused, and never touched by the superuser.
	exec1(`CREATE FUNCTION public.sneaky() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`)
	exec1(`CREATE TRIGGER sneaky BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION public.sneaky()`)
	sneaky := pgDump()
	exec1(`DROP TRIGGER sneaky ON audit_log`)
	exec1(`DROP FUNCTION public.sneaky()`)
	if err := p.Load(ctx, bytes.NewReader(sneaky)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Check(ctx); !errors.Is(err, backup.ErrNotLinxSchema) {
		t.Errorf("Check() of a backup with an extra trigger = %v, want ErrNotLinxSchema", err)
	}

	// SQL in a dump runs as the loader, which can't run programs: a check
	// whose function tries to while the data loads makes the load fail.
	exec1(`CREATE FUNCTION public.evil(t text) RETURNS text LANGUAGE plpgsql AS $$
		BEGIN
			IF current_database() = 'linx_restore' THEN
				EXECUTE 'COPY (SELECT 1) TO PROGRAM ''touch /tmp/pwned''';
			END IF;
			RETURN t;
		END $$`)
	exec1(`CREATE TABLE evil (v text CHECK (public.evil(v) IS NOT NULL))`)
	exec1(`INSERT INTO evil VALUES ('x')`)
	evil := pgDump()
	if err := p.Load(ctx, bytes.NewReader(evil)); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("Load() of a dump that runs a program = %v, want permission denied", err)
	}
	if err := execRun(ctx, nil, nil, "docker", "exec", container, "test", "-e", "/tmp/pwned"); err == nil {
		t.Error("the dump ran a program in the database container")
	}

	// A broken dump leaves nothing behind and the live database alone.
	if err := p.Load(ctx, strings.NewReader("not a dump")); err == nil {
		t.Error("Load() accepted a broken dump")
	}
	if got := query("postgres", `SELECT count(*) FROM pg_database WHERE datname = 'linx_restore'`); got != "0" {
		t.Errorf("a failed load left linx_restore behind")
	}
}
