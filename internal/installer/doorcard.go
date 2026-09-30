package installer

import (
	"fmt"
	"net/netip"
	"strings"

	"linxpbx.com/linx/internal/install"
)

// The front-door card (docs/SIMPLER.md §2.2, ADR-062): the three things
// every front door that passes Linx through does, and "How to do this in…"
// for each product, generated from the same facts. Linx's side is the same
// whichever product it is, so choosing one only changes what's shown.

// Front-door guides, in the order the card shows them.
const (
	GuidePangolin = "pangolin"
	GuideNginx    = "nginx"
	GuideHAProxy  = "haproxy"
	GuideCaddy    = "caddy"
	GuideNPM      = "npm"
	GuideRouter   = "router"
)

// PangolinResourcesNote is why the Pangolin guide uses Traefik's file: its
// web page's raw TCP resources are passed on by port (their own entry
// point, "HostSNI(`*`)"), never by name, so they can't share 443 with
// Pangolin's own sites (checked on Pangolin 1.23 EE, 2026-09-30).
const PangolinResourcesNote = "Pangolin's Resources page can't do this: it passes raw TCP on by port, not by name, so Linx would need a port of its own. This goes in Traefik's settings file instead, next to Pangolin's own, and Pangolin's sites keep working as before."

// DoorCard is the card for a front door that passes Linx through (nil for
// any other).
func DoorCard(c Config, lan LAN) *install.DoorCard {
	if !PassesThrough(c.FrontDoor.Kind) {
		return nil
	}
	d, linx, p, udp := c.Domain.Name, lan.BindAddress(), c.FrontDoor.ProxyAddress, c.FrontDoor.UDPPort()
	card := &install.DoorCard{
		Routes: []install.DoorRoute{
			{Name: d, Address: netip.AddrPortFrom(linx, WebPort).String(), ProxyProtocol: true},
			{Name: "turn." + d, Address: netip.AddrPortFrom(linx, TURNTLSPort).String()},
		},
		Proxy:  p,
		Guides: doorGuides(d, linx, p, udp),
	}
	switch c.FrontDoor.Kind {
	case FrontDoorPangolin:
		card.Pick = GuidePangolin
	case FrontDoorNginx:
		card.Pick = GuideNginx
	}
	return card
}

// routerStep is what's left for the router with any front door that
// passes Linx through.
func routerStep(p string, linx netip.Addr, udp int) string {
	return fmt.Sprintf("On your router, keep TCP port 443 going to your front door (%s), and send UDP port %d to this server (%s) for call audio.", p, udp, linx)
}

func doorGuides(d string, linx netip.Addr, p string, udp int) []install.DoorGuide {
	pangolin := []string{
		fmt.Sprintf("On the Pangolin machine (%s), open config/traefik/dynamic_config.yml.", p),
		"If it has no line that is exactly “tcp:”, add the block below to the end of the file.",
		"If it has one, don't add a second (Traefik would ignore the whole file): put the block's “routers:” and “services:” entries under the existing “tcp:”, and its “linx-proxy-protocol” lines under the existing “serversTransports:”, keeping Pangolin's own entries. Another Linx's entries stay: this one's are named " + PangolinName(d) + "-….",
		"Traefik picks it up by itself within a few seconds. To see that it took it: docker logs traefik --since 1m 2>&1 | grep -i error should say nothing about dynamic_config.yml.",
	}
	if udp == PublicPort {
		pangolin = append(pangolin, "Recommended: in traefik_config.yml, remove the “http3:” lines (and “advertisedPort: 443” under them) from the websecure entry point, then docker restart traefik. Otherwise browsers visiting your other Pangolin sites try UDP 443, which now goes to Linx, and wait a moment before falling back.")
	}
	return []install.DoorGuide{
		{
			ID: GuidePangolin, Title: "Pangolin", Note: PangolinResourcesNote, Steps: pangolin,
			Files: []install.SetupFile{{Title: "the block for Pangolin", Path: "config/traefik/dynamic_config.yml", Text: string(PangolinTraefik(d, linx))}},
		},
		{
			ID: GuideNginx, Title: "nginx", Note: "nginx needs its stream module (it's in nginx.org's and Debian's packages). The block takes over port 443 and passes your own websites on to them.",
			Steps: []string{
				"Add the block below to nginx.conf at the top level (next to “http {”, not inside it).",
				fmt.Sprintf("In each of your own sites' “server” blocks that has “listen 443 ssl”, change that line to “listen %s ssl proxy_protocol;” and add “set_real_ip_from 127.0.0.1;” and “real_ip_header proxy_protocol;”. Your sites keep their visitors' real addresses.", nginxSitesHop),
				"sudo nginx -t && sudo systemctl reload nginx",
			},
			Files: []install.SetupFile{{Title: "the block for nginx", Path: "nginx.conf, at the top level", Text: string(NginxStream(d, linx))}},
		},
		{
			ID: GuideHAProxy, Title: "HAProxy",
			Steps: []string{
				"In your frontend on port 443 (mode tcp, with “tcp-request inspect-delay 5s” and “tcp-request content accept if { req_ssl_hello_type 1 }”), add the two use_backend lines from the block's comments above your other use_backend lines.",
				"Add the two backends from the block, then reload HAProxy.",
			},
			Files: []install.SetupFile{{Title: "the block for HAProxy", Path: "haproxy.cfg", Text: string(HAProxySnippet(d, linx))}},
		},
		{
			ID: GuideCaddy, Title: "Caddy",
			Note: "Caddy needs its layer-4 add-on (github.com/mholt/caddy-l4) to pass Linx through: download Caddy with the “layer4” package from caddyserver.com/download, or build it with xcaddy. Your own sites keep working as before.",
			Steps: []string{
				"Add the block below to the top of your Caddyfile. If it already starts with a “{ … }” block, put the “servers { … }” part inside that one instead of adding a second.",
				"caddy reload (or restart Caddy's container).",
			},
			Files: []install.SetupFile{{Title: "the block for Caddy", Path: "the top of your Caddyfile", Text: string(CaddyLayer4(d, linx, udp))}},
		},
		{
			ID: GuideNPM, Title: "Nginx Proxy Manager",
			Note: "Nginx Proxy Manager can only pass traffic through by port, not by name, so this works only if nothing else needs port 443.",
			Steps: []string{
				"If nothing else needs port 443: have your router send TCP and UDP port 443 straight to this server, and choose “Nothing else uses port 443 — Linx takes it” instead.",
				"If your other sites need port 443 too: Caddy (with its layer-4 add-on), nginx or HAProxy can pass Linx through by name next to them. As a last resort, Advanced has “My proxy must unlock the traffic itself”, which works with Nginx Proxy Manager as it is.",
			},
		},
		{
			ID: GuideRouter, Title: "A router or firewall",
			Note: "A router or firewall passes traffic through by port, not by name, so it can send port 443 to one machine only.",
			Steps: []string{
				fmt.Sprintf("If nothing else needs port 443, forward TCP and UDP port 443 to this server (%s) and choose “Nothing else uses port 443 — Linx takes it” instead.", linx),
				"If another program needs it, keep port 443 going to that program and follow its tab here.",
			},
		},
	}
}

// CaddyLayer4 is the Caddyfile global block for a Caddy (with caddy-l4)
// that already owns port 443: a layer-4 listener wrapper, in front of
// Caddy's own TLS, passes Linx's names through by name before Caddy
// decrypts anything; everything else falls through to Caddy's own sites.
// With call audio on UDP 443, Caddy's HTTP/3 is turned off so browsers
// don't try it for Caddy's sites on a port that now goes to Linx.
func CaddyLayer4(domain string, linx netip.Addr, udpPort int) []byte {
	protocols := ""
	if udpPort == PublicPort {
		protocols = "\n\t\t# Call audio uses UDP 443, so no HTTP/3 for Caddy's own sites.\n\t\tprotocols h1 h2"
	}
	return fmt.Appendf(nil, `# Linx at %[1]s, passed through Caddy by name (docs/SIMPLER.md §2).
# Generated by Linx. Needs Caddy with the layer4 add-on (caddy-l4).
{
	servers {
		listener_wrappers {
			layer4 {
				@linx_web tls sni %[1]s
				route @linx_web {
					proxy {
						proxy_protocol v2
						upstream %[2]s
					}
				}
				@linx_turn tls sni turn.%[1]s
				route @linx_turn {
					proxy %[3]s
				}
			}
			tls
		}%[4]s
	}
}
`, domain, netip.AddrPortFrom(linx, WebPort), netip.AddrPortFrom(linx, TURNTLSPort), protocols)
}

// DoorCardText is the card as a text file (FrontDoorStepsFile), for the
// terminal setup and anyone reading it on the server.
func DoorCardText(card *install.DoorCard, router string) string {
	var b strings.Builder
	b.WriteString("Linx behind another program on port 443: what to do (generated by linx setup)\n")
	b.WriteString("==============================================================================\n\n")
	b.WriteString("Your front door needs to do three things:\n\n1. Pass these names through without unlocking them:\n")
	for _, r := range card.Routes {
		fmt.Fprintf(&b, "     %s\n", r.Name)
	}
	b.WriteString("   Linx's own certificate has to reach the browser.\n\n2. Send them to this server:\n")
	for _, r := range card.Routes {
		fmt.Fprintf(&b, "     %s  ->  %s\n", r.Name, r.Address)
	}
	b.WriteString("\n3. Tell Linx who's visiting:\n")
	for _, r := range card.Routes {
		on := "leave PROXY protocol off"
		if r.ProxyProtocol {
			on = `turn on "PROXY protocol, version 2"`
		}
		fmt.Fprintf(&b, "     %s: %s\n", r.Name, on)
	}
	fmt.Fprintf(&b, "   Linx only accepts it from %s, your front door.\n\n", card.Proxy)
	b.WriteString("How to do this in...\n")
	for _, g := range card.Guides {
		fmt.Fprintf(&b, "\n%s\n%s\n", g.Title, strings.Repeat("-", len(g.Title)))
		if g.Note != "" {
			fmt.Fprintf(&b, "%s\n", g.Note)
		}
		for i, s := range g.Steps {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, s)
		}
		for _, f := range g.Files {
			fmt.Fprintf(&b, "  The block (%s) is in %s.\n", f.Path, doorFileFor(g.ID))
		}
	}
	fmt.Fprintf(&b, "\nThen, whichever it is:\n  %s\n", router)
	b.WriteString("  DNS: setup points the names at your home's public address, and keeps them there if it changes.\n")
	b.WriteString("  Check everything: sudo linx doctor (\"Calls from outside\").\n")
	return b.String()
}

// doorFileFor is where setup writes a guide's block on this server.
func doorFileFor(guide string) string {
	return map[string]string{GuidePangolin: PangolinTraefikFile, GuideNginx: NginxStreamFile, GuideHAProxy: HAProxySnippetFile, GuideCaddy: CaddyLayer4File}[guide]
}
