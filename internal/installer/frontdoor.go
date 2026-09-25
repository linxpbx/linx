package installer

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Front doors (docs/WEB.md §3, ADR-040): what sits between the internet and
// Linx. Every one passes HTTPS through to Linx without decrypting it (Linx
// terminates TLS with its own certificate) and tells Linx the visitor's
// address with a PROXY protocol v2 header; TURN over TLS is passed through
// by name on the same port 443; TURN over UDP goes straight to coturn.
const (
	// FrontDoorHomeOnly: nothing is published to the internet (the default
	// until the owner picks one; the home-only front door proper, with DNS
	// at the LAN address, arrives in step 7).
	FrontDoorHomeOnly = "home-only"
	// FrontDoorPangolin: Pangolin on another machine at home (the router
	// forwards TCP 443 to it) passes meet., api. and turn. through to Linx
	// by name (a Traefik file Linx generates). The router forwards UDP 443
	// straight to Linx.
	FrontDoorPangolin = "pangolin"
	// FrontDoorLinx443: Linx's own HAProxy (linx-sni) owns TCP 443 (ADR-009)
	// and coturn UDP 443.
	FrontDoorLinx443 = "linx-443"
)

// FrontDoors lists the choices, as setup offers them.
var FrontDoors = []string{FrontDoorPangolin, FrontDoorLinx443, FrontDoorHomeOnly}

// FrontDoorDescription is each choice in plain words.
var FrontDoorDescription = map[string]string{
	FrontDoorPangolin: "Pangolin, on another machine on this home network (your router sends port 443 to it)",
	FrontDoorLinx443:  "nothing: Linx takes port 443 itself (your router, or this VPS, sends port 443 here)",
	FrontDoorHomeOnly: "nothing yet: only this home network (calls from outside don't work)",
}

// FrontDoorConfig is setup.yaml's front_door.
type FrontDoorConfig struct {
	// Kind is one of FrontDoors.
	Kind string `yaml:"kind"`
	// PangolinAddress is the home-network address of the machine Pangolin
	// runs on: the only address allowed to reach Linx's web port, and whose
	// PROXY headers Linx believes.
	PangolinAddress string `yaml:"pangolin_address"`
}

// Validate checks the front door settings.
func (f FrontDoorConfig) Validate() error {
	if !slices.Contains(FrontDoors, f.Kind) {
		return fmt.Errorf("kind: must be one of %v, got %q", FrontDoors, f.Kind)
	}
	if f.Kind == FrontDoorPangolin {
		if err := ValidatePangolinAddress(f.PangolinAddress); err != nil {
			return fmt.Errorf("pangolin_address: %w", err)
		}
	} else if f.PangolinAddress != "" {
		return errors.New("pangolin_address: only used with kind: pangolin")
	}
	return nil
}

// ValidatePangolinAddress checks Pangolin's home-network address.
func ValidatePangolinAddress(s string) error {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil || !a.Is4() {
		return fmt.Errorf("%q isn't an IPv4 address like 192.168.1.30", s)
	}
	if !a.IsPrivate() {
		return fmt.Errorf("%s isn't a home-network address (Pangolin on a VPS, with Newt, comes in a later Linx update)", a)
	}
	return nil
}

// Container ports the front door reaches (compose.yaml).
const (
	WebPort     = 8443 // the control plane's HTTPS
	TURNTLSPort = 5349 // coturn's TLS
	TURNUDPPort = 3478 // coturn's UDP
	// PublicPort is the only port a front door or router needs: 443, TCP
	// and UDP.
	PublicPort = 443
	// SNIContainerName is the Linx-takes-443 HAProxy, trusted by name.
	SNIContainerName = "linx-sni"
)

// FrontDoorSettings is what a front door changes on this server.
type FrontDoorSettings struct {
	// TrustedProxies is LINX_TRUSTED_PROXIES: whose PROXY headers (and
	// X-Forwarded-For) the control plane believes, and requires.
	TrustedProxies string
	// WebAddress is where 8443 (web) and 5349 (TURN over TLS) are
	// published on this server; 127.0.0.1 keeps them unpublished.
	WebAddress netip.Addr
	// WebClients may reach them (the firewall drops everyone else).
	WebClients []netip.Addr
	// TURNUDPAddress:TURNUDPPort is where coturn's UDP is published.
	TURNUDPAddress netip.Addr
	TURNUDPPort    int
	// SNIAddress is where linx-sni publishes 443 (Linx takes 443 only).
	SNIAddress netip.Addr
	// ComposeProfiles turns on optional services (linx-sni).
	ComposeProfiles string
}

var loopback = netip.AddrFrom4([4]byte{127, 0, 0, 1})

// FrontDoorFor works out a front door's settings on this server.
func FrontDoorFor(c Config, lan LAN) FrontDoorSettings {
	s := FrontDoorSettings{WebAddress: loopback, TURNUDPAddress: loopback, TURNUDPPort: TURNUDPPort, SNIAddress: loopback}
	switch c.FrontDoor.Kind {
	case FrontDoorPangolin:
		p, _ := netip.ParseAddr(c.FrontDoor.PangolinAddress)
		s.TrustedProxies = p.String()
		s.WebAddress, s.WebClients = lan.BindAddress(), []netip.Addr{p}
		s.TURNUDPAddress, s.TURNUDPPort = lan.BindAddress(), PublicPort
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
	HAProxyConfigFile   = StackDir + "/haproxy.cfg"
)

// FrontDoorPlan returns the front door's files, to write before the stack
// starts, and the DNS step (linx-certd -records, pointing its names at this
// network's public address), to run once it's up.
func FrontDoorPlan(c Config, lan LAN) (files, dns Plan) {
	switch c.FrontDoor.Kind {
	case FrontDoorPangolin:
		files = Plan{
			fileStep("Write the Pangolin settings to add ("+PangolinTraefikFile+")", PangolinTraefikFile,
				PangolinTraefik(c.Domain.Name, lan.BindAddress()), 0o644, 0o755),
			fileStep("Write the Pangolin steps ("+PangolinStepsFile+")", PangolinStepsFile,
				[]byte(PangolinSteps(c.Domain.Name, lan.BindAddress())), 0o644, 0o755),
		}
	case FrontDoorLinx443:
		files = Plan{fileStep("Write the port 443 router's settings ("+HAProxyConfigFile+")", HAProxyConfigFile,
			HAProxyConfig(c.Domain.Name), 0o644, 0o755)}
	default:
		return nil, nil
	}
	return files, Plan{recordsStep(c)}
}

func recordsStep(c Config) Step {
	names := make([]string, len(PublicHosts))
	for i, h := range PublicHosts {
		names[i] = h + "." + c.Domain.Name
	}
	return cmdStep("Point "+strings.Join(names, ", ")+" at this network's public address (DNS)",
		"docker", "compose", "--file", stackFile, "run", "--rm", "certd", "-records", strings.Join(PublicHosts, ","))
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
# Add this to the END of Pangolin's config/traefik/dynamic_config.yml.
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
func PangolinSteps(domain string, linx netip.Addr) string {
	return fmt.Sprintf(`Linx behind Pangolin: what to do (generated by linx setup)
============================================================

People reach Linx at https://meet.%[1]s from anywhere. Pangolin passes that
through to Linx without opening it: Linx uses its own certificate, and
Pangolin tells Linx each visitor's real address.

1. On the Pangolin machine, open Pangolin's config/traefik/dynamic_config.yml
   and add the contents of %[2]s
   to the end of it (copy it over, or paste it). Traefik picks it up by
   itself within a few seconds.

   You don't create resources for Linx in Pangolin's dashboard: this file is
   all Pangolin needs. (Pangolin's own resources keep working as before.)

2. Recommended, in the same folder: in traefik_config.yml, remove the
   "http3:" lines (and the "advertisedPort: 443" under them) from the
   websecure entry point, then restart Traefik (docker restart traefik).
   Otherwise browsers visiting your other Pangolin sites try UDP 443,
   which your router now sends to Linx, and wait a moment before falling
   back.

3. On your router, keep TCP 443 going to the Pangolin machine, and forward
   UDP 443 to this Linx server, %[3]s. That's how calls from outside send
   their audio when the network allows it (otherwise it goes over TCP 443
   through Pangolin, which works everywhere but is a little less smooth).

4. DNS: setup pointed meet., api. and turn.%[1]s at your home's public
   address (or left them alone if they already point there).

5. Check everything: sudo linx doctor ("Calls from outside").
`, domain, PangolinTraefikFile, linx)
}

// HAProxyConfig is linx-sni's configuration (Linx takes 443, ADR-009): TCP
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
