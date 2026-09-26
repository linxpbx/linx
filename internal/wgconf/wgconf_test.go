package wgconf

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestParseTunnelAddress(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"10.6.0.2/32", "10.6.0.2"},
		{"10.6.0.2", "10.6.0.2"},
		{"fd00::2/128, 10.6.0.2/24", "10.6.0.2"},
		{" 10.6.0.2/24 ,fd00::2/64", "10.6.0.2"},
		{"fd00::2/128", ""},
		{"127.0.0.1/8", ""},
		{"0.0.0.0/0", ""},
		{"not an address", ""},
		{"", ""},
	} {
		got, err := ParseTunnelAddress(tt.in)
		if tt.want == "" {
			if err == nil {
				t.Errorf("%q: got %s, want an error", tt.in, got)
			}
			continue
		}
		if err != nil || got.String() != tt.want {
			t.Errorf("%q: got %s, %v; want %s", tt.in, got, err, tt.want)
		}
	}
}

func TestRender(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	five := 5
	profiles := []Profile{
		{ID: a, Name: "A", Address: "10.6.0.2/24", PrivateKey: "priv", PeerPublicKey: "pub",
			Endpoint: netip.MustParseAddrPort("198.51.100.1:51820"), Keepalive: &five},
		{ID: b, Name: "B", Address: "fd00::2/128", PrivateKey: "priv", PeerPublicKey: "pub"},
		{ID: c, Name: "Unused", Address: "10.7.0.2", PrivateKey: "priv", PeerPublicKey: "pub"},
	}
	ips := func(s ...string) []netip.Addr {
		var out []netip.Addr
		for _, x := range s {
			out = append(out, netip.MustParseAddr(x))
		}
		return out
	}
	cfg, problems := Render(profiles, map[uuid.UUID][]netip.Addr{a: ips("10.6.0.1", "10.6.0.1", "192.0.2.9")})
	if len(cfg.Tunnels) != 2 {
		t.Fatalf("tunnels %+v", cfg.Tunnels)
	}
	var ta, tc Tunnel
	for _, tun := range cfg.Tunnels {
		switch tun.ID {
		case a:
			ta = tun
		case c:
			tc = tun
		}
	}
	// Split tunnel: exactly its trunks' addresses, once each.
	if ta.Address.String() != "10.6.0.2" || ta.Keepalive != 5 || len(ta.AllowedIPs) != 2 || ta.Interface != InterfaceName(a) {
		t.Errorf("A: %+v", ta)
	}
	// Unused: up (for its handshake), carrying nothing; the default
	// keep-alive; no endpoint yet.
	if len(tc.AllowedIPs) != 0 || tc.Keepalive != DefaultKeepalive || tc.Endpoint.IsValid() {
		t.Errorf("unused: %+v", tc)
	}
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "B) left out: it has no IPv4 address") || !strings.Contains(joined, "Unused): its endpoint doesn't resolve") {
		t.Errorf("problems:\n%s", joined)
	}
	if string(cfg.Encode()) != string(cfg.Encode()) || !strings.Contains(string(cfg.Encode()), `"allowed_ips": [`) {
		t.Error("encoding")
	}
}

func TestInterfaceName(t *testing.T) {
	id := uuid.MustParse("0192b1c2-0000-7000-8000-000000000001")
	n := InterfaceName(id)
	if len(n) > 15 || !strings.HasPrefix(n, InterfacePrefix) || n != InterfaceName(id) || n == InterfaceName(uuid.New()) {
		t.Errorf("InterfaceName = %q", n)
	}
}

func TestFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Tunnels: []Tunnel{{ID: uuid.New(), Interface: "linx-wg00000000", PrivateKey: "k",
		Address: netip.MustParseAddr("10.6.0.2"), AllowedIPs: []netip.Addr{netip.MustParseAddr("10.6.0.1")}}}}
	if changed, err := WriteConfig(dir, cfg); err != nil || !changed {
		t.Fatalf("first write: %v %v", changed, err)
	}
	if changed, err := WriteConfig(dir, cfg); err != nil || changed {
		t.Fatalf("same again: %v %v", changed, err)
	}
	got, err := ReadConfig(dir)
	if err != nil || len(got.Tunnels) != 1 || got.Tunnels[0].AllowedIPs[0] != cfg.Tunnels[0].AllowedIPs[0] {
		t.Fatalf("read back %+v %v", got, err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := WriteStatus(dir, Status{WrittenAt: now, Tunnels: map[string]TunnelStatus{"x": {Interface: "i", UpSince: now}}}); err != nil {
		t.Fatal(err)
	}
	s, err := ReadStatus(dir)
	if err != nil || !s.WrittenAt.Equal(now) || s.Tunnels["x"].Interface != "i" {
		t.Fatalf("status %+v %v", s, err)
	}
}

func TestDecide(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	id := uuid.New()
	at := func(d time.Duration) *time.Time { x := now.Add(-d); return &x }
	file := func(s TunnelStatus) Status {
		return Status{WrittenAt: now.Add(-2 * time.Second), Tunnels: map[string]TunnelStatus{id.String(): s}}
	}
	for _, tt := range []struct {
		name   string
		file   Status
		err    error
		state  string
		detail string
	}{
		{"no report", Status{}, errors.New("missing"), StateUnknown, "isn't reporting"},
		{"stale report", Status{WrittenAt: now.Add(-2 * time.Minute)}, nil, StateUnknown, "isn't reporting"},
		{"not set up", Status{WrittenAt: now}, nil, StateUnknown, "isn't set up yet"},
		{"agent error", Status{WrittenAt: now, Error: "no WireGuard"}, nil, StateDown, "no WireGuard"},
		{"tunnel error", file(TunnelStatus{Error: "bad key"}), nil, StateDown, "bad key"},
		{"fresh handshake", file(TunnelStatus{UpSince: *at(time.Hour), LastHandshake: at(30 * time.Second)}), nil, StateUp, "30 seconds ago"},
		{"just applied", file(TunnelStatus{UpSince: *at(time.Minute)}), nil, StateConnecting, "first handshake"},
		{"never shook hands", file(TunnelStatus{UpSince: *at(5 * time.Minute)}), nil, StateDown, "check the keys"},
		{"handshake too old", file(TunnelStatus{UpSince: *at(time.Hour), LastHandshake: at(4 * time.Minute)}), nil, StateDown, "for 4 minutes"},
	} {
		state, detail := Decide(tt.file, tt.err, id, now)
		if state != tt.state || !strings.Contains(detail, tt.detail) {
			t.Errorf("%s: %s %q, want %s %q", tt.name, state, detail, tt.state, tt.detail)
		}
	}
}
