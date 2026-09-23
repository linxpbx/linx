// Command certd issues, renews and deploys public certificates (lego, ADR-010).
// Phase 0b item 4 adds ACME; for now it only exposes a health endpoint.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"linxpbx.com/linx/internal/health"
	"linxpbx.com/linx/internal/server"
	"linxpbx.com/linx/internal/version"
)

const service = "linx-certd"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service)
	log.Info("starting", "version", version.String(service))

	mux := http.NewServeMux()
	mux.Handle("/healthz", health.Handler(service))

	addr := os.Getenv("LINX_LISTEN_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	if err := server.Run(addr, mux, log); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
