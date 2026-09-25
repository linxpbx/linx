// Command coturn-entrypoint runs linx-coturn, the relay for browsers away
// from home (ADR-039, docs/WEB.md §2): it finds Asterisk's address on
// linx-media, renders coturn's configuration there (internal/turnconf),
// starts turnserver, and stays beside it: it forwards docker stop's signal,
// sends SIGUSR2 when linx-certd renews the certificate (coturn re-reads it
// without dropping anything), and exits if Asterisk's address changes, so
// Docker restarts the container with the new one.
//
// `coturn-entrypoint healthcheck` is the container's Docker health check.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/turnconf"
	"linxpbx.com/linx/internal/version"
)

const service = "linx-coturn"

const turnserverBin = "/usr/bin/turnserver"

// checkInterval is how often the certificate and Asterisk's address are
// checked (LINX_CHECK_INTERVAL overrides it for tests).
const checkInterval = time.Minute

func main() {
	cfg := turnconf.ConfigFromEnv(os.Getenv)
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := turnconf.Healthy(ctx, "127.0.0.1", cfg.Realm, cfg.CertsDir); err != nil {
			os.Stderr.WriteString("unhealthy: " + err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(0)
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service)
	log.Info("starting", "version", version.String(service))
	interval := checkInterval
	if v := os.Getenv("LINX_CHECK_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			log.Error("LINX_CHECK_INTERVAL: want a duration like 30s", "value", v)
			os.Exit(2)
		}
		interval = d
	}

	addrs, err := cfg.Resolve()
	if err != nil {
		log.Error("finding the phone system", "err", err)
		os.Exit(1)
	}
	if err := cfg.Render(addrs); err != nil {
		log.Error("render configuration", "err", err)
		os.Exit(2)
	}
	log.Info("relaying to the phone system only", "peer", addrs.Peer, "relay", addrs.Relay)

	cmd := exec.Command(turnserverBin, "-c", cfg.ConfPath)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	watcher := &certs.Watcher{Dir: cfg.CertsDir, Log: log, Reload: func(context.Context) error {
		return cmd.Process.Signal(syscall.SIGUSR2)
	}}
	watcher.Start()

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT)
	if err := cmd.Start(); err != nil {
		log.Error("start turnserver", "err", err)
		os.Exit(1)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peerMoved := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			watcher.Check(ctx)
			// Asterisk came back with another address (a new container):
			// coturn's allowed peer is fixed at start, so start over.
			if now, err := cfg.Resolve(); err == nil && now != addrs {
				log.Warn("the phone system's address changed; restarting to relay to the new one", "was", addrs.Peer, "now", now.Peer)
				close(peerMoved)
				return
			}
		}
	}()

	for {
		select {
		case s := <-sigs:
			_ = cmd.Process.Signal(s)
		case <-peerMoved:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			<-done
			os.Exit(3)
		case err := <-done:
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				log.Info("turnserver exited", "status", exit.ExitCode())
				os.Exit(max(exit.ExitCode(), 1))
			}
			if err != nil {
				log.Error("turnserver", "err", err)
				os.Exit(1)
			}
			log.Info("turnserver exited")
			os.Exit(0)
		}
	}
}
