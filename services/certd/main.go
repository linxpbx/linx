// Command certd issues, renews and deploys Linx's public certificate (lego,
// ADR-010). With -once it checks and renews once, then exits (tests, doctor).
// With -records @,turn it points those names at this network's
// public address in DNS, then exits (linx setup, docs/WEB.md §3). With
// -bootstrap staging|real it gets the web install's first certificate by
// TLS-ALPN-01, with no DNS token (docs/INSTALL.md §4), prints one result
// line and exits.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"linxpbx.com/linx/internal/dnsapi/clients"
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

// The DNS companies' clients (internal/dnsapi/clients) are linked here, not
// into every program that uses internal/certs.
func init() { certs.Connect = clients.New }

func main() {
	once := flag.Bool("once", false, "check and renew once, then exit")
	records := flag.String("records", "", "point these host names (comma-separated, e.g. @,turn,sip=192.168.1.212; @ is the domain itself; a name with =ADDRESS points there) at this network's public address, then exit")
	address := flag.String("address", "", "with -records: point them at this address instead (home only)")
	bootstrap := flag.String("bootstrap", "", "staging or real: get the install's first certificate through port 443, print the result, then exit")
	flag.Parse()

	if *bootstrap != "" {
		os.Exit(runBootstrap(*bootstrap, os.Getenv, os.Stdout))
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service)
	legolog.Logger = slog.NewLogLogger(log.With("component", "lego").Handler(), slog.LevelInfo)
	log.Info("starting", "version", version.String(service))

	cfg, err := certs.ConfigFromEnv(os.Getenv)
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(2)
	}
	if *records != "" {
		os.Exit(pointRecords(cfg, *records, *address, log))
	}
	follower, err := followerFromEnv(cfg, os.Getenv, log)
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(2)
	}
	var issuers []certs.Issuer
	if cfg.Challenge == certs.ChallengeALPN {
		// No DNS token: renewed through port 443 (docs/INSTALL.md §5).
		issuers = []certs.Issuer{certs.ALPNIssuer(cfg)}
	} else {
		dns, err := certs.DNSProvider(cfg)
		if err != nil {
			log.Error("DNS provider", "err", err)
			os.Exit(2)
		}
		issuers = certs.Issuers(cfg, dns)
	}
	m := &certs.Manager{
		Config:  cfg,
		Store:   certs.Store{Dir: cfg.CertsDir},
		Issuers: issuers,
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
	mux.Handle("/dns", certs.DNSStatusHandler(cfg, follower))

	addr := os.Getenv("LINX_LISTEN_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	if err := server.Run(addr, mux, log); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// parseRecords accepts only Linx's own host names (and the web app's names
// from before it moved to the base domain, until setup runs again), each
// optionally pinned to its own address ("sip=192.168.1.212").
func parseRecords(cfg certs.Config, s string) ([]string, []certs.PinnedRecord, error) {
	hosts, pinned, err := certs.ParseRecords(s, slices.Concat(certs.Hostnames, certs.LegacyHosts))
	if err == nil && len(pinned) > 0 && cfg.Provider == certs.ProviderDuckDNS { // dnsapi: DuckDNS is OneAddress
		err = errors.New("DuckDNS gives every name one address, so no name can have its own")
	}
	return hosts, pinned, err
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
func pointRecords(cfg certs.Config, records, address string, log *slog.Logger) int {
	fixed, err := parseAddress(address)
	var (
		hosts  []string
		pinned []certs.PinnedRecord
	)
	if err == nil {
		hosts, pinned, err = parseRecords(cfg, records)
	}
	if err != nil {
		log.Error("-records", "err", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := certs.NewRecordsClientFor(cfg)
	client.State = &certs.DNSState{Path: certs.StatePath(cfg.StateDir)}
	f := &certs.Follower{Client: client, Config: cfg, Hosts: hosts, Fixed: fixed, Pinned: pinned, Log: log}
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
	hosts, pinned, err := parseRecords(cfg, v)
	if err != nil {
		return nil, fmt.Errorf("LINX_DNS_RECORDS: %w", err)
	}
	fixed, err := parseAddress(strings.TrimSpace(getenv("LINX_DNS_ADDRESS")))
	if err != nil {
		return nil, fmt.Errorf("LINX_DNS_ADDRESS: %w", err)
	}
	client := certs.NewRecordsClientFor(cfg)
	client.OwnOnly = true
	client.State = &certs.DNSState{Path: certs.StatePath(cfg.StateDir)}
	return &certs.Follower{Client: client, Config: cfg, Hosts: hosts, Fixed: fixed, Pinned: pinned, Log: log.With("component", "dns")}, nil
}

// runBootstrap is -bootstrap.
func runBootstrap(mode string, getenv func(string) string, stdout io.Writer) int {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("service", service, "mode", "bootstrap")
	legolog.Logger = slog.NewLogLogger(log.With("component", "lego").Handler(), slog.LevelInfo)
	res := certs.BootstrapResult{OK: true}
	defer func() { _ = json.NewEncoder(stdout).Encode(map[string]certs.BootstrapResult{certs.ResultKey: res}) }()
	if mode != "staging" && mode != "real" {
		res = certs.BootstrapResult{Kind: certs.ProblemOther, Detail: "-bootstrap: want staging or real, got " + mode}
		return 2
	}
	b, err := certs.BootstrapFromEnv(getenv)
	if err != nil {
		log.Error("invalid configuration", "err", err)
		res = certs.BootstrapResult{Kind: certs.ProblemOther, Detail: err.Error()}
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("getting the first certificate", "names", b.Names(), "staging", mode == "staging")
	if err := b.Obtain(ctx, mode == "staging", time.Now()); err != nil {
		p := certs.Classify(err)
		log.Error("first certificate", "err", err)
		res = certs.BootstrapResult{Kind: p.Kind, Detail: p.Detail}
		return 1
	}
	log.Info("first certificate done", "staging", mode == "staging")
	return 0
}
