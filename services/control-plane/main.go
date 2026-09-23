// Command control-plane is the Linx API, ARI app, provisioning and push gateway.
// Phase 1 so far: database, migrations, the API skeleton and authentication
// (API keys, OAuth client credentials, scopes, rate limits; docs/API.md §8).
//
// `control-plane api-key ...` is the server-side key tool that `linx api-key`
// runs inside this container (apikey_cmd.go).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/health"
	"linxpbx.com/linx/internal/server"
	"linxpbx.com/linx/internal/store"
	"linxpbx.com/linx/internal/version"
)

const service = "linx-control-plane"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "api-key" {
		os.Exit(runAPIKeyCommand(context.Background(), os.Args[2:], os.Stdout, os.Stderr))
	}

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

	signingKey, err := auth.LoadSigningKey(auth.SigningKeyPathFromEnv(os.Getenv))
	if err != nil {
		log.Error("token signing key", "err", err)
		os.Exit(1)
	}
	tokens, err := auth.NewTokens(signingKey)
	if err != nil {
		log.Error("token signing key", "err", err)
		os.Exit(1)
	}
	ips, err := auth.NewClientIPResolver(os.Getenv("LINX_TRUSTED_PROXIES"))
	if err != nil {
		log.Error("LINX_TRUSTED_PROXIES", "err", err)
		os.Exit(1)
	}

	st := store.New(pool)
	if _, err := st.DefaultTenant(startCtx); err != nil {
		log.Error("database setup failed", "err", err)
		os.Exit(1)
	}
	authn := auth.NewAuthenticator(st, tokens, ips, log)

	apiHandler, err := newAPIHandler(log, st, authn)
	if err != nil {
		log.Error("api handler setup failed", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/healthz", health.Handler(service))
	mux.Handle("/api/v1/", apiHandler)
	mux.Handle(auth.TokenPath, authn.TokenHandler())

	addr := os.Getenv("LINX_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if err := server.Run(addr, mux, log); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
