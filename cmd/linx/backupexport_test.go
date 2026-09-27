package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/backup"
)

func TestBackupExportWritesTheLocalRepository(t *testing.T) {
	env, r, _ := setupBackupEnv(t)
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "config"), []byte("cfg"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := env.destinationsManifest()
	if err := m.Save([]backup.Destination{
		{Name: "nas", Kind: backup.KindSFTP, Host: "nas", User: "u", RemotePath: "/b"},
		{Name: "disk2", Kind: backup.KindLocal, Path: repo},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.PasswordPath("disk2"), []byte("pw-disk2"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.stdout = map[int]string{0: `[{"id":"abcdef1234","time":"2026-09-27T10:00:00Z","tags":["pair:p1"]}]`}
	out := filepath.Join(t.TempDir(), "b.tar")
	var stdout, stderr bytes.Buffer
	if code := runBackup(t.Context(), []string{"export", "--json", "--out", out}, &stdout, &stderr, env); code != 0 {
		t.Fatalf("code %d: %s %s", code, stdout.String(), stderr.String())
	}
	var res exportResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Destination != "disk2" || res.Password != "pw-disk2" || res.SnapshotID != "abcdef1234" || res.Size == 0 {
		t.Fatalf("result %+v", res)
	}
	if !strings.Contains(strings.Join(r.calls[0], " "), "-r "+repo) {
		t.Errorf("looked up the snapshot in %v", r.calls[0])
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := backup.UnpackRepository(f, t.TempDir()); err != nil {
		t.Fatalf("the file doesn't unpack: %v", err)
	}
}

func TestBackupExportRefusals(t *testing.T) {
	env, _, _ := setupBackupEnv(t)
	out := filepath.Join(t.TempDir(), "b.tar")
	var stdout, stderr bytes.Buffer

	// Only a remote destination: nothing on this server to download.
	if err := env.destinationsManifest().Save([]backup.Destination{{Name: "nas", Kind: backup.KindSFTP}}); err != nil {
		t.Fatal(err)
	}
	if code := runBackup(t.Context(), []string{"export", "--out", out}, &stdout, &stderr, env); code != 1 ||
		!strings.Contains(stderr.String(), "nothing here to download") {
		t.Fatalf("code %d: %s", code, stderr.String())
	}

	stderr.Reset()
	env.isRoot = false
	if code := runBackup(t.Context(), []string{"export", "--out", out}, &stdout, &stderr, env); code != 1 {
		t.Fatalf("ran without root: %d", code)
	}
	if code := runBackup(t.Context(), []string{"export"}, &stdout, &stderr, env); code != 2 {
		t.Fatalf("ran without --out: %d", code)
	}
}
