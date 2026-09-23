// Command certd issues, renews and deploys Linx's public certificate (lego,
// ADR-010). With -once it checks and renews once, then exits (tests, doctor).
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	legolog "github.com/go-acme/lego/v4/log"

	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/health"
	"linxpbx.com/linx/internal/server"
	"linxpbx.com/linx/internal/version"
)

const service = "linx-certd"

func main() {
	once := flag.Bool("once", false, "check and renew once, then exit")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service)
	legolog.Logger = slog.NewLogLogger(log.With("component", "lego").Handler(), slog.LevelInfo)
	log.Info("starting", "version", version.String(service))

	cfg, err := certs.ConfigFromEnv(os.Getenv)
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(2)
	}
	dns, err := certs.DNSProvider(cfg)
	if err != nil {
		log.Error("DNS provider", "err", err)
		os.Exit(2)
	}
	m := &certs.Manager{
		Config:  cfg,
		Store:   certs.Store{Dir: cfg.CertsDir},
		Issuers: certs.Issuers(cfg, dns),
		Log:     log,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *once {
		if err := m.Check(ctx); err != nil {
			log.Error("certificate check failed", "err", err)
			os.Exit(1)
		}
		return
	}

	go m.Run(ctx)

	mux := http.NewServeMux()
	mux.Handle("/healthz", health.Handler(service))
	mux.Handle("/metrics", certs.MetricsHandler(m))

	addr := os.Getenv("LINX_LISTEN_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	if err := server.Run(addr, mux, log); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
