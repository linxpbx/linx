package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"linxpbx.com/linx/internal/moved"
	"linxpbx.com/linx/internal/publicip"
)

// recordPlace writes where this server is (docs/INSTALL.md §8): setup's
// id for it, its domain, home network and front door from its settings,
// and its public address as the internet sees it (left out when that
// can't be found; the last one known is kept).
func recordPlace(ctx context.Context, svc *moved.Service, getenv func(string) string, phoneNetworks []netip.Prefix, log *slog.Logger) {
	p := moved.Place{ServerID: getenv("LINX_SERVER_ID"), Domain: getenv("LINX_DOMAIN"), LANAddress: getenv("LINX_SIP_ADDRESS"),
		FrontDoor: getenv("LINX_FRONT_DOOR"), LANNetworks: []string{}}
	for _, n := range phoneNetworks {
		p.LANNetworks = append(p.LANNetworks, n.String())
	}
	if a, err := netip.ParseAddr(p.LANAddress); err != nil || a.IsLoopback() || a.IsUnspecified() {
		p.LANAddress = ""
	}
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	a, err := publicip.Lookup(lctx, &http.Client{Timeout: 10 * time.Second}, publicip.TraceURL)
	cancel()
	if err == nil {
		p.PublicAddress = a.String()
	} else if ctx.Err() == nil {
		log.Warn("this server's public address", "err", err)
	}
	m, err := svc.Record(ctx, p)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			log.Error("recording where this server is", "err", err)
		}
	case m != nil:
		log.Warn("this database was last used at another place: the admin home lists what to check",
			"before", m.Before.Domain, "now", m.After.Domain)
	}
}
