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
		// Like linx-certd: a new symlink renamed over current.
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
	if err := os.Symlink("v1", filepath.Join(dir, "current")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reloaded:
	case <-time.After(firstCertCheck + 3*time.Second):
		t.Fatal("the first certificate wasn't picked up within seconds")
	}
}
