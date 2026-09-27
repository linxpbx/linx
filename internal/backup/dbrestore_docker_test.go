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
	exec1(`CREATE TABLE restore_marker (v text)`)
	exec1(`INSERT INTO restore_marker VALUES ('backup')`)
	exec1(`UPDATE pbx_setting SET backup_requested_at = now(), backup_requested_by = 'user:x'`)

	var dump bytes.Buffer
	if err := execRun(ctx, &dump, nil, "docker", "exec", container, "pg_dump", "--username", "linx", "--dbname", "linx", "--format", "custom"); err != nil {
		t.Fatal(err)
	}
	exec1(`UPDATE restore_marker SET v = 'live'`)

	p := backup.Postgres{Container: container, User: "linx", Database: "linx", Run: execRun, RunInput: execRunInput,
		Sleep: func(time.Duration) {}}
	restore := func() {
		t.Helper()
		if err := p.Load(ctx, bytes.NewReader(dump.Bytes())); err != nil {
			t.Fatal(err)
		}
		live, loaded, err := p.SchemaVersions(ctx)
		if err != nil || live != loaded || live < 25 {
			t.Fatalf("SchemaVersions() = %d, %d, %v", live, loaded, err)
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
	if got := query("linx", `SELECT v FROM restore_marker`); got != "backup" {
		t.Errorf("live database's marker = %q, want the backup's", got)
	}
	if got := query("linx_before_restore", `SELECT v FROM restore_marker`); got != "live" {
		t.Errorf("kept database's marker = %q, want the one from before the restore", got)
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
	if err := pool.QueryRow(ctx, `SELECT v FROM restore_marker`).Scan(&v); err != nil || v != "backup" {
		t.Errorf("pool after restore: %q, %v", v, err)
	}

	// Again: the database kept from the first restore makes way.
	restore()
	if got := query("linx_before_restore", `SELECT v FROM restore_marker`); got != "backup" {
		t.Errorf("after a second restore, kept marker = %q", got)
	}

	// A backup from a newer Linx shows up as a newer schema version.
	if err := p.Load(ctx, bytes.NewReader(dump.Bytes())); err != nil {
		t.Fatal(err)
	}
	query(p.LoadedName(), `INSERT INTO schema_migrations (version, name) VALUES (999, 'future')`)
	if live, loaded, err := p.SchemaVersions(ctx); err != nil || loaded != 999 || live >= loaded {
		t.Errorf("SchemaVersions() = %d, %d, %v; want the loaded one newer", live, loaded, err)
	}
	if err := p.DropLoaded(ctx); err != nil {
		t.Fatal(err)
	}

	// A broken dump leaves nothing behind and the live database alone.
	if err := p.Load(ctx, strings.NewReader("not a dump")); err == nil {
		t.Error("Load() accepted a broken dump")
	}
	if got := query("postgres", `SELECT count(*) FROM pg_database WHERE datname = 'linx_restore'`); got != "0" {
		t.Errorf("a failed load left linx_restore behind")
	}
}
