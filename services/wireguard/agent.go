package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"linxpbx.com/linx/internal/wgconf"
)

// Config is where the agent reads and writes, and how often.
type Config struct {
	ConfigDir, StatusDir string
	Interval             time.Duration
	// BootFile is where Asterisk writes the value of its own start
	// (services/asterisk-entrypoint). It is how this agent knows Asterisk
	// is running again before it exits; "" or "none" to do without it.
	BootFile string
	// OrphanGrace is how long to wait before exiting when there is no boot
	// file to read (zero: defaultOrphanGrace).
	OrphanGrace time.Duration
}

// defaultOrphanGrace is the wait when Asterisk writes no boot file -- an
// image older than this agent. Long enough for a container to come back,
// short enough that a tunnel isn't down for long.
const defaultOrphanGrace = 20 * time.Second

// Link is a network interface in the namespace.
type Link struct {
	Name string
	// Type is the kernel's link kind ("veth", "wireguard", ...).
	Type     string
	Prefixes []netip.Prefix
}

// Device is a WireGuard interface's peer state.
type Device struct {
	LastHandshake    time.Time // zero: never
	RxBytes, TxBytes int64
}

// Net is the kernel side (kernel_linux.go); tests use a fake.
type Net interface {
	Links() ([]Link, error)
	// ApplyTunnel creates or updates t's interface: keys, its one peer
	// with exactly allowed, its address, and a route into it for each of
	// allowed in wgconf.RouteTable.
	ApplyTunnel(t wgconf.Tunnel, allowed []netip.Addr) error
	RemoveTunnel(name string) error
	// EnsureRule makes sure every packet not marked wgconf.FirewallMark
	// looks up wgconf.RouteTable first.
	EnsureRule() error
	Device(name string) (Device, error)
	Close() error
}

// errOrphaned: this namespace has lost Asterisk (it restarted).
var errOrphaned = errors.New("namespace orphaned")

// Agent applies the rendered tunnels and reports on them.
type Agent struct {
	Config Config
	Net    Net
	Now    func() time.Time
	Log    *slog.Logger

	// bootWas is what Asterisk's boot file said when this agent started,
	// so a change in it means Asterisk has started again.
	bootWas string

	// applied is each interface's last applied tunnel (a hash), so an
	// unchanged tunnel isn't touched: reconfiguring a peer can drop its
	// session.
	applied map[string][32]byte
	upSince map[string]time.Time
	errs    map[string]string
	skipped map[string][]string
	lastErr string
}

// Run applies and reports every Interval until ctx ends, or returns
// errOrphaned when Asterisk's namespace is gone.
func (a *Agent) Run(ctx context.Context) error {
	a.bootWas = a.asteriskBoot()
	t := time.NewTicker(a.Config.Interval)
	defer t.Stop()
	for {
		if err := a.Tick(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Tick is one round: check the namespace, apply, report.
func (a *Agent) Tick() error {
	if a.applied == nil {
		a.applied, a.upSince, a.errs, a.skipped = map[string][32]byte{}, map[string]time.Time{}, map[string]string{}, map[string][]string{}
	}
	links, err := a.Net.Links()
	if err != nil {
		return fmt.Errorf("listing interfaces: %w", err)
	}
	if orphaned(links) {
		return errOrphaned
	}
	status := wgconf.Status{Tunnels: map[string]wgconf.TunnelStatus{}}
	cfg, err := wgconf.ReadConfig(a.Config.ConfigDir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		status.Error = "Waiting for the control plane to send the tunnels."
		a.report(status)
		return nil
	case err != nil:
		// Keep what's up: a broken file must not take working tunnels down.
		status.Error = "Can't read the tunnels the control plane sent: " + err.Error()
		if status.Error != a.lastErr {
			a.Log.Error("reading the tunnels", "err", err)
		}
		a.report(status)
		return nil
	}

	local := localPrefixes(links)
	want := map[string]bool{}
	for _, t := range cfg.Tunnels {
		want[t.Interface] = true
		allowed, skipped := splitAllowed(t, local)
		h := tunnelHash(t, allowed)
		exists := slices.ContainsFunc(links, func(l Link) bool { return l.Name == t.Interface })
		if a.applied[t.Interface] != h || !exists {
			if err := a.Net.ApplyTunnel(t, allowed); err != nil {
				msg := describe(err)
				if a.errs[t.Interface] != msg {
					a.Log.Error("bringing a tunnel up", "profile", t.ID, "name", t.Name, "interface", t.Interface, "err", err)
				}
				a.errs[t.Interface] = msg
				delete(a.applied, t.Interface)
			} else {
				a.Log.Info("tunnel applied", "profile", t.ID, "name", t.Name, "interface", t.Interface,
					"endpoint", t.Endpoint, "addresses", allowed)
				a.applied[t.Interface] = h
				a.upSince[t.Interface] = a.Now().UTC()
				delete(a.errs, t.Interface)
				for _, s := range skipped {
					a.Log.Warn("an address left out of a tunnel", "profile", t.ID, "why", s)
				}
			}
			a.skipped[t.Interface] = skipped
		}
	}
	for _, l := range links {
		if strings.HasPrefix(l.Name, wgconf.InterfacePrefix) && !want[l.Name] {
			if err := a.Net.RemoveTunnel(l.Name); err != nil {
				a.Log.Error("removing a tunnel no profile names", "interface", l.Name, "err", err)
			} else {
				a.Log.Info("tunnel removed", "interface", l.Name)
			}
		}
	}
	for name := range a.applied {
		if !want[name] {
			delete(a.applied, name)
			delete(a.upSince, name)
			delete(a.skipped, name)
		}
	}
	if len(cfg.Tunnels) > 0 {
		if err := a.Net.EnsureRule(); err != nil {
			status.Error = "Can't route trunks into their tunnels: " + describe(err)
			if status.Error != a.lastErr {
				a.Log.Error("adding the tunnels' routing rule", "err", err)
			}
		}
	}

	for _, t := range cfg.Tunnels {
		ts := wgconf.TunnelStatus{Interface: t.Interface, UpSince: a.upSince[t.Interface],
			Error: a.errs[t.Interface], Skipped: a.skipped[t.Interface]}
		if ts.Error == "" {
			d, err := a.Net.Device(t.Interface)
			if err != nil {
				ts.Error = "Can't read the tunnel's state: " + describe(err)
			} else {
				if !d.LastHandshake.IsZero() {
					hs := d.LastHandshake.UTC()
					ts.LastHandshake = &hs
				}
				ts.RxBytes, ts.TxBytes = d.RxBytes, d.TxBytes
			}
		}
		status.Tunnels[t.ID.String()] = ts
	}
	a.report(status)
	return nil
}

func (a *Agent) report(s wgconf.Status) {
	s.WrittenAt = a.Now().UTC()
	if err := wgconf.WriteStatus(a.Config.StatusDir, s); err != nil && err.Error() != a.lastErr {
		a.Log.Error("writing the tunnels' state", "err", err)
	}
	a.lastErr = s.Error
}

// asteriskBoot is what Asterisk said about its own start, or "" when
// there is nothing to read (no file, no mount, or an older image).
func (a *Agent) asteriskBoot() string {
	if a.Config.BootFile == "" || a.Config.BootFile == "none" {
		return ""
	}
	b, err := os.ReadFile(a.Config.BootFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// WaitForAsteriskBack blocks until Asterisk has started again (its boot
// file says something new), so the exit that follows lands while its
// container is running.
//
// It exists because Docker tries **once**. When Asterisk restarts, this
// agent is left holding a namespace with no network card and has to exit so
// Docker can start it in the new one -- but if Asterisk isn't running at
// that moment, the attempt fails with "cannot join network namespace of a
// non running container" and the container stays exited for good, with
// every WireGuard trunk down until somebody starts it by hand. Every
// restart policy behaves that way; it was proved with plain containers on
// 2026-10-23, and it is what the call suite's restart scenario had been
// catching intermittently since 2026-10-19.
//
// With no boot file to read (an Asterisk image older than this agent) it
// waits OrphanGrace instead and exits anyway: no worse than before, and
// usually long enough. If Asterisk never comes back it waits for ever,
// which is right -- exiting would be the end of this container, while
// waiting costs nothing and recovers the moment Asterisk returns.
func (a *Agent) WaitForAsteriskBack(ctx context.Context) {
	a.report(wgconf.Status{Tunnels: map[string]wgconf.TunnelStatus{},
		Error: "Asterisk restarted; waiting for its network to come back."})
	grace := a.Config.OrphanGrace
	if grace <= 0 {
		grace = defaultOrphanGrace
	}
	until := a.Now().Add(grace)
	every := a.Config.Interval
	if every <= 0 || every > time.Second {
		every = time.Second
	}
	for {
		if boot := a.asteriskBoot(); boot != "" && boot != a.bootWas {
			return
		}
		if a.bootWas == "" && !a.Now().Before(until) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// orphaned: Asterisk's container always has a network card (a veth) on
// its Docker networks. When it restarts, Docker gives it a new namespace
// and removes the cards from the old one, which only this process still
// holds.
func orphaned(links []Link) bool {
	return !slices.ContainsFunc(links, func(l Link) bool { return l.Type == "veth" })
}

// localPrefixes are the networks the namespace is on outside the tunnels
// (Docker's): routing any of them into a tunnel would cut Asterisk off.
func localPrefixes(links []Link) []netip.Prefix {
	var out []netip.Prefix
	for _, l := range links {
		if l.Type == "wireguard" || l.Name == "lo" {
			continue
		}
		out = append(out, l.Prefixes...)
	}
	return out
}

// splitAllowed is t's allowed addresses minus any on Asterisk's own
// networks (or its own tunnel address), with why each was left out.
func splitAllowed(t wgconf.Tunnel, local []netip.Prefix) (keep []netip.Addr, skipped []string) {
	keep = []netip.Addr{}
	for _, a := range t.AllowedIPs {
		switch {
		case !a.Is4():
			skipped = append(skipped, fmt.Sprintf("%s: only IPv4 addresses go through tunnels", a))
		case a == t.Address:
			skipped = append(skipped, fmt.Sprintf("%s is this end's own tunnel address", a))
		case slices.ContainsFunc(local, func(p netip.Prefix) bool { return p.Contains(a) }):
			skipped = append(skipped, fmt.Sprintf("%s is on one of Linx's own networks; it can't go through a tunnel", a))
		default:
			keep = append(keep, a)
		}
	}
	return keep, skipped
}

func tunnelHash(t wgconf.Tunnel, allowed []netip.Addr) [32]byte {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s|%d|", t.Interface, t.PrivateKey, t.PresharedKey, t.PeerPublicKey, t.Address, t.Endpoint, t.Keepalive)
	for _, a := range allowed {
		fmt.Fprintf(h, "%s,", a)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// errNoWireGuard: the host's kernel has no wireguard module loaded.
var errNoWireGuard = errors.New("no WireGuard in this kernel")

// describe is err in plain words for the status file.
func describe(err error) string {
	if errors.Is(err, errNoWireGuard) {
		return "This server's kernel has no WireGuard. Run: sudo modprobe wireguard (linx setup loads it at every start)."
	}
	return err.Error()
}
