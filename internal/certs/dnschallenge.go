package certs

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/dns01"

	"linxpbx.com/linx/internal/dnsapi"
)

// dnsChallenge is lego's DNS-01 provider for the companies internal/dnsapi
// changes records at: the _acme-challenge TXT record is added there and
// taken away after, and lego's own checks and settle wait (acme.go) run as
// for Cloudflare. A wildcard certificate puts two values at one name, so
// each value is added to what's there and removed on its own.
type dnsChallenge struct {
	api dnsapi.API
	mu  sync.Mutex
}

var _ challenge.ProviderTimeout = (*dnsChallenge)(nil)

// presentTimeout bounds one call to the DNS company.
const presentTimeout = 2 * time.Minute

func (d *dnsChallenge) where(domain, keyAuth string) (zone, name, value string, err error) {
	info := dns01.GetChallengeInfo(domain, keyAuth)
	zone, err = dnsapi.FindZone(info.EffectiveFQDN)
	if err != nil {
		return "", "", "", err
	}
	return zone, dnsapi.Relative(info.EffectiveFQDN, zone), info.Value, nil
}

func (d *dnsChallenge) Present(domain, _, keyAuth string) error {
	zone, name, value, err := d.where(domain, keyAuth)
	if err != nil {
		return err
	}
	return d.change(zone, name, func(have []string) []string {
		if slices.Contains(have, value) {
			return have
		}
		return append(have, value)
	})
}

func (d *dnsChallenge) CleanUp(domain, _, keyAuth string) error {
	zone, name, value, err := d.where(domain, keyAuth)
	if err != nil {
		return err
	}
	return d.change(zone, name, func(have []string) []string {
		return slices.DeleteFunc(have, func(v string) bool { return v == value })
	})
}

// change reads name's TXT values and writes edit's (none: the record goes).
func (d *dnsChallenge) change(zone, name string, edit func([]string) []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), presentTimeout)
	defer cancel()
	have, err := d.api.Get(ctx, zone, name, dnsapi.TypeTXT)
	if err != nil {
		return err
	}
	next := edit(slices.Clone(have))
	if slices.Equal(next, have) {
		return nil
	}
	if len(next) == 0 {
		return d.api.Delete(ctx, zone, name, dnsapi.TypeTXT)
	}
	return d.api.Set(ctx, zone, name, dnsapi.TypeTXT, next)
}

func (d *dnsChallenge) Timeout() (timeout, interval time.Duration) {
	return propagationTimeout, pollInterval
}
