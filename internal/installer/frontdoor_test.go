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
		{FrontDoorConfig{Kind: FrontDoorNone}, true},
		{FrontDoorConfig{Kind: FrontDoorHomeOnly}, true},
		{FrontDoorConfig{Kind: FrontDoorNginx, ProxyAddress: "192.168.1.20"}, true}, // on this server
		{FrontDoorConfig{Kind: FrontDoorHTTPProxy, ProxyAddress: "192.168.1.40"}, true},
		{FrontDoorConfig{Kind: FrontDoorHTTPProxy}, false},
		{FrontDoorConfig{Kind: FrontDoorHomeOnly, ProxyAddress: "192.168.1.30"}, false},
		{FrontDoorConfig{Kind: FrontDoorLinx443}, true},
		{FrontDoorConfig{Kind: FrontDoorPangolin, ProxyAddress: "192.168.1.30"}, true},
		{FrontDoorConfig{Kind: FrontDoorPangolin}, false},
		{FrontDoorConfig{Kind: FrontDoorPangolin, ProxyAddress: "203.0.113.5"}, false}, // not at home
		{FrontDoorConfig{Kind: FrontDoorPangolin, ProxyAddress: "fd00::1"}, false},
		{FrontDoorConfig{Kind: FrontDoorLinx443, ProxyAddress: "192.168.1.30"}, false},
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
	if steps := PangolinSteps("lab.example.com", lan.Address, 3478); !strings.Contains(steps, "UDP 3478 to this Linx server") ||
		strings.Contains(steps, "remove the") {
		t.Errorf("pangolin steps, UDP 3478:\n%s", steps)
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
	steps := PangolinSteps("lab.example.com", linx, 443)
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
		t.Error("no front door writes front door files")
	}
	if env := string(stackDotEnv(cfg, "abc", lan)); !strings.Contains(env, "LINX_DNS_RECORDS=\n") {
		t.Errorf("no front door, but certd would manage DNS:\n%s", env)
	}
	cfg.FrontDoor = FrontDoorConfig{Kind: FrontDoorPangolin, ProxyAddress: "192.168.1.30"}
	files, dns := FrontDoorPlan(cfg, lan)
	if len(files) != 2 || len(dns) != 1 || !strings.Contains(dns[0].Cmd.String(), "certd -records meet,api,turn") {
		t.Errorf("pangolin plan: %+v %+v", files, dns)
	}
	env := string(stackDotEnv(cfg, "abc", lan))
	for _, want := range []string{"LINX_TRUSTED_PROXIES=192.168.1.30\n", "LINX_WEB_ADDRESS=192.168.1.20\n",
		"LINX_TURN_UDP_ADDRESS=192.168.1.20\n", "LINX_TURN_UDP_PORT=443\n", "COMPOSE_PROFILES=\n",
		"LINX_PROXY_PROTOCOL=true\n", "LINX_DNS_RECORDS=meet,api,turn\n", "LINX_DNS_ADDRESS=\n"} {
		if !strings.Contains(env, want) {
			t.Errorf(".env missing %q:\n%s", want, env)
		}
	}
}

func TestFrontDoorFilesMore(t *testing.T) {
	linx := netip.MustParseAddr("192.168.1.20")
	ng := string(NginxStream("lab.example.com", linx))
	for _, want := range []string{"stream {", "map $ssl_preread_server_name $linx_route", "meet.lab.example.com  192.168.1.20:8443;",
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
	for _, want := range []string{"meet.lab.example.com, api.lab.example.com {", "reverse_proxy https://192.168.1.20:8443", "tls_server_name meet.lab.example.com", "header_up Host {host}"} {
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
	if len(files) != 1 || files[0].File.Path != HAProxyConfigFile || !strings.HasSuffix(dns[0].Cmd.String(), "-records meet,api,turn -address 192.168.1.20") {
		t.Errorf("home-only plan: %+v / %s", files, dns[0].Cmd)
	}
	for _, k := range []string{FrontDoorNginx, FrontDoorHTTPProxy} {
		cfg.FrontDoor = FrontDoorConfig{Kind: k, ProxyAddress: "192.168.1.30"}
		files, _ := FrontDoorPlan(cfg, lan)
		if len(files) < 2 || StepsFile(k) == "" || files[len(files)-1].File.Path != StepsFile(k) {
			t.Errorf("%s plan: %+v", k, files)
		}
	}
}
