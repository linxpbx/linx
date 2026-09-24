package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"linxpbx.com/linx/internal/ari"
	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/stepca"
)

// ARIHost is the name Asterisk dials for the control plane's ARI websocket:
// a network alias compose.yaml gives the control plane on linx-private only,
// so it never resolves to an address on another network.
const ARIHost = "linx-ari"

type ariConfig struct {
	// Listen is where the ARI websocket listens. Empty: this container's
	// address on linx-private, port 8089 (see privateAddr).
	Listen         string
	PasswordFile   string
	CAURL          string
	CARootFile     string
	CAPasswordFile string
}

func ariConfigFromEnv(getenv func(string) string) ariConfig {
	or := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	return ariConfig{
		Listen:         getenv("LINX_ARI_LISTEN_ADDR"),
		PasswordFile:   or("LINX_ARI_PASSWORD_FILE", "/run/secrets/linx_ari_password"),
		CAURL:          or("LINX_CA_URL", "https://step-ca:9000"),
		CARootFile:     or("LINX_CA_ROOT_FILE", "/etc/linx/ca/root_ca.crt"),
		CAPasswordFile: or("LINX_CA_SERVICES_PASSWORD_FILE", "/run/secrets/linx_ca_services_password"),
	}
}

// startARI serves Asterisk's outbound ARI websocket (ADR-034): TLS only,
// with a certificate from the internal CA's linx-services provisioner that
// renews itself, and Asterisk's password checked on every connection. It
// returns a func that stops it.
func startARI(ctx context.Context, cfg ariConfig, app ari.App, log *slog.Logger, runBackground func(func(context.Context))) (func(), error) {
	password, err := readSecret(cfg.PasswordFile)
	if err != nil {
		return nil, fmt.Errorf("ARI password: %w", err)
	}
	caPassword, err := readSecret(cfg.CAPasswordFile)
	if err != nil {
		return nil, fmt.Errorf("internal CA provisioner password: %w", err)
	}
	roots, err := stepca.LoadRoots(cfg.CARootFile)
	if err != nil {
		return nil, err
	}
	addr := cfg.Listen
	if addr == "" {
		ip, err := privateAddr(ctx, "postgres")
		if err != nil {
			return nil, fmt.Errorf("finding this container's linx-private address: %w", err)
		}
		addr = netip.AddrPortFrom(ip, 8089).String()
	}

	renewer := &stepca.Renewer{
		Issuer:     stepca.NewClient(cfg.CAURL, stepca.ServicesProvisioner, []byte(caPassword), roots),
		CommonName: ARIHost,
		DNSNames:   []string{ARIHost},
		Log:        log,
	}
	runBackground(renewer.Run)

	handler := &ari.Handler{User: asteriskconf.ARIUser, Password: []byte(password), App: app, Log: log}
	mux := http.NewServeMux()
	mux.Handle("/ari", handler)
	srv := &http.Server{
		Handler:           mux,
		TLSConfig:         &tls.Config{GetCertificate: renewer.GetCertificate, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("ARI listener: %w", err)
	}
	log.Info("ARI listening", "addr", addr)
	go func() {
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("ARI listener stopped", "err", err)
		}
	}()
	return func() {
		handler.Close()
		srv.Close()
	}, nil
}

// privateAddr is this container's address on the network where host lives.
// Postgres is only on linx-private, so privateAddr("postgres") is the
// linx-private address: binding ARI there keeps it off linx-public
// (CLAUDE.md: ARI stays on linx-private), with no subnet to configure.
func privateAddr(ctx context.Context, host string) (netip.Addr, error) {
	var ips []netip.Addr
	for attempt := 0; ; attempt++ {
		var err error
		ips, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err == nil {
			break
		}
		if attempt == 10 {
			return netip.Addr{}, err
		}
		time.Sleep(time.Second)
	}
	ifaces, err := net.InterfaceAddrs()
	if err != nil {
		return netip.Addr{}, err
	}
	for _, a := range ifaces {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		prefix, err := netip.ParsePrefix(n.String())
		if err != nil {
			continue
		}
		for _, ip := range ips {
			if prefix.Contains(ip.Unmap()) {
				return prefix.Addr().Unmap(), nil
			}
		}
	}
	return netip.Addr{}, fmt.Errorf("no interface on %s's network (%v)", host, ips)
}

func readSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return s, nil
}
