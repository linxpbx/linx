package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"strings"

	"linxpbx.com/linx/internal/asteriskconf"
)

// LAN is the local network this server is on: phones connect from there
// (docs/PBX.md §6). The zero value means "no local network" (e.g. a cloud
// server with a public address), and then no phone can connect yet.
type LAN struct {
	// Address is this server's address on the network.
	Address netip.Addr
	// Network is the whole network, e.g. 192.168.1.0/24.
	Network netip.Prefix
}

// OK reports whether a local network was found.
func (l LAN) OK() bool { return l.Network.IsValid() }

// BindAddress is where the phone ports are published: the server's LAN
// address, or 127.0.0.1 when there is none, so they never face the internet.
func (l LAN) BindAddress() netip.Addr {
	if !l.OK() {
		return netip.AddrFrom4([4]byte{127, 0, 0, 1})
	}
	return l.Address
}

// Networks are the networks phones may connect from (LINX_SIP_NETWORKS).
func (l LAN) Networks() []netip.Prefix {
	if !l.OK() {
		return nil
	}
	return []netip.Prefix{l.Network}
}

// DetectLAN finds the local network: the one holding the default route's
// source address, if that address is private.
func DetectLAN() LAN {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return LAN{}
	}
	return lanFrom(LANAddress(), addrs)
}

func lanFrom(a netip.Addr, addrs []net.Addr) LAN {
	if a.IsLoopback() || !a.IsPrivate() {
		return LAN{}
	}
	for _, ad := range addrs {
		n, ok := ad.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		if !ok || ip.Unmap() != a {
			continue
		}
		ones, bits := n.Mask.Size()
		if bits != 32 || ones == 32 {
			return LAN{} // a point-to-point link isn't a network phones are on
		}
		return LAN{Address: a, Network: netip.PrefixFrom(a, ones).Masked()}
	}
	return LAN{}
}

// Host files the phone setup writes.
const (
	DockerDaemonConfig = "/etc/docker/daemon.json"
	FirewallRules      = StackDir + "/nftables.conf"
	FirewallUnit       = "linx-firewall.service"
	firewallUnitPath   = "/etc/systemd/system/" + FirewallUnit
	// FirewallTable is the nftables table Linx owns. Nothing else touches it,
	// and Linx touches nothing else.
	FirewallTable = "linx"
)

// PhonesPlan lets phones on the local network reach the phone system and
// nobody else:
//   - Docker's per-port helper process ("userland proxy") is turned off.
//     With it on, Docker starts one process per published port, and the
//     200 audio ports would cost hundreds of megabytes. Without it,
//     Docker forwards published ports in the kernel only.
//   - An nftables table drops SIP and audio traffic that doesn't come from
//     the LAN, before Docker forwards it, and drops port 5060 (unencrypted
//     SIP) from everyone. A systemd unit loads it at every boot.
//
// compose.yaml publishes the ports on the LAN address only (StackPlan's
// .env), and Asterisk's own ACL refuses other sources too (asteriskconf).
func PhonesPlan(ctx context.Context, r Runner, lan LAN, fd FrontDoorSettings, readFile func(string) ([]byte, error)) (Plan, error) {
	var p Plan
	if _, err := r.Run(ctx, nil, "nft", "--version"); err != nil {
		p = append(p,
			aptStep("Refresh the package list", "update"),
			aptStep("Install the firewall tools (nftables)", "install", "-y", "nftables"))
	}

	daemon, changed, err := dockerDaemonConfig(readFile)
	if err != nil {
		return nil, err
	}
	if changed {
		p = append(p,
			fileStep("Tell Docker to forward ports without a helper process per port (needed for the phone audio ports)",
				DockerDaemonConfig, daemon, 0o644, 0o755),
			cmdStep("Restart Docker so the setting applies (running containers restart too)", "systemctl", "restart", "docker"))
	}

	what := "Allow phones from your local network (" + lan.Network.String() + ") only"
	if !lan.OK() {
		what = "Keep the phone ports closed (this server isn't on a local network)"
	}
	p = append(p,
		fileStep("Write the firewall rules: "+what+"; never allow unencrypted SIP (port 5060)",
			FirewallRules, FirewallRuleset(lan, fd), 0o644, 0o755),
		fileStep("Load the firewall rules at every start", firewallUnitPath, []byte(firewallUnit), 0o644, 0o755),
		cmdStep("Tell the system about the firewall service", "systemctl", "daemon-reload"),
		cmdStep("Turn the firewall service on", "systemctl", "enable", FirewallUnit),
		cmdStep("Apply the firewall rules now", "systemctl", "reload-or-restart", FirewallUnit),
	)
	return p, nil
}

// dockerDaemonConfig returns daemon.json with "userland-proxy": false added
// to whatever is already there, and whether that changes anything.
func dockerDaemonConfig(readFile func(string) ([]byte, error)) ([]byte, bool, error) {
	m := map[string]json.RawMessage{}
	b, err := readFile(DockerDaemonConfig)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, false, fmt.Errorf("reading %s: %w", DockerDaemonConfig, err)
	case len(strings.TrimSpace(string(b))) > 0:
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, false, fmt.Errorf("%s isn't valid JSON (%v); fix or remove it, then run setup again", DockerDaemonConfig, err)
		}
	}
	if v, ok := m["userland-proxy"]; ok && strings.TrimSpace(string(v)) == "false" {
		return nil, false, nil
	}
	m["userland-proxy"] = json.RawMessage("false")
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(out, '\n'), true, nil
}

// FirewallRuleset renders the linx nftables table. It only ever drops: it
// opens nothing, and it leaves every other table (Docker's included) alone.
// Its chain runs before Docker's port forwarding (dstnat, priority -100), so
// traffic from outside the LAN never reaches a container. Loading the file
// replaces the table atomically. The front door's addresses (a proxy at
// home) are the only ones that may reach the web port, and coturn's TLS
// port unless the router forwards it from the internet (docs/WEB.md §3).
func FirewallRuleset(lan LAN, fd FrontDoorSettings) []byte {
	web := fd.WebClients
	elements := ""
	if lan.OK() {
		elements = "\t\telements = { " + lan.Network.String() + " }\n"
	}
	webElements := ""
	if len(web) > 0 {
		s := make([]string, len(web))
		for i, a := range web {
			s[i] = a.String()
		}
		webElements = "\t\telements = { " + strings.Join(s, ", ") + " }\n"
	}
	guarded := fmt.Sprintf("{ %d, %d }", WebPort, TURNTLSPort)
	if fd.TURNTLSOpen {
		guarded = fmt.Sprint(WebPort) // the router forwards 5349 from the internet
	}
	sip, rtp := asteriskconf.SIPPort, fmt.Sprintf("%d-%d", asteriskconf.RTPStart, asteriskconf.RTPEnd)
	return fmt.Appendf(nil, `#!/usr/sbin/nft -f
# Generated by linx setup (%s). Changes are overwritten when setup runs again.
# Phones reach the phone system (SIP over TLS on %d/tcp, encrypted audio on
# %s/udp) from the local network only; unencrypted SIP (5060) from nobody.
# The web port (%[6]d/tcp) and the call relay's TLS port (%[7]d/tcp) only
# from the front door, if any (the relay's TLS port from anywhere when the
# router forwards it).

table inet %[4]s
delete table inet %[4]s

table inet %[4]s {
	set phone_networks {
		type ipv4_addr
		flags interval
%[5]s	}

	set front_door {
		type ipv4_addr
%[8]s	}

	chain prerouting {
		type filter hook prerouting priority mangle; policy accept;
		iif lo accept
		fib daddr type local tcp dport %[2]d ip saddr @phone_networks accept
		fib daddr type local udp dport %[3]s ip saddr @phone_networks accept
		fib daddr type local tcp dport { 5060, %[2]d } counter drop
		fib daddr type local udp dport { 5060, %[3]s } counter drop
		fib daddr type local tcp dport %[9]s ip saddr @front_door accept
		fib daddr type local tcp dport %[9]s counter drop
	}
}
`, FirewallUnit, sip, rtp, FirewallTable, elements, WebPort, TURNTLSPort, webElements, guarded)
}

// firewallUnit loads the rules early at boot, like Debian's own
// nftables.service. Stopping it leaves the rules in place; setup's
// reload-or-restart replaces them atomically (nft -f).
const firewallUnit = `# Generated by linx setup. Changes are overwritten when setup runs again.
[Unit]
Description=Linx firewall rules (phones from the local network only)
Wants=network-pre.target
Before=network-pre.target docker.service
DefaultDependencies=no

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/nft -f ` + FirewallRules + `
ExecReload=/usr/sbin/nft -f ` + FirewallRules + `

[Install]
WantedBy=multi-user.target
`
