// Command linx-ops-agent is System → Status's hands on the server
// (docs/ADMIN.md §9, ADR-056): it reports which Linx containers are
// running, reads a service's recent log lines and restarts one, when an
// admin asks in the browser, and does nothing else. It keeps one link to
// the control plane open through docker exec (internal/ops); the control
// plane never gets the Docker socket. linx setup installs it as a systemd
// service (installer.OpsAgentPlan). It must run as root: docker does.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"linxpbx.com/linx/internal/ops"
)

func main() {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "linx-ops-agent must run as root (docker does). "+
			"linx setup installs it as a systemd service; don't run it by hand.")
		os.Exit(1)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("service", "linx-ops-agent")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	a := &ops.Agent{Exec: ops.RunCommand, Dial: ops.DialControlPlane, Log: log}
	a.Run(ctx)
}
