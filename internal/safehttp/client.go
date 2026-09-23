package safehttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Timeout bounds a whole request: connecting, TLS, sending and reading the
// response (docs/API.md §4).
const Timeout = 10 * time.Second

// Resolver looks up a host's addresses (net.DefaultResolver in production).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Options adjust a client; the zero value is right for production.
type Options struct {
	Resolver Resolver
	// RootCAs replaces the system roots (tests only).
	RootCAs *x509.CertPool
}

// URLError is a URL that can never be used, whatever it resolves to.
type URLError struct{ Detail string }

func (e *URLError) Error() string { return e.Detail }

// CheckURL returns the parsed URL if raw is an absolute https:// URL with a
// host and no user name or password in it (credentials in a URL end up in
// logs; receivers should verify the signature instead).
func CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, &URLError{"That isn't a valid URL."}
	}
	switch {
	case u.Scheme != "https":
		return nil, &URLError{"The URL must start with https:// (Linx never sends over plain http)."}
	case u.Hostname() == "":
		return nil, &URLError{"The URL has no host name."}
	case u.User != nil:
		return nil, &URLError{"Remove the user name or password from the URL."}
	case u.Opaque != "":
		return nil, &URLError{"That isn't a valid URL."}
	}
	return u, nil
}

// NewClient returns an HTTP client that only makes HTTPS requests to
// addresses policy allows, follows no redirects and uses no proxy.
func NewClient(policy Policy, opts Options) *http.Client {
	resolver := opts.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	d := &dialer{policy: policy, resolver: resolver}
	transport := &http.Transport{
		// Never an environment proxy: it would do its own resolving and
		// connecting, past every check here.
		Proxy:                 nil,
		DialContext:           d.dial,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: opts.RootCAs},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   Timeout,
		ResponseHeaderTimeout: Timeout,
		MaxIdleConns:          50,
		MaxIdleConnsPerHost:   2,
		// Idle connections were checked when opened; keep them briefly so an
		// allowlist removal applies soon.
		IdleConnTimeout:        30 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &http.Client{
		Transport: httpsOnly{transport},
		Timeout:   Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// httpsOnly refuses anything but https before a connection is made.
type httpsOnly struct{ next http.RoundTripper }

func (t httpsOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" {
		return nil, &URLError{"The URL must start with https:// (Linx never sends over plain http)."}
	}
	return t.next.RoundTrip(r)
}

type dialer struct {
	policy   Policy
	resolver Resolver
}

// dial resolves addr's host once, checks every address, then connects to
// the checked addresses in turn. The net.Dialer's Control checks the exact
// address again right before connecting.
func (d *dialer) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		addrs, err = d.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("looking up %s: %w", host, err)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("looking up %s: no addresses", host)
		}
	}
	if err := d.policy.Check(ctx, host, addrs); err != nil {
		return nil, err
	}

	nd := &net.Dialer{
		Timeout: Timeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return err
			}
			return d.policy.Check(ctx, host, []netip.Addr{ap.Addr()})
		},
	}
	var errs []error
	for _, a := range addrs {
		conn, err := nd.DialContext(ctx, network, net.JoinHostPort(a.WithZone("").String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}

// OwnNetworks returns the ranges of this machine's (container's) own
// network interfaces, for Policy.Own.
func OwnNetworks() ([]netip.Prefix, error) {
	ifaddrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, a := range ifaddrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		p, err := netip.ParsePrefix(n.String())
		if err != nil {
			continue
		}
		p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()).Masked()
		// Link-local fe80::/64 is on every interface; it's handled by the
		// link-local rules, not as one of Linx's networks.
		if p.Addr().IsLinkLocalUnicast() {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// Describe turns a request error into a short plain-language reason for a
// delivery log.
func Describe(err error) string {
	var b *BlockedError
	var u *URLError
	var ne net.Error
	switch {
	case errors.As(err, &b):
		return b.Error()
	case errors.As(err, &u):
		return u.Detail
	case errors.As(err, &ne) && ne.Timeout():
		return "No response within 10 seconds."
	}
	var ce *tls.CertificateVerificationError
	if errors.As(err, &ce) {
		return "The receiver's HTTPS certificate isn't trusted: " + ce.Err.Error()
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return "The host name doesn't exist (DNS lookup failed)."
		}
		return "DNS lookup failed: " + dnsErr.Err
	}
	msg := err.Error()
	// url.Error wraps as `Post "https://...": <cause>`; the URL is already
	// shown next to the log entry.
	var ue *url.Error
	if errors.As(err, &ue) {
		msg = ue.Err.Error()
	}
	return strings.TrimSpace(msg)
}

// CheckHost resolves host and checks its addresses, so an admin hears at
// once that a URL they typed is refused. A name that doesn't resolve yet
// passes: every connection is checked again anyway.
func (p Policy) CheckHost(ctx context.Context, r Resolver, host string) error {
	if a, err := netip.ParseAddr(host); err == nil {
		return p.Check(ctx, host, []netip.Addr{a})
	}
	if r == nil {
		r = net.DefaultResolver
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return nil
	}
	return p.Check(ctx, host, addrs)
}
