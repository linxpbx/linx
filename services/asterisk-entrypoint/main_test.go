package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/trunkstatus"
)

func TestWaitForCertificate(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	if waitForCertificate(dir, 600*time.Millisecond, log) {
		t.Fatal("found a certificate that isn't there")
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		os.MkdirAll(filepath.Join(dir, "v1"), 0o755)
		os.WriteFile(filepath.Join(dir, "v1", "fullchain.pem"), []byte("x"), 0o644)
		os.Symlink("v1", filepath.Join(dir, "current"))
	}()
	if !waitForCertificate(dir, 5*time.Second, log) {
		t.Fatal("missed the certificate written while waiting")
	}
}

func TestTrunkWatcher(t *testing.T) {
	root := t.TempDir()
	cfg := asteriskconf.Config{ConfDir: filepath.Join(root, "conf"), TrunksDir: filepath.Join(root, "trunks"),
		SystemCAFile: filepath.Join(root, "ca.crt")}
	for _, d := range []string{cfg.ConfDir, cfg.TrunksDir} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(cfg.SystemCAFile, []byte("# public CAs\n"), 0o644)
	os.WriteFile(filepath.Join(cfg.TrunksDir, asteriskconf.TrunksFile), []byte("; none\n"), 0o644)

	reloads, fail := 0, false
	w := &trunkWatcher{cfg: cfg, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		reload: func(context.Context) error {
			if fail {
				return errors.New("busy")
			}
			reloads++
			return nil
		}}
	w.Start()
	if _, err := os.Stat(cfg.TrunkCAPath()); err != nil {
		t.Fatalf("Start didn't write the CA list: %v", err)
	}
	ctx := context.Background()
	w.Check(ctx)
	if reloads != 0 {
		t.Fatalf("reloaded with nothing changed")
	}

	os.WriteFile(filepath.Join(cfg.TrunksDir, asteriskconf.TrunksFile), []byte("[trunk-x]\ntype=aor\n"), 0o644)
	fail = true
	w.Check(ctx)
	fail = false
	w.Check(ctx) // retried after the failed reload
	w.Check(ctx)
	if reloads != 1 {
		t.Fatalf("reloads = %d, want 1", reloads)
	}

	// A new pinned certificate alone is a change too.
	os.WriteFile(filepath.Join(cfg.TrunksDir, asteriskconf.PinnedCAFile), []byte("junk"), 0o644)
	w.Check(ctx)
	if reloads != 2 {
		t.Fatalf("reloads = %d, want 2", reloads)
	}
}

func TestStatusWriter(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	answers := map[string]string{
		"pjsip show registrations": " trunk-a/sip:x  trunk-a  Registered  (exp. 3585s)\n",
		"pjsip show contacts":      "  Contact:  trunk-a/sip 6a87682040 Avail  1.0\n  Contact:  trunk-b/sip 6a87682041 Unavail  nan\n",
	}
	up := true
	w := &statusWriter{dir: dir, now: func() time.Time { return at }, log: slog.New(slog.DiscardHandler),
		console: func(_ context.Context, cmd string) (string, error) {
			if !up {
				return "", errors.New("Unable to connect to remote asterisk")
			}
			return answers[cmd], nil
		}}
	w.Write(context.Background())
	f, err := trunkstatus.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]trunkstatus.Trunk{"trunk-a": {Registration: "Registered", Contact: "Avail"}, "trunk-b": {Contact: "Unavail"}}
	if !f.WrittenAt.Equal(at) || !reflect.DeepEqual(f.Trunks, want) {
		t.Fatalf("status file = %+v", f)
	}

	// Asterisk not answering: the old file stays, and ages.
	up, at = false, at.Add(time.Minute)
	w.Write(context.Background())
	f, err = trunkstatus.Read(dir)
	if err != nil || f.WrittenAt.Equal(at) {
		t.Fatalf("status file rewritten while Asterisk was away: %+v %v", f, err)
	}
}
