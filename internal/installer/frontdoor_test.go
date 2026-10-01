package installer

import (
	"bytes"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/install"
)

func TestFrontDoorConfigValidate(t *testing.T) {
	for _, tc := range []struct {
		f  FrontDoorConfig
		ok bool
	}{
		{FrontDoorConfig{Kind: FrontDoorNone}, true},
		{FrontDoorConfig{Kind: FrontDoorHomeOnly}, true},
		{FrontDoorConfig{Kind: FrontDoorNginx, ProxyAddress: "192.168.1.20"}, true}, // on this server
		{FrontDoorConfig{Kind: FrontDoorHTTPProxy, ProxyAddress: "192.168.1.40"}, true},
		{FrontDoorConfig{Kind: FrontDoorHTTPProxy}, false},
		{FrontDoorConfig{Kind: FrontDoorHomeOnly, ProxyAddress: "192.168.1.30"}, false},
		{FrontDoorConfig{Kind: FrontDoorLinx443}, true},
		{FrontDoorConfig{Kind: FrontDoorPangolin, ProxyAddress: "192.168.1.30"}, true},
		{FrontDoorConfig{Kind: FrontDoorProxy, ProxyAddress: "192.168.1.30"}, true},
		{FrontDoorConfig{Kind: FrontDoorProxy}, false},
		{FrontDoorConfig{Kind: FrontDoorPangolin}, false},
		{FrontDoorConfig{Kind: FrontDoorPangolin, ProxyAddress: "203.0.113.5"}, false}, // not at home
		{FrontDoorConfig{Kind: FrontDoorPangolin, ProxyAddress: "fd00::1"}, false},
		{FrontDoorConfig{Kind: FrontDoorLinx443, ProxyAddress: "192.168.1.30"}, false},
		{FrontDoorConfig{Kind: "nginx"}, false},
		{FrontDoorConfig{Kind: FrontDoorPublicPort, PublicPort: 8443}, true},
		{FrontDoorConfig{Kind: FrontDoorPublicPort, PublicPort: 8443, TURNUDPPort: 3478}, true},
		{FrontDoorConfig{Kind: FrontDoorPublicPort}, false},                   // which port?
		{FrontDoorConfig{Kind: FrontDoorPublicPort, PublicPort: 443}, false},  // that's linx-443
		{FrontDoorConfig{Kind: FrontDoorPublicPort, PublicPort: 5061}, false}, // Linx's own
		{FrontDoorConfig{Kind: FrontDoorPublicPort, PublicPort: 6666}, false}, // browsers refuse it
		{FrontDoorConfig{Kind: FrontDoorPublicPort, PublicPort: 80}, false},
		{FrontDoorConfig{Kind: FrontDoorLinx443, PublicPort: 8443}, false},
		{FrontDoorConfig{Kind: FrontDoorPublicPort, PublicPort: 8443, ProxyAddress: "192.168.1.30"}, false},
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

	none := FrontDoorFor(cfg, lan)
	if none.TrustedProxies != "" || none.WebAddress.String() != "127.0.0.1" || none.TURNUDPAddress.String() != "127.0.0.1" ||
		none.ComposeProfiles != "" || len(none.WebClients) != 0 || none.DNSAddress != "" {
		t.Errorf("no front door publishes something: %+v", none)
	}

	cfg.FrontDoor = FrontDoorConfig{Kind: FrontDoorPangolin, ProxyAddress: "192.168.1.30"}
	p := FrontDoorFor(cfg, lan)
	if p.TrustedProxies != "192.168.1.30" || p.WebAddress != lan.Address || p.TURNUDPAddress != lan.Address ||
		p.TURNUDPPort != 443 || len(p.WebClients) != 1 || p.WebClients[0].String() != "192.168.1.30" || p.SNIAddress.String() != "127.0.0.1" {
		t.Errorf("pangolin: %+v", p)
	}

	// A router that won't split port 443 by protocol (UniFi): call audio on
	// UDP 3478, advertised to browsers; the relay over TLS stays on 443.
	cfg.FrontDoor = FrontDoorConfig{Kind: FrontDoorPangolin, ProxyAddress: "192.168.1.30", TURNUDPPort: 3478}
	if err := cfg.FrontDoor.Validate(); err != nil {
		t.Fatal(err)
	}
	if u := FrontDoorFor(cfg, lan); u.TURNUDPPort != 3478 || u.TURNUDPAddress != lan.Address ||
		u.TURNURLs != "turn:turn.lab.example.com:3478?transport=udp,turns:turn.lab.example.com:443?transport=tcp" {
		t.Errorf("pangolin, UDP 3478: %+v", u)
	}
	if s := DoorSetup(cfg, lan); !strings.Contains(s.Steps[0], "UDP port 3478 to this server (192.168.1.20)") ||
		strings.Contains(strings.Join(s.Card.Guides[0].Steps, "\n"), "http3") || strings.Contains(string(CaddyLayer4("x.example.com", lan.Address, 3478)), "protocols") {
		t.Errorf("pangolin steps, UDP 3478: %+v", s)
	}
	for _, bad := range []FrontDoorConfig{
		{Kind: FrontDoorPangolin, ProxyAddress: "192.168.1.30", TURNUDPPort: 80},
		{Kind: FrontDoorPangolin, ProxyAddress: "192.168.1.30", TURNUDPPort: 10050},
		{Kind: FrontDoorLinx443, TURNUDPPort: 3478},
	} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
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

	cfg.FrontDoor = FrontDoorConfig{Kind: FrontDoorNginx, ProxyAddress: "192.168.1.30"}
	if n := FrontDoorFor(cfg, lan); !n.ProxyProtocol || n.TURNTLSOpen || n.TURNURLs != "" || n.TrustedProxies != "192.168.1.30" {
		t.Errorf("nginx: %+v", n)
	}
	cfg.FrontDoor = FrontDoorConfig{Kind: FrontDoorHTTPProxy, ProxyAddress: "192.168.1.30"}
	h := FrontDoorFor(cfg, lan)
	if h.ProxyProtocol || !h.TURNTLSOpen || h.WebAddress != lan.Address ||
		h.TURNURLs != "turn:turn.lab.example.com:443?transport=udp,turns:turn.lab.example.com:5349?transport=tcp" {
		t.Errorf("http-proxy: %+v", h)
	}
	cfg.FrontDoor = FrontDoorConfig{Kind: FrontDoorHomeOnly}
	ho := FrontDoorFor(cfg, lan)
	if ho.SNIAddress != lan.Address || ho.TrustedProxies != SNIContainerName || ho.ComposeProfiles != FrontDoorLinx443 ||
		ho.DNSAddress != "192.168.1.20" || ho.TURNUDPAddress.String() != "127.0.0.1" {
		t.Errorf("home-only: %+v", ho)
	}
}

func TestFrontDoorFiles(t *testing.T) {
	linx := netip.MustParseAddr("192.168.1.20")
	tr := string(PangolinTraefik("lab.example.com", linx))
	for _, want := range []string{
		"rule: \"HostSNI(`lab.example.com`)\"",
		"rule: \"HostSNI(`turn.lab.example.com`)\"",
		"passthrough: true",
		"entryPoints: [websecure]",
		`- address: "192.168.1.20:8443"`,
		`- address: "192.168.1.20:5349"`,
		"serversTransport: linx-proxy-protocol",
		"version: 2",
		"linx-lab-example-com-web:",
		"service: linx-lab-example-com-turn",
	} {
		if !strings.Contains(tr, want) {
			t.Errorf("Traefik file missing %q:\n%s", want, tr)
		}
	}
	// A second Linx behind the same Pangolin gets its own names (found in
	// the install demo: both used linx-web, so one replaced the other).
	if other := string(PangolinTraefik("home.example.com", linx)); strings.Contains(other, "linx-lab-example-com") ||
		strings.Contains(tr, "linx-web:") {
		t.Errorf("names shared between two Linx servers:\n%s", other)
	}
	if strings.Contains(tr, "insecureSkipVerify") {
		t.Error("the Traefik file must never turn certificate checks off")
	}
	hp := string(HAProxyConfig("lab.example.com"))
	for _, want := range []string{
		"mode tcp", "bind :443", "req_ssl_sni -i turn.lab.example.com",
		"server control-plane control-plane:8443 send-proxy-v2", "server coturn coturn:5349",
		"resolvers docker",
		// Sized for a small office (low-resource rule): about 13 MB, not 40-100.
		"maxconn 2000", "nbthread 1",
	} {
		if !strings.Contains(hp, want) {
			t.Errorf("HAProxy config missing %q:\n%s", want, hp)
		}
	}

	cfg := DefaultConfig()
	cfg.Domain.Name = "lab.example.com"
	lan := LAN{Address: linx, Network: netip.MustParsePrefix("192.168.1.0/24")}
	// No front door: only the desk phones' name, at the home address.
	if f, d := FrontDoorPlan(cfg, lan); f != nil || len(d) != 1 || !strings.HasSuffix(d[0].Cmd.String(), "certd -records sip=192.168.1.20") ||
		d[0].Title != "Point sip.lab.example.com at 192.168.1.20 (for desk phones at home) (DNS)" {
		t.Errorf("no front door: files %v, dns %+v", f, d)
	}
	if env := string(stackDotEnv(cfg, "abc", lan)); !strings.Contains(env, "LINX_DNS_RECORDS=sip=192.168.1.20\n") {
		t.Errorf("no front door, but certd wouldn't keep sip.:\n%s", env)
	}
	// DuckDNS can't: every name has the domain's address.
	duck := cfg
	duck.Domain = DomainConfig{Name: "me.duckdns.org", DNSProvider: DNSDuckDNS}
	if _, d := FrontDoorPlan(duck, lan); d != nil {
		t.Errorf("DuckDNS, no front door: %+v", d)
	}
	// Any other of the ten can (ADR-063).
	pork := cfg
	pork.Domain.DNSProvider = "porkbun"
	if got := DNSRecords(pork, lan); got != "sip=192.168.1.20" {
		t.Errorf("Porkbun at home: %q", got)
	}
	// Stop: the records are the owner's, the key still gets the certificate.
	pork.Domain.DNSByHand = true
	if _, d := FrontDoorPlan(pork, lan); d != nil || DNSRecords(pork, lan) != "" ||
		!strings.Contains(string(stackDotEnv(pork, "abc", lan)), "LINX_DNS_RECORDS=\n") {
		t.Errorf("by hand: %+v %q", d, DNSRecords(pork, lan))
	}
	back, err := ParseConfig(bytes.NewReader(pork.Marshal()))
	if err != nil || !back.Domain.DNSByHand || back.Domain.DNSProvider != "porkbun" {
		t.Errorf("setup.yaml round trip: %+v %v", back.Domain, err)
	}
	cfg.FrontDoor = FrontDoorConfig{Kind: FrontDoorPangolin, ProxyAddress: "192.168.1.30"}
	files, dns := FrontDoorPlan(cfg, lan)
	if len(files) != 5 || len(dns) != 1 || !strings.Contains(dns[0].Cmd.String(), "certd -records @,turn") ||
		files[4].File.Path != FrontDoorStepsFile {
		t.Errorf("pangolin plan: %+v %+v", files, dns)
	}
	steps := string(files[4].File.Data)
	for _, want := range []string{PangolinTraefikFile, CaddyLayer4File, "UDP port 443 to this server (192.168.1.20)", "http3",
		"sudo linx doctor", "lab.example.com  ->  192.168.1.20:8443", "Linx only accepts it from 192.168.1.30"} {
		if !strings.Contains(steps, want) {
			t.Errorf("steps missing %q:\n%s", want, steps)
		}
	}
	env := string(stackDotEnv(cfg, "abc", lan))
	for _, want := range []string{"LINX_TRUSTED_PROXIES=192.168.1.30\n", "LINX_WEB_ADDRESS=192.168.1.20\n",
		"LINX_TURN_UDP_ADDRESS=192.168.1.20\n", "LINX_TURN_UDP_PORT=443\n", "COMPOSE_PROFILES=\n",
		"LINX_PROXY_PROTOCOL=true\n", "LINX_DNS_RECORDS=@,turn,sip=192.168.1.20\n", "LINX_DNS_ADDRESS=\n"} {
		if !strings.Contains(env, want) {
			t.Errorf(".env missing %q:\n%s", want, env)
		}
	}
}

func TestFrontDoorFilesMore(t *testing.T) {
	linx := netip.MustParseAddr("192.168.1.20")
	ng := string(NginxStream("lab.example.com", linx))
	for _, want := range []string{"stream {", "map $ssl_preread_server_name $linx_route", "lab.example.com       192.168.1.20:8443;",
		"turn.lab.example.com  127.0.0.1:4445;", "listen 443;", "proxy_protocol on;", "listen 127.0.0.1:4445 proxy_protocol;",
		"proxy_pass 192.168.1.20:5349;"} {
		if !strings.Contains(ng, want) {
			t.Errorf("nginx stream missing %q:\n%s", want, ng)
		}
	}
	hs := string(HAProxySnippet("lab.example.com", linx))
	if !strings.Contains(hs, "server linx 192.168.1.20:8443 send-proxy-v2") || !strings.Contains(hs, "server linx 192.168.1.20:5349\n") {
		t.Errorf("HAProxy snippet:\n%s", hs)
	}
	cd := string(CaddyConfig("lab.example.com", linx))
	for _, want := range []string{"\nlab.example.com {", "reverse_proxy https://192.168.1.20:8443", "tls_server_name lab.example.com", "header_up Host {host}"} {
		if !strings.Contains(cd, want) {
			t.Errorf("Caddyfile missing %q:\n%s", want, cd)
		}
	}
	if strings.Contains(cd, "insecure") || strings.Contains(HTTPProxySteps("x.example.com", linx, 443), "proxy_ssl_verify off") {
		t.Error("an HTTP proxy's settings must keep checking Linx's certificate")
	}
	if !strings.Contains(HTTPProxySteps("lab.example.com", linx, 443), "TCP 5349") {
		t.Error("HTTP proxy steps don't mention forwarding 5349")
	}

	cfg := DefaultConfig()
	cfg.Domain.Name = "lab.example.com"
	lan := LAN{Address: linx, Network: netip.MustParsePrefix("192.168.1.0/24")}
	cfg.FrontDoor = FrontDoorConfig{Kind: FrontDoorHomeOnly}
	files, dns := FrontDoorPlan(cfg, lan)
	if len(files) != 1 || files[0].File.Path != HAProxyConfigFile || !strings.HasSuffix(dns[0].Cmd.String(), "-records @,turn,sip=192.168.1.20 -address 192.168.1.20") {
		t.Errorf("home-only plan: %+v / %s", files, dns[0].Cmd)
	}
	for _, k := range []string{FrontDoorProxy, FrontDoorNginx, FrontDoorHTTPProxy} {
		cfg.FrontDoor = FrontDoorConfig{Kind: k, ProxyAddress: "192.168.1.30"}
		files, _ := FrontDoorPlan(cfg, lan)
		if len(files) < 2 || StepsFile(k) == "" || files[len(files)-1].File.Path != StepsFile(k) {
			t.Errorf("%s plan: %+v", k, files)
		}
	}
}

// The front-door card (docs/ui/SCREENS_PHASE1F.md §1.2): the three facts,
// and a guide per product, the same whichever product was chosen.
func TestDoorCard(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Domain.Name = "lab.example.com"
	lan := LAN{Address: netip.MustParseAddr("192.168.1.20"), Network: netip.MustParsePrefix("192.168.1.0/24")}
	if DoorCard(cfg, lan) != nil {
		t.Error("a card with no front door")
	}
	var ids []string
	for _, k := range []string{FrontDoorProxy, FrontDoorPangolin, FrontDoorNginx} {
		cfg.FrontDoor = FrontDoorConfig{Kind: k, ProxyAddress: "192.168.1.30"}
		c := DoorCard(cfg, lan)
		if len(c.Routes) != 2 || c.Routes[0] != (install.DoorRoute{Name: "lab.example.com", Address: "192.168.1.20:8443", ProxyProtocol: true}) ||
			c.Routes[1] != (install.DoorRoute{Name: "turn.lab.example.com", Address: "192.168.1.20:5349"}) || c.Proxy != "192.168.1.30" {
			t.Errorf("%s: facts %+v", k, c)
		}
		ids = ids[:0]
		for _, g := range c.Guides {
			ids = append(ids, g.ID)
		}
		if strings.Join(ids, ",") != "pangolin,nginx,haproxy,caddy,npm,router" {
			t.Errorf("%s: guides %v", k, ids)
		}
		if want := map[string]string{FrontDoorPangolin: GuidePangolin, FrontDoorNginx: GuideNginx}[k]; c.Pick != want {
			t.Errorf("%s: picks %q", k, c.Pick)
		}
	}
	cfg.FrontDoor = FrontDoorConfig{Kind: FrontDoorHTTPProxy, ProxyAddress: "192.168.1.30"}
	if DoorCard(cfg, lan) != nil || DoorSetup(cfg, lan).Card != nil {
		t.Error("a proxy that decrypts gets the pass-through card")
	}

	cd := string(CaddyLayer4("lab.example.com", lan.Address, 443))
	for _, want := range []string{"listener_wrappers {", "layer4 {", "@linx_web tls sni lab.example.com", "proxy_protocol v2",
		"upstream 192.168.1.20:8443", "@linx_turn tls sni turn.lab.example.com", "proxy 192.168.1.20:5349", "\t\t\ttls\n", "protocols h1 h2"} {
		if !strings.Contains(cd, want) {
			t.Errorf("Caddy layer4 block missing %q:\n%s", want, cd)
		}
	}
}

// linx-sni reads its settings only at start: an update that changes them
// restarts it, an ordinary re-run doesn't (found in the 2026-09-29 demo).
func TestSNIRestartPlan(t *testing.T) {
	var c Config
	c.Domain.Name = "vps.example.com"
	c.FrontDoor.Kind = FrontDoorLinx443
	same := func(string) ([]byte, error) { return HAProxyConfig("vps.example.com"), nil }
	old := func(string) ([]byte, error) { return []byte("global\n"), nil }
	if p := SNIRestartPlan(c, LAN{}, same); p != nil {
		t.Errorf("unchanged settings restart it: %v", p)
	}
	if p := SNIRestartPlan(c, LAN{}, old); len(p) != 1 || !strings.Contains(p[0].Cmd.String(), "--force-recreate sni") {
		t.Errorf("changed settings: %v", p)
	}
	c.FrontDoor.Kind = FrontDoorPangolin
	if p := SNIRestartPlan(c, LAN{}, old); p != nil {
		t.Errorf("no linx-sni behind Pangolin: %v", p)
	}
}

// TestFrontDoorPublicPort: another public port (docs/SIMPLER.md §2.5):
// linx-sni published there, coturn's UDP on its own port, both advertised
// to browsers, and 8443 taken over from the web port.
func TestFrontDoorPublicPort(t *testing.T) {
	lan := LAN{Address: netip.MustParseAddr("192.168.1.20"), Network: netip.MustParsePrefix("192.168.1.0/24")}
	cfg := DefaultConfig()
	cfg.Domain.Name = "lab.example.com"
	cfg.FrontDoor = FrontDoorConfig{Kind: FrontDoorPublicPort, PublicPort: 9443}
	if a := cfg.Address(); a != "https://lab.example.com:9443" {
		t.Errorf("Address = %q", a)
	}
	home := FrontDoorFor(cfg, lan)
	if home.SNIAddress != lan.Address || home.SNIPort != 9443 || home.TURNUDPAddress != lan.Address || home.TURNUDPPort != 443 ||
		home.TrustedProxies != SNIContainerName || home.ComposeProfiles != FrontDoorLinx443 || home.WebHostPort != "8443" ||
		home.TURNURLs != "turn:turn.lab.example.com:443?transport=udp,turns:turn.lab.example.com:9443?transport=tcp" {
		t.Errorf("public port 9443 at home: %+v", home)
	}
	if fw := string(FirewallRuleset(lan, home)); !strings.Contains(fw, "tcp dport { 8443, 5349 } counter drop") {
		t.Errorf("9443: the web port is no longer guarded:\n%s", fw)
	}

	cfg.FrontDoor = FrontDoorConfig{Kind: FrontDoorPublicPort, PublicPort: 8443, TURNUDPPort: 3478}
	vps := FrontDoorFor(cfg, LAN{})
	if vps.SNIAddress.String() != "0.0.0.0" || vps.SNIPort != 8443 || vps.TURNUDPPort != 3478 || vps.WebHostPort != "" ||
		vps.TURNURLs != "turn:turn.lab.example.com:3478?transport=udp,turns:turn.lab.example.com:8443?transport=tcp" {
		t.Errorf("public port 8443 on a rented server: %+v", vps)
	}
	fw := string(FirewallRuleset(LAN{}, vps))
	if strings.Contains(fw, "dport { 8443") || !strings.Contains(fw, "tcp dport 5349 counter drop") {
		t.Errorf("8443 is the public port, yet the firewall drops it:\n%s", fw)
	}
	env := string(stackDotEnv(cfg, "v1", LAN{}))
	for _, want := range []string{"\nLINX_PUBLIC_PORT=8443\n", "\nLINX_WEB_HOST_PORT=\n", "\nLINX_TURN_UDP_PORT=3478\n", "\nLINX_SNI_ADDRESS=0.0.0.0\n"} {
		if !strings.Contains(env, want) {
			t.Errorf(".env misses %q:\n%s", want, env)
		}
	}
	if CertMode(FrontDoorPublicPort) != install.CertToken {
		t.Error("another public port gets its certificate through port 443")
	}
	if s := DoorSetup(cfg, LAN{}); len(s.Steps) != 1 || !strings.Contains(s.Steps[0], "opens TCP 8443 and UDP 3478 on this server") {
		t.Errorf("rented server steps: %+v", s)
	}
	if s := DoorSetup(cfg, lan); len(s.Steps) != 3 || s.Steps[0] != "On your router, forward TCP 8443 to 192.168.1.20 port 8443." ||
		s.Steps[1] != "On your router, forward UDP 3478 to 192.168.1.20 port 3478." {
		t.Errorf("home steps: %+v", s)
	}

	// Saved and read back the same; certificates need the DNS key.
	cfg.Certificates = CertificateConfig{Email: "me@example.com"}
	back, err := ParseConfig(bytes.NewReader(cfg.Marshal()))
	if err != nil || back.FrontDoor != cfg.FrontDoor {
		t.Errorf("read back %+v, %v", back.FrontDoor, err)
	}
	cfg.Certificates.NoDNSToken = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "no_dns_token") {
		t.Errorf("no DNS key with another public port: %v", err)
	}

	// Linx-443 stays as it was.
	cfg.FrontDoor = FrontDoorConfig{Kind: FrontDoorLinx443}
	if l := FrontDoorFor(cfg, LAN{}); l.SNIPort != 443 || l.WebHostPort != "8443" || l.TURNURLs != "" {
		t.Errorf("linx-443: %+v", l)
	}
	if a := cfg.Address(); a != "https://lab.example.com" {
		t.Errorf("Address = %q", a)
	}
}

func TestFrontDoorChoicePublicPort(t *testing.T) {
	for _, tc := range []struct {
		port, udp int
		field     string
	}{
		{8443, 0, ""},
		{8443, 3478, ""},
		{0, 0, "public_port"},
		{443, 0, "public_port"},
		{5349, 0, "public_port"},
		{8443, 80, "turn_udp_port"},
	} {
		fd, errs := FrontDoorChoice(install.WhereRented, FrontDoorPublicPort, "", tc.udp, tc.port, LAN{})
		switch {
		case tc.field == "" && len(errs) > 0:
			t.Errorf("%d/%d: %+v", tc.port, tc.udp, errs)
		case tc.field == "" && (fd.PublicPort != tc.port || fd.TURNUDPPort != tc.udp):
			t.Errorf("%d/%d: got %+v", tc.port, tc.udp, fd)
		case tc.field != "" && (len(errs) != 1 || errs[0].Field != tc.field):
			t.Errorf("%d/%d: want a %s refusal, got %+v", tc.port, tc.udp, tc.field, errs)
		}
	}
}

// TestPublicPortWordsMatchWeb: the web pages say the owner's warning and
// the trade-offs in the same words as setup (web/src/lib/install.ts).
func TestPublicPortWordsMatchWeb(t *testing.T) {
	b, err := os.ReadFile("../../web/src/lib/install.ts")
	if err != nil {
		t.Fatal(err)
	}
	// The TypeScript strings are split over lines with "a " + "b".
	web := regexp.MustCompile(`"\s*\+\s*"`).ReplaceAllString(string(b), "")
	for _, w := range append([]string{PublicPortWarning}, PublicPortWarnings...) {
		if !strings.Contains(web, `"`+w+`"`) {
			t.Errorf("web/src/lib/install.ts doesn't say, word for word:\n%s", w)
		}
	}
}
