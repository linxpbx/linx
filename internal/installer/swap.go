package installer

import (
	"bytes"
	"errors"
	"io/fs"
	"strings"

	"linxpbx.com/linx/internal/hostinfo"
)

// Swap on small servers (under about 2 GB of memory): Linx's services use
// about 170 MB at rest, but updates (new images unpacking while the old
// ones run) and a busy moment can briefly need more, and a server with no
// swap then kills a process instead of slowing down. 1 GB of swap, used only
// when memory is really short (swappiness 10), is the safety margin.
const (
	swapFile     = "/swapfile"
	swapSize     = 1 << 30
	fstabPath    = "/etc/fstab"
	swapSysctl   = "/etc/sysctl.d/90-linx-swap.conf"
	swappiness   = "vm.swappiness = 10\n"
	fstabSwapFmt = swapFile + " none swap sw 0 0\n"
)

// SwapPlan adds a 1 GB swap file when h has little memory and less than
// that much swap already. Nothing in a container (a Proxmox LXC can't turn
// swap on itself: the host gives it memory and swap), and nothing when
// /swapfile is already listed in /etc/fstab.
func SwapPlan(h hostinfo.Info, readFile func(string) ([]byte, error)) (Plan, error) {
	if !LowMemory(h) || h.SwapBytes >= swapSize || h.Container != "" {
		return nil, nil
	}
	fstab, err := readFile(fstabPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, line := range strings.Split(string(fstab), "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == swapFile {
			return nil, nil
		}
	}
	if len(fstab) > 0 && !bytes.HasSuffix(fstab, []byte("\n")) {
		fstab = append(fstab, '\n')
	}
	return Plan{
		cmdStep("Make a 1 GB swap file (this server has little memory)", "fallocate", "--length", "1G", swapFile),
		cmdStep("Make the swap file readable by root only", "chmod", "600", swapFile),
		cmdStep("Prepare the swap file", "mkswap", swapFile),
		cmdStep("Turn the swap file on", "swapon", swapFile),
		fileStep("Turn it on at every start", fstabPath, append(fstab, fstabSwapFmt...), 0o644, 0o755),
		fileStep("Use swap only when memory is really short", swapSysctl, []byte(swappiness), 0o644, 0o755),
		cmdStep("Apply that now", "sysctl", "--load", swapSysctl),
	}, nil
}
