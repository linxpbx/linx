package doctor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/installer"
)

// TestPlatformQueryDocker runs doctor's database query against a real,
// migrated Postgres, so a schema change that breaks it fails here rather
// than on a server. It needs Docker: make test-docker.
func TestPlatformQueryDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-doctor-test")
	version, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}

	read := func() platformState {
		t.Helper()
		var raw string
		if err := pool.QueryRow(ctx, platformQuery).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var st platformState
		if err := json.Unmarshal([]byte(raw), &st); err != nil {
			t.Fatalf("%v in %s", err, raw)
		}
		return st
	}

	// The exact command doctor runs, inside the real Postgres image.
	out, err := installer.ExecRunner{}.Run(ctx, nil, "docker", psqlArgs("linx-doctor-test", platformQuery)...)
	if err != nil {
		t.Fatalf("docker exec psql: %v\n%s", err, out)
	}
	var viaExec platformState
	if err := json.Unmarshal(out, &viaExec); err != nil || viaExec.Schema != version {
		t.Fatalf("docker exec psql output %q: %+v, %v", out, viaExec, err)
	}

	st := read()
	if st.Schema != version || st.APIKeys != 0 || st.AlertChannels != 0 || len(st.OpenAlerts) != 0 {
		t.Fatalf("empty database: %+v", st)
	}

	// One open alert with awkward characters, one resolved (not listed).
	if _, err := pool.Exec(ctx, `INSERT INTO tenant (id, name) VALUES ('01900000-0000-7000-8000-000000000001', 'T')`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO alert (id, tenant_id, key, severity, title, message, status, first_seen_at, last_seen_at, stable_since)
		 VALUES ('01900000-0000-7000-8000-000000000002', '01900000-0000-7000-8000-000000000001', 'k1', 'critical',
		 'Trunk | "down"', E'line one\nline two', 'open', now(), now(), now())`,
		`INSERT INTO alert (id, tenant_id, key, severity, title, message, status, first_seen_at, last_seen_at, stable_since, resolved_at)
		 VALUES ('01900000-0000-7000-8000-000000000003', '01900000-0000-7000-8000-000000000001', 'k2', 'warning',
		 'Old', 'gone', 'resolved', now(), now(), now(), now())`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	st = read()
	if len(st.OpenAlerts) != 1 || st.OpenAlerts[0].Title != `Trunk | "down"` ||
		st.OpenAlerts[0].Message != "line one\nline two" || st.OpenAlerts[0].Severity != "critical" || st.OpenAlerts[0].Since.IsZero() {
		t.Fatalf("open alerts: %+v", st.OpenAlerts)
	}
}

// TestPhoneQueryDocker checks doctor's view of the linx_asterisk role
// against the real migrations: exactly the four views, read-only, and a
// widened grant is noticed.
func TestPhoneQueryDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-doctor-phone-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	read := func() (st struct{ Views, Other int }) {
		t.Helper()
		out, err := installer.ExecRunner{}.Run(ctx, nil, "docker", psqlArgs("linx-doctor-phone-test", phoneQuery)...)
		if err != nil {
			t.Fatalf("docker exec psql: %v\n%s", err, out)
		}
		if err := json.Unmarshal(out, &st); err != nil {
			t.Fatalf("%v in %s", err, out)
		}
		return st
	}
	if st := read(); st.Views != 4 || st.Other != 0 {
		t.Fatalf("fresh database: %+v", st)
	}
	for _, grant := range []string{"GRANT SELECT ON api_key TO linx_asterisk", "GRANT INSERT ON asterisk.ps_auths TO linx_asterisk"} {
		if _, err := pool.Exec(ctx, grant); err != nil {
			t.Fatal(err)
		}
		if st := read(); st.Other == 0 {
			t.Errorf("%s: not noticed (%+v)", grant, st)
		}
		if _, err := pool.Exec(ctx, "REVOKE ALL ON api_key, asterisk.ps_auths FROM linx_asterisk; GRANT SELECT ON asterisk.ps_auths TO linx_asterisk"); err != nil {
			t.Fatal(err)
		}
	}
}
