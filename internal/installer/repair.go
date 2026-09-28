package installer

import (
	"fmt"
	"net/netip"
	"slices"

	"linxpbx.com/linx/internal/install"
)

// The repair page (docs/INSTALL.md §7): when https://<domain> is broken on
// an installed server, setup opens the Server settings page on port 6464
// too, served by the running control plane. Two things open that port, and
// both are undone when the page closes:
//
//   - A Compose override (RepairFile) publishes 6464 on the control plane,
//     with the first page's temporary certificate. Every compose command
//     setup runs while it's open carries it (WithRepair), so a change made
//     from the page doesn't close the page under itself.
//   - The firewall's drop rule for 6464 stays; the address set
//     RepairSet, empty whenever the ruleset loads (so at every boot), is
//     the only way past it.

// RepairFile is the Compose override that publishes port 6464.
const RepairFile = StackDir + "/repair.yaml"

// RepairSet is the firewall set that lets addresses reach port 6464.
const RepairSet = "install_page"

// RepairCompose is RepairFile for address.
func RepairCompose(address netip.Addr) []byte {
	return fmt.Appendf(nil, `# Written by linx setup while the repair page is open (docs/INSTALL.md §7).
# Removed, and port %[1]d closed again, when it closes.
services:
  control-plane:
    environment:
      LINX_REPAIR_ADDR: ":%[1]d"
    volumes:
      - %[2]s:%[3]s:ro
    ports:
      - "%[4]s:%[1]d:%[1]d/tcp"
`, install.Port, install.FirstPageTLSDir, install.FirstPageTLSMount, address)
}

// RepairOpenPlan opens the repair page on address: the firewall rules as
// setup writes them (they bring RepairSet), the override, the set opened,
// and the control plane started again with port 6464.
func RepairOpenPlan(c Config, lan LAN, address netip.Addr) Plan {
	return Plan{
		fileStep("Write the firewall rules", FirewallRules, FirewallRuleset(lan, FrontDoorFor(c, lan)), 0o644, 0o755),
		cmdStep("Apply the firewall rules now", "systemctl", "reload-or-restart", FirewallUnit),
		fileStep("Publish the repair page on port "+fmt.Sprint(install.Port), RepairFile, RepairCompose(address), 0o644, 0o755),
		RepairFirewallStep(),
		cmdStep("Start the control plane with the repair page", "docker",
			"compose", "--file", stackFile, "--file", RepairFile, "up", "--detach", "--wait", "control-plane"),
	}
}

// RepairFirewallStep lets browsers past the firewall to port 6464. The
// ruleset empties RepairSet whenever it loads, so a change that writes the
// firewall while the repair page is open runs this again after it.
func RepairFirewallStep() Step {
	return cmdStep("Let browsers reach port "+fmt.Sprint(install.Port)+" until the repair page closes",
		"nft", "add", "element", "inet", FirewallTable, RepairSet, "{ 0.0.0.0/0 }")
}

// RepairClosePlan closes port 6464 again: safe to run when it's closed.
func RepairClosePlan() Plan {
	return Plan{
		cmdStep("Block port "+fmt.Sprint(install.Port)+" again", "nft", "flush", "set", "inet", FirewallTable, RepairSet),
		cmdStep("Remove the repair page's settings", "rm", "-f", RepairFile),
		cmdStep("Start the control plane without the repair page", "docker",
			"compose", "--file", stackFile, "up", "--detach", "--wait", "control-plane"),
	}
}

// WithRepair is p with the repair override added to each compose command
// on the full stack, so running it keeps port 6464 open.
func WithRepair(p Plan) Plan {
	out := make(Plan, len(p))
	for i, s := range p {
		out[i] = s
		if s.Cmd == nil || s.Cmd.Name != "docker" {
			continue
		}
		at := slices.Index(s.Cmd.Args, stackFile)
		if at < 1 || s.Cmd.Args[at-1] != "--file" || slices.Contains(s.Cmd.Args, RepairFile) {
			continue
		}
		c := *s.Cmd
		c.Args = slices.Concat(c.Args[:at+1], []string{"--file", RepairFile}, c.Args[at+1:])
		out[i].Cmd = &c
	}
	return out
}

// StackCertdRun is the docker arguments that run certd from the full stack
// for domain (a new one, before .env names it): the first certificate for
// a new domain through port 443 (docs/INSTALL.md §7), with args.
func StackCertdRun(domain string, args ...string) []string {
	return append([]string{"compose", "--file", stackFile, "run", "--rm", "--no-TTY", "--env", "LINX_DOMAIN=" + domain, "certd"}, args...)
}

// RestartSNIStep starts Linx's own port 443 router again, so it reads its
// settings file again (a new domain's names): "up" alone leaves a
// container whose Compose settings didn't change as it is.
func RestartSNIStep() Step {
	return cmdStep("Restart Linx's port 443 router with the new names", "docker",
		"compose", "--file", stackFile, "up", "--detach", "--wait", "--force-recreate", "sni")
}
