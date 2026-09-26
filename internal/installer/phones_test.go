package installer

import (
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/netip"
	"strings"
	"testing"
)

func TestLANFrom(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.ParseIP("172.17.0.1"), Mask: net.CIDRMask(16, 32)},
		&net.IPNet{IP: net.ParseIP("192.168.1.20"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("10.8.0.1"), Mask: net.CIDRMask(32, 32)},
	}
	lan := lanFrom(netip.MustParseAddr("192.168.1.20"), addrs)
	if !lan.OK() || lan.Network.String() != "192.168.1.0/24" || lan.BindAddress().String() != "192.168.1.20" {
		t.Errorf("LAN = %+v", lan)
	}
	for _, a := range []string{"127.0.0.1", "203.0.113.5", "10.8.0.1", "192.168.9.9"} {
		if l := lanFrom(netip.MustParseAddr(a), addrs); l.OK() || l.BindAddress().String() != "127.0.0.1" || l.Networks() != nil {
			t.Errorf("%s: LAN = %+v, want none", a, l)
		}
	}
}

func readFiles(files map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, fs.ErrNotExist
	}
}

func TestDockerDaemonConfig(t *testing.T) {
	b, changed, err := dockerDaemonConfig(readFiles(nil))
	if err != nil || !changed || strings.TrimSpace(string(b)) != "{\n  \"userland-proxy\": false\n}" {
		t.Errorf("no file: %q %v %v", b, changed, err)
	}

	b, changed, err = dockerDaemonConfig(readFiles(map[string]string{DockerDaemonConfig: `{"log-driver": "local", "userland-proxy": true}`}))
	var m map[string]any
	if err != nil || !changed || json.Unmarshal(b, &m) != nil || m["log-driver"] != "local" || m["userland-proxy"] != false {
		t.Errorf("existing settings: %s %v %v", b, changed, err)
	}

	if _, changed, err := dockerDaemonConfig(readFiles(map[string]string{DockerDaemonConfig: `{"userland-proxy": false}`})); err != nil || changed {
		t.Errorf("already off: changed %v, %v", changed, err)
	}
	if _, _, err := dockerDaemonConfig(readFiles(map[string]string{DockerDaemonConfig: `{nope`})); err == nil {
		t.Error("broken daemon.json accepted")
	}
}

func TestPhonesPlan(t *testing.T) {
	lan := LAN{Address: netip.MustParseAddr("192.168.1.20"), Network: netip.MustParsePrefix("192.168.1.0/24")}
	cmds := func(p Plan) (out []string) {
		for _, s := range p {
			if s.Cmd != nil {
				out = append(out, s.Cmd.String())
			}
		}
		return out
	}

	// nft installed, Docker already set up.
	r := &fakeRunner{answers: map[string]string{"nft --version": "nftables v1.0.9"}}
	p, err := PhonesPlan(context.Background(), r, lan, FrontDoorSettings{}, readFiles(map[string]string{DockerDaemonConfig: `{"userland-proxy": false}`}))
	if err != nil {
		t.Fatal(err)
	}
	want := "systemctl daemon-reload\nsystemctl enable linx-firewall.service\nsystemctl reload-or-restart linx-firewall.service"
	if got := strings.Join(cmds(p), "\n"); got != want {
		t.Errorf("commands:\n%s\nwant:\n%s", got, want)
	}
	files := map[string]*File{}
	for _, s := range p {
		if s.File != nil {
			files[s.File.Path] = s.File
		}
	}
	if f := files[FirewallRules]; f == nil || f.Mode != 0o644 || !strings.Contains(string(f.Data), "elements = { 192.168.1.0/24 }") {
		t.Errorf("rules = %+v", f)
	}
	if f := files["/etc/systemd/system/linx-firewall.service"]; f == nil ||
		!strings.Contains(string(f.Data), "ExecStart=/usr/sbin/nft -f /etc/linx/nftables.conf") {
		t.Errorf("unit = %+v", f)
	}
	if f := files[TrunkAudioForwardFile]; f == nil || !strings.Contains(string(f.Data), "forward UDP ports 10000-10199 to 192.168.1.20") {
		t.Errorf("trunk audio forward steps = %+v", f)
	}

	// No local network: nothing to forward a port to.
	p, err = PhonesPlan(context.Background(), r, LAN{}, FrontDoorSettings{}, readFiles(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p {
		if s.File != nil && s.File.Path == TrunkAudioForwardFile {
			t.Errorf("trunk audio forward steps written with no LAN: %+v", s.File)
		}
	}

	// No nft, fresh Docker.
	p, err = PhonesPlan(context.Background(), &fakeRunner{}, lan, FrontDoorSettings{}, readFiles(nil))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(cmds(p), "\n")
	for _, w := range []string{"apt-get install -y nftables", "systemctl restart docker"} {
		if !strings.Contains(got, w) {
			t.Errorf("commands missing %q:\n%s", w, got)
		}
	}
}

func TestFirewallRuleset(t *testing.T) {
	lan := LAN{Address: netip.MustParseAddr("192.168.1.20"), Network: netip.MustParsePrefix("192.168.1.0/24")}
	rs := string(FirewallRuleset(lan, FrontDoorSettings{WebClients: []netip.Addr{netip.MustParseAddr("192.168.1.30")}}))
	for _, want := range []string{
		"table inet linx\ndelete table inet linx\n", // replaced atomically, never flushes anything else
		"elements = { 192.168.1.0/24 }",
		"type filter hook prerouting priority mangle; policy accept;",
		"fib daddr type local tcp dport 5061 ip saddr @phone_networks accept",
		"fib daddr type local udp dport 10000-10199 ip saddr @phone_networks accept",
		"fib daddr type local tcp dport 5061 ip saddr @trunk_addresses accept",
		"fib daddr type local udp dport 10000-10199 ip saddr @trunk_addresses accept",
		"fib daddr type local tcp dport 5062 ip saddr @trunk_plain_addresses accept",
		"fib daddr type local udp dport 5062 ip saddr @trunk_plain_addresses accept",
		"set trunk_addresses {\n\t\ttype ipv4_addr\n\t}",
		"set trunk_plain_addresses {\n\t\ttype ipv4_addr\n\t}",
		"fib daddr type local tcp dport { 5060, 5061, 5062 } counter drop",
		"fib daddr type local udp dport { 5060, 10000-10199, 5062 } counter drop",
		// The front door (Pangolin at 192.168.1.30) alone reaches the web
		// port and the relay's TLS port.
		"set front_door {\n\t\ttype ipv4_addr\n\t\telements = { 192.168.1.30 }\n\t}",
		"fib daddr type local tcp dport { 8443, 5349 } ip saddr @front_door accept",
		"fib daddr type local tcp dport { 8443, 5349 } counter drop",
	} {
		if !strings.Contains(rs, want) {
			t.Errorf("ruleset missing %q:\n%s", want, rs)
		}
	}
	// Behind an HTTP-only proxy the router forwards 5349 from the internet.
	open := string(FirewallRuleset(lan, FrontDoorSettings{WebClients: []netip.Addr{netip.MustParseAddr("192.168.1.30")}, TURNTLSOpen: true}))
	if !strings.Contains(open, "tcp dport 8443 ip saddr @front_door accept") || strings.Contains(open, "5349") && strings.Contains(open, "dport { 8443, 5349 }") {
		t.Errorf("relay TLS port still guarded:\n%s", open)
	}
	if strings.Contains(rs, "flush ruleset") {
		t.Error("ruleset must never flush other tables")
	}
	if none := string(FirewallRuleset(LAN{}, FrontDoorSettings{})); strings.Contains(none, "elements = {") || !strings.Contains(none, "counter drop") {
		t.Errorf("no-LAN ruleset:\n%s", none)
	}
}
