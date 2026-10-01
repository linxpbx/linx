package clients

import (
	"context"
	"errors"
	"fmt"
	"linxpbx.com/linx/internal/dnsapi"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/libdns/desec"
	"github.com/libdns/godaddy"
	"github.com/libdns/libdns"
	"github.com/libdns/namecheap"
	"github.com/libdns/ovh"
	"github.com/libdns/porkbun"
)

// provider is the part of libdns Linx uses.
type provider interface {
	libdns.RecordGetter
	libdns.RecordSetter
	libdns.RecordDeleter
}

// libdnsAPI is API over a libdns provider.
type libdnsAPI struct {
	p    provider
	ttl  time.Duration
	name string
	key  dnsapi.Key
}

// clean keeps the key out of an error: Namecheap's API takes it in the
// web address, which Go's own errors quote (and libdns's may too), and an
// error reaches certd's log and the admin's page.
func (a *libdnsAPI) clean(err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = fmt.Errorf("%s: %s: %w", a.name, ue.Op, ue.Err)
	}
	msg := err.Error()
	for _, v := range a.key {
		if len(v) >= 6 {
			msg = strings.ReplaceAll(msg, v, "…")
		}
	}
	if msg == err.Error() {
		return err
	}
	return errors.New(msg)
}

func newLibdns(ctx context.Context, c dnsapi.Company, k dnsapi.Key, o dnsapi.Options) (dnsapi.API, error) {
	var p provider
	switch c.ID {
	case dnsapi.GoDaddy:
		p = &godaddy.Provider{APIToken: k["api_key"] + ":" + k["api_secret"]}
	case dnsapi.Namecheap:
		ip := ""
		if o.PublicAddress != nil {
			var err error
			if ip, err = o.PublicAddress(ctx); err != nil {
				return nil, fmt.Errorf("Namecheap needs this network's public address: %w", err)
			}
		}
		if ip == "" {
			// Never let the library ask a service of its own choosing.
			return nil, fmt.Errorf("Namecheap needs this network's public address, and Linx couldn't find it")
		}
		p = &namecheap.Provider{User: k["api_user"], APIKey: k["api_key"], ClientIP: ip}
	case dnsapi.Porkbun:
		p = &porkbun.Provider{APIKey: k["api_key"], APISecretKey: k["secret_api_key"]}
	case dnsapi.DeSEC:
		p = &desec.Provider{Token: k["token"]}
	case dnsapi.OVH:
		p = &ovh.Provider{Endpoint: k["endpoint"], ApplicationKey: k["application_key"],
			ApplicationSecret: k["application_secret"], ConsumerKey: k["consumer_key"]}
	default:
		return nil, fmt.Errorf("Linx can't change records at %s", c.Name)
	}
	return &libdnsAPI{p: p, ttl: c.TTL, name: c.Name, key: k}, nil
}

func fqdn(zone string) string { return zone + "." }

func (a *libdnsAPI) Get(ctx context.Context, zone, name, typ string) ([]string, error) {
	recs, err := a.p.GetRecords(ctx, fqdn(zone))
	if err != nil {
		return nil, a.clean(err)
	}
	var out []string
	for _, r := range recs {
		rr := r.RR()
		if rr.Type != typ || libdns.AbsoluteName(rr.Name, fqdn(zone)) != libdns.AbsoluteName(name, fqdn(zone)) {
			continue
		}
		switch v := r.(type) {
		case libdns.Address:
			out = append(out, v.IP.String())
		case libdns.TXT:
			out = append(out, v.Text)
		default:
			out = append(out, rr.Data)
		}
	}
	return out, nil
}

func (a *libdnsAPI) Set(ctx context.Context, zone, name, typ string, values []string) error {
	if len(values) == 0 {
		return a.Delete(ctx, zone, name, typ)
	}
	recs := make([]libdns.Record, 0, len(values))
	for _, v := range values {
		switch typ {
		case dnsapi.TypeA:
			ip, err := netip.ParseAddr(v)
			if err != nil {
				return err
			}
			recs = append(recs, libdns.Address{Name: name, TTL: a.ttl, IP: ip})
		case dnsapi.TypeTXT:
			recs = append(recs, libdns.TXT{Name: name, TTL: a.ttl, Text: v})
		default:
			return fmt.Errorf("Linx doesn't write %s records", typ)
		}
	}
	_, err := a.p.SetRecords(ctx, fqdn(zone), recs)
	return a.clean(err)
}

func (a *libdnsAPI) Delete(ctx context.Context, zone, name, typ string) error {
	recs, err := a.p.GetRecords(ctx, fqdn(zone))
	if err != nil {
		return a.clean(err)
	}
	var gone []libdns.Record
	for _, r := range recs {
		rr := r.RR()
		if rr.Type == typ && libdns.AbsoluteName(rr.Name, fqdn(zone)) == libdns.AbsoluteName(name, fqdn(zone)) {
			gone = append(gone, r)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	_, err = a.p.DeleteRecords(ctx, fqdn(zone), gone)
	return a.clean(err)
}
