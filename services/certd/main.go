// Command certd issues, renews and deploys Linx's public certificate (lego,
// ADR-010). With -once it checks and renews once, then exits (tests, doctor).
// With -records meet,api,turn it points those names at this network's
// public address in DNS, then exits (linx setup, docs/WEB.md §3).
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	legolog "github.com/go-acme/lego/v4/log"

	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/health"
	"linxpbx.com/linx/internal/server"
	"linxpbx.com/linx/internal/version"
)

const service = "linx-certd"

func main() {
	once := flag.Bool("once", false, "check and renew once, then exit")
	records := flag.String("records", "", "point these host names (comma-separated, e.g. meet,api,turn) at this network's public address, then exit")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service)
	legolog.Logger = slog.NewLogLogger(log.With("component", "lego").Handler(), slog.LevelInfo)
	log.Info("starting", "version", version.String(service))

	cfg, err := certs.ConfigFromEnv(os.Getenv)
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(2)
	}
	if *records != "" {
		os.Exit(pointRecords(cfg, strings.Split(*records, ","), log))
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

// pointRecords is -records: only Linx's own host names are accepted.
func pointRecords(cfg certs.Config, hosts []string, log *slog.Logger) int {
	for _, h := range hosts {
		if !slices.Contains(certs.Hostnames, h) {
			log.Error("not one of Linx's host names", "host", h, "known", certs.Hostnames)
			return 2
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := certs.NewRecordsClient()
	ip, err := c.PublicIPv4(ctx)
	if err != nil {
		log.Error("public address", "err", err)
		return 1
	}
	res, err := c.PointRecords(ctx, cfg, hosts, ip)
	for _, r := range res {
		log.Info("DNS record", "name", r.Name, "result", r.Outcome)
	}
	if err != nil {
		log.Error("DNS records", "err", err)
		return 1
	}
	return 0
}
