package installer

// WireGuardModulesFile makes systemd load the kernel's WireGuard at every
// boot, for phone lines through a WireGuard tunnel (docs/TRUNKS.md §7):
// linx-wireguard creates tunnels inside a container, which can't load a
// module itself.
const WireGuardModulesFile = "/etc/modules-load.d/linx-wireguard.conf"

// WireGuardPlan loads the wireguard module now and at every boot. Ubuntu
// 24.04's kernel has it; nothing else is installed.
func WireGuardPlan() Plan {
	return Plan{
		fileStep("Load WireGuard at every start (for phone lines through a WireGuard tunnel)",
			WireGuardModulesFile, []byte("# Written by linx setup: phone lines through WireGuard tunnels (linx-wireguard).\nwireguard\n"), 0o644, 0o755),
		cmdStep("Load WireGuard now", "modprobe", "wireguard"),
	}
}
