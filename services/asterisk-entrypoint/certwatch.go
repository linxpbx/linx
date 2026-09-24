package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// certWatcher reloads Asterisk's TLS certificate when linx-certd deploys a
// new one (docs/ops/CERT_RELOAD.md). certd writes each certificate to a new
// directory and swaps the "current" symlink to it, so the symlink's target
// names the deployed version. Asterisk reads current/{fullchain,privkey}.pem
// only when res_pjsip loads; "module reload res_pjsip.so" makes it read them
// again without dropping connected phones or calls (checked against the
// real image; PJSIP's transport stays up and only its certificate changes).
type certWatcher struct {
	certsDir string
	reload   func(context.Context) error
	log      *slog.Logger

	loaded string // the version Asterisk has loaded
}

// version is the deployed certificate's version: the current symlink's
// target, or "" if there's no certificate yet.
func (w *certWatcher) version() string {
	v, err := os.Readlink(filepath.Join(w.certsDir, "current"))
	if err != nil {
		return ""
	}
	return v
}

// start records the version Asterisk is about to load. Called before
// Asterisk starts, so a certificate deployed while it's starting is seen as
// new by the next check (a spare reload, never a missed one).
func (w *certWatcher) start() { w.loaded = w.version() }

// check reloads if a different certificate is deployed. A failed reload is
// retried at the next check.
func (w *certWatcher) check(ctx context.Context) {
	v := w.version()
	if v == "" || v == w.loaded {
		return
	}
	if err := w.reload(ctx); err != nil {
		w.log.Error("reloading the TLS certificate failed; retrying at the next check", "version", v, "err", err)
		return
	}
	w.log.Info("TLS certificate reloaded", "version", v, "previous", w.loaded)
	w.loaded = v
}

// run checks every interval until ctx ends.
func (w *certWatcher) run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.check(ctx)
		}
	}
}
