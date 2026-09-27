package main

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupDestinationEnv(t *testing.T) backupEnv {
	t.Helper()
	env, _, _ := setupBackupEnv(t)
	env.lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host == "nas.lan" {
			return []netip.Addr{netip.MustParseAddr("192.168.1.50")}, nil
		}
		if host == "own.test" {
			return []netip.Addr{netip.MustParseAddr("172.20.0.5")}, nil
		}
		return nil, errors.New("no such host")
	}
	env.ownNetworks = func() ([]netip.Prefix, error) {
		return []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16")}, nil
	}
	return env
}

func TestBackupDestinationAddLocal(t *testing.T) {
	env := setupDestinationEnv(t)
	var out, errb bytes.Buffer
	code := runBackup(t.Context(), []string{"destination", "add", "--kind", "local", "--path", "/mnt/usb/linx-backups", "office"}, &out, &errb, env)
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "office") {
		t.Fatalf("output missing the destination name: %q", out.String())
	}
	dests, err := env.destinationsManifest().Load()
	if err != nil || len(dests) != 1 || dests[0].Name != "office" || dests[0].Path != "/mnt/usb/linx-backups" {
		t.Fatalf("Load() = %v, %v", dests, err)
	}
	if _, err := os.Stat(env.destinationsManifest().PasswordPath("office")); err != nil {
		t.Fatalf("password file not written: %v", err)
	}
}

func TestBackupDestinationAddSFTPGeneratesKeyAndRefusesOwnNetwork(t *testing.T) {
	env := setupDestinationEnv(t)
	var out, errb bytes.Buffer
	code := runBackup(t.Context(), []string{"destination", "add", "--kind", "sftp", "--host", "nas.lan", "--user", "linx", "--remote-path", "backups/linx", "nas"}, &out, &errb, env)
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "ssh-ed25519") {
		t.Fatalf("public key not shown: %q", out.String())
	}
	m := env.destinationsManifest()
	if _, err := os.Stat(m.SFTPKeyPath("nas")); err != nil {
		t.Fatalf("private key not written: %v", err)
	}
	pub, err := os.ReadFile(m.SFTPPublicKeyPath("nas"))
	if err != nil || !strings.HasPrefix(string(pub), "ssh-ed25519") {
		t.Fatalf("public key file = %q, %v", pub, err)
	}

	// Refused: nas at one of Linx's own networks.
	out.Reset()
	errb.Reset()
	code = runBackup(t.Context(), []string{"destination", "add", "--kind", "sftp", "--host", "own.test", "--user", "linx", "--remote-path", "x", "bad"}, &out, &errb, env)
	if code == 0 || !strings.Contains(errb.String(), "backup destination") {
		t.Fatalf("code %d, stderr %q, want a refusal", code, errb.String())
	}
}

func TestBackupDestinationAddS3SealsCredentials(t *testing.T) {
	env := setupDestinationEnv(t)
	var out, errb bytes.Buffer
	code := runBackup(t.Context(), []string{"destination", "add", "--kind", "s3",
		"--endpoint", "https://nas.lan:9000", "--bucket", "linx", "--access-key-id", "AKID", "--secret-access-key", "SECRET", "b2"}, &out, &errb, env)
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
	if strings.Contains(out.String(), "SECRET") {
		t.Fatal("the secret access key must never be printed")
	}
	d, found, err := env.destinationsManifest().Find("b2")
	if err != nil || !found {
		t.Fatalf("Find(b2) = %v, %v, %v", d, found, err)
	}
	target, err := env.destinationsManifest().Target(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(target.Env) == 0 {
		t.Fatal("S3 credentials weren't threaded into the Target's Env")
	}
}

func TestBackupDestinationAddDuplicateNameFails(t *testing.T) {
	env := setupDestinationEnv(t)
	var out, errb bytes.Buffer
	runBackup(t.Context(), []string{"destination", "add", "--kind", "local", "--path", "/a", "office"}, &out, &errb, env)
	out.Reset()
	errb.Reset()
	code := runBackup(t.Context(), []string{"destination", "add", "--kind", "local", "--path", "/b", "office"}, &out, &errb, env)
	if code == 0 {
		t.Fatal("adding a duplicate name should fail")
	}
}

func TestBackupDestinationListAndRemove(t *testing.T) {
	env := setupDestinationEnv(t)
	var out, errb bytes.Buffer
	runBackup(t.Context(), []string{"destination", "add", "--kind", "local", "--path", "/a", "office"}, &out, &errb, env)

	out.Reset()
	if code := runBackup(t.Context(), []string{"destination", "list"}, &out, &errb, env); code != 0 || !strings.Contains(out.String(), "office") {
		t.Fatalf("code %d, out %q, stderr %q", code, out.String(), errb.String())
	}

	out.Reset()
	errb.Reset()
	if code := runBackup(t.Context(), []string{"destination", "remove", "office"}, &out, &errb, env); code != 0 {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
	dests, err := env.destinationsManifest().Load()
	if err != nil || len(dests) != 0 {
		t.Fatalf("Load() after remove = %v, %v", dests, err)
	}
	if _, err := os.Stat(env.destinationsManifest().PasswordPath("office")); !os.IsNotExist(err) {
		t.Fatalf("password file should be removed too: %v", err)
	}

	out.Reset()
	errb.Reset()
	if code := runBackup(t.Context(), []string{"destination", "remove", "office"}, &out, &errb, env); code == 0 {
		t.Fatal("removing an unknown destination should fail")
	}
}

func TestBackupDestinationNotRoot(t *testing.T) {
	env := setupDestinationEnv(t)
	env.isRoot = false
	var out, errb bytes.Buffer
	if code := runBackup(t.Context(), []string{"destination", "list"}, &out, &errb, env); code != 1 || !strings.Contains(errb.String(), "root") {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
}

func TestBackupRunsAgainstAConfiguredDestination(t *testing.T) {
	env := setupDestinationEnv(t)
	var out, errb bytes.Buffer
	runBackup(t.Context(), []string{"destination", "add", "--kind", "local", "--path", filepath.Join(t.TempDir(), "repo"), "office"}, &out, &errb, env)

	// Re-fetch the fake runner isn't exported from setupBackupEnv's return,
	// so build a fresh one wired to record calls for this run.
	r := &fakeBackupRunner{stdout: map[int]string{2: `{"message_type":"summary","snapshot_id":"snap1"}` + "\n"}, fail: map[int]error{}}
	env.run = r.run

	out.Reset()
	errb.Reset()
	code := runBackup(t.Context(), nil, &out, &errb, env)
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "office") || !strings.Contains(out.String(), "snap1") {
		t.Fatalf("output missing destination/snapshot: %q", out.String())
	}
	// The destination's own password was already generated at add time, so a
	// plain run must not show it again.
	pw, err := os.ReadFile(env.destinationsManifest().PasswordPath("office"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), string(pw)) {
		t.Fatal("the password shouldn't be shown again on a normal backup run")
	}
}
