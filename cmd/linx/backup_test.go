package main

import (
	"bytes"
	"context"
	"io"
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
	stdout map[int]string
	fail   map[int]error
	n      int
}

func (f *fakeBackupRunner) run(_ context.Context, w io.Writer, name string, args ...string) error {
	i := f.n
	f.n++
	f.calls = append(f.calls, append([]string{name}, args...))
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
	// Call order: restic init, docker exec pg_dump, restic backup, restic forget.
	r.stdout[2] = `{"message_type":"summary","snapshot_id":"snap1"}` + "\n"
	env = backupEnv{isRoot: true, secretsDir: secretsDir, stagingDir: stagingDir, run: r.run}
	return env, r, secretsDir
}

func TestRunBackupGeneratesAndShowsThePasswordOnce(t *testing.T) {
	env, r, secretsDir := setupBackupEnv(t)
	var out, errb bytes.Buffer

	code := runBackup(t.Context(), []string{"--repo", "/repo"}, &out, &errb, env)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "snap1") {
		t.Fatalf("snapshot id missing from output: %q", out.String())
	}
	pwFile := filepath.Join(secretsDir, backupPasswordName)
	saved, err := os.ReadFile(pwFile)
	if err != nil {
		t.Fatalf("password file not written: %v", err)
	}
	if !strings.Contains(out.String(), string(saved)) {
		t.Fatal("the generated password wasn't shown")
	}
	if len(r.calls) != 4 {
		t.Fatalf("expected 4 commands (init, dump, backup, forget), got %d: %v", len(r.calls), r.calls)
	}
	joinedInit := strings.Join(r.calls[0], " ")
	if !strings.Contains(joinedInit, "/repo") || !strings.Contains(joinedInit, "init") {
		t.Fatalf("unexpected init call: %v", r.calls[0])
	}
	joinedDump := strings.Join(r.calls[1], " ")
	if r.calls[1][0] != "docker" || !strings.Contains(joinedDump, "pg_dump") || !strings.Contains(joinedDump, backupPostgresContainer) {
		t.Fatalf("unexpected dump call: %v", r.calls[1])
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
	if code := runBackup(t.Context(), []string{"--repo", "/repo"}, &out, &errb, env); code != 0 {
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
	r.fail[1] = context.DeadlineExceeded
	var out, errb bytes.Buffer
	if code := runBackup(t.Context(), nil, &out, &errb, env); code != 1 || !strings.Contains(errb.String(), "dump") {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
}
