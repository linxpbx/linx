// Command control-plane is the Linx API, ARI app, provisioning and push gateway.
// Phase 1 so far: database, migrations, the API skeleton, authentication
// (API keys, OAuth client credentials, scopes, rate limits), webhooks
// (outbox worker, SSRF-guarded delivery, delivery log) and admin alerts
// (engine, six channels, first sources; docs/API.md §8).
//
// `control-plane api-key ...` is the server-side key tool that `linx api-key`
// runs inside this container (apikey_cmd.go).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/alert"
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

// certdPollInterval is how often the certificate renewal alert source
// checks linx-certd's status (docs/API.md §5).
const certdPollInterval = 5 * time.Minute

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
	tenant, err := st.DefaultTenant(startCtx)
	if err != nil {
		log.Error("database setup failed", "err", err)
		os.Exit(1)
	}
	authn := auth.NewAuthenticator(st, tokens, ips, log)

	// Outbound connections to admin-given URLs (docs/API.md §4, §5): never
	// to this container's own networks, private ranges only if allowlisted.
	own, err := safehttp.OwnNetworks()
	if err != nil {
		log.Error("reading network interfaces", "err", err)
		os.Exit(1)
	}
	policy := safehttp.Policy{Own: own, Allowlist: webhook.Allowlist(st)}
	guardedClient := safehttp.NewClient(policy, safehttp.Options{})
	sealer := dbsecret.NewSealer(encKey)

	sender := &webhook.Sender{Client: guardedClient, Sealer: sealer, Now: time.Now}
	webhooks := &webhook.Service{Store: st, Sealer: sealer, Sender: sender, Policy: policy, Now: time.Now}

	alertSender := &alert.Sender{Client: guardedClient, Sealer: sealer, Now: time.Now}
	alerts := &alert.Service{Store: st, Sealer: sealer, Sender: alertSender, Policy: policy, Now: time.Now}
	engine := &alert.Engine{Store: st, Sender: alertSender, Log: log}

	// "webhook endpoint disabled" (docs/API.md §5): fired whether the
	// worker turned it off automatically or an admin did, resolved once
	// it's turned back on. The same key either way, so re-enabling always
	// clears it.
	webhookDisabledKey := func(endpoint uuid.UUID) string { return "webhook.disabled:" + endpoint.String() }
	worker := &webhook.Worker{
		Store: st, Sender: sender, Log: log,
		OnDisabled: func(ctx context.Context, tenant, endpoint uuid.UUID, reason string) {
			msg := "Linx turned this webhook off because every delivery failed for 5 days."
			if reason == webhook.DisabledGone {
				msg = "The receiver answered 410 Gone, so Linx turned this webhook off."
			}
			if err := engine.Fire(ctx, tenant, webhookDisabledKey(endpoint), alert.SeverityWarning,
				"A webhook was turned off", msg, ""); err != nil {
				log.Error("firing webhook-disabled alert failed", "err", err)
			}
		},
	}
	webhooks.OnEnabledChanged = func(ctx context.Context, tenant, endpoint uuid.UUID, enabled bool) {
		var err error
		if enabled {
			err = engine.Resolve(ctx, tenant, webhookDisabledKey(endpoint))
		} else {
			err = engine.Fire(ctx, tenant, webhookDisabledKey(endpoint), alert.SeverityWarning,
				"A webhook was turned off", "An admin turned this webhook off.", "")
		}
		if err != nil {
			log.Error("updating webhook-disabled alert failed", "err", err)
		}
	}

	bgCtx, stopBackground := context.WithCancel(context.Background())
	var bg sync.WaitGroup
	runBackground := func(run func(context.Context)) {
		bg.Add(1)
		go func() {
			defer bg.Done()
			run(bgCtx)
		}()
	}
	runBackground(worker.Run)
	runBackground(engine.Run)
	// The certificate renewal alert source polls linx-certd directly (not
	// through the SSRF guard: it's Linx's own service, not an admin URL).
	runBackground(func(ctx context.Context) {
		pollCertd(ctx, &http.Client{Timeout: 10 * time.Second}, engine, tenant, certdPollInterval, log)
	})
	defer func() {
		stopBackground()
		bg.Wait()
	}()

	apiHandler, err := newAPIHandler(log, st, authn, webhooks, alerts)
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
		stopBackground()
		bg.Wait()
		os.Exit(1)
	}
}
