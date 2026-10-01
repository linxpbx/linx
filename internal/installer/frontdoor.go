package installer

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/dnsname"
	"linxpbx.com/linx/internal/weburl"
)

// Front doors (docs/WEB.md §3, ADR-040): what sits between the internet and
// Linx. Those that can pass TLS through by name (Pangolin's Traefik, nginx
// or HAProxy, Linx's own HAProxy) do so without decrypting it (Linx
// terminates TLS with its own certificate) and tell Linx the visitor's
// address with a PROXY protocol v2 header; TURN over TLS goes through the
// same port 443 by name. An HTTP-only proxy decrypts and re-encrypts to
// Linx, checking Linx's certificate, and sends X-Forwarded-For; TURN over
// TLS then has its own port. TURN over UDP always goes straight to coturn.
const (
	// FrontDoorNone: nothing is published (the default until the owner
	// picks one).
	FrontDoorNone = "none"
	// FrontDoorProxy: another program that already uses port 443 (Pangolin,
	// nginx, HAProxy, Caddy with its layer-4 add-on, ...) passes <domain>
	// and turn.<domain> through by name, without decrypting, and sends
	// PROXY v2 for the first (docs/SIMPLER.md §2, ADR-062). Linx's side is
	// the same whatever the product; the front-door card (DoorCard) shows
	// how to do it in each. The router forwards UDP 443 (or turn_udp_port)
	// straight to Linx.
	FrontDoorProxy = "proxy"
	// FrontDoorPangolin: Pangolin on the home network (the router forwards
	// TCP 443 to it) passes <domain> and turn.<domain> through by name (a block
	// for its Traefik that Linx generates). The router forwards UDP 443
	// (or turn_udp_port) straight to Linx.
	// Setups from before the card (ADR-062) name it; it works exactly like
	// FrontDoorProxy and is no longer offered.
	FrontDoorPangolin = "pangolin"
	// FrontDoorNginx: nginx or HAProxy already on port 443, on this server
	// or another one at home, passes the names through (generated stream
	// config); the router forwards UDP 443 to Linx. Like FrontDoorPangolin,
	// kept for older setups and no longer offered.
	FrontDoorNginx = "nginx"
	// FrontDoorHTTPProxy: a proxy that decrypts (Caddy or Nginx Proxy
	// Manager as a website proxy; advanced, home only since ADR-062) forwards <domain> to Linx over HTTPS; the router
	// forwards TCP 5349 (TURN over TLS) and UDP 443 to Linx.
	FrontDoorHTTPProxy = "http-proxy"
	// FrontDoorLinx443: Linx's own HAProxy (linx-sni) owns TCP 443 (ADR-009)
	// and coturn UDP 443.
	FrontDoorLinx443 = "linx-443"
	// FrontDoorPublicPort: like FrontDoorLinx443, but nothing here can pass
	// Linx through on public 443, so the router (or, on a rented server,
	// Docker) brings another public port (PublicPort, e.g. 8443) to
	// linx-sni, and UDP 443 or TURNUDPPort to coturn (docs/SIMPLER.md
	// §2.5, ADR-064). Advanced: networks that allow only 443 may block it.
	// Certificates then need a DNS key (TLS-ALPN-01 checks port 443).
	FrontDoorPublicPort = "public-port"
	// FrontDoorHomeOnly: linx-sni answers on the home network's address and
	// the names point there: https://<domain> works at home only, with
	// no router changes.
	FrontDoorHomeOnly = "home-only"
)

// FrontDoors lists the choices, as setup offers them.
var FrontDoors = []string{FrontDoorLinx443, FrontDoorProxy, FrontDoorHomeOnly, FrontDoorHTTPProxy, FrontDoorPublicPort, FrontDoorNone}

// frontDoorsAccepted are FrontDoors and the older names setup.yaml may still have.
var frontDoorsAccepted = append([]string{FrontDoorPangolin, FrontDoorNginx}, FrontDoors...)

// FrontDoorDescription is each choice in plain words.
var FrontDoorDescription = map[string]string{
	FrontDoorProxy:     "another program already uses port 443 and passes Linx through (Pangolin, nginx, HAProxy, Caddy, ...)",
	FrontDoorPangolin:  "Pangolin, on this home network (your router sends port 443 to it)",
	FrontDoorNginx:     "nginx or HAProxy that already uses port 443, here or on another machine at home",
	FrontDoorHTTPProxy: "advanced: my proxy must unlock the traffic itself (Caddy or Nginx Proxy Manager as a website proxy; not recommended)",
	FrontDoorLinx443:   "nothing: Linx takes port 443 itself (your router, or this VPS, sends port 443 here)",
	FrontDoorHomeOnly:  "nothing, and only at home: Linx answers on this home network only",
	FrontDoorPublicPort: "advanced: nothing here can pass Linx through on port 443, so use another public port, like 8443 " +
		"(some networks block it: port 443 is always the recommended choice)",
	FrontDoorNone: "not now: no calls from outside, and no web address",
}

// proxyKinds are the front doors that are another program at ProxyAddress.
var proxyKinds = []string{FrontDoorProxy, FrontDoorPangolin, FrontDoorNginx, FrontDoorHTTPProxy}

// PassesThrough reports whether kind is another program passing Linx
// through by name (FrontDoorProxy, or its older names).
func PassesThrough(kind string) bool {
	return kind == FrontDoorProxy || kind == FrontDoorPangolin || kind == FrontDoorNginx
}

// FrontDoorConfig is setup.yaml's front_door.
type FrontDoorConfig struct {
	// Kind is one of FrontDoors.
	Kind string `yaml:"kind"`
	// ProxyAddress is, for Pangolin, nginx/HAProxy or an HTTP-only proxy,
	// the home-network address of the machine it runs on (this server's
	// own, if it runs here): the only address allowed to reach Linx's web
	// port, and whose PROXY headers or X-Forwarded-For Linx believes.
	ProxyAddress string `yaml:"proxy_address"`
	// TURNUDPPort is, for those same front doors and FrontDoorPublicPort,
	// the UDP port the router forwards to Linx for call audio (0: 443).
	// Some routers (UniFi) won't forward UDP 443 to one machine while TCP
	// 443 goes to another; 3478, the usual port for this, works instead.
	TURNUDPPort int `yaml:"turn_udp_port,omitempty"`
	// PublicPort is, for FrontDoorPublicPort only, the public TCP port
	// people's browsers use (https://<domain>:<port>).
	PublicPort int `yaml:"public_port,omitempty"`
}

// Port is the public TCP port browsers use: 443 unless FrontDoorPublicPort.
func (f FrontDoorConfig) Port() int {
	if f.Kind == FrontDoorPublicPort && f.PublicPort != 0 {
		return f.PublicPort
	}
	return PublicPort
}

// udpPortKinds may set TURNUDPPort.
func udpPortKinds() []string { return append(slices.Clone(proxyKinds), FrontDoorPublicPort) }

// UDPPort is the public UDP port for call audio.
func (f FrontDoorConfig) UDPPort() int {
	if f.TURNUDPPort == 0 {
		return PublicPort
	}
	return f.TURNUDPPort
}

// ValidateTURNUDPPort checks a UDP port for call audio: 443, or an
// unprivileged one Linx doesn't already use on the home network.
func ValidateTURNUDPPort(p int) error {
	switch {
	case p == PublicPort:
		return nil
	case p < 1024 || p > 65535:
		return fmt.Errorf("%d: use 443, or a port from 1024 to 65535 (3478 is the usual one)", p)
	case p == 5060, p == 5061, p == TURNTLSPort, p == WebPort, p >= 10000 && p <= 10199:
		return fmt.Errorf("%d: Linx already uses that port (3478 is the usual one)", p)
	}
	return nil
}

// NeedsProxyAddress reports whether kind is another program at an address.
func NeedsProxyAddress(kind string) bool { return slices.Contains(proxyKinds, kind) }

// Validate checks the front door settings.
func (f FrontDoorConfig) Validate() error {
	if !slices.Contains(frontDoorsAccepted, f.Kind) {
		return fmt.Errorf("kind: must be one of %v, got %q", FrontDoors, f.Kind)
	}
	if NeedsProxyAddress(f.Kind) {
		if err := ValidateProxyAddress(f.ProxyAddress); err != nil {
			return fmt.Errorf("proxy_address: %w", err)
		}
	} else if f.ProxyAddress != "" {
		return fmt.Errorf("proxy_address: only used with kind %s", strings.Join(proxyKinds, ", "))
	}
	if f.TURNUDPPort != 0 {
		if !slices.Contains(udpPortKinds(), f.Kind) {
			return fmt.Errorf("turn_udp_port: only used with kind %s", strings.Join(udpPortKinds(), ", "))
		}
		if err := ValidateTURNUDPPort(f.TURNUDPPort); err != nil {
			return fmt.Errorf("turn_udp_port: %w", err)
		}
	}
	switch {
	case f.Kind == FrontDoorPublicPort && f.PublicPort == 0:
		return errors.New("public_port: required with kind " + FrontDoorPublicPort + " (8443, for example)")
	case f.Kind == FrontDoorPublicPort:
		if msg := weburl.Problem(f.PublicPort); msg != "" {
			return errors.New("public_port: " + msg)
		}
		if f.PublicPort == PublicPort {
			return errors.New("public_port: 443 is kind " + FrontDoorLinx443)
		}
	case f.PublicPort != 0:
		return errors.New("public_port: only used with kind " + FrontDoorPublicPort)
	}
	return nil
}

// ValidateProxyAddress checks the front door machine's home-network address.
func ValidateProxyAddress(s string) error {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil || !a.Is4() {
		return fmt.Errorf("%q isn't an IPv4 address like 192.168.1.30", s)
	}
	if !a.IsPrivate() {
		return errors.New(a.String() + " isn't a home-network address (a proxy on a VPS, e.g. Pangolin with Newt, comes in a later Linx update)")
	}
	return nil
}

// Container ports the front door reaches (compose.yaml).
const (
	WebPort     = 8443 // the control plane's HTTPS
	TURNTLSPort = 5349 // coturn's TLS
	TURNUDPPort = 3478 // coturn's UDP
	// PublicPort is the only port most front doors and routers need: 443,
	// TCP and UDP.
	PublicPort = 443
	// SNIContainerName is the Linx-takes-443 HAProxy, trusted by name.
	SNIContainerName = "linx-sni"
)

// FrontDoorSettings is what a front door changes on this server.
type FrontDoorSettings struct {
	// TrustedProxies is LINX_TRUSTED_PROXIES: whose PROXY headers (or, for
	// an HTTP-only proxy, X-Forwarded-For) the control plane believes.
	TrustedProxies string
	// ProxyProtocol is LINX_PROXY_PROTOCOL: whether trusted proxies send
	// PROXY headers (required then) or X-Forwarded-For.
	ProxyProtocol bool
	// WebAddress is where 8443 (web) and 5349 (TURN over TLS) are
	// published on this server; 127.0.0.1 keeps them unpublished.
	WebAddress netip.Addr
	// WebClients may reach 8443 (the firewall drops everyone else); also
	// 5349 unless TURNTLSOpen.
	WebClients []netip.Addr
	// TURNTLSOpen: 5349 is reached from the internet (the router forwards
	// it), not through the front door.
	TURNTLSOpen bool
	// TURNUDPAddress:TURNUDPPort is where coturn's UDP is published.
	TURNUDPAddress netip.Addr
	TURNUDPPort    int
	// TURNURLs is LINX_TURN_URLS ("": the default, turn.<domain>:443).
	TURNURLs string
	// SNIAddress:SNIPort is where linx-sni (listening on 443) is published.
	SNIAddress netip.Addr
	SNIPort    int
	// WebHostPort is LINX_WEB_HOST_PORT, the host port the web port is
	// published on: 8443, or "" (a free one Docker picks, on 127.0.0.1)
	// when linx-sni takes 8443 itself as the public port.
	WebHostPort string
	// ComposeProfiles turns on optional services (linx-sni).
	ComposeProfiles string
	// DNSAddress is where the public names point: "" follows this
	// network's public address (linx-certd), else this address.
	DNSAddress string
}

var loopback = netip.AddrFrom4([4]byte{127, 0, 0, 1})

// FrontDoorFor works out a front door's settings on this server.
func FrontDoorFor(c Config, lan LAN) FrontDoorSettings {
	s := FrontDoorSettings{ProxyProtocol: true, WebAddress: loopback, TURNUDPAddress: loopback, TURNUDPPort: TURNUDPPort,
		SNIAddress: loopback, SNIPort: PublicPort, WebHostPort: strconv.Itoa(WebPort)}
	switch c.FrontDoor.Kind {
	case FrontDoorProxy, FrontDoorPangolin, FrontDoorNginx, FrontDoorHTTPProxy:
		p, _ := netip.ParseAddr(c.FrontDoor.ProxyAddress)
		s.TrustedProxies = p.String()
		s.WebAddress, s.WebClients = lan.BindAddress(), []netip.Addr{p}
		s.TURNUDPAddress, s.TURNUDPPort = lan.BindAddress(), c.FrontDoor.UDPPort()
		tlsPort := PublicPort
		if c.FrontDoor.Kind == FrontDoorHTTPProxy {
			s.ProxyProtocol, s.TURNTLSOpen = false, true
			tlsPort = TURNTLSPort
		}
		if s.TURNUDPPort != PublicPort || tlsPort != PublicPort {
			host := "turn." + c.Domain.Name
			s.TURNURLs = fmt.Sprintf("turn:%s:%d?transport=udp,turns:%s:%d?transport=tcp", host, s.TURNUDPPort, host, tlsPort)
		}
	case FrontDoorLinx443, FrontDoorPublicPort:
		s.TrustedProxies = SNIContainerName
		// Behind a home router the forwards arrive at the LAN address; on
		// a VPS (no LAN) on the public one.
		public := netip.IPv4Unspecified()
		if lan.OK() {
			public = lan.Address
		}
		s.SNIAddress, s.SNIPort = public, c.FrontDoor.Port()
		s.TURNUDPAddress, s.TURNUDPPort = public, c.FrontDoor.UDPPort()
		s.ComposeProfiles = FrontDoorLinx443
		if s.SNIPort == WebPort {
			// The web port then stays on 127.0.0.1 at a port Docker picks:
			// nothing on this server uses it (the health check runs inside).
			s.WebHostPort = ""
		}
		if s.SNIPort != PublicPort || s.TURNUDPPort != PublicPort {
			host := "turn." + c.Domain.Name
			s.TURNURLs = fmt.Sprintf("turn:%s:%d?transport=udp,turns:%s:%d?transport=tcp", host, s.TURNUDPPort, host, s.SNIPort)
		}
	case FrontDoorHomeOnly:
		// At home browsers send audio straight to Asterisk; the relay is
		// only reached over TLS through linx-sni, like everything else.
		s.TrustedProxies = SNIContainerName
		s.SNIAddress = lan.BindAddress()
		s.ComposeProfiles = FrontDoorLinx443
		s.DNSAddress = lan.BindAddress().String()
	}
	return s
}

// PublicHosts are the names the front door serves: the domain itself (the
// web app and the API) and turn. under it.
var PublicHosts = []string{dnsname.Apex, "turn"}

// PublicNames is PublicHosts for domain, in plain words.
func PublicNames(domain string) string { return dnsname.And(dnsname.Hosts(PublicHosts, domain)) }

// Front door files setup writes.
const (
	FrontDoorDir        = StackDir + "/front-door"
	FrontDoorStepsFile  = FrontDoorDir + "/FRONT-DOOR.txt"
	PangolinTraefikFile = FrontDoorDir + "/pangolin-dynamic-config.yml"
	NginxStreamFile     = FrontDoorDir + "/nginx-stream.conf"
	HAProxySnippetFile  = FrontDoorDir + "/haproxy-linx.cfg"
	CaddyLayer4File     = FrontDoorDir + "/Caddyfile-layer4"
	CaddyFile           = FrontDoorDir + "/Caddyfile"
	HTTPProxyStepsFile  = FrontDoorDir + "/HTTP-PROXY.txt"
	HAProxyConfigFile   = StackDir + "/haproxy.cfg"
)

// StepsFile is where a front door's plain-language steps are ("" if none).
func StepsFile(kind string) string {
	switch {
	case PassesThrough(kind):
		return FrontDoorStepsFile
	case kind == FrontDoorHTTPProxy:
		return HTTPProxyStepsFile
	}
	return ""
}

// FrontDoorPlan returns the front door's files, to write before the stack
// starts, and the DNS step (linx-certd -records, pointing its names at this
// network's public address, or the home address for home-only), to run
// once it's up.
func FrontDoorPlan(c Config, lan LAN) (files, dns Plan) {
	d, linx, udp := c.Domain.Name, lan.BindAddress(), c.FrontDoor.UDPPort()
	write := func(title, path string, data []byte) Step {
		return fileStep(title+" ("+path+")", path, data, 0o644, 0o755)
	}
	switch k := c.FrontDoor.Kind; {
	case PassesThrough(k):
		card := DoorCard(c, lan)
		files = Plan{
			write("Write the Pangolin settings to add", PangolinTraefikFile, PangolinTraefik(d, linx)),
			write("Write the nginx settings to add", NginxStreamFile, NginxStream(d, linx)),
			write("Write the HAProxy settings to add", HAProxySnippetFile, HAProxySnippet(d, linx)),
			write("Write the Caddy settings to add", CaddyLayer4File, CaddyLayer4(d, linx, udp)),
			write("Write the front door's steps", FrontDoorStepsFile, []byte(DoorCardText(card, routerStep(c.FrontDoor.ProxyAddress, linx, udp)))),
		}
	case k == FrontDoorHTTPProxy:
		files = Plan{
			write("Write the Caddy settings to add", CaddyFile, CaddyConfig(d, linx)),
			write("Write the proxy steps", HTTPProxyStepsFile, []byte(HTTPProxySteps(d, linx, udp))),
		}
	case k == FrontDoorLinx443, k == FrontDoorHomeOnly:
		files = Plan{write("Write the port 443 router's settings", HAProxyConfigFile, HAProxyConfig(d))}
	}
	if DNSRecords(c, lan) != "" {
		dns = Plan{recordsStep(c, lan)}
	}
	return files, dns
}

func recordsStep(c Config, lan LAN) Step {
	return cmdStep(RecordsTitle(c, lan), "docker", append([]string{"compose", "--file", stackFile, "run", "--rm", "certd"}, RecordsArgs(c, lan)...)...)
}

// RecordsTitle says in plain words which names go where.
func RecordsTitle(c Config, lan LAN) string {
	hosts, pinned, _ := certs.ParseRecords(DNSRecords(c, lan), certs.Hostnames)
	var parts []string
	if len(hosts) > 0 {
		where := "this network's public address"
		if a := FrontDoorFor(c, lan).DNSAddress; a != "" {
			where = a + " (this server, at home)"
		}
		parts = append(parts, strings.Join(dnsname.Hosts(hosts, c.Domain.Name), ", ")+" at "+where)
	}
	for _, p := range pinned {
		parts = append(parts, dnsname.Host(p.Host, c.Domain.Name)+" at "+p.Address.String()+" (for desk phones at home)")
	}
	return "Point " + strings.Join(parts, "; ") + " (DNS)"
}

// PangolinTraefik is the block to add to Pangolin's Traefik file-provider
// config (config/traefik/dynamic_config.yml): TCP routers on Pangolin's
// HTTPS entrypoint that pass <domain> and turn.<domain> through undecrypted, by
// name. Linx decrypts them itself, with its own certificate, so Pangolin
// never needs (or skips checking) one; the web one sends the visitor's
// address in a PROXY v2 header. TCP routers matching a name take
// precedence over Pangolin's HTTP routers. The routers and services are
// named after the domain (PangolinName), so a second Linx behind the same
// Pangolin adds its own next to the first's instead of replacing them; the
// PROXY transport is the same for every Linx and shared.
func PangolinTraefik(domain string, linx netip.Addr) []byte {
	name := PangolinName(domain)
	return fmt.Appendf(nil, `# Linx at %[1]s, passed through Pangolin's Traefik (docs/WEB.md §3). Generated by linx setup.
# Add this to Pangolin's config/traefik/dynamic_config.yml (see FRONT-DOOR.txt:
# if that file already has a "tcp:" line, merge into it; never add a second).
# Traefik picks it up by itself; nothing needs restarting.
tcp:
  routers:
    %[5]s-web:
      entryPoints: [websecure]
      rule: "HostSNI(`+"`%[1]s`"+`)"
      tls:
        passthrough: true
      service: %[5]s-web
    %[5]s-turn:
      entryPoints: [websecure]
      rule: "HostSNI(`+"`turn.%[1]s`"+`)"
      tls:
        passthrough: true
      service: %[5]s-turn
  services:
    %[5]s-web:
      loadBalancer:
        serversTransport: linx-proxy-protocol
        servers:
          - address: "%[2]s:%[3]d"
    %[5]s-turn:
      loadBalancer:
        servers:
          - address: "%[2]s:%[4]d"
  serversTransports:
    linx-proxy-protocol:
      proxyProtocol:
        version: 2
`, domain, linx, WebPort, TURNTLSPort, name)
}

// PangolinName is the name a Linx's Traefik routers and services start
// with: "linx-" and its domain with dashes for dots (linx-pbx-example-com).
func PangolinName(domain string) string {
	return "linx-" + strings.ReplaceAll(strings.ToLower(domain), ".", "-")
}

// HAProxyConfig is linx-sni's configuration (Linx takes 443 and home-only, ADR-009): TCP
// mode only. turn.<domain> goes to coturn's TLS port; everything else to the
// control plane with a PROXY v2 header. Server names are looked up through
// Docker's DNS at run time, so a recreated container is found again.
func HAProxyConfig(domain string) []byte {
	return fmt.Appendf(nil, `# Linx takes 443 (docs/WEB.md §3, ADR-009). Generated by linx setup; changes
# are overwritten when setup runs again.
global
    log stdout format raw local0 info
    # Sized for a small office, not the container's million-file limit:
    # about 13 MB instead of 40-100 (docs/RESOURCES.md). A pass-through
    # needs one thread; 2000 connections is plenty (each browser holds one
    # or two, each call from outside one more).
    maxconn 2000
    nbthread 1

defaults
    mode tcp
    log global
    option tcplog
    timeout connect 5s
    timeout client 1h
    timeout server 1h
    default-server init-addr last,libc,none resolvers docker

resolvers docker
    nameserver dns 127.0.0.11:53
    hold valid 10s

frontend https
    bind :443
    tcp-request inspect-delay 5s
    tcp-request content accept if { req_ssl_hello_type 1 }
    # Let's Encrypt's port 443 check (acme-tls/1) is answered by the control
    # plane for every name, turn. included (docs/INSTALL.md §5).
    use_backend web if { req.ssl_alpn -m str acme-tls/1 }
    use_backend turn if { req_ssl_sni -i turn.%[1]s }
    default_backend web

backend web
    server control-plane control-plane:%[2]d send-proxy-v2

backend turn
    server coturn coturn:%[3]d
`, domain, WebPort, TURNTLSPort)
}

// Internal hops the nginx stream config uses on the nginx machine.
const (
	nginxTurnHop  = "127.0.0.1:4445" // strips the PROXY header before coturn
	nginxSitesHop = "127.0.0.1:4443" // the owner's own HTTPS sites
)

// NginxStream is the stream block for an nginx that already owns port 443:
// ssl_preread picks the backend by name without decrypting. It sends PROXY
// v2 from its one 443 listener, so Linx's web gets the visitor's address;
// coturn can't read PROXY, so turn. goes through a local hop that drops it;
// the owner's own sites move to 127.0.0.1:4443 with proxy_protocol.
func NginxStream(domain string, linx netip.Addr) []byte {
	return fmt.Appendf(nil, `# Linx, passed through nginx by name (docs/WEB.md §3). Generated by linx setup.
# Goes in nginx.conf at the top level (next to http { }, not inside it).
# nginx needs the stream module (in nginx.org's and Debian's nginx packages).
stream {
    map $ssl_preread_server_name $linx_route {
        %[1]s       %[2]s:%[3]d;
        turn.%[1]s  %[4]s;
        default     %[5]s;
    }

    server {
        listen 443;
        ssl_preread on;
        proxy_protocol on;
        proxy_pass $linx_route;
    }

    # Relay over TLS: coturn can't read PROXY headers, so they're dropped here.
    server {
        listen %[4]s proxy_protocol;
        proxy_pass %[2]s:%[6]d;
    }
}
`, domain, linx, WebPort, nginxTurnHop, nginxSitesHop, TURNTLSPort)
}

// HAProxySnippet is the same for an HAProxy that already owns 443 (TCP mode).
func HAProxySnippet(domain string, linx netip.Addr) []byte {
	return fmt.Appendf(nil, `# Linx, passed through HAProxy by name (docs/WEB.md §3). Generated by linx setup.
# In your HAProxy frontend on port 443 (mode tcp, with
#   tcp-request inspect-delay 5s
#   tcp-request content accept if { req_ssl_hello_type 1 }
# ) add these two lines above your other use_backend lines:
#   use_backend linx_web if { req_ssl_sni -i %[1]s }
#   use_backend linx_turn if { req_ssl_sni -i turn.%[1]s }
# and add these backends:

backend linx_web
    mode tcp
    server linx %[2]s:%[3]d send-proxy-v2

backend linx_turn
    mode tcp
    server linx %[2]s:%[4]d
`, domain, linx, WebPort, TURNTLSPort)
}

// CaddyConfig is the site block for Caddy: HTTPS to Linx, checking Linx's
// certificate for <domain> (Caddy checks by default; tls_server_name
// makes it check the right name, since it connects by address). Caddy
// sends X-Forwarded-For itself, ignoring any a visitor sends. It would
// replace Host with Linx's address for an HTTPS backend; Linx's websockets
// check the page's origin against Host, so the visitor's Host is kept.
func CaddyConfig(domain string, linx netip.Addr) []byte {
	return fmt.Appendf(nil, `# Linx behind Caddy (docs/WEB.md §3). Generated by linx setup.
# Add to your Caddyfile, then: caddy reload (or restart Caddy's container).
%[1]s {
	reverse_proxy https://%[2]s:%[3]d {
		header_up Host {host}
		transport http {
			tls_server_name %[1]s
		}
	}
}
`, domain, linx, WebPort)
}

// HTTPProxySteps is what the owner does for a proxy that only does websites.
func HTTPProxySteps(domain string, linx netip.Addr, udpPort int) string {
	return fmt.Sprintf(`Linx behind a proxy that only does websites: what to do (generated by linx setup)
===============================================================================

People reach Linx at https://%[1]s from anywhere. Your proxy opens the
connection and passes it on to Linx over a new encrypted one. It must check
Linx's certificate on that second connection (the settings below do), and
Linx believes the visitor address it reports (X-Forwarded-For) from your
proxy's address only.

Linx needs a trusted certificate for this (certificates.staging: false in
/etc/linx/setup.yaml): your proxy won't accept Let's Encrypt test ones.

Caddy
  Add %[2]s to your Caddyfile, then reload Caddy.

Nginx Proxy Manager (or any nginx "location" setup)
  Add a proxy host for %[1]s:
    Scheme https, forward to %[3]s port %[4]d, turn on "Websockets Support".
    Under Advanced, paste:
        proxy_ssl_verify on;
        proxy_ssl_server_name on;
        proxy_ssl_name %[1]s;
        proxy_ssl_trusted_certificate /etc/ssl/certs/ca-certificates.crt;
        client_max_body_size 3g;
    (The last line lets a backup file of up to about 2 GB be uploaded.)

Anything else
  An HTTPS proxy to https://%[3]s:%[4]d for %[1]s, with websockets
  on, checking the certificate for %[1]s, keeping the visitor's Host
  header, and sending X-Forwarded-For.

On your router
  Keep TCP 443 going to your proxy. Forward to this Linx server (%[3]s):
    - TCP 5349 (the call relay over TLS: your proxy can't pass it on 443)
    - UDP %[5]d (smoother call audio)

DNS: setup pointed %[1]s and turn.%[1]s at your home's public address,
and keeps them there if it changes.

Check everything: sudo linx doctor ("Calls from outside").
`, domain, CaddyFile, linx, WebPort, udpPort)
}

// SNIRestartPlan restarts Linx's port 443 router when setup is about to
// write it settings that differ from the ones it runs with: it reads them
// only when it starts, and compose doesn't restart it for a changed file
// (found in the 2026-09-29 demo: an update wrote the small-office settings,
// and the router kept using 42 MB with the old ones). Nothing when they're
// the same, so an ordinary re-run doesn't drop the connections it carries.
func SNIRestartPlan(c Config, lan LAN, readFile func(string) ([]byte, error)) Plan {
	kind := FrontDoorFor(c, lan).ComposeProfiles
	if kind != FrontDoorLinx443 {
		return nil
	}
	// A missing file counts as changed (restarting a router that has just
	// started is harmless).
	if old, err := readFile(HAProxyConfigFile); err == nil && bytes.Equal(old, HAProxyConfig(c.Domain.Name)) {
		return nil
	}
	return Plan{cmdStep("Restart Linx's port 443 router with its new settings", "docker",
		"compose", "--file", stackFile, "up", "--detach", "--wait", "--force-recreate", "sni")}
}
