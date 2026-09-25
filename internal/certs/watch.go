package certs

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Watcher makes a service that reads current/{fullchain,privkey}.pem only
// at startup (Asterisk, coturn) reload them when a new certificate is
// deployed (docs/ops/CERT_RELOAD.md). linx-certd, and the control plane
// for internal certificates, write each certificate to a new directory and
// swap the "current" symlink to it (Store.Deploy), so the symlink's target
// names the deployed version.
type Watcher struct {
	Dir    string
	Reload func(context.Context) error
	Log    *slog.Logger

	loaded string // the version the service has loaded
}

// version is the deployed certificate's version: the current symlink's
// target, or "" if there's no certificate yet.
func (w *Watcher) version() string {
	v, err := os.Readlink(filepath.Join(w.Dir, "current"))
	if err != nil {
		return ""
	}
	return v
}

// Start records the version the service is about to load. Call it before
// the service starts, so a certificate deployed while it's starting is seen
// as new by the next check (a spare reload, never a missed one).
func (w *Watcher) Start() { w.loaded = w.version() }

// Loaded is the version the service last loaded.
func (w *Watcher) Loaded() string { return w.loaded }

// Check reloads if a different certificate is deployed. A failed reload is
// retried at the next check.
func (w *Watcher) Check(ctx context.Context) {
	v := w.version()
	if v == "" || v == w.loaded {
		return
	}
	if err := w.Reload(ctx); err != nil {
		w.Log.Error("reloading the TLS certificate failed; retrying at the next check", "version", v, "err", err)
		return
	}
	w.Log.Info("TLS certificate reloaded", "version", v, "previous", w.loaded)
	w.loaded = v
}

// firstCertCheck is how often Run checks while the service has no
// certificate loaded yet: a service that started before its certificate was
// written (Asterisk's browser websocket, whose certificate the control plane
// issues at its own start) gets it within seconds, not a whole interval.
const firstCertCheck = 2 * time.Second

// Run checks every interval (every firstCertCheck until a certificate is
// loaded) until ctx ends.
func (w *Watcher) Run(ctx context.Context, interval time.Duration) {
	for {
		wait := interval
		if w.loaded == "" && firstCertCheck < wait {
			wait = firstCertCheck
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
			w.Check(ctx)
		}
	}
}
