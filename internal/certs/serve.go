package certs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ServingCert gives a TLS server the deployed certificate (Store layout)
// and picks up a renewed one on the next handshake after the "current"
// symlink moves, so a Go server needs no reload step (docs/ops/CERT_RELOAD.md).
type ServingCert struct {
	Dir string

	mu      sync.Mutex
	version string
	cert    *tls.Certificate
}

// ErrNoCertificate means nothing is deployed yet (linx-certd hasn't got the
// first certificate). Handshakes fail until it has.
var ErrNoCertificate = errors.New("no TLS certificate deployed yet")

// GetCertificate is a tls.Config.GetCertificate.
func (s *ServingCert) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return s.Current()
}

// Current returns the deployed certificate, loading it if the current
// symlink points somewhere new. A failed load keeps serving the previous
// certificate (a half-finished deploy can't happen: Deploy writes the
// version first and swaps the symlink last).
func (s *ServingCert) Current() (*tls.Certificate, error) {
	v, err := os.Readlink(filepath.Join(s.Dir, CurrentLink))
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		if s.cert != nil {
			return s.cert, nil
		}
		return nil, ErrNoCertificate
	}
	if v == s.version && s.cert != nil {
		return s.cert, nil
	}
	dir := filepath.Join(s.Dir, CurrentLink)
	c, err := tls.LoadX509KeyPair(filepath.Join(dir, FullchainFile), filepath.Join(dir, PrivkeyFile))
	if err != nil {
		if s.cert != nil {
			return s.cert, nil
		}
		return nil, err
	}
	s.version, s.cert = v, &c
	return s.cert, nil
}

// ServesCurrent completes a TLS handshake with addr that trusts only the
// certificate deployed in dir itself (as its own root), so it succeeds only
// if that exact certificate is served, valid for name. Health checks use it
// to catch a service still serving a certificate from before a renewal.
func ServesCurrent(ctx context.Context, addr, name, dir string) error {
	b, err := os.ReadFile(filepath.Join(dir, CurrentLink, FullchainFile))
	if err != nil {
		return err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return ErrNoCertificate
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	d := tls.Dialer{Config: &tls.Config{RootCAs: roots, ServerName: name, MinVersion: tls.VersionTLS12}}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("doesn't serve the current certificate: %w", err)
	}
	return c.Close()
}
