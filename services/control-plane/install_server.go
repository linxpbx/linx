package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"linxpbx.com/linx/internal/install"
	"linxpbx.com/linx/internal/version"
	"linxpbx.com/linx/internal/webapp"
)

// runInstallServer is `install-server`, the control plane in install mode
// (docs/INSTALL.md §3, ADR-057): deploy/compose/install.yaml runs it before
// anything else on the server is set up, so it needs no database, domain,
// certificate or secret. It serves the one-time setup link's pages on plain
// HTTP port 6464 and nothing else, and listens for linx setup's bridge on
// its own socket (install-bridge); linx setup, on the host, decides
// everything.
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

	health := http.NewServeMux()
	health.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	servers := []*http.Server{
		{Addr: envOr(getenv, "LINX_INSTALL_ADDR", ":6464"), Handler: srv.Handler(),
			ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second,
			IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10},
		{Addr: envOr(getenv, "LINX_HEALTH_ADDR", defaultHealthAddr), Handler: health, ReadHeaderTimeout: 5 * time.Second},
	}
	errc := make(chan error, len(servers))
	for _, s := range servers {
		l, err := net.Listen("tcp", s.Addr)
		if err != nil {
			log.Error("listening", "addr", s.Addr, "err", err)
			return 1
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
