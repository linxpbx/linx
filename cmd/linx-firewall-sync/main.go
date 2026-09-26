// Command linx-firewall-sync keeps the host firewall's phone-line provider
// sets (docs/TRUNKS.md §12) matching the trunks in the database. It runs
// once and exits: linx setup installs it as a systemd timer
// (installer.FirewallSyncPlan), not a long-running service. It must run as
// root (it changes nftables) and reads the control plane only through
// docker exec, the same trust as linx user.
package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"linxpbx.com/linx/internal/firewallsync"
)

func main() {
	os.Exit(run())
}

func run() int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "linx-firewall-sync must run as root (it changes nftables). "+
			"linx setup installs it as a systemd timer; don't run it by hand.")
		return 1
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("service", "linx-firewall-sync")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	env := firewallsync.Env{Exec: execRun, Log: log}
	if err := env.Once(ctx); err != nil {
		log.Error("syncing the phone-line firewall", "err", err)
		return 1
	}
	return 0
}

func execRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.Bytes(), err
}
