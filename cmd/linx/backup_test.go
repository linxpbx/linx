package main

import (
	"bytes"
	"context"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/backup"
)

// fakeBackupRunner simulates restic and docker for runBackup's tests: it
// records every command, and lets each be scripted to write stdout and/or
// fail, in call order.
type fakeBackupRunner struct {
	calls  [][]string
	envs   [][]string
	stdout map[int]string
	fail   map[int]error
	n      int
}

func (f *fakeBackupRunner) run(_ context.Context, w io.Writer, env []string, name string, args ...string) error {
	i := f.n
	f.n++
	f.calls = append(f.calls, append([]string{name}, args...))
	f.envs = append(f.envs, env)
	if s, ok := f.stdout[i]; ok && w != nil {
		_, _ = io.WriteString(w, s)
	}
	return f.fail[i]
}

func setupBackupEnv(t *testing.T) (env backupEnv, r *fakeBackupRunner, secretsDir string) {
	t.Helper()
	secretsDir = t.TempDir()
	stagingDir := filepath.Join(t.TempDir(), "staging")
	if err := os.WriteFile(filepath.Join(secretsDir, "linx_db_encryption_key"), []byte("db-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "linx_jwt_signing_key"), []byte("jwt-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = &fakeBackupRunner{stdout: map[int]string{}, fail: map[int]error{}}
	// Call order: docker exec pg_dump, restic init, restic backup, restic forget.
	r.stdout[2] = `{"message_type":"summary","snapshot_id":"snap1"}` + "\n"
	env = backupEnv{
		isRoot: true, secretsDir: secretsDir, stagingDir: stagingDir, run: r.run,
		lookup:      backup.DefaultLookup,
		ownNetworks: func() ([]netip.Prefix, error) { return nil, nil },
	}
	return env, r, secretsDir
}

func TestRunBackupGeneratesAndShowsThePasswordOnce(t *testing.T) {
	env, r, _ := setupBackupEnv(t)
	var out, errb bytes.Buffer

	code := runBackup(t.Context(), nil, &out, &errb, env)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "snap1") {
		t.Fatalf("snapshot id missing from output: %q", out.String())
	}
	pwFile := env.destinationsManifest().PasswordPath(defaultDestinationName)
	saved, err := os.ReadFile(pwFile)
	if err != nil {
		t.Fatalf("password file not written: %v", err)
	}
	if !strings.Contains(out.String(), string(saved)) {
		t.Fatal("the generated password wasn't shown")
	}
	if len(r.calls) != 4 {
		t.Fatalf("expected 4 commands (dump, init, backup, forget), got %d: %v", len(r.calls), r.calls)
	}
	joinedDump := strings.Join(r.calls[0], " ")
	if r.calls[0][0] != "docker" || !strings.Contains(joinedDump, "pg_dump") || !strings.Contains(joinedDump, backupPostgresContainer) {
		t.Fatalf("unexpected dump call: %v", r.calls[0])
	}
	joinedInit := strings.Join(r.calls[1], " ")
	if !strings.Contains(joinedInit, defaultBackupRepo) || !strings.Contains(joinedInit, "init") {
		t.Fatalf("unexpected init call: %v", r.calls[1])
	}
	joinedBackup := strings.Join(r.calls[2], " ")
	if !strings.Contains(joinedBackup, "backup") || !strings.Contains(joinedBackup, backup.KeysPath) {
		t.Fatalf("unexpected backup call: %v", r.calls[2])
	}

	// A second run reuses the saved password and doesn't show it again.
	out.Reset()
	errb.Reset()
	r2 := &fakeBackupRunner{stdout: map[int]string{2: `{"message_type":"summary","snapshot_id":"snap2"}` + "\n"}, fail: map[int]error{}}
	env.run = r2.run
	if code := runBackup(t.Context(), nil, &out, &errb, env); code != 0 {
		t.Fatalf("second run: code %d, %q", code, errb.String())
	}
	if strings.Contains(out.String(), string(saved)) {
		t.Fatal("the password was shown again on a later run")
	}
}

func TestRunBackupNotRoot(t *testing.T) {
	env, _, _ := setupBackupEnv(t)
	env.isRoot = false
	var out, errb bytes.Buffer
	if code := runBackup(t.Context(), nil, &out, &errb, env); code != 1 || !strings.Contains(errb.String(), "root") {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
}

func TestRunBackupMissingSecretFails(t *testing.T) {
	env, _, secretsDir := setupBackupEnv(t)
	if err := os.Remove(filepath.Join(secretsDir, "linx_jwt_signing_key")); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runBackup(t.Context(), nil, &out, &errb, env); code != 1 {
		t.Fatalf("code = %d, want 1 when a secret is missing; stderr %q", code, errb.String())
	}
}

func TestRunBackupDumpFailure(t *testing.T) {
	env, r, _ := setupBackupEnv(t)
	r.fail[0] = context.DeadlineExceeded
	var out, errb bytes.Buffer
	if code := runBackup(t.Context(), nil, &out, &errb, env); code != 1 || !strings.Contains(errb.String(), "dump") {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
}

func TestRunBackupMultipleDestinationsContinuesOnFailure(t *testing.T) {
	env, r, _ := setupBackupEnv(t)
	m := env.destinationsManifest()
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dests := []backup.Destination{
		{Name: "local-a", Kind: backup.KindLocal, Path: "/repo-a"},
		{Name: "local-b", Kind: backup.KindLocal, Path: "/repo-b"},
	}
	for _, d := range dests {
		if err := os.WriteFile(m.PasswordPath(d.Name), []byte("pw-"+d.Name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Save(dests); err != nil {
		t.Fatal(err)
	}
	// Call order: 0 dump (shared, once), 1 local-a init (made to fail),
	// then local-b: 2 init, 3 backup, 4 forget.
	r.fail[1] = context.DeadlineExceeded
	r.stdout[3] = `{"message_type":"summary","snapshot_id":"snap-b"}` + "\n"

	var out, errb bytes.Buffer
	code := runBackup(t.Context(), nil, &out, &errb, env)
	if code != 0 {
		t.Fatalf("code = %d (want 0: one destination still succeeded), stderr = %q", code, errb.String())
	}
	if !strings.Contains(errb.String(), "local-a") {
		t.Fatalf("failed destination not reported: %q", errb.String())
	}
	if !strings.Contains(out.String(), "local-b") || !strings.Contains(out.String(), "snap-b") {
		t.Fatalf("successful destination not reported: %q", out.String())
	}
}
