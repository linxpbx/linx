// Command certd issues, renews and deploys Linx's public certificate (lego,
// ADR-010). With -once it checks and renews once, then exits (tests, doctor).
// With -records meet,api,turn it points those names at this network's
// public address in DNS, then exits (linx setup, docs/WEB.md §3).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
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
	address := flag.String("address", "", "with -records: point them at this address instead (home only)")
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
		os.Exit(pointRecords(cfg, strings.Split(*records, ","), *address, log))
	}
	follower, err := followerFromEnv(cfg, os.Getenv, log)
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
	if follower != nil {
		go follower.Run(ctx)
	}

	mux := http.NewServeMux()
	mux.Handle("/healthz", health.Handler(service))
	mux.Handle("/metrics", certs.MetricsHandler(m, follower))

	addr := os.Getenv("LINX_LISTEN_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	if err := server.Run(addr, mux, log); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// checkHosts accepts only Linx's own host names.
func checkHosts(hosts []string) error {
	for _, h := range hosts {
		if !slices.Contains(certs.Hostnames, h) {
			return fmt.Errorf("%q isn't one of Linx's host names %v", h, certs.Hostnames)
		}
	}
	return nil
}

func parseAddress(s string) (netip.Addr, error) {
	if s == "" {
		return netip.Addr{}, nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() {
		return netip.Addr{}, fmt.Errorf("%q isn't an IPv4 address", s)
	}
	return a, nil
}

// pointRecords is -records.
func pointRecords(cfg certs.Config, hosts []string, address string, log *slog.Logger) int {
	fixed, err := parseAddress(address)
	if err == nil {
		err = checkHosts(hosts)
	}
	if err != nil {
		log.Error("-records", "err", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := &certs.Follower{Client: certs.NewRecordsClient(), Config: cfg, Hosts: hosts, Fixed: fixed, Log: log}
	if err := f.Check(ctx); err != nil {
		log.Error("DNS records", "err", err)
		return 1
	}
	return 0
}

// followerFromEnv is the IP follower when LINX_DNS_RECORDS is set.
func followerFromEnv(cfg certs.Config, getenv func(string) string, log *slog.Logger) (*certs.Follower, error) {
	v := strings.TrimSpace(getenv("LINX_DNS_RECORDS"))
	if v == "" {
		return nil, nil
	}
	hosts := strings.Split(v, ",")
	if err := checkHosts(hosts); err != nil {
		return nil, fmt.Errorf("LINX_DNS_RECORDS: %w", err)
	}
	fixed, err := parseAddress(strings.TrimSpace(getenv("LINX_DNS_ADDRESS")))
	if err != nil {
		return nil, fmt.Errorf("LINX_DNS_ADDRESS: %w", err)
	}
	client := certs.NewRecordsClient()
	client.OwnOnly = true
	return &certs.Follower{Client: client, Config: cfg, Hosts: hosts, Fixed: fixed, Log: log.With("component", "dns")}, nil
}
