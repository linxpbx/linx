package certs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

const (
	checkInterval = 24 * time.Hour
	retryMin      = time.Minute
	retryMax      = 6 * time.Hour
)

// Manager keeps the deployed certificate valid: it checks daily, renews at
// RenewBefore, tries each issuer in order and retries failures with backoff.
type Manager struct {
	Config  Config
	Store   Store
	Issuers []Issuer
	Log     *slog.Logger
	// OnDeploy runs after a new certificate is live (consumer reload hooks).
	OnDeploy func(Meta)

	now func() time.Time

	mu    sync.Mutex
	stats Stats
}

// Stats is exported as Prometheus metrics.
type Stats struct {
	NotAfter    time.Time
	Issuer      string
	LastSuccess time.Time
	Failures    uint64
}

// Snapshot returns the current stats.
func (m *Manager) Snapshot() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stats
}

func (m *Manager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// renewReason says why the deployed certificate must be replaced, or "".
func (m *Manager) renewReason(d *Deployed) string {
	switch {
	case d == nil:
		return "no certificate yet"
	case !sameNames(d.Meta.Names, m.Config.Names()):
		return "hostnames changed"
	case d.Meta.Staging != m.Config.Staging:
		if m.Config.Staging {
			return "switching to staging"
		}
		return "switching from staging to a trusted certificate"
	case d.Leaf.NotAfter.Sub(m.clock()) <= RenewBefore:
		return "expires within 30 days"
	}
	return ""
}

// Check renews the certificate if needed. It returns nil when nothing was due.
func (m *Manager) Check(ctx context.Context) error {
	cur, err := m.Store.Current()
	if err != nil {
		m.Log.Warn("deployed certificate unreadable; replacing it", "err", err)
		cur = nil
	}
	if cur != nil {
		m.record(func(s *Stats) { s.NotAfter, s.Issuer = cur.Leaf.NotAfter, cur.Meta.Issuer })
	}
	reason := m.renewReason(cur)
	if reason == "" {
		m.Log.Info("certificate is current", "names", m.Config.Names(), "issuer", cur.Meta.Issuer, "expires", cur.Leaf.NotAfter)
		return nil
	}
	m.Log.Info("requesting certificate", "reason", reason, "names", m.Config.Names())

	var errs []error
	for _, iss := range m.Issuers {
		if err := ctx.Err(); err != nil {
			return err
		}
		issued, err := iss.Obtain(ctx, m.Config.Names())
		if err != nil {
			m.Log.Warn("certificate authority failed", "issuer", iss.ID(), "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", iss.ID(), err))
			continue
		}
		return m.deploy(iss.ID(), issued)
	}
	m.record(func(s *Stats) { s.Failures++ })
	return errors.Join(errs...)
}

func (m *Manager) deploy(issuer string, issued *Issued) error {
	leaf, err := parseLeaf(issued.Chain)
	if err != nil {
		return err
	}
	meta := Meta{
		Names:    m.Config.Names(),
		Issuer:   issuer,
		Staging:  m.Config.Staging,
		NotAfter: leaf.NotAfter,
		IssuedAt: m.clock().UTC(),
	}
	if err := m.Store.Deploy(issued.Chain, issued.Key, meta); err != nil {
		m.record(func(s *Stats) { s.Failures++ })
		return fmt.Errorf("deploying certificate: %w", err)
	}
	m.record(func(s *Stats) { s.NotAfter, s.Issuer, s.LastSuccess = leaf.NotAfter, issuer, meta.IssuedAt })
	m.Log.Info("certificate deployed", "issuer", issuer, "names", meta.Names, "expires", leaf.NotAfter)
	if m.OnDeploy != nil {
		m.OnDeploy(meta)
	}
	return nil
}

func (m *Manager) record(f func(*Stats)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f(&m.stats)
}

// Run checks now, then daily, retrying failures with exponential backoff
// (1 min doubling to 6 h, ±10% jitter), until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	failures := 0
	for {
		wait := checkInterval
		if err := m.Check(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			wait = Backoff(failures)
			m.Log.Error("certificate renewal failed", "err", err, "retry_in", wait.Round(time.Second))
		} else {
			failures = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter(wait)):
		}
	}
}

// Backoff is the wait before retry n (n ≥ 1).
func Backoff(n int) time.Duration {
	d := retryMin
	for i := 1; i < n && d < retryMax; i++ {
		d *= 2
	}
	return min(d, retryMax)
}

func jitter(d time.Duration) time.Duration {
	return d + time.Duration((rand.Float64()*0.2-0.1)*float64(d))
}
