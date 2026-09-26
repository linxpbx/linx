package trunk

import (
	"context"
	"net/netip"
	"testing"

	"github.com/google/uuid"
)

func addrs(s ...string) []netip.Addr {
	out := make([]netip.Addr, len(s))
	for i, a := range s {
		out[i] = netip.MustParseAddr(a)
	}
	return out
}

func TestFirewallAddresses(t *testing.T) {
	wg := uuid.New()
	hosts := map[string][]netip.Addr{
		"tls.example.com": addrs("203.0.113.1"),
		"tcp.example.com": addrs("203.0.113.2"),
		"udp.example.com": addrs("203.0.113.3", "203.0.113.4"), // two addresses for one trunk
	}
	resolve := func(_ context.Context, host string) []netip.Addr { return hosts[host] }

	trunks := []Trunk{
		{Kind: KindIPAuthenticated, Host: "tls.example.com", Transport: TransportTLS, Enabled: true},
		{Kind: KindIPAuthenticated, Host: "tcp.example.com", Transport: TransportTCP, Enabled: true},
		{Kind: KindIPAuthenticated, Host: "udp.example.com", Transport: TransportUDP, Enabled: true},
		// Left out: disabled, a registration trunk (dials out, opens
		// nothing), a LAN peer (already inside phone_networks), and a
		// trunk through a WireGuard tunnel (the tunnel is the only way
		// in, §7).
		{Kind: KindIPAuthenticated, Host: "disabled.example.com", Transport: TransportTLS, Enabled: false},
		{Kind: KindRegistration, Host: "registers.example.com", Transport: TransportTLS, Enabled: true},
		{Kind: KindLANPeer, Host: "192.168.1.50", Transport: TransportTLS, Enabled: true},
		{Kind: KindIPAuthenticated, Host: "tunnel.example.com", Transport: TransportUDP, Enabled: true, WireGuardProfileID: &wg},
	}

	tls, plain := FirewallAddresses(context.Background(), trunks, resolve)

	wantTLS := addrs("203.0.113.1", "203.0.113.2", "203.0.113.3", "203.0.113.4")
	if !equalAddrs(tls, wantTLS) {
		t.Errorf("tls = %v, want %v", tls, wantTLS)
	}
	// Every IP-authenticated trunk's address goes into tls (the audio range
	// needs it either way); only the ones using a plain transport also go
	// into plain.
	wantPlain := addrs("203.0.113.2", "203.0.113.3", "203.0.113.4")
	if !equalAddrs(plain, wantPlain) {
		t.Errorf("plain = %v, want %v", plain, wantPlain)
	}
}

func TestFirewallAddressesEmpty(t *testing.T) {
	tls, plain := FirewallAddresses(context.Background(), nil, func(context.Context, string) []netip.Addr { return nil })
	if len(tls) != 0 || len(plain) != 0 {
		t.Errorf("no trunks: tls=%v plain=%v", tls, plain)
	}
}

func equalAddrs(a, b []netip.Addr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
