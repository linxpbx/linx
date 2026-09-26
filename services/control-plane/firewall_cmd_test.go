package main

import (
	"bytes"
	"context"
	"net/netip"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/trunk"
)

type fakeFirewallTrunks struct{ trunks []trunk.Trunk }

func (f *fakeFirewallTrunks) AllTrunks(context.Context) ([]trunk.Trunk, error) { return f.trunks, nil }

func TestFirewallCommand(t *testing.T) {
	st := &fakeFirewallTrunks{trunks: []trunk.Trunk{
		{Kind: trunk.KindIPAuthenticated, Host: "tls.example.com", Transport: trunk.TransportTLS, Enabled: true},
		{Kind: trunk.KindIPAuthenticated, Host: "udp.example.com", Transport: trunk.TransportUDP, Enabled: true},
	}}
	resolve := func(_ context.Context, host string) []netip.Addr {
		switch host {
		case "tls.example.com":
			return []netip.Addr{netip.MustParseAddr("203.0.113.1")}
		case "udp.example.com":
			return []netip.Addr{netip.MustParseAddr("203.0.113.2")}
		}
		return nil
	}
	var out, errb bytes.Buffer
	if code := firewallCommand(t.Context(), st, resolve, []string{"addresses"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	want := "trunk_addresses 203.0.113.1\ntrunk_addresses 203.0.113.2\ntrunk_plain_addresses 203.0.113.2\n"
	if out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}

	out.Reset()
	errb.Reset()
	if code := firewallCommand(t.Context(), st, resolve, []string{"nope"}, &out, &errb); code != 2 {
		t.Errorf("unknown command: exit %d", code)
	}
	if !strings.Contains(errb.String(), "Unknown firewall command") {
		t.Errorf("errb = %q", errb.String())
	}
}

func TestFirewallCommandUnresolvedKeeps(t *testing.T) {
	st := &fakeFirewallTrunks{trunks: []trunk.Trunk{
		{Kind: trunk.KindIPAuthenticated, Host: "203.0.113.1", Transport: trunk.TransportTLS, Enabled: true},
		{Kind: trunk.KindIPAuthenticated, Host: "gone.example.com", Transport: trunk.TransportTLS, Enabled: true},
	}}
	resolve := func(_ context.Context, host string) []netip.Addr {
		if a, err := netip.ParseAddr(host); err == nil {
			return []netip.Addr{a}
		}
		return nil
	}
	var out, errb bytes.Buffer
	if code := firewallCommand(t.Context(), st, resolve, []string{"addresses"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if want := "keep\ntrunk_addresses 203.0.113.1\n"; out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestFirewallCommandNoTrunks(t *testing.T) {
	var out, errb bytes.Buffer
	code := firewallCommand(t.Context(), &fakeFirewallTrunks{}, func(context.Context, string) []netip.Addr { return nil },
		[]string{"addresses"}, &out, &errb)
	if code != 0 || out.String() != "" {
		t.Errorf("exit %d, out %q, err %q", code, out.String(), errb.String())
	}
}
