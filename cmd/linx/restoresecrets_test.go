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

// fakeRestoreRunner simulates restic for runRestoreSecrets's tests: on
// "restore", it writes the two secret files where restic would really put
// them (target + backup.KeysPath), so the command's own file-copying logic
// is exercised for real.
type fakeRestoreRunner struct {
	snapshotsOut string
	restoreErr   error
	initErr      error
}

func (f *fakeRestoreRunner) run(_ context.Context, w io.Writer, _ string, args ...string) error {
	for i, a := range args {
		switch a {
		case "snapshots":
			if w != nil {
				_, _ = io.WriteString(w, f.snapshotsOut)
			}
			return f.initErr
		case "restore":
			if f.restoreErr != nil {
				return f.restoreErr
			}
			target := args[i+3] // restore SNAPSHOT --target VALUE ... (SNAPSHOT i+1, --target i+2, VALUE i+3)
			dir := filepath.Join(target, backup.KeysPath)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, "linx_db_encryption_key"), []byte("restored-db-key"), 0o600); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "linx_jwt_signing_key"), []byte("restored-jwt-key"), 0o600)
		}
	}
	return nil
}

func TestRunRestoreSecretsRequiresYes(t *testing.T) {
	env := restoreSecretsEnv{isRoot: true, secretsDir: t.TempDir(), run: (&fakeRestoreRunner{}).run}
	var out, errb bytes.Buffer
	code := runRestoreSecrets(t.Context(), []string{"--password-file", passwordFile(t, "pw"), "/repo", "snap1"}, &out, &errb, env)
	if code != 1 || !strings.Contains(errb.String(), "--yes") {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
}

func TestRunRestoreSecretsNotRoot(t *testing.T) {
	env := restoreSecretsEnv{isRoot: false}
	var out, errb bytes.Buffer
	if code := runRestoreSecrets(t.Context(), []string{"--yes", "/repo", "snap1"}, &out, &errb, env); code != 1 || !strings.Contains(errb.String(), "root") {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
}

func TestRunRestoreSecretsWritesTheTwoFiles(t *testing.T) {
	secretsDir := t.TempDir()
	r := &fakeRestoreRunner{snapshotsOut: `[{"short_id":"snap1","tags":["pair:pair-99"]}]`}
	env := restoreSecretsEnv{isRoot: true, secretsDir: secretsDir, run: r.run}
	var out, errb bytes.Buffer

	code := runRestoreSecrets(t.Context(), []string{"--yes", "--password-file", passwordFile(t, "pw"), "/repo", "snap1"}, &out, &errb, env)
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "pair-99") {
		t.Fatalf("pair id missing from output: %q", out.String())
	}
	got, err := os.ReadFile(filepath.Join(secretsDir, "linx_db_encryption_key"))
	if err != nil || string(got) != "restored-db-key" {
		t.Fatalf("linx_db_encryption_key = %q, %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(secretsDir, "linx_jwt_signing_key"))
	if err != nil || string(got) != "restored-jwt-key" {
		t.Fatalf("linx_jwt_signing_key = %q, %v", got, err)
	}
}

func TestRunRestoreSecretsUnknownSnapshot(t *testing.T) {
	r := &fakeRestoreRunner{snapshotsOut: `[]`}
	env := restoreSecretsEnv{isRoot: true, secretsDir: t.TempDir(), run: r.run}
	var out, errb bytes.Buffer
	if code := runRestoreSecrets(t.Context(), []string{"--yes", "--password-file", passwordFile(t, "pw"), "/repo", "nope"}, &out, &errb, env); code != 1 {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
}

func passwordFile(t *testing.T, password string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(p, []byte(password), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
