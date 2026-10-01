package installer

import (
	"fmt"
	"linxpbx.com/linx/internal/dnsapi"
	"net/netip"
	"strings"

	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/dnsname"
	"linxpbx.com/linx/internal/install"
)

// The web install's certificate page (docs/INSTALL.md §4): what the host
// shows and runs once the plain page's answers are saved.

// CertMode is how the first certificate is got for a front door: through
// port 443 without a token, unless the front door can't pass Let's
// Encrypt's check through to Linx (an HTTP-only proxy decrypts it; home
// only isn't reachable at all; another public port isn't 443, the only
// port Let's Encrypt checks), where the DNS token is asked instead.
func CertMode(kind string) string {
	if kind == FrontDoorHTTPProxy || kind == FrontDoorHomeOnly || kind == FrontDoorPublicPort {
		return install.CertToken
	}
	return install.CertPort443
}

// CertView is the certificate page for the saved answers: its mode, the
// record to add and the front door's own steps.
func CertView(c Config, lan LAN, facts install.Facts) install.CertView {
	v := install.CertView{Mode: CertMode(c.FrontDoor.Kind), Domain: c.Domain.Name, Address: c.Address(), FrontDoor: c.FrontDoor.Kind, Setup: DoorSetup(c, lan)}
	if v.Mode == install.CertPort443 {
		for _, h := range certs.BootstrapHosts {
			v.AddRecords = append(v.AddRecords, install.Record{Type: "A", Name: dnsname.Host(h, c.Domain.Name), Value: facts.PublicAddress})
		}
	}
	return v
}

// DoorSetup is what to do on the front door before Let's Encrypt (or a
// browser) can get through it, as the certificate page shows it; nil when
// there's nothing (a rented server where Linx takes 443, or home only).
func DoorSetup(c Config, lan LAN) *install.DoorSetup {
	d, linx, udp := c.Domain.Name, lan.BindAddress(), c.FrontDoor.UDPPort()
	switch c.FrontDoor.Kind {
	case FrontDoorProxy, FrontDoorPangolin, FrontDoorNginx:
		return &install.DoorSetup{Card: DoorCard(c, lan), Steps: []string{routerStep(c.FrontDoor.ProxyAddress, linx, udp)}}
	case FrontDoorHTTPProxy:
		return &install.DoorSetup{
			Files: []install.SetupFile{{Title: "the block for Caddy", Path: "your Caddyfile", Text: string(CaddyConfig(d, linx))}},
			Steps: []string{
				"Add the block below to your Caddyfile and reload Caddy. With Nginx Proxy Manager, follow " + HTTPProxyStepsFile + " on this server instead.",
				fmt.Sprintf("On your router, keep TCP port 443 going to your proxy (%s), and send TCP port %d and UDP port %d to this server (%s).", c.FrontDoor.ProxyAddress, TURNTLSPort, udp, linx),
			},
		}
	case FrontDoorLinx443:
		if lan.OK() {
			return &install.DoorSetup{Steps: []string{fmt.Sprintf("On your router, send TCP and UDP port 443 to this server (%s).", lan.Address)}}
		}
	case FrontDoorPublicPort:
		return &install.DoorSetup{Steps: PublicPortSteps(c.FrontDoor, lan)}
	}
	return nil
}

// PublicPortWarning is the owner's words above the choice of another
// public port, and next to it (docs/SIMPLER.md §2.5, owner 2026-09-29).
const PublicPortWarning = "Linx is designed and tuned to work best on port 443, the standard port for secure websites. " +
	"It's always the recommended choice: almost every network lets it through, so sign-in, calls and meetings work " +
	"wherever people are. Another port can work, but some networks block it, so some features may not work everywhere, " +
	"and calls from those places may fail or sound worse."

// PublicPortWarnings are what another public port trades away
// (docs/SIMPLER.md §2.5 items 5 and 6).
var PublicPortWarnings = []string{
	"Browser calls from networks that only allow standard web traffic, like some hotels, workplaces, public Wi-Fi and " +
		"mobile networks, may fail or have no audio, because this setup can't use port 443 for them. Calls from normal " +
		"home and mobile networks work.",
	"Some company, school and public networks block any address with a port like :8443. There, even this page and " +
		"signing in can fail. A front door on port 443 (Pangolin, nginx, Caddy or Nginx Proxy Manager passing Linx " +
		"through), or a small rented server as your front door, keeps port 443.",
}

// PublicPortSteps are the router rules for another public port
// (docs/ui/SCREENS_PHASE1F.md §4.2), or, on a rented server, what Linx
// opens itself.
func PublicPortSteps(f FrontDoorConfig, lan LAN) []string {
	tcp, udp := f.Port(), f.UDPPort()
	if !lan.OK() {
		return []string{fmt.Sprintf("Linx opens TCP %d and UDP %d on this server itself: there's nothing to forward. "+
			"If your server provider has a firewall of its own, open those two there.", tcp, udp)}
	}
	return []string{
		fmt.Sprintf("On your router, forward TCP %d to %s port %d.", tcp, lan.Address, tcp),
		fmt.Sprintf("On your router, forward UDP %d to %s port %d.", udp, lan.Address, udp),
		fmt.Sprintf("People at the office use the same address. If it doesn't open there, turn on NAT loopback (hairpin) "+
			"on your router, or add the domain to your local DNS pointing at %s.", lan.Address),
	}
}

// installEnvCert is the certificate page's part of install.env.
func installEnvCert(c Config, lan LAN) string {
	fd := FrontDoorFor(c, lan)
	// certd's own profile stays off: "up" never starts it, and "run"
	// starts a service it names whatever its profile.
	return fmt.Sprintf(`# The certificate page (docs/INSTALL.md §4): the front door is %s.
LINX_DOMAIN=%s
LINX_ACME_EMAIL=%s
LINX_DNS_PROVIDER=%s
LINX_TRUSTED_PROXIES=%s
LINX_PROXY_PROTOCOL=%t
LINX_WEB_ADDRESS=%s
LINX_SNI_ADDRESS=%s
LINX_PUBLIC_PORT=%d
LINX_WEB_HOST_PORT=%s
COMPOSE_PROFILES=%s
`, c.FrontDoor.Kind, c.Domain.Name, c.Certificates.Email, c.Domain.DNSProvider, fd.TrustedProxies, fd.ProxyProtocol,
		fd.WebAddress, fd.SNIAddress, fd.SNIPort, fd.WebHostPort, fd.ComposeProfiles)
}

// InstallCertEnv is install.env for the certificate page.
func InstallCertEnv(c Config, lan LAN, imageTag string, address netip.Addr) []byte {
	return []byte(installEnv(imageTag, address) + installEnvCert(c, lan))
}

func installEnv(imageTag string, address netip.Addr) string {
	return fmt.Sprintf("# Generated by linx setup for the web install's first page (docs/INSTALL.md).\n"+
		"LINX_VERSION=%s\nLINX_INSTALL_ADDRESS=%s\n", imageTag, address)
}

// InstallCertPlan writes the front door's files and starts the install's
// stack again with its settings: the control plane's HTTPS port where the
// front door reaches it, and Linx's own port 443 router when Linx takes
// 443 or is home only.
func InstallCertPlan(c Config, lan LAN, imageTag string, address netip.Addr) Plan {
	files, _ := FrontDoorPlan(c, lan)
	dc := installCompose()
	return append(files,
		fileStep("Write the installer's settings", installStackEnv, InstallCertEnv(c, lan, imageTag, address), 0o644, 0o755),
		cmdStep("Start Linx's web port", "docker", append(dc, "up", "--detach", "--wait", "--remove-orphans")...),
	)
}

// CertdRun is the docker arguments that run certd from the install's
// stack with args; token mounts the DNS token for that run only.
func CertdRun(token bool, args ...string) []string {
	a := append(installCompose(), "run", "--rm", "--no-TTY")
	if token {
		a = append(a, "--volume", DNSTokenPath+":/run/secrets/linx_dns_token:ro")
	}
	return append(append(a, "certd"), args...)
}

// RecordsArgs is certd -records for DNSRecords: the public names at this
// network's public address (or home only's at the home address), and
// sip.<domain> at the home address.
func RecordsArgs(c Config, lan LAN) []string {
	args := []string{"-records", DNSRecords(c, lan)}
	if a := FrontDoorFor(c, lan).DNSAddress; a != "" {
		args = append(args, "-address", a)
	}
	return args
}

// SaveDNSKeyPlan saves a DNS company's key (SaveDNSTokenPlan) and, when
// it's for another company than c names, the company in setup.yaml.
func SaveDNSKeyPlan(c Config, provider, secret string) Plan {
	p := SaveDNSTokenPlan(secret)
	if provider != c.Domain.DNSProvider {
		c.Domain.DNSProvider = provider
		p = append(p, Step{Title: "Save your DNS company (" + dnsapi.Name(provider) + ") to " + ConfigPath,
			File: &File{Path: ConfigPath, Data: c.Marshal(), Mode: 0o600, DirMode: 0o755}})
	}
	return p
}

// SaveDNSTokenPlan saves the DNS token as linx_dns_token, readable by root
// and the certificate service only (as StackPlan does).
func SaveDNSTokenPlan(token string) Plan {
	s := fileStep("Save the DNS token (readable by root and the certificate service only)",
		DNSTokenPath, []byte(strings.TrimSpace(token)), 0o440, 0o700)
	s.File.Gid = nonrootGID
	return Plan{s}
}
