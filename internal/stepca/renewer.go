package stepca

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"
)

// Issuer is what a Renewer needs from a Client (a fake in tests).
type Issuer interface {
	Issue(ctx context.Context, commonName string, dnsNames []string) (*tls.Certificate, error)
}

// Renewer keeps one service certificate current. Until the first certificate
// arrives, GetCertificate fails, so TLS handshakes fail closed rather than
// fall back to anything.
type Renewer struct {
	Issuer     Issuer
	CommonName string
	DNSNames   []string
	Log        *slog.Logger
	Now        func() time.Time
	// OnChange, if set, is called after each new certificate.
	OnChange func(*tls.Certificate)

	cert atomic.Pointer[tls.Certificate]
}

// retryMin and retryMax bound the wait between failed attempts.
const (
	retryMin = 5 * time.Second
	retryMax = 5 * time.Minute
)

// Run issues a certificate at once, then renews it at two-thirds of its
// lifetime until ctx ends. Failures retry with backoff; a certificate that
// is still valid keeps being served meanwhile.
func (r *Renewer) Run(ctx context.Context) {
	now := r.Now
	if now == nil {
		now = time.Now
	}
	backoff := retryMin
	for {
		wait := r.renewIn(now())
		if wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		cert, err := r.Issuer.Issue(ctx, r.CommonName, r.DNSNames)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			r.Log.Error("service certificate", "name", r.CommonName, "err", err, "retry_in", backoff.String())
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, retryMax)
			continue
		}
		backoff = retryMin
		r.cert.Store(cert)
		r.Log.Info("service certificate issued", "name", r.CommonName, "not_after", cert.Leaf.NotAfter)
		if r.OnChange != nil {
			r.OnChange(cert)
		}
	}
}

// renewIn is how long until the current certificate is due for renewal:
// zero when there's none yet or it's past two-thirds of its lifetime.
func (r *Renewer) renewIn(now time.Time) time.Duration {
	c := r.cert.Load()
	if c == nil {
		return 0
	}
	life := c.Leaf.NotAfter.Sub(c.Leaf.NotBefore)
	due := c.Leaf.NotBefore.Add(life * 2 / 3)
	return max(due.Sub(now), 0)
}

// GetCertificate is for tls.Config.
func (r *Renewer) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c := r.cert.Load()
	if c == nil {
		return nil, errors.New("no service certificate yet (internal CA unreachable?)")
	}
	return c, nil
}

// Current returns the certificate being served, or nil.
func (r *Renewer) Current() *tls.Certificate { return r.cert.Load() }
