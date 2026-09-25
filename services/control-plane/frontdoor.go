package main

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/pires/go-proxyproto"

	"linxpbx.com/linx/internal/auth"
)

// The front door (docs/WEB.md §3) passes HTTPS through to port 8443 without
// decrypting it (Pangolin's Traefik, or Linx's own HAProxy), so it can't add
// X-Forwarded-For: it sends the visitor's address in a PROXY protocol v2
// header instead. Only a trusted proxy (LINX_TRUSTED_PROXIES) may, and must,
// send one; from anyone else a header is refused, so nobody can claim
// someone else's address to dodge the sign-in lockout.

// proxyHeaderTimeout bounds how long a connection may take to send it.
const proxyHeaderTimeout = 5 * time.Second

// proxyListener wraps the HTTPS port's listener.
func proxyListener(ips *auth.ClientIPResolver) func(net.Listener) net.Listener {
	return func(ln net.Listener) net.Listener {
		return &proxyproto.Listener{
			Listener:          ln,
			ReadHeaderTimeout: proxyHeaderTimeout,
			ConnPolicy: func(o proxyproto.ConnPolicyOptions) (proxyproto.Policy, error) {
				return proxyPolicy(ips, o.Upstream), nil
			},
		}
	}
}

func proxyPolicy(ips *auth.ClientIPResolver, upstream net.Addr) proxyproto.Policy {
	if tcp, ok := upstream.(*net.TCPAddr); ok {
		if a, ok := netip.AddrFromSlice(tcp.IP); ok && ips.Trusted(a) {
			return proxyproto.REQUIRE
		}
	}
	return proxyproto.REJECT
}

// trustedProxyRefresh is how often trusted proxies given by name are looked
// up again; trustedProxyRetry while one can't be found (linx-sni starts
// after the control plane, so at first it isn't there yet).
const (
	trustedProxyRefresh = 30 * time.Second
	trustedProxyRetry   = 2 * time.Second
)

// refreshTrustedProxies keeps trusted proxy names resolved until ctx ends.
func refreshTrustedProxies(ctx context.Context, ips *auth.ClientIPResolver, log *slog.Logger) {
	if len(ips.Names()) == 0 {
		return
	}
	lookup := func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
	failing := false
	for {
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := ips.Refresh(lctx, lookup)
		cancel()
		switch {
		case err != nil && !failing && ctx.Err() == nil:
			log.Warn("can't find the front door's address yet; its connections are refused until it's there", "err", err)
		case err == nil && failing:
			log.Info("found the front door's address", "names", ips.Names())
		}
		failing = err != nil
		wait := trustedProxyRefresh
		if failing {
			wait = trustedProxyRetry
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}
