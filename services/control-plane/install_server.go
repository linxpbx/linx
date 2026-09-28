package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/install"
	"linxpbx.com/linx/internal/version"
	"linxpbx.com/linx/internal/webapp"
)

// runInstallServer is `install-server`, the control plane in install mode
// (docs/INSTALL.md §3, ADR-057): deploy/compose/install.yaml runs it before
// anything else on the server is set up, so it needs no database, domain,
// certificate or secret. It serves the one-time setup link's pages on plain
// HTTP port 6464, and listens for linx setup's bridge on its own socket
// (install-bridge); linx setup, on the host, decides everything. Once the
// answers are saved it's also what the front door sends port 443 to: 8443
// answers Let's Encrypt's acme-tls/1 check with certd's challenge
// certificates, then serves the secure page with the first certificate;
// 5349 (TURN over TLS, where turn.<domain> goes through Pangolin and nginx)
// answers only the check (docs/INSTALL.md §4.1).
func runInstallServer(getenv func(string) string) int {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service, "mode", "install")
	log.Info("starting in install mode", "version", version.String(service))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &install.Server{Web: os.DirFS(envOr(getenv, "LINX_WEB_DIR", webapp.DefaultDir)), Log: log}
	ln, err := install.Listen(envOr(getenv, "LINX_INSTALL_SOCKET", install.DefaultSocket))
	if err != nil {
		log.Error("install bridge socket", "err", err)
		return 1
	}
	go srv.ServeBridge(ctx, ln)

	ips, err := auth.NewClientIPResolver(getenv("LINX_TRUSTED_PROXIES"))
	if err != nil {
		log.Error("LINX_TRUSTED_PROXIES", "err", err)
		return 1
	}
	useProxyProtocol, err := strconv.ParseBool(envOr(getenv, "LINX_PROXY_PROTOCOL", "true"))
	if err != nil {
		log.Error("LINX_PROXY_PROTOCOL: want true or false", "err", err)
		return 1
	}
	ips.IgnoreForwardedFor = useProxyProtocol
	go refreshTrustedProxies(ctx, ips, log)
	challenges := certs.Challenges{Dir: envOr(getenv, "LINX_CHALLENGE_DIR", defaultChallengeDir)}
	serving := &certs.ServingCert{Dir: envOr(getenv, "LINX_CERTS_DIR", defaultCertsDir)}
	turnLn, err := net.Listen("tcp", envOr(getenv, "LINX_CHALLENGE_ADDR", ":5349"))
	if err != nil {
		log.Error("listening", "err", err)
		return 1
	}
	go install.ServeChallenges(ctx, turnLn, install.ChallengeTLSConfig(challenges))

	health := http.NewServeMux()
	health.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	servers := []*http.Server{
		{Addr: envOr(getenv, "LINX_INSTALL_ADDR", ":6464"), Handler: srv.Handler(),
			ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second,
			IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10},
		{Addr: envOr(getenv, "LINX_HEALTH_ADDR", defaultHealthAddr), Handler: health, ReadHeaderTimeout: 5 * time.Second},
		{Addr: envOr(getenv, "LINX_LISTEN_ADDR", ":8443"), Handler: srv.SecureHandler(), TLSConfig: install.TLSConfig(challenges, serving),
			ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second,
			IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10},
	}
	errc := make(chan error, len(servers))
	for _, s := range servers {
		l, err := net.Listen("tcp", s.Addr)
		if err != nil {
			log.Error("listening", "addr", s.Addr, "err", err)
			return 1
		}
		if s.TLSConfig != nil {
			l = proxyListener(ips, useProxyProtocol)(l)
			go func() { errc <- s.ServeTLS(l, "", "") }()
			continue
		}
		go func() { errc <- s.Serve(l) }()
	}
	log.Info("install page ready", "addr", servers[0].Addr)
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("install server", "err", err)
			return 1
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdown)
	}
	return 0
}

func installSocket(getenv func(string) string) string {
	return envOr(getenv, "LINX_INSTALL_SOCKET", install.DefaultSocket)
}

// defaultChallengeDir is where compose mounts certd's challenge
// certificates (install.yaml's acme-challenge volume).
const defaultChallengeDir = "/var/lib/linx/acme-challenge"
