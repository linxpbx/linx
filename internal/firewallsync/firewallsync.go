// Package firewallsync is linx-firewall-sync's logic (docs/TRUNKS.md §12,
// §13 step 6): once a minute (its systemd timer, not this package), it
// reads the IP-authenticated trunks' addresses from the control plane and
// makes the host firewall's two sets match — adding an address the moment
// a trunk starts using it, and removing it within one tick of the trunk
// being disabled, deleted, or resolving elsewhere. It never touches
// anything else: the two set names it may ever pass to nft are fixed here,
// never taken from what the control plane prints, so nothing it reads
// could ever make it touch a rule or a set it wasn't built to.
package firewallsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"

	"linxpbx.com/linx/internal/installer"
	"linxpbx.com/linx/internal/nftset"
)

// ControlPlaneContainer and ControlPlaneBinary are the same container and
// binary linx user/linx trunk run through (docs exec, same trust level):
// duplicated here, not imported, since services/control-plane is a
// container image, not a library this host binary can depend on.
const (
	ControlPlaneContainer = "linx-control-plane"
	ControlPlaneBinary    = "/usr/local/bin/service"
)

// sets is every nftables set this package may ever add or remove an
// element in. Fixed at compile time: whatever a line of the control
// plane's output names, only these two are ever passed to nft.
var sets = []string{installer.TrunkAddressSet, installer.TrunkPlainAddressSet}

// Exec runs a command and returns its combined output: docker exec to read
// the control plane, nft to read and change the firewall.
type Exec func(ctx context.Context, name string, args ...string) ([]byte, error)

// Env is what Once needs from the host.
type Env struct {
	Exec Exec
	Log  *slog.Logger
}

func (e Env) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

// Once runs one sync: reads the control plane, then brings each of the two
// sets in line. A set's own failure doesn't stop the other one: they're
// independent (docs/TRUNKS.md §12), and one bad read shouldn't lock out a
// provider that's fine. If reading the control plane itself fails (it's
// not running, say), the firewall is left exactly as it was: an address
// that already worked keeps working through an unrelated outage, and a
// stale one waits at most one more tick to be caught, never handed a free
// pass to stay forever.
func (e Env) Once(ctx context.Context) error {
	log := e.log()
	out, err := e.Exec(ctx, "docker", "exec", ControlPlaneContainer, ControlPlaneBinary, "firewall", "addresses")
	if err != nil {
		return fmt.Errorf("reading trunk addresses from the control plane: %w: %s", err, strings.TrimSpace(string(out)))
	}
	want := parse(out)

	nft := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return e.Exec(ctx, name, args...)
	}
	var errs []error
	for _, set := range sets {
		added, removed, err := nftset.Sync(ctx, nft, installer.FirewallTable, set, want[set])
		if err != nil {
			errs = append(errs, fmt.Errorf("set %s: %w", set, err))
			continue
		}
		for _, a := range added {
			log.Info("a phone-line provider's address was admitted", "set", set, "address", a)
		}
		for _, a := range removed {
			log.Info("a phone-line provider's address was withdrawn", "set", set, "address", a)
		}
	}
	return errors.Join(errs...)
}

// parse reads "<set> <address>" lines. A line naming a set that isn't one
// of the two Once ever touches, or that doesn't parse as an address, is
// ignored: it's simply never looked up in the sync loop below, whatever it
// says.
func parse(out []byte) map[string][]netip.Addr {
	want := map[string][]netip.Addr{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		set, addr, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		a, err := netip.ParseAddr(addr)
		if err != nil {
			continue
		}
		want[set] = append(want[set], a)
	}
	return want
}
