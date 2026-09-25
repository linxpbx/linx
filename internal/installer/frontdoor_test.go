package installer

import (
	"net/netip"
	"strings"
	"testing"
)

func TestFrontDoorConfigValidate(t *testing.T) {
	for _, tc := range []struct {
		f  FrontDoorConfig
		ok bool
	}{
		{FrontDoorConfig{Kind: FrontDoorHomeOnly}, true},
		{FrontDoorConfig{Kind: FrontDoorLinx443}, true},
		{FrontDoorConfig{Kind: FrontDoorPangolin, PangolinAddress: "192.168.1.30"}, true},
		{FrontDoorConfig{Kind: FrontDoorPangolin}, false},
		{FrontDoorConfig{Kind: FrontDoorPangolin, PangolinAddress: "203.0.113.5"}, false}, // not at home
		{FrontDoorConfig{Kind: FrontDoorPangolin, PangolinAddress: "fd00::1"}, false},
		{FrontDoorConfig{Kind: FrontDoorLinx443, PangolinAddress: "192.168.1.30"}, false},
		{FrontDoorConfig{Kind: "nginx"}, false},
	} {
		if err := tc.f.Validate(); (err == nil) != tc.ok {
			t.Errorf("%+v: %v", tc.f, err)
		}
	}
}

func TestFrontDoorFor(t *testing.T) {
	lan := LAN{Address: netip.MustParseAddr("192.168.1.20"), Network: netip.MustParsePrefix("192.168.1.0/24")}
	cfg := DefaultConfig()
	cfg.Domain.Name = "lab.example.com"

	home := FrontDoorFor(cfg, lan)
	if home.TrustedProxies != "" || home.WebAddress.String() != "127.0.0.1" || home.TURNUDPAddress.String() != "127.0.0.1" ||
		home.ComposeProfiles != "" || len(home.WebClients) != 0 {
		t.Errorf("home only publishes something: %+v", home)
	}

	cfg.FrontDoor = FrontDoorConfig{Kind: FrontDoorPangolin, PangolinAddress: "192.168.1.30"}
	p := FrontDoorFor(cfg, lan)
	if p.TrustedProxies != "192.168.1.30" || p.WebAddress != lan.Address || p.TURNUDPAddress != lan.Address ||
		p.TURNUDPPort != 443 || len(p.WebClients) != 1 || p.WebClients[0].String() != "192.168.1.30" || p.SNIAddress.String() != "127.0.0.1" {
		t.Errorf("pangolin: %+v", p)
	}

	cfg.FrontDoor = FrontDoorConfig{Kind: FrontDoorLinx443}
	l := FrontDoorFor(cfg, lan)
	if l.TrustedProxies != SNIContainerName || l.SNIAddress != lan.Address || l.TURNUDPPort != 443 ||
		l.ComposeProfiles != FrontDoorLinx443 || l.WebAddress.String() != "127.0.0.1" {
		t.Errorf("linx-443 at home: %+v", l)
	}
	if vps := FrontDoorFor(cfg, LAN{}); vps.SNIAddress.String() != "0.0.0.0" || vps.TURNUDPAddress.String() != "0.0.0.0" {
		t.Errorf("linx-443 on a VPS: %+v", vps)
	}
}

func TestFrontDoorFiles(t *testing.T) {
	linx := netip.MustParseAddr("192.168.1.20")
	tr := string(PangolinTraefik("lab.example.com", linx))
	for _, want := range []string{
		"rule: \"HostSNI(`meet.lab.example.com`) || HostSNI(`api.lab.example.com`)\"",
		"rule: \"HostSNI(`turn.lab.example.com`)\"",
		"passthrough: true",
		"entryPoints: [websecure]",
		`- address: "192.168.1.20:8443"`,
		`- address: "192.168.1.20:5349"`,
		"serversTransport: linx-proxy-protocol",
		"version: 2",
	} {
		if !strings.Contains(tr, want) {
			t.Errorf("Traefik file missing %q:\n%s", want, tr)
		}
	}
	if strings.Contains(tr, "insecureSkipVerify") {
		t.Error("the Traefik file must never turn certificate checks off")
	}
	steps := PangolinSteps("lab.example.com", linx)
	for _, want := range []string{PangolinTraefikFile, "UDP 443 to this Linx server, 192.168.1.20", "http3", "sudo linx doctor"} {
		if !strings.Contains(steps, want) {
			t.Errorf("steps missing %q", want)
		}
	}
	hp := string(HAProxyConfig("lab.example.com"))
	for _, want := range []string{
		"mode tcp", "bind :443", "req_ssl_sni -i turn.lab.example.com",
		"server control-plane control-plane:8443 send-proxy-v2", "server coturn coturn:5349",
		"resolvers docker",
	} {
		if !strings.Contains(hp, want) {
			t.Errorf("HAProxy config missing %q:\n%s", want, hp)
		}
	}

	cfg := DefaultConfig()
	cfg.Domain.Name = "lab.example.com"
	lan := LAN{Address: linx, Network: netip.MustParsePrefix("192.168.1.0/24")}
	if f, d := FrontDoorPlan(cfg, lan); f != nil || d != nil {
		t.Error("home only writes front door files")
	}
	cfg.FrontDoor = FrontDoorConfig{Kind: FrontDoorPangolin, PangolinAddress: "192.168.1.30"}
	files, dns := FrontDoorPlan(cfg, lan)
	if len(files) != 2 || len(dns) != 1 || !strings.Contains(dns[0].Cmd.String(), "certd -records meet,api,turn") {
		t.Errorf("pangolin plan: %+v %+v", files, dns)
	}
	env := string(stackDotEnv(cfg, "abc", lan))
	for _, want := range []string{"LINX_TRUSTED_PROXIES=192.168.1.30\n", "LINX_WEB_ADDRESS=192.168.1.20\n",
		"LINX_TURN_UDP_ADDRESS=192.168.1.20\n", "LINX_TURN_UDP_PORT=443\n", "COMPOSE_PROFILES=\n"} {
		if !strings.Contains(env, want) {
			t.Errorf(".env missing %q:\n%s", want, env)
		}
	}
}
