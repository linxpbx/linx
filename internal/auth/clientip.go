package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"sync"
)

// ClientIPResolver finds the caller's address. X-Forwarded-For is only
// believed when the connection comes from a trusted proxy; otherwise anyone
// could pick an address to dodge rate limits or IP allowlists. The same list
// decides who must (and who may not) send a PROXY protocol header on the
// HTTPS port (docs/WEB.md §3).
type ClientIPResolver struct {
	trusted []netip.Prefix
	// names are trusted proxies given by container name (the "Linx takes
	// 443" front door's HAProxy, whose address changes when it's
	// recreated); Refresh looks them up.
	names    []string
	mu       sync.RWMutex
	resolved []netip.Addr
}

// hostnameRE is a container or host name: letters, digits, dots and dashes.
var hostnameRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)

// NewClientIPResolver parses a comma-separated list of trusted proxy
// addresses, CIDRs or host names (LINX_TRUSTED_PROXIES). Empty trusts no
// proxy. Names are trusted only once Refresh has looked them up.
func NewClientIPResolver(list string) (*ClientIPResolver, error) {
	r := &ClientIPResolver{}
	for _, s := range strings.Split(list, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		p, err := parsePrefix(s)
		if err == nil {
			r.trusted = append(r.trusted, p)
			continue
		}
		if !hostnameRE.MatchString(s) || strings.Contains(s, "/") {
			return nil, fmt.Errorf("trusted proxy %q: not an address, network or name", s)
		}
		r.names = append(r.names, s)
	}
	return r, nil
}

// Names are the trusted proxies given by name.
func (r *ClientIPResolver) Names() []string { return r.names }

// Refresh looks up the trusted names. A name that doesn't resolve is
// trusted at none of its old addresses: a stale address could belong to
// something else by now.
func (r *ClientIPResolver) Refresh(ctx context.Context, lookup func(ctx context.Context, host string) ([]netip.Addr, error)) error {
	var addrs []netip.Addr
	var firstErr error
	for _, n := range r.names {
		as, err := lookup(ctx, n)
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("trusted proxy %q: %w", n, err)
		}
		for _, a := range as {
			addrs = append(addrs, a.Unmap())
		}
	}
	r.mu.Lock()
	r.resolved = addrs
	r.mu.Unlock()
	return firstErr
}

// Trusted reports whether a is a trusted proxy.
func (r *ClientIPResolver) Trusted(a netip.Addr) bool { return r.isTrusted(a.Unmap()) }

// ParseAllowedIP parses one IP allowlist entry (an address or a CIDR).
func ParseAllowedIP(s string) (netip.Prefix, error) { return parsePrefix(s) }

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// ClientIP returns the caller's address: the connection's peer, or, if the
// peer is a trusted proxy, the right-most X-Forwarded-For entry that isn't one.
func (r *ClientIPResolver) ClientIP(req *http.Request) netip.Addr {
	peer := addrOf(req.RemoteAddr)
	if !peer.IsValid() || !r.isTrusted(peer) {
		return peer
	}
	hops := strings.Split(strings.Join(req.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return peer
		}
		a = a.Unmap()
		if !r.isTrusted(a) {
			return a
		}
	}
	return peer
}

func (r *ClientIPResolver) isTrusted(a netip.Addr) bool {
	for _, p := range r.trusted {
		if p.Contains(a) {
			return true
		}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, t := range r.resolved {
		if t == a {
			return true
		}
	}
	return false
}

func addrOf(remote string) netip.Addr {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

// IPAllowed reports whether ip is inside one of allowed. An empty list allows
// every address.
func IPAllowed(allowed []netip.Prefix, ip netip.Addr) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, p := range allowed {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
