package main

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/dnscheck"
	"linxpbx.com/linx/internal/publicip"
	"linxpbx.com/linx/internal/reach"
	"linxpbx.com/linx/internal/turn"
)

// Check it (docs/SIMPLER.md §2.3, internal/reach): the checks from this
// server and the phone links, from the front door setup gave this
// container. Nothing runs until an admin asks.

// sniAddress is Linx's own port 443 router on the network it shares with
// the control plane (compose service "sni").
const sniAddress = "sni:443"

// proxyDoors are the front doors that are another program at
// LINX_TRUSTED_PROXIES.
var proxyDoors = []string{"proxy", "pangolin", "nginx", "http-proxy"}

func newReach(getenv func(string) string, cert *certs.ServingCert, issuer *turn.Issuer, ips *auth.ClientIPResolver) (func(context.Context) []reach.Line, *reach.Links) {
	domain, door := getenv("LINX_DOMAIN"), getenv("LINX_FRONT_DOOR")
	home, _ := netip.ParseAddr(getenv("LINX_SIP_ADDRESS"))
	public := &cachedPublicIP{}
	c := &reach.Checker{Domain: domain, Door: door, SNI: sniAddress, Home: home,
		Leaf:       cert.Current,
		TURNSecret: issuer.Secret,
		Lookup:     dnscheck.Resolver{}.LookupA,
		PublicIP:   public.Get,
		Dial:       (&net.Dialer{Timeout: reach.Timeout}).DialContext,
		Now:        time.Now,
	}
	for _, d := range proxyDoors {
		if door == d {
			c.Proxy = strings.TrimSpace(getenv("LINX_TRUSTED_PROXIES"))
		}
	}
	if home.IsValid() {
		c.TURNTLS = netip.AddrPortFrom(home, 5349).String()
	}
	links := &reach.Links{Domain: domain, Proxies: ips.Trusted, PublicIP: public.Get, TURN: issuer, Now: time.Now}
	return c.Run, links
}

// cachedPublicIP is this network's public address, asked of the internet
// at most every five minutes.
type cachedPublicIP struct {
	mu   sync.Mutex
	addr netip.Addr
	at   time.Time
}

func (p *cachedPublicIP) Get(ctx context.Context) (netip.Addr, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.addr.IsValid() && time.Since(p.at) < 5*time.Minute {
		return p.addr, nil
	}
	ctx, cancel := context.WithTimeout(ctx, reach.Timeout)
	defer cancel()
	a, err := publicip.Lookup(ctx, &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}}, publicip.TraceURL)
	if err != nil {
		return netip.Addr{}, err
	}
	p.addr, p.at = a, time.Now()
	return a, nil
}
