package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/backup"
)

// fakeRestoreHost stands in for restic and docker during linx restore:
// restic "restore" writes a backup's files where restic would really put
// them, and docker psql answers the schema-version question.
type fakeRestoreHost struct {
	liveVersion, loadedVersion string
	missingKey                 bool
	failRename                 bool
	unhealthy                  bool
	calls                      []string // "docker stop linx-control-plane", "psql: ALTER DATABASE ...", ...
	repos                      []string // each restic call's -r, with whether it existed then
}

func (f *fakeRestoreHost) run(_ context.Context, w io.Writer, _ []string, name string, args ...string) error {
	if name == "restic" {
		if len(args) > 1 && args[0] == "-r" {
			_, err := os.Stat(filepath.Join(args[1], "config"))
			f.repos = append(f.repos, fmt.Sprintf("%s %v", args[1], err == nil))
		}
		for i, a := range args {
			switch a {
			case "snapshots":
				_, _ = io.WriteString(w, `[{"id":"abcdef0123456789","short_id":"abcdef01","time":"2026-09-20T03:00:00Z","tags":["pair:p1"]}]`)
				return nil
			case "restore":
				f.calls = append(f.calls, "restic restore "+args[i+1])
				staged := filepath.Join(args[i+3], backup.StagingDir)
				keys := filepath.Join(staged, backup.KeysDirName)
				if err := os.MkdirAll(keys, 0o700); err != nil {
					return err
				}
				_ = os.WriteFile(filepath.Join(staged, "database.pgcustom"), []byte("dump"), 0o600)
				_ = os.WriteFile(filepath.Join(keys, "linx_db_encryption_key"), []byte("backup-db-key"), 0o600)
				if !f.missingKey {
					_ = os.WriteFile(filepath.Join(keys, "linx_jwt_signing_key"), []byte("backup-jwt-key"), 0o600)
				}
				return nil
			}
		}
		return errors.New("unexpected restic call")
	}
	if slices.Contains(args, "psql") {
		sql := args[len(args)-1]
		db := args[slices.Index(args, "--dbname")+1]
		f.calls = append(f.calls, "psql "+db+": "+sql)
		if strings.Contains(sql, "max(version)") {
			v := f.liveVersion
			if db == "linx_restore" {
				v = f.loadedVersion
			}
			_, _ = io.WriteString(w, v+"\n")
		}
		if f.failRename && strings.Contains(sql, `RENAME TO "linx"`) {
			return errors.New("rename failed")
		}
		return nil
	}
	if len(args) > 0 && args[0] == "inspect" {
		if f.unhealthy {
			_, _ = io.WriteString(w, "starting\n")
		} else {
			_, _ = io.WriteString(w, "healthy\n")
		}
		return nil
	}
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return nil
}

func (f *fakeRestoreHost) runInput(_ context.Context, stdin io.Reader, _ io.Writer, _ string, args ...string) error {
	b, _ := io.ReadAll(stdin)
	f.calls = append(f.calls, "pg_restore "+args[slices.Index(args, "--dbname")+1]+" "+string(b))
	return nil
}

func (f *fakeRestoreHost) index(prefix string) int {
	return slices.IndexFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func newRestoreEnv(t *testing.T, f *fakeRestoreHost) restoreEnv {
	t.Helper()
	secrets := t.TempDir()
	for _, name := range restoreKeys {
		if err := os.WriteFile(filepath.Join(secrets, name), []byte("live-"+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return restoreEnv{
		backupEnv:     backupEnv{isRoot: true, secretsDir: secrets, run: f.run},
		stdin:         strings.NewReader("the-password\n"),
		workDir:       t.TempDir(),
		runInput:      f.runInput,
		sleep:         func(time.Duration) {},
		healthTimeout: 0,
	}
}

func readSecret(t *testing.T, env restoreEnv, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(env.secretsDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func runRestoreJSON(t *testing.T, env restoreEnv, args ...string) (restoreResult, int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runRestore(t.Context(), append([]string{"--json", "--password-stdin"}, args...), &out, &errb, env)
	var res restoreResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("output %q isn't JSON: %v (stderr %q)", out.String(), err, errb.String())
	}
	return res, code, errb.String()
}

func TestRestoreRefusesWithoutConfirmation(t *testing.T) {
	f := &fakeRestoreHost{}
	env := newRestoreEnv(t, f)
	for _, args := range [][]string{
		{"--path", "/var/backups/linx"}, // no --yes
		{"--yes"},                       // nowhere
		{"--yes", "--path", "/a", "--destination", "nas"}, // both
		{"--yes", "--path", "sftp:u@h:/x"},                // not a folder
		{"--yes", "--path", "/a", "latest; rm -rf /"},     // not a snapshot
		{"--yes", "--destination", "nas"},                 // not set up here
	} {
		res, code, _ := runRestoreJSON(t, env, args...)
		if code != 1 || res.OK || res.Changed || res.Error == "" {
			t.Errorf("%v: code %d, %+v", args, code, res)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("refused restores still ran: %v", f.calls)
	}
	env.isRoot = false
	if res, code, _ := runRestoreJSON(t, env, "--yes", "--path", "/a"); code != 1 || !strings.Contains(res.Error, "root") {
		t.Errorf("not root: code %d, %+v", code, res)
	}
}

func TestRestoreFromAFolder(t *testing.T) {
	f := &fakeRestoreHost{liveVersion: "25", loadedVersion: "24"}
	env := newRestoreEnv(t, f)
	res, code, stderr := runRestoreJSON(t, env, "--yes", "--path", defaultBackupRepo)
	if code != 0 || !res.OK || !res.Changed || res.SnapshotID != "abcdef0123456789" || res.SnapshotTime.IsZero() {
		t.Fatalf("code %d, %+v, stderr %q", code, res, stderr)
	}
	if got := readSecret(t, env, "linx_jwt_signing_key"); got != "backup-jwt-key" {
		t.Errorf("jwt key = %q, want the backup's", got)
	}
	if got := readSecret(t, env, "linx_db_encryption_key.before-restore"); got != "live-linx_db_encryption_key" {
		t.Errorf("kept key = %q, want the one from before", got)
	}
	// The resolved snapshot id is restored, not "latest".
	if f.index("restic restore abcdef0123456789") < 0 {
		t.Errorf("didn't restore the snapshot it looked at: %v", f.calls)
	}
	if f.index("pg_restore linx_restore dump") < 0 {
		t.Errorf("dump not loaded into linx_restore: %v", f.calls)
	}
	// Loaded and checked before the control plane stops; swapped while it's
	// stopped; started (and Asterisk restarted) after.
	stop, swap, start := f.index("docker stop linx-control-plane"), f.index(`psql postgres: ALTER DATABASE "linx_restore" RENAME`), f.index("docker start linx-control-plane")
	prepare := f.index("psql linx_restore: DO $$ BEGIN IF to_regclass('user_session')")
	if !(prepare >= 0 && prepare < stop && stop < swap && swap < start) || f.index("docker restart linx-asterisk") < start {
		t.Errorf("wrong order: %v", f.calls)
	}
	// Later backups use the password that worked.
	pw, err := os.ReadFile(env.destinationsManifest().PasswordPath(defaultDestinationName))
	if err != nil || string(pw) != "the-password" {
		t.Errorf("saved password = %q, %v", pw, err)
	}
}

func TestRestoreRefusesANewerBackup(t *testing.T) {
	f := &fakeRestoreHost{liveVersion: "25", loadedVersion: "30"}
	env := newRestoreEnv(t, f)
	res, code, _ := runRestoreJSON(t, env, "--yes", "--path", "/mnt/old")
	if code != 1 || res.Changed || !strings.Contains(res.Error, "newer version") {
		t.Fatalf("code %d, %+v", code, res)
	}
	if f.index("docker stop") >= 0 || readSecret(t, env, "linx_jwt_signing_key") != "live-linx_jwt_signing_key" {
		t.Errorf("a refused restore changed the server: %v", f.calls)
	}
	if f.index(`psql postgres: DROP DATABASE IF EXISTS "linx_restore"`) < 0 {
		t.Errorf("loaded database not cleared: %v", f.calls)
	}
}

func TestRestoreRefusesABackupMissingAKey(t *testing.T) {
	f := &fakeRestoreHost{missingKey: true}
	env := newRestoreEnv(t, f)
	res, code, _ := runRestoreJSON(t, env, "--yes", "--path", "/mnt/old")
	if code != 1 || res.Changed || !strings.Contains(res.Error, "linx_jwt_signing_key") {
		t.Fatalf("code %d, %+v", code, res)
	}
	if f.index("pg_restore") >= 0 {
		t.Errorf("loaded a database without its keys: %v", f.calls)
	}
}

func TestRestorePutsKeysBackWhenTheSwapFails(t *testing.T) {
	f := &fakeRestoreHost{liveVersion: "25", loadedVersion: "25", failRename: true}
	env := newRestoreEnv(t, f)
	res, code, _ := runRestoreJSON(t, env, "--yes", "--path", "/mnt/old")
	if code != 1 || res.Changed {
		t.Fatalf("code %d, %+v", code, res)
	}
	if got := readSecret(t, env, "linx_jwt_signing_key"); got != "live-linx_jwt_signing_key" {
		t.Errorf("jwt key = %q, want the live one back", got)
	}
	if f.index("docker start linx-control-plane") < 0 {
		t.Errorf("control plane left stopped: %v", f.calls)
	}
	if f.index(`psql postgres: ALTER DATABASE "linx_before_restore" RENAME TO "linx"`) < 0 {
		t.Errorf("old database not put back: %v", f.calls)
	}
}

func TestRestoreReportsAControlPlaneThatDoesntComeBack(t *testing.T) {
	f := &fakeRestoreHost{liveVersion: "25", loadedVersion: "25", unhealthy: true}
	env := newRestoreEnv(t, f)
	res, code, _ := runRestoreJSON(t, env, "--yes", "--path", "/mnt/old")
	if code != 1 || !res.Changed || !strings.Contains(res.Error, "linx_before_restore") {
		t.Fatalf("code %d, %+v", code, res)
	}
}

func TestRestoreFromADestination(t *testing.T) {
	f := &fakeRestoreHost{liveVersion: "25", loadedVersion: "25"}
	env := newRestoreEnv(t, f)
	m := env.destinationsManifest()
	if err := m.Save([]backup.Destination{{Name: "nas", Kind: backup.KindLocal, Path: "/mnt/nas/linx"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.PasswordPath("nas"), []byte("a-new-password-from-destination-add"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, code, stderr := runRestoreJSON(t, env, "--yes", "--destination", "nas", "abcdef01")
	if code != 0 || !res.OK {
		t.Fatalf("code %d, %+v, stderr %q", code, res, stderr)
	}
	if pw, _ := os.ReadFile(m.PasswordPath("nas")); string(pw) != "the-password" {
		t.Errorf("nas's password = %q, want the backup's (so later backups reach the same repository)", pw)
	}
}

func TestRestoreFromABackupFile(t *testing.T) {
	f := &fakeRestoreHost{liveVersion: "25", loadedVersion: "25"}
	env := newRestoreEnv(t, f)
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "config"), []byte("cfg"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "b.tar")
	out, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.PackRepository(repo, out); err != nil {
		t.Fatal(err)
	}
	out.Close()

	res, code, stderr := runRestoreJSON(t, env, "--yes", "--password-stdin", "--file", file)
	if code != 0 || !res.OK {
		t.Fatalf("code %d, %+v, %s", code, res, stderr)
	}
	if len(f.repos) == 0 {
		t.Fatal("restic never ran")
	}
	for _, r := range f.repos {
		if !strings.HasPrefix(r, env.workDir) || !strings.HasSuffix(r, " true") {
			t.Fatalf("restic read %s, want an unpacked repository in the work folder", r)
		}
	}
	if entries, _ := os.ReadDir(env.workDir); len(entries) != 0 {
		t.Errorf("left %d things in the work folder", len(entries))
	}
	if got := readSecret(t, env, "linx_jwt_signing_key"); got != "backup-jwt-key" {
		t.Errorf("key %q", got)
	}
}

func TestRestoreRefusesTwoSourcesAndABadFile(t *testing.T) {
	env := newRestoreEnv(t, &fakeRestoreHost{liveVersion: "25", loadedVersion: "25"})
	if res, code, _ := runRestoreJSON(t, env, "--yes", "--password-stdin", "--file", "/x.tar", "--path", "/var/backups/linx"); code != 1 || res.Changed {
		t.Fatalf("two sources: code %d %+v", code, res)
	}
	bad := filepath.Join(t.TempDir(), "bad.tar")
	_ = os.WriteFile(bad, []byte("not a backup"), 0o600)
	env.stdin = strings.NewReader("pw")
	res, code, _ := runRestoreJSON(t, env, "--yes", "--password-stdin", "--file", bad)
	if code != 1 || res.Changed || !strings.Contains(res.Error, "Linx backup file") {
		t.Fatalf("bad file: code %d %+v", code, res)
	}
}
