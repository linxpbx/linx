package installer

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
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
	// FrontDoorPangolin: Pangolin on the home network (the router forwards
	// TCP 443 to it) passes meet., api. and turn. through by name (a block
	// for its Traefik that Linx generates). The router forwards UDP 443
	// (or turn_udp_port) straight to Linx.
	FrontDoorPangolin = "pangolin"
	// FrontDoorNginx: nginx or HAProxy already on port 443, on this server
	// or another one at home, passes the names through (generated stream
	// config); the router forwards UDP 443 to Linx.
	FrontDoorNginx = "nginx"
	// FrontDoorHTTPProxy: a proxy that can only do HTTP (Caddy, Nginx Proxy
	// Manager, ...) forwards meet. and api. to Linx over HTTPS; the router
	// forwards TCP 5349 (TURN over TLS) and UDP 443 to Linx.
	FrontDoorHTTPProxy = "http-proxy"
	// FrontDoorLinx443: Linx's own HAProxy (linx-sni) owns TCP 443 (ADR-009)
	// and coturn UDP 443.
	FrontDoorLinx443 = "linx-443"
	// FrontDoorHomeOnly: linx-sni answers on the home network's address and
	// the names point there: https://meet.<domain> works at home only, with
	// no router changes.
	FrontDoorHomeOnly = "home-only"
)

// FrontDoors lists the choices, as setup offers them.
var FrontDoors = []string{FrontDoorPangolin, FrontDoorNginx, FrontDoorHTTPProxy, FrontDoorLinx443, FrontDoorHomeOnly, FrontDoorNone}

// FrontDoorDescription is each choice in plain words.
var FrontDoorDescription = map[string]string{
	FrontDoorPangolin:  "Pangolin, on this home network (your router sends port 443 to it)",
	FrontDoorNginx:     "nginx or HAProxy that already uses port 443, here or on another machine at home",
	FrontDoorHTTPProxy: "another proxy that only does websites (Caddy, Nginx Proxy Manager, ...)",
	FrontDoorLinx443:   "nothing: Linx takes port 443 itself (your router, or this VPS, sends port 443 here)",
	FrontDoorHomeOnly:  "nothing, and only at home: Linx answers on this home network only",
	FrontDoorNone:      "not now: no calls from outside, and no web address",
}

// proxyKinds are the front doors that are another program at ProxyAddress.
var proxyKinds = []string{FrontDoorPangolin, FrontDoorNginx, FrontDoorHTTPProxy}

// FrontDoorConfig is setup.yaml's front_door.
type FrontDoorConfig struct {
	// Kind is one of FrontDoors.
	Kind string `yaml:"kind"`
	// ProxyAddress is, for Pangolin, nginx/HAProxy or an HTTP-only proxy,
	// the home-network address of the machine it runs on (this server's
	// own, if it runs here): the only address allowed to reach Linx's web
	// port, and whose PROXY headers or X-Forwarded-For Linx believes.
	ProxyAddress string `yaml:"proxy_address"`
	// TURNUDPPort is, for those same front doors, the UDP port the router
	// forwards to Linx for call audio (0: 443). Some routers (UniFi) won't
	// forward UDP 443 to one machine while TCP 443 goes to another; 3478,
	// the usual port for this, works instead.
	TURNUDPPort int `yaml:"turn_udp_port,omitempty"`
}

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
	if !slices.Contains(FrontDoors, f.Kind) {
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
		if !NeedsProxyAddress(f.Kind) {
			return fmt.Errorf("turn_udp_port: only used with kind %s", strings.Join(proxyKinds, ", "))
		}
		if err := ValidateTURNUDPPort(f.TURNUDPPort); err != nil {
			return fmt.Errorf("turn_udp_port: %w", err)
		}
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
	// SNIAddress is where linx-sni publishes 443.
	SNIAddress netip.Addr
	// ComposeProfiles turns on optional services (linx-sni).
	ComposeProfiles string
	// DNSAddress is where the public names point: "" follows this
	// network's public address (linx-certd), else this address.
	DNSAddress string
}

var loopback = netip.AddrFrom4([4]byte{127, 0, 0, 1})

// FrontDoorFor works out a front door's settings on this server.
func FrontDoorFor(c Config, lan LAN) FrontDoorSettings {
	s := FrontDoorSettings{ProxyProtocol: true, WebAddress: loopback, TURNUDPAddress: loopback, TURNUDPPort: TURNUDPPort, SNIAddress: loopback}
	switch c.FrontDoor.Kind {
	case FrontDoorPangolin, FrontDoorNginx, FrontDoorHTTPProxy:
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
	case FrontDoorLinx443:
		s.TrustedProxies = SNIContainerName
		// Behind a home router the forwards arrive at the LAN address; on
		// a VPS (no LAN) on the public one.
		public := netip.IPv4Unspecified()
		if lan.OK() {
			public = lan.Address
		}
		s.SNIAddress = public
		s.TURNUDPAddress, s.TURNUDPPort = public, PublicPort
		s.ComposeProfiles = FrontDoorLinx443
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

// PublicHosts are the names the front door serves, under the domain.
var PublicHosts = []string{"meet", "api", "turn"}

// Front door files setup writes.
const (
	FrontDoorDir        = StackDir + "/front-door"
	PangolinTraefikFile = FrontDoorDir + "/pangolin-dynamic-config.yml"
	PangolinStepsFile   = FrontDoorDir + "/PANGOLIN.txt"
	NginxStreamFile     = FrontDoorDir + "/nginx-stream.conf"
	HAProxySnippetFile  = FrontDoorDir + "/haproxy-linx.cfg"
	NginxStepsFile      = FrontDoorDir + "/NGINX-HAPROXY.txt"
	CaddyFile           = FrontDoorDir + "/Caddyfile"
	HTTPProxyStepsFile  = FrontDoorDir + "/HTTP-PROXY.txt"
	HAProxyConfigFile   = StackDir + "/haproxy.cfg"
)

// StepsFile is where a front door's plain-language steps are ("" if none).
func StepsFile(kind string) string {
	return map[string]string{FrontDoorPangolin: PangolinStepsFile, FrontDoorNginx: NginxStepsFile, FrontDoorHTTPProxy: HTTPProxyStepsFile}[kind]
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
	switch c.FrontDoor.Kind {
	case FrontDoorPangolin:
		files = Plan{
			write("Write the Pangolin settings to add", PangolinTraefikFile, PangolinTraefik(d, linx)),
			write("Write the Pangolin steps", PangolinStepsFile, []byte(PangolinSteps(d, linx, udp))),
		}
	case FrontDoorNginx:
		files = Plan{
			write("Write the nginx settings to add", NginxStreamFile, NginxStream(d, linx)),
			write("Write the HAProxy settings to add", HAProxySnippetFile, HAProxySnippet(d, linx)),
			write("Write the nginx/HAProxy steps", NginxStepsFile, []byte(NginxSteps(d, linx, udp))),
		}
	case FrontDoorHTTPProxy:
		files = Plan{
			write("Write the Caddy settings to add", CaddyFile, CaddyConfig(d, linx)),
			write("Write the proxy steps", HTTPProxyStepsFile, []byte(HTTPProxySteps(d, linx, udp))),
		}
	case FrontDoorLinx443, FrontDoorHomeOnly:
		files = Plan{write("Write the port 443 router's settings", HAProxyConfigFile, HAProxyConfig(d))}
	default:
		return nil, nil
	}
	return files, Plan{recordsStep(c, FrontDoorFor(c, lan))}
}

func recordsStep(c Config, s FrontDoorSettings) Step {
	names := make([]string, len(PublicHosts))
	for i, h := range PublicHosts {
		names[i] = h + "." + c.Domain.Name
	}
	where, args := "this network's public address", []string{"-records", strings.Join(PublicHosts, ",")}
	if s.DNSAddress != "" {
		where, args = s.DNSAddress+" (this server, at home)", append(args, "-address", s.DNSAddress)
	}
	return cmdStep("Point "+strings.Join(names, ", ")+" at "+where+" (DNS)",
		"docker", append([]string{"compose", "--file", stackFile, "run", "--rm", "certd"}, args...)...)
}

// PangolinTraefik is the block to add to Pangolin's Traefik file-provider
// config (config/traefik/dynamic_config.yml): TCP routers on Pangolin's
// HTTPS entrypoint that pass meet., api. and turn. through undecrypted, by
// name. Linx decrypts them itself, with its own certificate, so Pangolin
// never needs (or skips checking) one; the web one sends the visitor's
// address in a PROXY v2 header. TCP routers matching a name take
// precedence over Pangolin's HTTP routers.
func PangolinTraefik(domain string, linx netip.Addr) []byte {
	return fmt.Appendf(nil, `# Linx, passed through Pangolin's Traefik (docs/WEB.md §3). Generated by linx setup.
# Add this to Pangolin's config/traefik/dynamic_config.yml (see PANGOLIN.txt:
# if that file already has a "tcp:" line, merge into it; never add a second).
# Traefik picks it up by itself; nothing needs restarting.
tcp:
  routers:
    linx-web:
      entryPoints: [websecure]
      rule: "HostSNI(`+"`meet.%[1]s`"+`) || HostSNI(`+"`api.%[1]s`"+`)"
      tls:
        passthrough: true
      service: linx-web
    linx-turn:
      entryPoints: [websecure]
      rule: "HostSNI(`+"`turn.%[1]s`"+`)"
      tls:
        passthrough: true
      service: linx-turn
  services:
    linx-web:
      loadBalancer:
        serversTransport: linx-proxy-protocol
        servers:
          - address: "%[2]s:%[3]d"
    linx-turn:
      loadBalancer:
        servers:
          - address: "%[2]s:%[4]d"
  serversTransports:
    linx-proxy-protocol:
      proxyProtocol:
        version: 2
`, domain, linx, WebPort, TURNTLSPort)
}

// PangolinSteps is what the owner does on the Pangolin machine and the
// router, in plain words.
func PangolinSteps(domain string, linx netip.Addr, udpPort int) string {
	http3 := `2. Recommended, in the same folder: in traefik_config.yml, remove the
   "http3:" lines (and the "advertisedPort: 443" under them) from the
   websecure entry point, then restart Traefik (docker restart traefik).
   Otherwise browsers visiting your other Pangolin sites try UDP 443,
   which your router now sends to Linx, and wait a moment before falling
   back.`
	if udpPort != PublicPort {
		http3 = `2. Nothing to change for HTTP/3: call audio uses UDP ` + strconv.Itoa(udpPort) + `, not 443.`
	}
	return fmt.Sprintf(`Linx behind Pangolin: what to do (generated by linx setup)
============================================================

People reach Linx at https://meet.%[1]s from anywhere. Pangolin passes that
through to Linx without opening it: Linx uses its own certificate, and
Pangolin tells Linx each visitor's real address.

1. On the Pangolin machine, open Pangolin's config/traefik/dynamic_config.yml
   and look for a line that is exactly "tcp:" (newer Pangolin versions have
   one, with "serversTransports:" under it).

   - No "tcp:" line: add the contents of
     %[2]s
     to the end of the file (copy it over, or paste it).
   - There is one: don't add a second. Traefik ignores the whole file if
     "tcp:" (or "serversTransports:" under it) appears twice. Put the
     "routers:" and "services:" parts from that file under the existing
     "tcp:", and the three "linx-proxy-protocol" lines under the existing
     "serversTransports:", keeping Pangolin's own entries there.

   Traefik picks it up by itself within a few seconds. Check it took it:
       docker logs traefik --since 1m 2>&1 | grep -i error
   should say nothing about dynamic_config.yml.

   You don't create resources for Linx in Pangolin's dashboard: this file is
   all Pangolin needs. (Pangolin's own resources keep working as before.)

%[4]s

3. On your router, keep TCP 443 going to the Pangolin machine, and forward
   UDP %[5]d to this Linx server, %[3]s (same port on both sides). That's
   how calls from outside send their audio when the network allows it
   (otherwise it goes over TCP 443 through Pangolin, which works everywhere
   but is a little less smooth).

4. DNS: setup pointed meet., api. and turn.%[1]s at your home's public
   address, and keeps them there if it changes.

5. Check everything: sudo linx doctor ("Calls from outside").
`, domain, PangolinTraefikFile, linx, http3, udpPort)
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
        meet.%[1]s  %[2]s:%[3]d;
        api.%[1]s   %[2]s:%[3]d;
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
#   use_backend linx_web if { req_ssl_sni -i meet.%[1]s } || { req_ssl_sni -i api.%[1]s }
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

// NginxSteps is what the owner does on the nginx/HAProxy machine and router.
func NginxSteps(domain string, linx netip.Addr, udpPort int) string {
	return fmt.Sprintf(`Linx behind nginx or HAProxy: what to do (generated by linx setup)
=================================================================

People reach Linx at https://meet.%[1]s from anywhere. Your nginx or HAProxy
passes that through to Linx without opening it (Linx uses its own
certificate) and tells Linx each visitor's real address.

nginx
  1. Add %[2]s to nginx.conf at the top level (next to "http {",
     not inside it). It takes over port 443.
  2. Your own websites can't listen on 443 any more: in each "server" block
     that has "listen 443 ssl", change it to
         listen %[3]s ssl proxy_protocol;
         set_real_ip_from 127.0.0.1;
         real_ip_header proxy_protocol;
     nginx then sends them their visitors (with real addresses) itself.
  3. sudo nginx -t && sudo systemctl reload nginx

HAProxy
  1. Follow %[4]s: two use_backend lines and two backends.
  2. Reload HAProxy.

Then, either way:
  4. On your router, keep TCP 443 going to that machine, and forward
     UDP %[6]d to this Linx server, %[5]s (smoother call audio from outside).
  5. DNS: setup pointed meet., api. and turn.%[1]s at your home's public
     address, and keeps them there if it changes.
  6. Check everything: sudo linx doctor ("Calls from outside").
`, domain, NginxStreamFile, nginxSitesHop, HAProxySnippetFile, linx, udpPort)
}

// CaddyConfig is the site block for Caddy: HTTPS to Linx, checking Linx's
// certificate for meet.<domain> (Caddy checks by default; tls_server_name
// makes it check the right name, since it connects by address). Caddy
// sends X-Forwarded-For itself, ignoring any a visitor sends. It would
// replace Host with Linx's address for an HTTPS backend; Linx's websockets
// check the page's origin against Host, so the visitor's Host is kept.
func CaddyConfig(domain string, linx netip.Addr) []byte {
	return fmt.Appendf(nil, `# Linx behind Caddy (docs/WEB.md §3). Generated by linx setup.
# Add to your Caddyfile, then: caddy reload (or restart Caddy's container).
meet.%[1]s, api.%[1]s {
	reverse_proxy https://%[2]s:%[3]d {
		header_up Host {host}
		transport http {
			tls_server_name meet.%[1]s
		}
	}
}
`, domain, linx, WebPort)
}

// HTTPProxySteps is what the owner does for a proxy that only does websites.
func HTTPProxySteps(domain string, linx netip.Addr, udpPort int) string {
	return fmt.Sprintf(`Linx behind a proxy that only does websites: what to do (generated by linx setup)
===============================================================================

People reach Linx at https://meet.%[1]s from anywhere. Your proxy opens the
connection and passes it on to Linx over a new encrypted one. It must check
Linx's certificate on that second connection (the settings below do), and
Linx believes the visitor address it reports (X-Forwarded-For) from your
proxy's address only.

Linx needs a trusted certificate for this (certificates.staging: false in
/etc/linx/setup.yaml): your proxy won't accept Let's Encrypt test ones.

Caddy
  Add %[2]s to your Caddyfile, then reload Caddy.

Nginx Proxy Manager (or any nginx "location" setup)
  Add a proxy host for meet.%[1]s and api.%[1]s:
    Scheme https, forward to %[3]s port %[4]d, turn on "Websockets Support".
    Under Advanced, paste:
        proxy_ssl_verify on;
        proxy_ssl_server_name on;
        proxy_ssl_name meet.%[1]s;
        proxy_ssl_trusted_certificate /etc/ssl/certs/ca-certificates.crt;

Anything else
  An HTTPS proxy to https://%[3]s:%[4]d for both names, with websockets
  on, checking the certificate for meet.%[1]s, keeping the visitor's Host
  header, and sending X-Forwarded-For.

On your router
  Keep TCP 443 going to your proxy. Forward to this Linx server (%[3]s):
    - TCP 5349 (the call relay over TLS: your proxy can't pass it on 443)
    - UDP %[5]d (smoother call audio)

DNS: setup pointed meet., api. and turn.%[1]s at your home's public address,
and keeps them there if it changes.

Check everything: sudo linx doctor ("Calls from outside").
`, domain, CaddyFile, linx, WebPort, udpPort)
}
