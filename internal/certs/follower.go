package certs

import (
	"context"
	"log/slog"
	"net/netip"
	"sync"
	"time"
)

// Follower keeps the front door's public names pointed at this network's
// public address as it changes (a home connection's address usually does),
// or at a fixed address (home-only: this server's). linx-certd runs it when
// LINX_DNS_RECORDS is set (docs/WEB.md §3).
type Follower struct {
	Client *RecordsClient
	Config Config
	Hosts  []string
	// Fixed, if valid, is the address to point at, instead of the public one.
	Fixed netip.Addr
	Log   *slog.Logger
	Now   func() time.Time

	mu      sync.Mutex
	current netip.Addr // what the records point at, as far as we know
	checked time.Time  // when they were last written or confirmed
	stats   FollowerStats
}

// FollowerStats is what /metrics reports.
type FollowerStats struct {
	Address     netip.Addr
	LastSuccess time.Time
	Failures    int
}

const (
	// followEvery is how often the public address is looked at: one small
	// HTTPS request.
	followEvery = 5 * time.Minute
	// reconfirmEvery re-reads the records even if the address hasn't
	// changed, in case someone edited them.
	reconfirmEvery = 6 * time.Hour
	// followRetry is the wait after a failure.
	followRetry = time.Minute
)

func (f *Follower) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Check points the records at the address if it changed, or if they
// haven't been confirmed for reconfirmEvery.
func (f *Follower) Check(ctx context.Context) error {
	ip := f.Fixed
	if !ip.IsValid() {
		var err error
		if ip, err = f.Client.PublicIPv4(ctx); err != nil {
			f.fail()
			return err
		}
	}
	f.mu.Lock()
	unchanged := ip == f.current && f.now().Sub(f.checked) < reconfirmEvery
	previous := f.current
	f.mu.Unlock()
	if unchanged {
		return nil
	}
	res, err := f.Client.PointRecords(ctx, f.Config, f.Hosts, ip)
	for _, r := range res {
		f.Log.Info("DNS record", "name", r.Name, "result", r.Outcome)
	}
	if err != nil {
		f.fail()
		return err
	}
	if previous.IsValid() && previous != ip {
		f.Log.Info("this network's public address changed; DNS follows it", "from", previous, "to", ip)
	}
	f.mu.Lock()
	f.current, f.checked = ip, f.now()
	f.stats.Address, f.stats.LastSuccess = ip, f.checked
	f.mu.Unlock()
	return nil
}

func (f *Follower) fail() {
	f.mu.Lock()
	f.stats.Failures++
	f.mu.Unlock()
}

// Snapshot is the follower's stats.
func (f *Follower) Snapshot() FollowerStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats
}

// Run checks now and then every followEvery (followRetry after a failure)
// until ctx ends.
func (f *Follower) Run(ctx context.Context) {
	for {
		wait := followEvery
		if err := f.Check(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			f.Log.Error("keeping the DNS records up to date failed; retrying", "err", err)
			wait = followRetry
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}
