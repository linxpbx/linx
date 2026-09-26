package main

import (
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/wgconf"
)

type fakeNet struct {
	links   []Link
	applied []string // interface names, in order
	allowed map[string][]netip.Addr
	removed []string
	rules   int
	fail    error
	hs      time.Time
}

func (f *fakeNet) Links() ([]Link, error) { return f.links, nil }
func (f *fakeNet) ApplyTunnel(t wgconf.Tunnel, allowed []netip.Addr) error {
	if f.fail != nil {
		return f.fail
	}
	f.applied = append(f.applied, t.Interface)
	f.allowed[t.Interface] = allowed
	for _, l := range f.links {
		if l.Name == t.Interface {
			return nil
		}
	}
	f.links = append(f.links, Link{Name: t.Interface, Type: "wireguard"})
	return nil
}
func (f *fakeNet) RemoveTunnel(name string) error {
	f.removed = append(f.removed, name)
	for i, l := range f.links {
		if l.Name == name {
			f.links = append(f.links[:i], f.links[i+1:]...)
			break
		}
	}
	return nil
}
func (f *fakeNet) EnsureRule() error { f.rules++; return nil }
func (f *fakeNet) Device(string) (Device, error) {
	return Device{LastHandshake: f.hs, RxBytes: 10, TxBytes: 20}, nil
}
func (f *fakeNet) Close() error { return nil }

func TestAgent(t *testing.T) {
	cfgDir, statusDir := t.TempDir(), t.TempDir()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	net := &fakeNet{allowed: map[string][]netip.Addr{}, links: []Link{
		{Name: "lo", Type: "device", Prefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}},
		{Name: "eth0", Type: "veth", Prefixes: []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16")}},
		// Left over from a profile since deleted.
		{Name: "linx-wgdeadbeef", Type: "wireguard"},
	}}
	a := &Agent{Config: Config{ConfigDir: cfgDir, StatusDir: statusDir}, Net: net, Now: func() time.Time { return now },
		Log: slog.New(slog.DiscardHandler)}

	// No config yet: nothing applied, the report says so.
	if err := a.Tick(); err != nil {
		t.Fatal(err)
	}
	s, _ := wgconf.ReadStatus(statusDir)
	if !strings.Contains(s.Error, "Waiting for the control plane") || len(net.applied) != 0 {
		t.Fatalf("before any config: %+v %v", s, net.applied)
	}

	id := uuid.New()
	tun := wgconf.Tunnel{ID: id, Name: "VPN", Interface: wgconf.InterfaceName(id), PrivateKey: "k", PeerPublicKey: "p",
		Address: netip.MustParseAddr("10.6.0.2"), Endpoint: netip.MustParseAddrPort("198.51.100.1:51820"), Keepalive: 25,
		AllowedIPs: []netip.Addr{netip.MustParseAddr("10.6.0.1"), netip.MustParseAddr("172.20.0.5"), netip.MustParseAddr("10.6.0.2")}}
	if _, err := wgconf.WriteConfig(cfgDir, wgconf.Config{Tunnels: []wgconf.Tunnel{tun}}); err != nil {
		t.Fatal(err)
	}
	net.hs = now.Add(-10 * time.Second)
	if err := a.Tick(); err != nil {
		t.Fatal(err)
	}
	// Addresses on Asterisk's own networks, or its own tunnel address,
	// never go into the tunnel.
	if got := net.allowed[tun.Interface]; len(got) != 1 || got[0].String() != "10.6.0.1" {
		t.Errorf("allowed %v", got)
	}
	if len(net.removed) != 1 || net.removed[0] != "linx-wgdeadbeef" || net.rules != 1 {
		t.Errorf("removed %v, rules %d", net.removed, net.rules)
	}
	s, _ = wgconf.ReadStatus(statusDir)
	ts := s.Tunnels[id.String()]
	if s.Error != "" || ts.LastHandshake == nil || !ts.UpSince.Equal(now) || len(ts.Skipped) != 2 || ts.TxBytes != 20 {
		t.Errorf("status %+v", s)
	}

	// Unchanged: not touched again (reconfiguring can drop the session).
	if err := a.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(net.applied) != 1 {
		t.Errorf("applied again: %v", net.applied)
	}
	// Changed: applied again.
	tun.Keepalive = 10
	wgconf.WriteConfig(cfgDir, wgconf.Config{Tunnels: []wgconf.Tunnel{tun}})
	a.Tick()
	if len(net.applied) != 2 {
		t.Errorf("not applied after a change: %v", net.applied)
	}

	// A broken file keeps what's up.
	wgconf.WriteConfig(cfgDir, wgconf.Config{})
	if err := writeRaw(cfgDir, "{not json"); err != nil {
		t.Fatal(err)
	}
	a.Tick()
	s, _ = wgconf.ReadStatus(statusDir)
	if !strings.Contains(s.Error, "Can't read the tunnels") || len(net.removed) != 1 {
		t.Errorf("broken file: %+v, removed %v", s, net.removed)
	}

	// A kernel without WireGuard: said in plain words.
	id2 := uuid.New()
	tun2 := tun
	tun2.ID, tun2.Interface = id2, wgconf.InterfaceName(id2)
	wgconf.WriteConfig(cfgDir, wgconf.Config{Tunnels: []wgconf.Tunnel{tun, tun2}})
	net.fail = errNoWireGuard
	a.Tick()
	s, _ = wgconf.ReadStatus(statusDir)
	if !strings.Contains(s.Tunnels[id2.String()].Error, "sudo modprobe wireguard") {
		t.Errorf("no module: %+v", s.Tunnels[id2.String()])
	}
	net.fail = nil

	// Asterisk restarted: its network cards left this namespace.
	net.links = []Link{{Name: "lo", Type: "device"}, {Name: tun.Interface, Type: "wireguard"}}
	if err := a.Tick(); !errors.Is(err, errOrphaned) {
		t.Errorf("orphaned namespace: %v", err)
	}
}

func writeRaw(dir, s string) error {
	return os.WriteFile(filepath.Join(dir, wgconf.ConfigFile), []byte(s), 0o600)
}
