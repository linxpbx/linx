// Command control-plane is the Linx API, ARI app, provisioning and push gateway.
// Phase 1: database, migrations and the API skeleton — validation, problem+json,
// pagination, /me, /openapi.json, /event-types (docs/API.md §8 step 2).
// Authentication (step 3) still stands in a fixed system principal.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/health"
	"linxpbx.com/linx/internal/server"
	"linxpbx.com/linx/internal/version"
)

const service = "linx-control-plane"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service)
	log.Info("starting", "version", version.String(service))

	startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := db.Connect(startCtx, db.ConfigFromEnv(os.Getenv))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	schemaVersion, err := db.Migrate(startCtx, pool)
	if err != nil {
		log.Error("database migration failed", "err", err)
		os.Exit(1)
	}
	log.Info("database ready", "schema_version", schemaVersion)

	// Not used yet (ADR-030's webhook/alert secret columns arrive in later
	// steps); loaded now to fail fast if the installer hasn't provisioned it.
	if _, err := dbsecret.LoadKey(dbsecret.KeyPathFromEnv(os.Getenv)); err != nil {
		log.Error("database encryption key", "err", err)
		os.Exit(1)
	}

	apiHandler, err := newAPIHandler(log)
	if err != nil {
		log.Error("api handler setup failed", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/healthz", health.Handler(service))
	mux.Handle("/api/v1/", apiHandler)

	addr := os.Getenv("LINX_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if err := server.Run(addr, mux, log); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
