// Package dnscheck looks up a name the way Let's Encrypt will soon see it:
// at the domain's own name servers, not through a caching resolver. Asking
// a public resolver before the record exists would make it remember "no
// such name" for up to the zone's negative TTL (docs/INSTALL.md §4.1), and
// the install page asks every few seconds.
package dnscheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/miekg/dns"
)

// Resolver looks up A records at a domain's own name servers.
type Resolver struct {
	// Server, if set, is asked directly instead ("host:port"): tests.
	Server string
	// Timeout is per question (default 5 s).
	Timeout time.Duration
}

func (r Resolver) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return 5 * time.Second
}

// LookupA returns name's IPv4 addresses, sorted; none (and no error) when
// there's no record yet.
func (r Resolver) LookupA(ctx context.Context, name string) ([]string, error) {
	fqdn := dns.Fqdn(strings.ToLower(name))
	if r.Server != "" {
		return r.ask(ctx, []string{r.Server}, fqdn, true)
	}
	zone, err := dns01.FindZoneByFqdn(fqdn)
	if err != nil {
		// No zone at all: the domain doesn't exist (yet) as far as DNS knows.
		return nil, fmt.Errorf("finding %s's name servers: %w", strings.TrimSuffix(fqdn, "."), err)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*r.timeout())
	defer cancel()
	nss, err := net.DefaultResolver.LookupNS(ctx, zone)
	if err != nil || len(nss) == 0 {
		return nil, fmt.Errorf("finding %s's name servers: %v", zone, err)
	}
	var servers []string
	for _, ns := range nss {
		addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", ns.Host)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			servers = append(servers, netip.AddrPortFrom(a, 53).String())
		}
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("none of %s's name servers could be found", zone)
	}
	return r.ask(ctx, servers, fqdn, false)
}

// ask tries each server in turn until one answers.
func (r Resolver) ask(ctx context.Context, servers []string, fqdn string, recurse bool) ([]string, error) {
	c := &dns.Client{Timeout: r.timeout()}
	m := new(dns.Msg)
	m.SetQuestion(fqdn, dns.TypeA)
	m.RecursionDesired = recurse
	var errs []error
	for _, s := range servers {
		in, _, err := c.ExchangeContext(ctx, m, s)
		if err == nil && in.Truncated {
			tc := &dns.Client{Net: "tcp", Timeout: r.timeout()}
			in, _, err = tc.ExchangeContext(ctx, m, s)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		switch in.Rcode {
		case dns.RcodeNameError:
			return nil, nil
		case dns.RcodeSuccess:
		default:
			errs = append(errs, fmt.Errorf("%s answered %s", s, dns.RcodeToString[in.Rcode]))
			continue
		}
		var out []string
		var cname string
		for _, rr := range in.Answer {
			switch rr := rr.(type) {
			case *dns.A:
				out = append(out, rr.A.String())
			case *dns.CNAME:
				cname = rr.Target
			}
		}
		if len(out) == 0 && cname != "" {
			// Pointed at another name, maybe elsewhere: follow it the
			// ordinary way.
			addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", cname)
			if err != nil {
				var dnsErr *net.DNSError
				if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
					return nil, nil
				}
				return nil, err
			}
			for _, a := range addrs {
				out = append(out, a.String())
			}
		}
		slices.Sort(out)
		return slices.Compact(out), nil
	}
	return nil, errors.Join(errs...)
}
