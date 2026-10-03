// Command control-plane is the Linx API, ARI app, provisioning and push gateway.
// Phase 1 so far: database, migrations, the API skeleton, authentication
// (API keys, OAuth client credentials, scopes, rate limits), webhooks
// (outbox worker, SSRF-guarded delivery, delivery log) and admin alerts
// (engine, six channels, first sources; docs/API.md §8). Phase 1B so far:
// extensions/devices, the linx_asterisk realtime role Asterisk reads over
// ODBC, and the ARI app Asterisk connects out to (ari.go; docs/PBX.md §4).
// Phase 1C so far: people's accounts and sessions (session.go), browsers'
// phone lines and the /sip relay (sip.go; docs/WEB.md §5), the web client
// itself on HTTPS 8443 (ADR-037) and the Team list's live updates (team.go).
//
// `control-plane api-key ...` is the server-side key tool that `linx api-key`
// runs inside this container (apikey_cmd.go); `control-plane healthcheck` is
// the container's Docker health check (healthcheck.go).
package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/alert"
	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/backupschedule"
	"linxpbx.com/linx/internal/callhistory"
	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/email"
	"linxpbx.com/linx/internal/health"
	"linxpbx.com/linx/internal/help"
	"linxpbx.com/linx/internal/helpanswers"
	"linxpbx.com/linx/internal/install"
	"linxpbx.com/linx/internal/moved"
	"linxpbx.com/linx/internal/numbering"
	"linxpbx.com/linx/internal/ops"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/routing"
	"linxpbx.com/linx/internal/safehttp"
	"linxpbx.com/linx/internal/server"
	"linxpbx.com/linx/internal/settings"
	"linxpbx.com/linx/internal/sso"
	"linxpbx.com/linx/internal/store"
	"linxpbx.com/linx/internal/trunk"
	"linxpbx.com/linx/internal/trunkconf"
	"linxpbx.com/linx/internal/trunkprobe"
	"linxpbx.com/linx/internal/turn"
	"linxpbx.com/linx/internal/version"
	"linxpbx.com/linx/internal/voicemail"
	"linxpbx.com/linx/internal/webapp"
	"linxpbx.com/linx/internal/webhook"
	"linxpbx.com/linx/internal/weburl"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

const service = "linx-control-plane"

// certdPollInterval is how often the certificate renewal alert source
// checks linx-certd's status (docs/API.md §5).
const certdPollInterval = 5 * time.Minute

// webDeviceSweepInterval is how often browser phone lines whose session
// ended on its own (expired) are marked revoked. Asterisk refuses them the
// moment the session ends regardless (migration 0013).
const webDeviceSweepInterval = 30 * time.Second

func main() {
	if len(os.Args) > 1 && os.Args[1] == "api-key" {
		os.Exit(runAPIKeyCommand(context.Background(), os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "user" {
		os.Exit(runUserCommand(context.Background(), os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "suggest-site" {
		os.Exit(runSuggestSiteCommand(context.Background(), os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "admin-access" {
		os.Exit(runAdminAccessCommand(context.Background(), os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "trunk" {
		os.Exit(runTrunkCommand(context.Background(), os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "route" {
		os.Exit(runRouteCommand(context.Background(), os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "firewall" {
		os.Exit(runFirewallCommand(context.Background(), os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "backup" {
		os.Exit(runBackupCommand(context.Background(), os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "ops-bridge" {
		os.Exit(runOpsBridge(opsSocket(os.Getenv), os.Stdin, os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "install-server" {
		os.Exit(runInstallServer(os.Getenv))
	}
	if len(os.Args) > 1 && os.Args[1] == "install-bridge" {
		// linx setup's side of the web install (docs/INSTALL.md §3): the
		// same joining of docker exec's input and output to a socket as
		// ops-bridge, to install mode's socket.
		os.Exit(runOpsBridge(installSocket(os.Getenv), os.Stdin, os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if len(os.Args) > 2 && os.Args[2] == "--install" {
			os.Exit(runInstallHealthcheck(os.Getenv, nil))
		}
		os.Exit(runHealthcheck(os.Getenv, nil))
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

	if err := db.EnsureAsteriskRole(startCtx, pool, db.AsteriskRolePasswordPathFromEnv(os.Getenv)); err != nil {
		log.Error("asterisk database role", "err", err)
		os.Exit(1)
	}
	sipDomain := ""
	if d := os.Getenv("LINX_DOMAIN"); d != "" {
		sipDomain = "sip." + d
	}
	if err := db.SetSIPDomain(startCtx, pool, sipDomain); err != nil {
		log.Error("phone system domain", "err", err)
		os.Exit(1)
	}
	// Office hours are in the server's time zone (TZ, set by setup).
	if err := db.SetTimeZone(startCtx, pool, os.Getenv("TZ")); err != nil {
		log.Warn("office hours time zone", "err", err)
	}

	// Outgoing calls are decided in the database from libphonenumber's data
	// (ADR-044); a new version of the library brings new data.
	numberingData, err := numbering.Build()
	if err != nil {
		log.Error("numbering data", "err", err)
		os.Exit(1)
	}
	if changed, err := store.New(pool).SyncNumbering(startCtx, numberingData, time.Now().UTC()); err != nil {
		log.Error("numbering data", "err", err)
		os.Exit(1)
	} else if changed {
		log.Info("numbering data updated", "version", numberingData.Version)
	}

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
	authn.Sessions = st

	// Admin portal settings: the numbering plan, site kind, Simple mode and
	// where admins may sign in from (docs/ADMIN.md §4, §3). "Only from my
	// home/office network" always includes the phone networks from setup,
	// on top of whatever the admin adds.
	phoneNetworks, err := asteriskconf.ParseSIPNetworks(os.Getenv("LINX_SIP_NETWORKS"))
	if err != nil {
		log.Error("LINX_SIP_NETWORKS", "err", err)
		os.Exit(1)
	}
	settingsSvc := &settings.Service{Store: st, Now: time.Now, PhoneNetworks: phoneNetworks}
	authn.Networks = settingsSvc

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

	// Backup schedule and history (docs/BACKUP.md §5, §8 step 3): this
	// process never runs restic itself, only decides when a run is due and
	// records what linx-backup-agent reports (services/control-plane/backup_cmd.go).
	// Backup files (§8 step 5) pass through the transfer folder, never
	// opened here.
	backups := &backupschedule.Service{Store: st, Alerts: engine, Restores: st, Sealer: sealer, Downloads: st,
		Transfer: &backupschedule.Transfer{Dir: envOr(os.Getenv, "LINX_BACKUP_TRANSFER_DIR", backupschedule.DefaultTransferDir)}, Now: time.Now}

	// People accounts and sessions (docs/WEB.md §4): the "someone is
	// guessing a password" alert reuses this same engine, and sign-in
	// failures share the per-address budget a bad API key or token draws on.
	accounts := &auth.Accounts{Store: st, Sealer: sealer, Alerts: engine, Failures: authn.Failures, Now: time.Now, Log: log, Passkeys: st}
	// Passkeys need the domain: it's their relying party (ADR-051).
	if d := os.Getenv("LINX_DOMAIN"); d != "" {
		wa, err := auth.NewWebAuthn(d, weburl.Port(os.Getenv))
		if err != nil {
			log.Error("passkeys", "err", err)
			os.Exit(1)
		}
		accounts.WebAuthn = wa
	}
	// Company sign-in (ADR-052): providers are reached through the same
	// guarded client as webhooks, so one on the home network needs its
	// address on the outbound allowlist. It needs the domain too: the
	// provider sends people back to Linx's address (with the public port
	// unless it's 443).
	ssoSvc := &sso.Service{Store: st, Sealer: sealer, Policy: policy, Now: time.Now}
	accounts.Company = st
	if d := os.Getenv("LINX_DOMAIN"); d != "" {
		client := &sso.Client{Store: st, Sealer: sealer, HTTP: guardedClient, RedirectURI: sso.RedirectURI(weburl.FromEnv(os.Getenv)), Now: time.Now}
		ssoSvc.Client, accounts.CompanyProviders = client, client
	}

	pbxSvc := &pbx.Service{Store: st, Now: time.Now, Domain: os.Getenv("LINX_DOMAIN")}
	// Trunk tests connect only where a phone line can be: never to Linx's
	// own container networks or loopback (docs/TRUNKS.md §10).
	trunks := &trunk.Service{Store: st, Sealer: sealer, Now: time.Now,
		Prober: &trunkprobe.Prober{Refuse: trunkprobe.RefuseOwn(own)}}
	// Trunks reach Asterisk as a rendered file (ADR-043, internal/trunkconf):
	// written at start, on every trunk change, and every minute (providers'
	// addresses can change on their own).
	// WireGuard profiles too, for linx-wireguard (docs/TRUNKS.md §7).
	trunkFiles := &trunkconf.Renderer{Store: st, Sealer: sealer, Log: log, SIPDomain: trunkSIPDomain(os.Getenv("LINX_DOMAIN")),
		Dir:          envOr(os.Getenv, "LINX_TRUNKS_DIR", "/var/lib/linx/trunks"),
		WireGuardDir: envOr(os.Getenv, "LINX_WIREGUARD_DIR", "/var/lib/linx/wireguard")}
	trunks.OnChange = trunkFiles.Changed

	// Browsers' phone lines (docs/WEB.md §5): relay credentials, and the
	// /sip relay to Asterisk's websocket, which drops a line the moment its
	// session ends.
	turnSecret, err := turn.LoadSecret(turn.SecretPathFromEnv(os.Getenv))
	if err != nil {
		log.Error("relay secret", "err", err)
		os.Exit(1)
	}
	turnURLs, err := turn.URLsFromEnv(os.Getenv)
	if err != nil {
		log.Error("relay addresses", "err", err)
		os.Exit(1)
	}
	turnIssuer := &turn.Issuer{Secret: turnSecret, URLs: turnURLs, Now: time.Now}
	ariCfg := ariConfigFromEnv(os.Getenv)
	relay, err := newSIPRelay(sipwsURLFromEnv(os.Getenv), ariCfg.CARootFile, st, log)
	if err != nil {
		log.Error("phone line relay", "err", err)
		os.Exit(1)
	}
	accounts.SessionsEnded = func(ctx context.Context, user uuid.UUID, session *uuid.UUID) {
		if session != nil {
			relay.CloseSession(*session)
		} else {
			relay.CloseUser(user)
		}
		revokeDeadWebDevices(ctx, st, relay, log)
	}
	// The ARI app: device online state, call webhooks, /calls/active.
	tracker := &pbx.CallTracker{Store: st, Log: log, Now: time.Now}
	// Emergency calls, unusual calling abroad and first calls to a country
	// (docs/TRUNKS.md §9).
	callAlerts := &trunk.CallAlerts{Store: st, Alerts: engine, InProgress: tracker.OutsideCallsInProgress, Now: time.Now, Log: log}
	tracker.Watch = callAlerts
	// Each trunk's state from Asterisk's report, trunk.status_changed and
	// the "trunk is down" alert; each WireGuard tunnel's from
	// linx-wireguard's, and the "tunnel is down" alert.
	trunkMonitor := &trunk.Monitor{Store: st, Alerts: engine, Now: time.Now, Log: log,
		Dir:          envOr(os.Getenv, "LINX_TRUNK_STATUS_DIR", "/var/lib/linx/trunk-status"),
		Tunnels:      st,
		WireGuardDir: envOr(os.Getenv, "LINX_WIREGUARD_STATUS_DIR", "/var/lib/linx/wireguard-status")}
	trunks.OnDeleted = func(ctx context.Context, tenant, id uuid.UUID) {
		if err := engine.Resolve(ctx, tenant, trunk.DownAlertKey(id)); err != nil {
			log.Error("closing a deleted trunk's alert", "err", err)
		}
	}
	trunks.OnWireGuardProfileDeleted = func(ctx context.Context, tenant, id uuid.UUID) {
		if err := engine.Resolve(ctx, tenant, trunk.TunnelDownAlertKey(id)); err != nil {
			log.Error("closing a deleted WireGuard profile's alert", "err", err)
		}
	}
	// The Team list and its live updates (docs/WEB.md §6).
	team := &pbx.Team{Store: st, Calls: tracker}
	hub := newTeamHub(team, log)
	tracker.OnChange, team.OnChange = hub.Changed, hub.Changed

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
	// Email (ADR-066): through the owner's own mail account, over the same
	// private-address guard. When it stops working, an alert goes out
	// through the other channels (never by email, internal/alert).
	emailSvc := &email.Service{Store: st, Sealer: sealer, Policy: policy, Now: time.Now, Log: log,
		Sender: &email.Sender{Dial: email.GuardedDial(policy, nil), Now: time.Now},
		Broken: func(ctx context.Context, tenant uuid.UUID, detail string) {
			if err := engine.Fire(ctx, tenant, alert.EmailBrokenKey, alert.SeverityWarning, "Email isn't sending",
				detail+" Invites, password resets and voicemail emails wait until it works again. "+
					"(This alert never comes by email.)", "/admin/system/settings"); err != nil {
				log.Error("firing the email alert failed", "err", err)
			}
		},
		Working: func(ctx context.Context, tenant uuid.UUID) {
			if err := engine.Resolve(ctx, tenant, alert.EmailBrokenKey); err != nil {
				log.Error("resolving the email alert failed", "err", err)
			}
		},
	}
	alertSender.Email, alertSender.WebAddress = emailSvc, weburl.FromEnv(os.Getenv)
	accounts.Mailer = accountMail{email: emailSvc, web: weburl.FromEnv(os.Getenv)}
	// Voicemail (ADR-069): Asterisk records into a folder only it and this
	// container share, and says so over ARI; the importer keeps each
	// message in the database, queues voicemail.created and emails it
	// (the audio fetched as the email goes out).
	voicemails := &voicemail.Importer{Dir: envOr(os.Getenv, "LINX_VOICEMAIL_DIR", asteriskconf.VoicemailDir), Store: st,
		Mail: emailSvc, WebAddress: weburl.FromEnv(os.Getenv), Changed: hub.VoicemailChanged, Now: time.Now, Log: log}
	// Listening, greetings and how long messages are kept (step 14). The
	// greetings in use are copied into a folder only Asterisk reads, kept
	// the same as the database at every change and at start.
	greetings := &voicemail.Greetings{Dir: envOr(os.Getenv, "LINX_GREETINGS_DIR", asteriskconf.GreetingsDir), Store: st, Log: log}
	if _, err := os.Stat(greetings.Dir); err != nil {
		log.Warn("no voicemail greetings folder; boxes play Linx's own greeting", "err", err)
		greetings = nil
	} else {
		greetings.SyncLogged(context.Background())
	}
	voicemailSvc := &voicemail.Service{Store: st, EmailOn: emailSvc.On, Greetings: greetings, TimeZone: st.TimeZone,
		Changed: hub.VoicemailChanged, Now: time.Now, Log: log}
	emailSvc.Voicemail = voicemail.Attachment(st)
	// Call history (ADR-070): Asterisk adds its call records to the
	// database itself; this reads them into one line per call as each
	// call ends, and moves the Call history badge's counter.
	historyBuilder := &callhistory.Builder{Store: store.CallHistory{Store: st}, Changed: hub.CallsChanged, Now: time.Now, Log: log}
	historySvc := &callhistory.Service{Store: store.CallHistory{Store: st}, Builder: historyBuilder, Now: time.Now}
	tracker.Ended = historyBuilder.Kick
	tracker.UserEvent = func(name string) {
		if name == voicemail.EventName {
			voicemails.Kick()
		}
	}
	if _, err := os.Stat(voicemails.Dir); err != nil {
		log.Warn("no voicemail folder; messages callers leave can't be kept", "err", err)
	} else {
		runBackground(voicemails.Run)
	}
	runBackground(worker.Run)
	runBackground(engine.Run)
	runBackground(emailSvc.Run)
	// The certificate renewal alert source polls linx-certd directly (not
	// through the SSRF guard: it's Linx's own service, not an admin URL).
	var certExpiry atomic.Int64
	runBackground(func(ctx context.Context) {
		pollCertd(ctx, &http.Client{Timeout: 10 * time.Second}, engine, tenant, certdPollInterval, log, &certExpiry)
	})
	runBackground(func(ctx context.Context) { sweepWebDevices(ctx, st, relay, webDeviceSweepInterval, log) })
	runBackground(hub.Run)
	runBackground(voicemailSvc.RunExpiry)
	runBackground(historyBuilder.Run)
	runBackground(trunkFiles.Run)
	runBackground(trunkMonitor.Run)
	runBackground(callAlerts.Run)
	runBackground(func(ctx context.Context) { sweepBackupFiles(ctx, backups, log) })
	// System → Status's server helper (linx-ops-agent, docs/ADMIN.md §9):
	// it connects in through ops-bridge. Without the socket (a dev run
	// outside the container) the page just says the helper isn't there.
	opsHub := &ops.Hub{Log: log}
	if ln, err := ops.Listen(opsSocket(os.Getenv)); err != nil {
		log.Warn("no server helper socket; System → Status can't show services, logs or restart them", "err", err)
	} else {
		runBackground(func(ctx context.Context) { opsHub.Serve(ctx, ln) })
	}
	// System → Server settings (docs/INSTALL.md §7): `sudo linx setup` on
	// an installed server connects in through install-bridge, the web
	// install's own socket, and relays this server's own settings.
	serverSettings := &install.Server{Log: log}
	if ln, err := install.Listen(installSocket(os.Getenv)); err != nil {
		log.Warn("no setup socket; System → Server settings can't reach setup on the server", "err", err)
	} else {
		runBackground(func(ctx context.Context) { serverSettings.ServeBridge(ctx, ln) })
	}
	stopARI, err := startARI(bgCtx, ariCfg, tracker, log, runBackground)
	if err != nil {
		log.Error("ARI setup failed", "err", err)
		stopBackground()
		bg.Wait()
		os.Exit(1)
	}
	defer func() {
		stopARI()
		stopBackground()
		bg.Wait()
	}()

	// "Moved to a new place?" (docs/INSTALL.md §8): where this server is,
	// written at every start (the public address takes a moment to find).
	movedSvc := &moved.Service{Store: st, Now: time.Now}
	runBackground(func(ctx context.Context) { recordPlace(ctx, movedSvc, os.Getenv, phoneNetworks, log) })
	// Help: the guides this release carries (docs/HELP.md). Without them
	// Linx still runs; Help says it isn't available. Written answers
	// (§4) go out through the same guard as webhooks, with room for a
	// whole answer.
	helpLib, err := help.Open(os.DirFS(envOr(os.Getenv, "LINX_HELP_DIR", help.DefaultDir)))
	if err != nil {
		log.Error("reading the help guides failed", "err", err)
		helpLib = nil
	}
	helpAnswers := &helpanswers.Service{Store: st, Sealer: sealer, Policy: policy, Help: helpLib, Now: time.Now,
		Client: safehttp.NewClient(policy, safehttp.Options{Timeout: helpanswers.AnswerTimeout + 5*time.Second})}
	// linx-certd's certificate, for the HTTPS port below and Check it.
	// Setting up iPhones and iPads (ADR-073, docs/PHASE2.md §4): the
	// internal CA's linx-devices provisioner signs a phone's Secure Enclave
	// key, and the phone's token speaks for its person afterwards. Without
	// that provisioner's password (a dev run outside the container) phones
	// simply can't be set up; everything else runs as before.
	enrollSvc, err := newEnroll(ariCfg, st, tokens, log)
	if err != nil {
		log.Warn("phones can't be set up on this server", "err", err)
	} else {
		authn.Devices = enrollSvc
		runBackground(enrollSvc.RunExpiry)
	}

	cert := &certs.ServingCert{Dir: envOr(os.Getenv, "LINX_CERTS_DIR", defaultCertsDir)}
	reachChecker, reachLinks := newReach(os.Getenv, cert, turnIssuer, ips)
	var apiServer *controlplaneapi.Server
	apiHandler, err := newAPIHandler(log, st, authn, webhooks, alerts, pbxSvc, trunks, st, tracker, accounts, turnIssuer, team, settingsSvc, st, ssoSvc, backups,
		func(s *controlplaneapi.Server) {
			apiServer = s
			s.SetServerSettings(serverSettings)
			s.SetMoved(movedSvc)
			s.SetHelpAnswers(helpAnswers)
			s.SetEmail(emailSvc, weburl.FromEnv(os.Getenv))
			routingSvc := &routing.Service{Store: st, Rules: st, Changes: st, Now: time.Now}
			st.SetRoutingWords(routingSvc.Words)
			s.SetRouting(routingSvc)
			s.SetVoicemail(voicemailSvc)
			if enrollSvc != nil {
				s.SetEnroll(enrollSvc)
			}
			s.SetCallHistory(historySvc)
			s.SetReach(reachChecker.Run, reachLinks)
			s.SetDNSRecords(reachChecker.Records)
			s.SetOps(opsHub, st.Audit, func() time.Time {
				if sec := certExpiry.Load(); sec > 0 {
					return time.Unix(sec, 0)
				}
				return time.Time{}
			})
		})
	if err != nil {
		log.Error("api handler setup failed", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/healthz", health.Handler(service))
	mux.Handle("/api/v1/", apiHandler)
	mux.Handle(auth.TokenPath, authn.TokenHandler())
	registerSessionHandlers(mux, authn, accounts, tenant)
	registerCompanyHandlers(mux, authn, accounts, ssoSvc, tenant, movedSvc.PasskeysMoved, log)
	registerBackupFileHandlers(mux, authn, backups)
	registerHelpHandlers(mux, authn, helpLib, helpAnswers, tenant, log)
	registerVoicemailHandlers(mux, authn, voicemailSvc)
	if enrollSvc != nil {
		registerEnrollHandlers(mux, ips, enrollSvc, log)
	}
	registerCallHandlers(mux, authn, historySvc, st.TimeZone, log)
	mux.Handle("GET "+controlplaneapi.SIPPath, sipHandler(authn, st, relay))
	mux.Handle("GET "+controlplaneapi.TeamLivePath, teamLiveHandler(authn, st, hub))
	// Everything else is the web client (ADR-037).
	mux.Handle("/", webapp.Handler(os.DirFS(envOr(os.Getenv, "LINX_WEB_DIR", webapp.DefaultDir))))

	// One HTTPS port for the page, the API and /sip (ADR-037), with
	// linx-certd's certificate, picked up again whenever it's renewed. Until
	// certd has the first certificate, connections fail their handshake
	// (and the logs say why) while everything else runs.
	if _, err := cert.Current(); err != nil {
		log.Warn("no TLS certificate yet; HTTPS connections fail until linx-certd deploys one", "err", err)
	}
	https := server.New(envOr(os.Getenv, "LINX_LISTEN_ADDR", ":8443"), webapp.HeadersFor(os.Getenv("LINX_DOMAIN"), legacyHosts(os.Getenv("LINX_DOMAIN"), weburl.FromEnv(os.Getenv), mux)))
	// Let's Encrypt's port 443 check is answered here too, for a server
	// that renews without a DNS token (docs/INSTALL.md §5): only while
	// certd has left a challenge for that name.
	https.TLSConfig = certs.ServerTLSConfig(certs.Challenges{Dir: envOr(os.Getenv, "LINX_CHALLENGE_DIR", defaultChallengeDir)}, cert)
	// Plain HTTP only on the container's own loopback, only for the Docker
	// health check (healthcheck.go): nothing else can reach it.
	healthMux := http.NewServeMux()
	healthMux.Handle("/healthz", health.Handler(service))
	plain := server.New(envOr(os.Getenv, "LINX_HEALTH_ADDR", defaultHealthAddr), healthMux)

	// Trusted front doors given by name (LINX_TRUSTED_PROXIES) are looked
	// up before the first connection and every 30 s after.
	lctx, lcancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = ips.Refresh(lctx, func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	})
	lcancel()
	runBackground(func(ctx context.Context) { refreshTrustedProxies(ctx, ips, log) })

	useProxyProtocol, err := strconv.ParseBool(envOr(os.Getenv, "LINX_PROXY_PROTOCOL", "true"))
	if err != nil {
		log.Error("LINX_PROXY_PROTOCOL: want true or false", "err", err)
		os.Exit(1)
	}
	ips.IgnoreForwardedFor = useProxyProtocol
	entries := []server.Entry{{Server: https, Wrap: proxyListener(ips, useProxyProtocol)}, {Server: plain}}
	// The repair page (docs/INSTALL.md §7): only while setup on the server
	// has it open, it publishes port 6464 here with the install's own
	// temporary certificate (installer.RepairCompose).
	if addr := os.Getenv("LINX_REPAIR_ADDR"); addr != "" {
		repair, err := repairServer(addr, os.Getenv, serverSettings, mux, apiServer.RepairSettingsHandler(tenant))
		if err != nil {
			log.Error("the repair page", "err", err)
			os.Exit(1)
		}
		entries = append(entries, server.Entry{Server: repair})
	}
	if err := server.Serve(log, entries...); err != nil {
		log.Error("server stopped", "err", err)
		stopBackground()
		bg.Wait()
		os.Exit(1)
	}
}

// defaultCertsDir is where compose mounts linx-certd's certificates.
const defaultCertsDir = "/var/lib/linx/certs"

func envOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}
