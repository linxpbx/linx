package certs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
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
	// Pinned are names kept at an address of their own whatever the others
	// follow: sip.<domain> at this server's home address, for desk phones
	// on the home network (Cloudflare only: DuckDNS has one address for
	// every name).
	Pinned []PinnedRecord
	Log    *slog.Logger
	Now    func() time.Time

	mu            sync.Mutex
	current       netip.Addr // what the records point at, as far as we know
	checked       time.Time  // when they were last written or confirmed
	pinnedChecked time.Time
	stats         FollowerStats
}

// PinnedRecord is a name kept at a fixed address.
type PinnedRecord struct {
	Host    string
	Address netip.Addr
}

// ParseRecords reads a record list as LINX_DNS_RECORDS and certd -records
// give it: host names (dnsname.Apex for the domain itself), each
// optionally with its own fixed address ("sip=192.168.1.212"). allowed are
// the names that may appear.
func ParseRecords(s string, allowed []string) (hosts []string, pinned []PinnedRecord, err error) {
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		host, addr, hasAddr := strings.Cut(item, "=")
		if !slices.Contains(allowed, host) {
			return nil, nil, fmt.Errorf("%q isn't one of Linx's host names %v", host, allowed)
		}
		if slices.Contains(hosts, host) || slices.ContainsFunc(pinned, func(p PinnedRecord) bool { return p.Host == host }) {
			return nil, nil, fmt.Errorf("%q is listed twice", host)
		}
		if !hasAddr {
			hosts = append(hosts, host)
			continue
		}
		a, err := netip.ParseAddr(addr)
		if err != nil || !a.Is4() || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() {
			return nil, nil, fmt.Errorf("%s: %q isn't an IPv4 address to point it at", host, addr)
		}
		pinned = append(pinned, PinnedRecord{Host: host, Address: a})
	}
	return hosts, pinned, nil
}

// errPinnedDuckDNS: DuckDNS gives every name under a domain one address.
var errPinnedDuckDNS = errors.New("DuckDNS can't give one name its own address")

// FollowerStats is what /metrics and /dns report.
type FollowerStats struct {
	Address     netip.Addr
	LastSuccess time.Time
	Failures    int
	// LastError is the last failure's reason, cleared by a success.
	LastError string
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

// checkTimeout bounds one Check: some DNS companies' libraries have no time
// limit of their own, and a hung one mustn't stop the follower for good.
const checkTimeout = 2 * time.Minute

// Check points the records at the address if it changed, or if they
// haven't been confirmed for reconfirmEvery.
func (f *Follower) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	if len(f.Hosts) > 0 {
		if err := f.checkFollowed(ctx); err != nil {
			return err
		}
	}
	return f.checkPinned(ctx)
}

// checkPinned writes or reconfirms the pinned names every reconfirmEvery.
func (f *Follower) checkPinned(ctx context.Context) error {
	if len(f.Pinned) == 0 {
		return nil
	}
	if f.Config.Provider == ProviderDuckDNS {
		f.fail(errPinnedDuckDNS)
		return errPinnedDuckDNS
	}
	f.mu.Lock()
	due := f.pinnedChecked.IsZero() || f.now().Sub(f.pinnedChecked) >= reconfirmEvery
	f.mu.Unlock()
	if !due {
		return nil
	}
	for _, p := range f.Pinned {
		res, err := f.Client.PointRecords(ctx, f.Config, []string{p.Host}, p.Address)
		for _, r := range res {
			f.Log.Info("DNS record", "name", r.Name, "result", r.Outcome)
		}
		if err != nil {
			f.fail(err)
			return err
		}
	}
	f.mu.Lock()
	f.pinnedChecked = f.now()
	f.stats.LastSuccess, f.stats.LastError = f.pinnedChecked, ""
	f.mu.Unlock()
	return nil
}

func (f *Follower) checkFollowed(ctx context.Context) error {
	ip := f.Fixed
	if !ip.IsValid() {
		var err error
		if ip, err = f.Client.PublicIPv4(ctx); err != nil {
			f.fail(err)
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
		f.fail(err)
		return err
	}
	if previous.IsValid() && previous != ip {
		f.Log.Info("this network's public address changed; DNS follows it", "from", previous, "to", ip)
	}
	if err := f.Client.State.Followed(ip.String(), f.now()); err != nil {
		f.Log.Warn("couldn't save the DNS state", "err", err)
	}
	f.mu.Lock()
	f.current, f.checked = ip, f.now()
	f.stats.Address, f.stats.LastSuccess, f.stats.LastError = ip, f.checked, ""
	f.mu.Unlock()
	return nil
}

func (f *Follower) fail(err error) {
	f.mu.Lock()
	f.stats.Failures++
	f.stats.LastError = err.Error()
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
