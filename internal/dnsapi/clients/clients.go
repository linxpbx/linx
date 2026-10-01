// Package clients talks to the DNS companies internal/dnsapi lists: seven
// through libdns (MIT), the library behind Caddy's DNS support, and
// Hetzner, DigitalOcean and Route 53 through Linx's own short clients
// (owner decision 2026-10-01: their libdns modules add 9 MB, 1 MB with
// MPL-2.0 code the licence check refuses, and 6 MB). It's its own package so
// only linx-certd and the linx command link these libraries, not the
// control plane, which only reads internal/dnsapi's list.
package clients

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"linxpbx.com/linx/internal/dnsapi"
)

// New is company id's API with key k.
func New(ctx context.Context, id string, k dnsapi.Key, o dnsapi.Options) (dnsapi.API, error) {
	c, ok := dnsapi.Find(id)
	if !ok {
		return nil, fmt.Errorf("Linx can't change records at %q", id)
	}
	if p := c.FirstProblem(k); p != "" {
		return nil, errors.New(p)
	}
	hc := o.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	ttl := int(c.TTL.Seconds())
	base := func(real string) string {
		if o.Endpoint != "" {
			return o.Endpoint
		}
		return real
	}
	switch id {
	case dnsapi.Cloudflare, dnsapi.DuckDNS:
		return nil, dnsapi.ErrBuiltin
	case dnsapi.Hetzner:
		return &hetzner{http: hc, base: base(hetznerAPI), token: k["token"], ttl: ttl}, nil
	case dnsapi.DigitalOcean:
		return &digitalOcean{http: hc, base: base(digitalOceanAPI), token: k["token"], ttl: ttl}, nil
	case dnsapi.Route53:
		return &route53{http: hc, base: base(route53API), keyID: k["access_key_id"], secret: k["secret_access_key"], ttl: ttl, now: time.Now}, nil
	}
	return newLibdns(ctx, c, k, o)
}

// CheckKey makes sure k lets Linx read domain's records at company id,
// changing nothing (dnsapi.CheckWith).
func CheckKey(ctx context.Context, id, domain string, k dnsapi.Key, o dnsapi.Options) error {
	api, err := New(ctx, id, k, o)
	if err != nil {
		return fmt.Errorf("%w: %v", dnsapi.ErrKeyRefused, err)
	}
	return dnsapi.CheckWith(ctx, api, id, domain, nil)
}
