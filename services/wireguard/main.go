// Command linx-wireguard brings up the WireGuard tunnels trunks connect
// through (docs/TRUNKS.md §7, ADR-046). It runs in Asterisk's network
// namespace (compose: network_mode service:asterisk), the only container
// with NET_ADMIN, and only there: Asterisk itself has no capability, and no
// other container's routes change.
//
// It starts as root only to hand itself NET_ADMIN as an ambient capability
// and re-run as uid 65532 (Docker gives a non-root user no capabilities);
// the root parent does nothing else but pass signals on. Then, every two
// seconds, it applies the tunnels the control plane rendered
// (internal/wgconf) and writes each one's last handshake back. When
// Asterisk restarts, its namespace is replaced and this one is left behind
// with no network card of its own: the agent notices and exits, and Docker
// starts it again in the new one.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"linxpbx.com/linx/internal/version"
)

const (
	defaultConfigDir = "/var/lib/linx/wireguard"
	defaultStatusDir = "/var/lib/linx/wireguard-status"
	// The distroless image's nonroot user.
	runAsUID = 65532
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if os.Getuid() == 0 {
		// The root parent: re-run as nonroot with NET_ADMIN, and exit as
		// the child does.
		os.Exit(dropPrivileges(log))
	}

	cfg := Config{
		ConfigDir: envOr("LINX_WIREGUARD_DIR", defaultConfigDir),
		StatusDir: envOr("LINX_WIREGUARD_STATUS_DIR", defaultStatusDir),
		Interval:  2 * time.Second,
	}
	if d, err := time.ParseDuration(os.Getenv("LINX_WIREGUARD_INTERVAL")); err == nil && d > 0 {
		cfg.Interval = d
	}
	log.Info("linx-wireguard starting", "version", version.Version, "config", cfg.ConfigDir, "status", cfg.StatusDir)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	k, err := newKernel()
	if err != nil {
		log.Error("WireGuard control unavailable", "err", err)
		os.Exit(1)
	}
	defer k.Close()
	a := &Agent{Config: cfg, Net: k, Now: time.Now, Log: log}
	err = a.Run(ctx)
	if errors.Is(err, errOrphaned) {
		log.Warn("Asterisk's network is gone (it restarted); exiting so Docker starts this again in its new one")
		os.Exit(3)
	}
	if err != nil && ctx.Err() == nil {
		log.Error("linx-wireguard stopped", "err", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
