package certs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcher(t *testing.T) {
	dir := t.TempDir()
	deploy := func(v string) {
		t.Helper()
		// Like linx-certd: a new version directory with its own
		// currently-valid certificate, then a new symlink renamed over
		// current.
		if err := os.Mkdir(filepath.Join(dir, v), 0o700); err != nil && !os.IsExist(err) {
			t.Fatal(err)
		}
		chain, _ := selfSigned(t, "sip.example.com", time.Now().Add(24*time.Hour))
		if err := os.WriteFile(filepath.Join(dir, v, "fullchain.pem"), chain, 0o600); err != nil {
			t.Fatal(err)
		}
		tmp := filepath.Join(dir, "current.tmp")
		if err := os.Symlink(v, tmp); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(dir, "current")); err != nil {
			t.Fatal(err)
		}
	}
	reloads := 0
	var fail error
	w := &Watcher{Dir: dir, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Reload: func(context.Context) error {
			if fail != nil {
				return fail
			}
			reloads++
			return nil
		}}
	ctx := context.Background()

	// No certificate yet: nothing to do, now or once one arrives at start.
	w.Start()
	w.Check(ctx)
	if reloads != 0 {
		t.Fatalf("reloaded with no certificate: %d", reloads)
	}

	deploy("v1")
	w.Start() // Asterisk starts with v1
	w.Check(ctx)
	if reloads != 0 {
		t.Fatalf("reloaded an unchanged certificate: %d", reloads)
	}

	deploy("v2")
	fail = errors.New("console not answering")
	w.Check(ctx)
	if reloads != 0 || w.loaded != "v1" {
		t.Fatalf("a failed reload counted as done: %d, %q", reloads, w.loaded)
	}
	fail = nil
	w.Check(ctx) // retried
	w.Check(ctx) // and only once
	if reloads != 1 || w.loaded != "v2" {
		t.Fatalf("after renewal: %d reloads, loaded %q", reloads, w.loaded)
	}
}

// A service that started before its first certificate existed picks it up
// within seconds, not a whole interval.
func TestWatcherFirstCertificateSoon(t *testing.T) {
	dir := t.TempDir()
	reloaded := make(chan struct{}, 1)
	w := &Watcher{Dir: dir, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Reload: func(context.Context) error { reloaded <- struct{}{}; return nil }}
	w.Start()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx, time.Hour)
	if err := os.Mkdir(filepath.Join(dir, "v1"), 0o700); err != nil {
		t.Fatal(err)
	}
	chain, _ := selfSigned(t, "sip.example.com", time.Now().Add(24*time.Hour))
	if err := os.WriteFile(filepath.Join(dir, "v1", "fullchain.pem"), chain, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("v1", filepath.Join(dir, "current")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reloaded:
	case <-time.After(firstCertCheck + 3*time.Second):
		t.Fatal("the first certificate wasn't picked up within seconds")
	}
}

// TestWatcherRefusesAnInvalidCertificate is the fix for a pre-launch audit
// finding: the watcher used to reload whatever it found at "current" with
// no check at all. Garbage and an expired certificate must both be
// refused, without ever calling Reload.
func TestWatcherRefusesAnInvalidCertificate(t *testing.T) {
	dir := t.TempDir()
	reloads := 0
	w := &Watcher{Dir: dir, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Reload: func(context.Context) error { reloads++; return nil }}
	ctx := context.Background()
	w.Start()

	deploySymlink := func(v string) {
		t.Helper()
		tmp := filepath.Join(dir, "current.tmp")
		if err := os.Symlink(v, tmp); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(dir, "current")); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.Mkdir(filepath.Join(dir, "garbage"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "garbage", "fullchain.pem"), []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	deploySymlink("garbage")
	w.Check(ctx)
	if reloads != 0 || w.loaded != "" {
		t.Fatalf("garbage was reloaded: %d reloads, loaded %q", reloads, w.loaded)
	}

	if err := os.Mkdir(filepath.Join(dir, "expired"), 0o700); err != nil {
		t.Fatal(err)
	}
	chain, _ := selfSigned(t, "sip.example.com", time.Now().Add(-24*time.Hour))
	if err := os.WriteFile(filepath.Join(dir, "expired", "fullchain.pem"), chain, 0o600); err != nil {
		t.Fatal(err)
	}
	deploySymlink("expired")
	w.Check(ctx)
	if reloads != 0 || w.loaded != "" {
		t.Fatalf("an expired certificate was reloaded: %d reloads, loaded %q", reloads, w.loaded)
	}
}
