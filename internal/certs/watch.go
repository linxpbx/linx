package certs

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
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
	// A pre-launch review flagged that this watcher would hot-load whatever
	// it found at "current" with no check at all: only structure and
	// validity period are verified here (not the hostname it names or
	// whether it chains to Linx's own CA — those need per-service context
	// this shared watcher doesn't have), but that alone stops a directory
	// that was ever writable by more than the intended issuer from being
	// used to silently serve garbage, an expired leftover, or a
	// not-yet-valid certificate.
	if err := verifyDeployed(filepath.Join(w.Dir, v, "fullchain.pem")); err != nil {
		w.Log.Error("new certificate failed validation; not loading it", "version", v, "err", err)
		return
	}
	if err := w.Reload(ctx); err != nil {
		w.Log.Error("reloading the TLS certificate failed; retrying at the next check", "version", v, "err", err)
		return
	}
	w.Log.Info("TLS certificate reloaded", "version", v, "previous", w.loaded)
	w.loaded = v
}

// verifyDeployed parses path as a PEM certificate chain and refuses it
// unless the leaf is a well-formed certificate valid right now. It doesn't
// check the hostname it names or its issuer: this is defense against
// garbage or a stale/not-yet-valid file ending up in the watched
// directory, not a replacement for the directory being writable only by
// the intended issuer (certd, or the control plane for internal certs).
func verifyDeployed(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var leaf *x509.Certificate
	for {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("parsing certificate: %w", err)
		}
		if leaf == nil {
			leaf = c
		}
	}
	if leaf == nil {
		return errors.New("no certificate found in fullchain.pem")
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return fmt.Errorf("certificate isn't valid now (valid %s to %s)", leaf.NotBefore, leaf.NotAfter)
	}
	return nil
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
