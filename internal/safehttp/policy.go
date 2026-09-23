// Package safehttp is the outbound HTTP client for anything that calls a
// URL an admin typed in: webhooks, alert senders, later CRM and storage
// (docs/API.md §4 "Safe outbound connections", ADR-028). It stops those
// URLs from being used to reach Linx's own services, the server's LAN or a
// cloud metadata endpoint (server-side request forgery):
//
//   - HTTPS only, normal certificate checks, no redirects, no proxy.
//   - The host is resolved once and every address is checked; the connection
//     goes to a checked address, so DNS can't change the answer in between.
//   - Non-public addresses are refused unless an admin put the exact range or
//     host name on the outbound allowlist. Some are refused even then.
package safehttp

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
)

// Allowlist is the admin's outbound allowlist: exact CIDR ranges, or exact
// host names whose addresses may be private (a NAS, Home Assistant).
type Allowlist struct {
	Prefixes []netip.Prefix
	Hosts    []string
}

// AllowlistSource returns the current allowlist. It's read on every new
// connection, so a change applies to the next one.
type AllowlistSource func(ctx context.Context) (Allowlist, error)

// neverAllowed can't be reached even when allowlisted: they're the server
// itself or cloud metadata services, never a legitimate LAN target.
var neverAllowed = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),          // "this network"
	netip.MustParsePrefix("127.0.0.0/8"),        // loopback
	netip.MustParsePrefix("169.254.169.254/32"), // cloud metadata (AWS, GCP, Azure, ...)
	netip.MustParsePrefix("169.254.170.2/32"),   // AWS ECS task metadata
	netip.MustParsePrefix("100.100.100.200/32"), // Alibaba Cloud metadata
	netip.MustParsePrefix("224.0.0.0/4"),        // multicast
	netip.MustParsePrefix("240.0.0.0/4"),        // reserved, broadcast
	netip.MustParsePrefix("::/128"),             // unspecified
	netip.MustParsePrefix("::1/128"),            // loopback
	netip.MustParsePrefix("fd00:ec2::254/128"),  // AWS metadata over IPv6
	netip.MustParsePrefix("ff00::/8"),           // multicast
}

// allowlistable are refused unless allowlisted: they aren't on the public
// internet, so a URL pointing there is aimed at someone's private network.
var allowlistable = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),      // RFC 1918
	netip.MustParsePrefix("172.16.0.0/12"),   // RFC 1918 (and Docker's default networks)
	netip.MustParsePrefix("192.168.0.0/16"),  // RFC 1918
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT (and Tailscale)
	netip.MustParsePrefix("169.254.0.0/16"),  // link-local
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("fc00::/7"),        // unique local
	netip.MustParsePrefix("fe80::/10"),       // link-local
	netip.MustParsePrefix("100::/64"),        // discard
	netip.MustParsePrefix("2001::/32"),       // Teredo (tunnels to any IPv4)
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
}

// IPv6 ranges that carry an IPv4 address inside; the embedded address is
// what's really reached, so it's checked instead.
var (
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

// Policy decides which addresses outbound connections may reach.
type Policy struct {
	// Own holds Linx's own networks (the container's interface ranges:
	// linx-private, linx-egress, linx-public and the Docker gateway on each).
	// They're never reachable, even if an admin allowlists a range that
	// covers them.
	Own []netip.Prefix
	// Allowlist returns the admin's allowlist; nil means an empty one.
	Allowlist AllowlistSource

	// allowLoopback lets this package's tests reach httptest servers.
	allowLoopback bool
}

// BlockedError says why a destination was refused. Its message is plain
// language, for the delivery log and the admin.
type BlockedError struct {
	Host string
	Addr netip.Addr
	// Allowlistable is true when an admin could allow it on the outbound
	// allowlist (a LAN address), false when it's never allowed.
	Allowlistable bool
}

func (e *BlockedError) Error() string {
	if e.Allowlistable {
		return fmt.Sprintf("%s resolves to %s, a private address. Add it to the outbound allowlist if it's a device on your network.", e.Host, e.Addr)
	}
	return fmt.Sprintf("%s resolves to %s, which Linx never connects to (the server itself, its internal networks or a cloud metadata service).", e.Host, e.Addr)
}

// Check returns a *BlockedError unless every address may be reached for
// host. It refuses the lot if any one address is refused: a name that
// resolves to both a public and a private address is treated as private.
func (p Policy) Check(ctx context.Context, host string, addrs []netip.Addr) error {
	var list Allowlist
	loaded := false
	for _, a := range addrs {
		a = effective(a)
		if p.never(a) {
			return &BlockedError{Host: host, Addr: a}
		}
		if public(a) || (p.allowLoopback && a.IsLoopback()) {
			continue
		}
		if !loaded && p.Allowlist != nil {
			var err error
			if list, err = p.Allowlist(ctx); err != nil {
				return fmt.Errorf("reading the outbound allowlist: %w", err)
			}
			loaded = true
		}
		if !list.allows(host, a) {
			return &BlockedError{Host: host, Addr: a, Allowlistable: true}
		}
	}
	return nil
}

func (p Policy) never(a netip.Addr) bool {
	if !a.IsValid() {
		return true
	}
	if p.allowLoopback && a.IsLoopback() {
		return false
	}
	for _, n := range neverAllowed {
		if n.Contains(a) {
			return true
		}
	}
	for _, n := range p.Own {
		if n.Contains(a) {
			return true
		}
	}
	return false
}

// public reports whether a is an ordinary internet address.
func public(a netip.Addr) bool {
	if !a.IsGlobalUnicast() {
		return false
	}
	for _, n := range allowlistable {
		if n.Contains(a) {
			return false
		}
	}
	return true
}

func (l Allowlist) allows(host string, a netip.Addr) bool {
	for _, h := range l.Hosts {
		if strings.EqualFold(strings.TrimSuffix(h, "."), strings.TrimSuffix(host, ".")) {
			return true
		}
	}
	for _, p := range l.Prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// effective returns the address a connection to a really reaches: IPv4
// for IPv4-mapped, NAT64 and 6to4 addresses; a itself otherwise. Zones are
// dropped (they only matter for link-local, which is checked as such).
func effective(a netip.Addr) netip.Addr {
	a = a.WithZone("").Unmap()
	if a.Is6() && (nat64.Contains(a) || sixToFour.Contains(a)) {
		b := a.As16()
		var v4 [4]byte
		if nat64.Contains(a) {
			copy(v4[:], b[12:16])
		} else {
			copy(v4[:], b[2:6])
		}
		return netip.AddrFrom4(v4)
	}
	return a
}

// Allowlistable reports whether an admin may put p on the outbound
// allowlist: it must lie inside one private range. Public ranges need no
// entry, and ranges Linx never reaches can't be opened up.
func Allowlistable(p netip.Prefix) bool {
	p = p.Masked()
	for _, n := range neverAllowed {
		if n.Bits() <= p.Bits() && n.Contains(p.Addr()) {
			return false
		}
	}
	for _, n := range allowlistable {
		if n.Bits() <= p.Bits() && n.Contains(p.Addr()) {
			return true
		}
	}
	return false
}
