package installer

import (
	"bytes"
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/install"
)

var (
	webHome = LAN{Address: netip.MustParseAddr("192.168.1.20"), Network: netip.MustParsePrefix("192.168.1.0/24")}
	webNow  = time.Date(2026, 9, 28, 12, 5, 0, 0, time.UTC)
)

func goodAnswers() install.Answers {
	return install.Answers{Where: install.WhereRented, FrontDoor: FrontDoorLinx443, Domain: " Example.COM ",
		Name: "Owner", Email: "owner@example.com", TimeZone: "Asia/Dubai", AgreedToTerms: true}
}

func TestWebConfig(t *testing.T) {
	c, errs := WebConfig(DefaultConfig(), goodAnswers(), LAN{}, webNow)
	if len(errs) > 0 {
		t.Fatalf("errors: %+v", errs)
	}
	if c.Domain.Name != "example.com" || c.Domain.DNSProvider != DNSCloudflare || c.Certificates.Staging ||
		c.Certificates.Email != "owner@example.com" || !c.Docker.Install || c.FrontDoor.Kind != FrontDoorLinx443 ||
		c.Install.Where != install.WhereRented || c.TimeZone != "Asia/Dubai" || c.Zone() != "Asia/Dubai" || c.Install.TermsAgreedAt != "2026-09-28T12:05:00Z" || c.Installed() {
		t.Errorf("config: %+v", c)
	}
	// What's saved reads back the same.
	back, err := ParseConfig(bytes.NewReader(c.Marshal()))
	if err != nil || back != c {
		t.Errorf("round trip: %v\n%+v\n%+v", err, back, c)
	}
	if got := WebProgress(c); got != "Domain: example.com, front door: Linx takes port 443" {
		t.Errorf("progress %q", got)
	}

	a := goodAnswers()
	a.Where, a.FrontDoor, a.ProxyAddress, a.TURNUDPPort, a.Domain = install.WhereHome, FrontDoorPangolin, "192.168.1.30", 3478, "me.duckdns.org"
	c, errs = WebConfig(DefaultConfig(), a, webHome, webNow)
	if len(errs) > 0 || c.Domain.DNSProvider != DNSDuckDNS || c.FrontDoor.ProxyAddress != "192.168.1.30" || c.FrontDoor.TURNUDPPort != 3478 {
		t.Errorf("pangolin + duckdns: %+v %+v", errs, c)
	}
	back, err = ParseConfig(bytes.NewReader(c.Marshal()))
	if err != nil || back != c {
		t.Errorf("turn_udp_port lost on the way to setup.yaml: %v %+v", err, back.FrontDoor)
	}
}

func TestWebConfigRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*install.Answers)
		lan   LAN
		field string
		msg   string
	}{
		{"where", func(a *install.Answers) { a.Where = "moon" }, LAN{}, "where", "Choose where"},
		{"none isn't offered", func(a *install.Answers) { a.FrontDoor = FrontDoorNone }, LAN{}, "front_door", "Choose what's in front"},
		{"pangolin on a rented server", func(a *install.Answers) { a.FrontDoor = FrontDoorPangolin }, LAN{}, "front_door", "Choose what's in front"},
		{"home-only without a home network", func(a *install.Answers) { a.Where, a.FrontDoor = install.WhereHome, FrontDoorHomeOnly }, LAN{}, "front_door", "isn't on one"},
		{"proxy address", func(a *install.Answers) {
			a.Where, a.FrontDoor, a.ProxyAddress = install.WhereHome, FrontDoorNginx, "8.8.8.8"
		}, webHome, "proxy_address", "isn't a home-network address"},
		{"udp port", func(a *install.Answers) {
			a.Where, a.FrontDoor, a.ProxyAddress, a.TURNUDPPort = install.WhereHome, FrontDoorPangolin, "192.168.1.30", 5061
		}, webHome, "turn_udp_port", "Linx already uses that port"},
		{"no domain", func(a *install.Answers) { a.Domain = "" }, LAN{}, "domain", "Give your domain"},
		{"an address", func(a *install.Answers) { a.Domain = "203.0.113.5" }, LAN{}, "domain", "That's an address"},
		{"not a domain", func(a *install.Answers) { a.Domain = "*.example.com" }, LAN{}, "domain", "That isn't a domain"},
		{"public suffix", func(a *install.Answers) { a.Domain = "co.uk" }, LAN{}, "domain", "co.uk is shared by everyone"},
		{"duckdns itself", func(a *install.Answers) { a.Domain = "duckdns.org" }, LAN{}, "domain", "shared by everyone"},
		{"duckdns too deep", func(a *install.Answers) { a.Domain = "a.me.duckdns.org" }, LAN{}, "domain", "DuckDNS names look like"},
		{"no name", func(a *install.Answers) { a.Name = " " }, LAN{}, "name", "Give your name"},
		{"control characters", func(a *install.Answers) { a.Name = "O\x1b[2Jwner" }, LAN{}, "name", "control characters"},
		{"email", func(a *install.Answers) { a.Email = "owner@example" }, LAN{}, "email", "doesn't look like an email"},
		{"email quote", func(a *install.Answers) { a.Email = `o"wner@example.com` }, LAN{}, "email", "doesn't look like an email"},
		{"no time zone", func(a *install.Answers) { a.TimeZone = "" }, LAN{}, "time_zone", "Choose your time zone"},
		{"unknown time zone", func(a *install.Answers) { a.TimeZone = "Mars/Olympus" }, LAN{}, "time_zone", "isn't a time zone"},
		{"terms", func(a *install.Answers) { a.AgreedToTerms = false }, LAN{}, "agreed_to_terms", "Subscriber Agreement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := goodAnswers()
			tc.edit(&a)
			_, errs := WebConfig(DefaultConfig(), a, tc.lan, webNow)
			if len(errs) != 1 || errs[0].Field != tc.field || !strings.Contains(errs[0].Message, tc.msg) {
				t.Errorf("errors %+v, want one for %s containing %q", errs, tc.field, tc.msg)
			}
		})
	}
}

func TestInstalled(t *testing.T) {
	for _, tc := range []struct {
		c    Config
		want bool
	}{
		{Config{}, false},
		{Config{Domain: DomainConfig{Name: "example.com"}}, true}, // set up from the terminal or --config
		{Config{Domain: DomainConfig{Name: "example.com"}, Install: InstallConfig{Where: install.WhereHome}}, false},
		{Config{Domain: DomainConfig{Name: "example.com"}, Install: InstallConfig{Where: install.WhereHome, FinishedAt: "2026-09-28T13:00:00Z"}}, true},
	} {
		if got := tc.c.Installed(); got != tc.want {
			t.Errorf("%+v: %v", tc.c, got)
		}
	}
}

type portRunner string

func (p portRunner) Run(context.Context, []string, string, ...string) ([]byte, error) {
	return []byte(p), nil
}

func TestPortUser(t *testing.T) {
	for out, want := range map[string]string{
		"": "",
		`LISTEN 0 511 0.0.0.0:443 0.0.0.0:* users:(("nginx",pid=812,fd=6),("nginx",pid=811,fd=6))`: "nginx",
		`LISTEN 0 4096 0.0.0.0:443 0.0.0.0:* users:(("docker-proxy",pid=9,fd=4))`:                  "Docker (a container)",
		`LISTEN 0 4096 0.0.0.0:443 0.0.0.0:*`:                                                      "another program",
	} {
		if got := PortUser(context.Background(), portRunner(out), 443); got != want {
			t.Errorf("%q: %q, want %q", out, got, want)
		}
	}
}

func TestInstallStackPlan(t *testing.T) {
	p := InstallStackPlan("sha-abc", netip.MustParseAddr("192.168.1.20"))
	if !bytes.Contains(p[1].File.Data, []byte("LINX_INSTALL_ADDRESS=192.168.1.20\n")) || !bytes.Contains(p[1].File.Data, []byte("LINX_VERSION=sha-abc\n")) {
		t.Errorf("env: %s", p[1].File.Data)
	}
	if !strings.Contains(p[3].Cmd.String(), "--env-file /etc/linx/install.env up --detach --wait") {
		t.Errorf("up: %s", p[3].Cmd)
	}
}
