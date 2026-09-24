package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestCertWatcher(t *testing.T) {
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
	w := &certWatcher{certsDir: dir, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		reload: func(context.Context) error {
			if fail != nil {
				return fail
			}
			reloads++
			return nil
		}}
	ctx := context.Background()

	// No certificate yet: nothing to do, now or once one arrives at start.
	w.start()
	w.check(ctx)
	if reloads != 0 {
		t.Fatalf("reloaded with no certificate: %d", reloads)
	}

	deploy("v1")
	w.start() // Asterisk starts with v1
	w.check(ctx)
	if reloads != 0 {
		t.Fatalf("reloaded an unchanged certificate: %d", reloads)
	}

	deploy("v2")
	fail = errors.New("console not answering")
	w.check(ctx)
	if reloads != 0 || w.loaded != "v1" {
		t.Fatalf("a failed reload counted as done: %d, %q", reloads, w.loaded)
	}
	fail = nil
	w.check(ctx) // retried
	w.check(ctx) // and only once
	if reloads != 1 || w.loaded != "v2" {
		t.Fatalf("after renewal: %d reloads, loaded %q", reloads, w.loaded)
	}
}
