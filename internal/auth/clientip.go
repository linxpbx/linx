package auth

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIPResolver finds the caller's address. X-Forwarded-For is only
// believed when the connection comes from a trusted proxy; otherwise anyone
// could pick an address to dodge rate limits or IP allowlists.
type ClientIPResolver struct {
	trusted []netip.Prefix
}

// NewClientIPResolver parses a comma-separated list of trusted proxy
// addresses or CIDRs (LINX_TRUSTED_PROXIES). Empty trusts no proxy.
func NewClientIPResolver(list string) (*ClientIPResolver, error) {
	r := &ClientIPResolver{}
	for _, s := range strings.Split(list, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		p, err := parsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", s, err)
		}
		r.trusted = append(r.trusted, p)
	}
	return r, nil
}

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
