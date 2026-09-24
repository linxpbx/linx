# Linx — working notes for Claude

Linx is a self-hosted, open-source (Apache-2.0) 3CX-style phone system: PBX, video meetings, guest links, presence. The owner is not a developer. Explain questions in plain language and always recommend an answer.

Full brief: `linx-build-prompt.md` (large; grep it, don't read it whole).
Detail lives in `docs/`: read only the part you need.
- `docs/ARCHITECTURE.md` components, hostnames, flows, profiles
- `docs/DECISIONS.md` ADRs (check before choosing any library/tool)
- `docs/THREAT_MODEL.md` STRIDE; update every phase
- `docs/ROADMAP.md` phases and exit criteria
- `docs/API.md` public API, auth, webhooks, admin alerts (Phase 1 foundation)
- `docs/PBX.md` phone engine: Asterisk image, realtime views, devices, calls (Phase 1B)
- `docs/ui/DESIGN_TOKENS.md` colours/type/status; mockup PNGs in `docs/ui/`

## Current state
- **Phase 0 complete, approved by the owner 2026-09-23** (demo: `docs/DEMO_PHASE0.md`, commit `ff145ad`). Built: skeleton + tokens, CI, `linx setup` (prereqs, domain/DNS token, compose stack), `linx-certd` (Cloudflare + DuckDNS, wildcard default), step-ca bootstrap, `linx doctor` certificate checks.
- Phase 1 started 2026-09-23: API/webhooks/alerts design in `docs/API.md` (ADR-025 to 030, approved 2026-09-23; webhooks HTTPS only, no LAN http exception). Build order is `docs/API.md` §8, one step per session:
  - Step 1 done: database (Postgres, control plane, migrations, encryption).
  - Step 2 done: API skeleton — `api/openapi.yaml`, `make api` codegen (oapi-codegen strict server, `services/control-plane/api/gen.go`), kin-openapi request validation, problem+json, cursor pagination, `/me`, `/openapi.json`, `/event-types`.
  - Step 3 done: authentication — `internal/auth` (keys, scopes/roles, EdDSA tokens, rate limits, middleware, `/oauth/token`), `internal/store` (Postgres), migration 0002, `/api-keys` + `/oauth-clients` endpoints, `linx api-key create|list|revoke`, `linx_jwt_signing_key` secret. Scopes are declared per operation in `openapi.yaml` (`security: [bearer: [...]]`) and enforced by the validator.
  - Step 4 done: webhooks — `internal/safehttp` (SSRF guard, shared by alert senders), `internal/webhook` (Standard Webhooks signer, sender, outbox worker, service), `internal/store/webhooks.go`, migration 0003, `/webhooks*`, `/webhook-deliveries/*`, `/outbound-allowlist`. Emit events with `webhook.NewEvent` + `store.InsertEvent` in the change's transaction. Worker's `OnDisabled` hook and `webhook.Service.OnEnabledChanged` are where step 5 fires/resolves the "webhook disabled" alert.
  - Step 5 done: admin alerts — `internal/alert` (channel config + 6 senders: ntfy, Gotify, Slack, Teams, Telegram, generic webhook; service; engine), `internal/store/alerts.go`, migration 0004, `/alert-channels*`, `/alerts`. `Fire`/`Resolve` are cheap in-process calls (see `services/control-plane/main.go`'s `webhookDisabledKey` and `certdpoll.go`); the engine's tick decides notify/remind/resolve timing, severity and quiet hours. Sources wired: webhook disabled, certificate renewal (polls `linx-certd`'s `/metrics` on `linx-private`, not through the SSRF guard — that's for admin URLs, not Linx's own services). Disk/storage and DDNS deferred: no filesystem/DDNS signal reaches the control plane yet (`docs/THREAT_MODEL.md`).
  - Step 6 done (2026-09-24): security review (findings and fixes in `docs/THREAT_MODEL.md` "Phase 1A review"), `linx doctor` now checks services, schema version, API keys, alert channels, open alerts and secret files (`internal/doctor/platform.go`; reads the DB via `docker exec linx-postgres psql`), control-plane Docker health check (`service healthcheck`), `docs/DEMO_PHASE1A.md`.
  - **Phase 1A (steps 1–6) complete, demo passed and approved by the owner 2026-09-24** (`docs/DEMO_PHASE1A.md`, commit `a6effa3`).
  - Phase 1B (phone engine) design approved by the owner 2026-09-24: `docs/PBX.md` (ADR-031 to 035). Build order is `docs/PBX.md` §8, one step per session.
    - Step 1 done (2026-09-24): Asterisk image — `deploy/docker/asterisk.Dockerfile` builds Asterisk 22.11.0 from the signed release tarball (GPG signature + pinned SHA-256 verified at build time against `deploy/docker/asterisk/asterisk-pubkey.asc`, ADR-031), `menuselect` trimmed to PJSIP/SRTP/ODBC-realtime/ARI/dialplan-basics/app_echo/func_odbc/ulaw+alaw+g722+Opus-passthrough (no chan_sip, AGI, AMI-over-network, telephony cards or add-ons). `internal/asteriskconf` (Go, tested) renders `/etc/asterisk/*.conf` at container start from env vars — this slice only brings up the TLS transport on 5061, no endpoints/dialplan/ARI yet. `services/asterisk-entrypoint` execs into `asterisk` as PID 1. Compose service `asterisk` in `deploy/compose/compose.yaml` (non-root, read-only rootfs, `asterisk-state` volume + tmpfs, health check via `asterisk -rx`), CI `images` job matrix extended (amd64+arm64, Trivy, cosign), `docs/ops/CERT_RELOAD.md` row filled in. Build validated end-to-end locally (image builds, container starts read-only/non-root, TLS transport binds, health check passes, clean shutdown on SIGTERM) — not yet run through `make test-docker` or CI.
    - Step 2 done (2026-09-24): realtime data — migration `0005_pbx.sql` adds `extension`/`device` (control plane's own tables) and schema `asterisk` with views `ps_endpoints`/`ps_aors`/`ps_auths`/`linx_ring_targets` plus role `linx_asterisk` (`SELECT` on those four views only, ADR-032). `internal/db.EnsureAsteriskRole` sets the role's login password at every control-plane startup from the new `linx_asterisk_db_password` Docker secret (installer: `internal/installer/stack.go`; mounted into both `control-plane` and `asterisk` in compose.yaml), so no usable credential is ever baked into the schema. `internal/asteriskconf` now also renders `sorcery.conf`, `extconfig.conf`, `odbcinst.ini`, `odbc.ini` and `res_odbc.conf` (unixODBC via the `odbc-postgresql` driver, symlinked to a fixed path in the image so the config doesn't need to know amd64 vs arm64); `services/asterisk-entrypoint` points unixODBC at the rendered config with `ODBCINI`/`ODBCSYSINI`. Docker test `internal/db/pbx_docker_test.go` (`make test-docker`) proves `linx_asterisk` can read a seeded device through the views and gets "permission denied" on every other table. Also verified by hand end-to-end: built the real image, ran it against a real Postgres on a throwaway network, and confirmed with `asterisk -rx` that ODBC connects and `pjsip show endpoint` loads the seeded device's endpoint/auth/aor from realtime, and that a disabled device doesn't appear.
    - Step 3 done (2026-09-24): API — migration `0006_extension_delete.sql` soft-deletes extensions (`deleted_at`, a partial unique index on `(tenant_id, number) WHERE deleted_at IS NULL` so a deleted number frees up at once; the realtime views gained `AND e.deleted_at IS NULL` as defense in depth). `internal/pbx` (Service + SIP credential generation: `NewSIPUsername`, `NewDevicePassword` via `crypto/rand.Text()`, `DigestHash` — MD5 as RFC 3261 digest auth requires, never a security choice) and `internal/store/pbx.go` implement `/extensions` and `/devices` (`docs/PBX.md` §5, minus `/calls/active`, which is step 4's): full CRUD, JSON Merge Patch with `If-Match`/etag, `POST .../devices` and `POST /devices/{id}/reset-password` return the SIP login once (username, password, `sip.<domain>:5061/tls`, a plain-language settings block) and never again — only the digest hash is stored. Deleting an extension revokes every device under it and frees its number, all in one transaction. New scopes `devices:read`, `devices:write` (sensitive — a device login is as powerful as an API key), `calls:read` (declared now per `docs/PBX.md` §5, wired up when step 4 builds `/calls/active`). Webhook catalog gained `extension.created/updated/deleted` and `device.created/updated/revoked/registered/unregistered` (replacing the Phase 1A placeholder `device.enrolled`); every extension/device write fires its event and an audit entry in the same transaction. Control plane reads `LINX_DOMAIN` now too, for the SIP settings text. Tests: `internal/pbx` (credential generation, validation), `internal/store/pbx_docker_test.go` (CRUD, optimistic concurrency, the delete-cascade, events — `make test-docker`), `services/control-plane/{extensions,devices}_test.go` (HTTP, scopes, `If-Match`, bad input) via a new `fakePbxStore`. 
    - Step 4 done (2026-09-24): calls — dialplan in `internal/asteriskconf` (ring-all via `func_odbc` `LINX_RING_TARGETS`, `*43` echo, "not in use"/"not available" prompts pinned by SHA-256 in the image), migration `0007_calls.sql` (endpoint caller ID = extension, ring-target view lists device-less extensions, `device.online`). ARI: Asterisk connects **out** (`websocket_client.conf`) to `wss://linx-ari:8089/ari`; REST goes over the same websocket, so Asterisk's HTTP server stays off (ADR-034 "As built"). The control plane serves it on its `linx-private` address only (`services/control-plane/ari.go`) with a 24 h step-ca certificate (`internal/stepca`: JWK-provisioner client + renewer; secrets `linx_ari_password`, `linx_ca_services_password`; compose mounts the step-ca volume's `certs` subpath, so Compose ≥ 2.30). `internal/ari` (handler, REST over websocket, events; `coder/websocket`), `pbx.CallTracker` (online state, `call.started/answered/ended/missed`, `/calls/active`). Automated suite `internal/calltest` (SIPp 3.7.7 with TLS built by `deploy/docker/sipp-test.Dockerfile`, real Asterisk + Postgres, 5 scenarios): `make test-calls` (needs `make image SERVICE=asterisk`), run in CI's amd64 Asterisk job. Also fixed: `asterisk.conf`'s `[directories](!)` was a template Asterisk ignored.
    - Step 5 done (2026-09-24): phone ports and doctor (`docs/PBX.md` §2 "Phone ports, as built"). `installer.DetectLAN` (default-route interface's network) → `.env` `LINX_SIP_ADDRESS`/`LINX_SIP_NETWORKS`; `installer.PhonesPlan` (`internal/installer/phones.go`): `userland-proxy: false` merged into `/etc/docker/daemon.json`, nftables table `inet linx` (`/etc/linx/nftables.conf`, `linx-firewall.service`; prerouting drop before Docker's DNAT, 5060 dropped for all). compose publishes 5061/tcp + 10000–10199/udp on the LAN address only. `internal/asteriskconf`: PJSIP `type=acl` from `LINX_SIP_NETWORKS` (required; `none` = refuse all), `external_*_address` + `local_net` (container nets), `rtp.conf` range. Doctor section "Phone system" (`internal/doctor/phones.go`): Asterisk health, ODBC connected + `linx_asterisk` grants, ARI session Up, TLS-only transport, published ports vs detected LAN, served 5061 certificate = certd's, no 5060 anywhere, firewall set vs LAN and enabled at boot, `sip.<domain>` DNS → LAN address; also checks `linx_ca_services_password`. Call suite gained "outside the phone networks" (403) and runs doctor's console parsers on the real image. Ruleset checked with real `nft` (load/reload; simulated LAN/internet/DNAT namespaces: LAN open, internet dropped). Not yet run on a real Ubuntu host with Docker's own DNAT. Nothing creates the `sip.<domain>` DNS record yet (setup prints it; doctor warns).
    - Step 6 done (2026-09-24): security review (`docs/THREAT_MODEL.md` "Phone engine" rows and "Phase 1B review"). Fixed: revoke is now permanent (migration `0008_device_revoked.sql`, `revoked_at` + DB check; PATCH/reset-password on a revoked device → 409 `device_revoked`); phone TLS accepts 1.2 and 1.3 (`method=sslv23` + rendered `openssl.cnf` `MinProtocol = TLSv1.2`, entrypoint sets `OPENSSL_CONF`); PJSIP `user_agent=Linx`; every API response `Cache-Control: no-store` (`apihttp.NoStore`); call suite gained "unencrypted audio refused" (488) and "TLS 1.2 and 1.3 only" (Asterisk publishes 5061 on 127.0.0.1 in the harness). `docs/DEMO_PHASE1B.md` written (needs a bridged VM on the home LAN and a trusted, not staging, certificate). Demo fixes after step 6: `b1d403f` (setup keeps the DNS token when the domain changes within a zone), `ba4502b` (Asterisk tmpfs mounts get explicit owner/mode: a restarted container's tmpfs came back root 0755), `6dc6d68` (migration 0009: codecs `g722,ulaw,opus` + `prefer:configured`, since Asterisk can't encode Opus and messages failed), `ae653f1` (migration 0010: `pbx_setting.sip_domain` from `LINX_DOMAIN` → `from_domain`; empty `contact` column on `ps_aors`).
  - **Phase 1B demo passed 2026-09-24** on commit `ae653f1` (`docs/DEMO_PHASE1B.md` Results). Next task (Opus): reload the public certificate into Asterisk on renewal (certd `OnDeploy` hook or a watcher; verify what `pjsip reload` actually does for TLS transports), and make doctor say "still starting" instead of waiting on Asterisk. Then Phase 1C design (web client, TURN, registration lockout, Opus encoder decision).
- Scope: server + Web + iOS/iPadOS only. No Android/macOS/Windows code.

## Stack (see ADRs)
- PBX: Asterisk 22 LTS, pjsip, ARI, Postgres realtime (via views the control plane owns)
- SFU: LiveKit + livekit-sip. TURN: coturn (UDP/TLS 443)
- Control plane, CLI `linx`, certd: **Go**. DB: PostgreSQL. Pub-sub: Valkey (optional on Lite)
- Web: React + TS + Vite, shadcn/ui + Radix, JsSIP, livekit-client
- iOS: Swift/SwiftUI, Google WebRTC (BSD), Linx SIP-over-WSS UA, CallKit + PushKit
- Certs: lego (LE staging first, ZeroSSL fallback), step-ca internal PKI + device certs
- Tokens: JWT EdDSA, pinned alg, `jti` revocation
- No custom tunnel for now (ADR-007): WSS + TURN/TLS 443 covers 443-only networks

## Repo layout
```
cmd/linx/  services/control-plane/  services/certd/  internal/
web/  ios/  design/tokens.json  deploy/compose/  deploy/profiles/  docs/
```

## Conventions
- Naming: product "Linx"; containers/networks prefixed `linx-`; bundle ID `com.linxpbx.app`.
- Pin every dependency (go.mod, lockfiles, image digests, GitHub Actions by SHA).
- Only permissive licences (MIT/BSD/Apache/ISC) in client bundles. No GPL SDKs in apps.
- Design tokens only from `design/tokens.json`. No hardcoded colours in UI code.
- Brand colour: Cobalt `#1F5FD6` (not the mockup teal). Status colours per DESIGN_TOKENS.md.
- English only for CLI, server UI and apps (ADR-021). No Arabic/RTL.
- Plain-language copy in default admin views; no telecom jargon.
- Prefer mature OSS; custom code only for glue, control plane, provisioning, clients.

## Commands
- `make setup-dev` (npm ci), `make lint`, `make test` (quiet: failures and summary only)
- `make tokens` after editing `design/tokens.json` (lint fails if generated files are stale; contrast is tested)
- `make test-docker` (needs Docker: internal CA end to end, and real-Postgres tests for migrations, store, webhooks, alerts and doctor's query)
- `make build` (bin/ + web/dist/). Go module path: `linxpbx.com/linx`
- `make security` (govulncheck, npm audit, licence allowlist), `make image SERVICE=control-plane`
- CI: `.github/workflows/ci.yml` (amd64+arm64 tests, gitleaks, SBOM, Trivy, cosign-signed images to ghcr.io on master). Actions pinned by SHA; Dependabot updates them. First external Go dep must add a Go licence check.
- `linx setup [--config FILE] [--dry-run]` (code: `cmd/linx/setup.go`, `internal/installer`, `internal/hostinfo`), `linx doctor` (code: `cmd/linx/doctor.go`, `internal/doctor`: certificates in `doctor.go`, everything else in `platform.go`), `linx api-key` (code: `cmd/linx/apikey.go` → `docker exec` → `services/control-plane/apikey_cmd.go`)

## Security rules (never break these)
- Never disable cert verification, never use `--insecure`, never fall back to plaintext. Only exception: a provider trunk whose provider can't encrypt (ADR-023): TLS/SRTP tried first, admin confirms a warning; no warning over WireGuard.
- No secrets in git or `.env` committed. Secrets are Docker secrets, generated by the installer.
- No public UDP/TCP 5060 (IP-auth trunks: provider IPs or WireGuard only). SIP only over TLS/WSS and encrypted media, except ADR-023 trunks.
- VPN: WireGuard only, split tunnel, per-trunk "Connection" choice (ADR-024).
- QR/enrollment tokens never contain SIP passwords.
- Containers: non-root, read-only FS where possible, no Docker socket mounts.
- DB, Valkey, step-ca, ARI/AMI, metrics stay on `linx-private` (internal network).
- Outbound HTTP (webhooks, CRM, storage) blocks private IPs unless allowlisted.
- If something is blocked by a security rule: stop and tell the owner.

## Working rules
- One task per session. Suggest `/clear` when done and `/compact` before context grows large.
- Use targeted reads and grep; filter logs to errors and the last ~50 lines.
- Use subagents for broad searches.
- Fetch library docs via the context7 MCP before using an unfamiliar API.
- Sonnet for routine implementation. Opus for architecture, security review and hard debugging. Tell the owner when to switch.
- Keep replies short. Don't restate plans or summarise diffs unless asked.
- Each phase ends with passing tests, `docker compose up` working on clean Ubuntu 24.04, updated docs, and `docs/DEMO_PHASE<N>.md`.
- Turn repeated procedures into `make` targets or project skills.

## Front-end rules (added by owner)
- Use docs/ui/linx-tokens.json as the single source for colours, fonts, radii and the logo. Never invent colours.
- Match the screenshots in docs/ui for layout. Brand colour is Linx Cobalt, replacing the teal in the mockups.
- After building or changing any screen, use Playwright (web) or XcodeBuild simulator screenshots (iOS) to capture it, compare with docs/ui, and fix differences before reporting done.
- Use the frontend-design skill for all UI work.
- If the web UI uses shadcn/ui, set up the shadcn MCP server before building components.
