package trunk

import (
	"context"
	"net/netip"
	"slices"
)

// FirewallAddresses returns the addresses the host firewall must admit for
// IP-authenticated trunks (docs/TRUNKS.md §12): tls for the phones' SIP
// port, since such a trunk always needs it admitted whichever transport it
// ends up using (the audio range needs the same admission either way, and
// this is the one set that covers it); plain, a subset, for the trunks
// using a plain transport (ADR-023), which also need the plain trunk port
// admitted.
//
// A registration trunk opens nothing (docs/TRUNKS.md §8): it dials out and
// the provider answers on that connection. A trunk through a WireGuard
// tunnel needs no host firewall opening either: the tunnel is the only way
// to reach it (§7). Both are left out.
//
// resolve turns a trunk's Host into its addresses (linxpbx.com/linx's
// trunkconf.ResolveHost, or a fake in tests); a host that doesn't resolve
// contributes nothing, same as trunkconf's own rendering.
func FirewallAddresses(ctx context.Context, trunks []Trunk, resolve func(context.Context, string) []netip.Addr) (tls, plain []netip.Addr) {
	seenTLS, seenPlain := map[netip.Addr]bool{}, map[netip.Addr]bool{}
	for _, t := range trunks {
		if !t.Enabled || t.Kind != KindIPAuthenticated || t.WireGuardProfileID != nil {
			continue
		}
		for _, a := range resolve(ctx, t.Host) {
			if !seenTLS[a] {
				seenTLS[a] = true
				tls = append(tls, a)
			}
			if t.Transport != TransportTLS && !seenPlain[a] {
				seenPlain[a] = true
				plain = append(plain, a)
			}
		}
	}
	slices.SortFunc(tls, netip.Addr.Compare)
	slices.SortFunc(plain, netip.Addr.Compare)
	return tls, plain
}
