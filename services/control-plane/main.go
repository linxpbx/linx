// Command control-plane is the Linx API, ARI app, provisioning and push gateway.
// Phase 1 so far: database, migrations, the API skeleton, authentication
// (API keys, OAuth client credentials, scopes, rate limits) and webhooks
// (outbox worker, SSRF-guarded delivery, delivery log; docs/API.md §8).
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

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/health"
	"linxpbx.com/linx/internal/safehttp"
	"linxpbx.com/linx/internal/server"
	"linxpbx.com/linx/internal/store"
	"linxpbx.com/linx/internal/version"
	"linxpbx.com/linx/internal/webhook"
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

	encKey, err := dbsecret.LoadKey(dbsecret.KeyPathFromEnv(os.Getenv))
	if err != nil {
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

	// Outbound connections to admin-given URLs (docs/API.md §4): never to
	// this container's own networks, private ranges only if allowlisted.
	own, err := safehttp.OwnNetworks()
	if err != nil {
		log.Error("reading network interfaces", "err", err)
		os.Exit(1)
	}
	policy := safehttp.Policy{Own: own, Allowlist: webhook.Allowlist(st)}
	sender := &webhook.Sender{
		Client: safehttp.NewClient(policy, safehttp.Options{}),
		Sealer: dbsecret.NewSealer(encKey),
		Now:    time.Now,
	}
	webhooks := &webhook.Service{Store: st, Sealer: sender.Sealer, Sender: sender, Policy: policy, Now: time.Now}
	worker := &webhook.Worker{
		Store: st, Sender: sender, Log: log,
		OnDisabled: func(_ context.Context, tenant, endpoint uuid.UUID, reason string) {
			// Admin alerts arrive in docs/API.md §8 step 5; until then the
			// log line and audit entry are the record.
			log.Warn("webhook endpoint disabled", "tenant", tenant, "endpoint", endpoint, "reason", reason)
		},
	}
	workerCtx, stopWorker := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		worker.Run(workerCtx)
	}()
	defer func() {
		stopWorker()
		<-workerDone
	}()

	apiHandler, err := newAPIHandler(log, st, authn, webhooks)
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
		stopWorker()
		<-workerDone
		os.Exit(1)
	}
}
