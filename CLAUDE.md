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
    - Step 3 done (2026-09-24): API — migration `0006_extension_delete.sql` soft-deletes extensions (`deleted_at`, a partial unique index on `(tenant_id, number) WHERE deleted_at IS NULL` so a deleted number frees up at once; the realtime views gained `AND e.deleted_at IS NULL` as defense in depth). `internal/pbx` (Service + SIP credential generation: `NewSIPUsername`, `NewDevicePassword` via `crypto/rand.Text()`, `DigestHash` — MD5 as RFC 3261 digest auth requires, never a security choice) and `internal/store/pbx.go` implement `/extensions` and `/devices` (`docs/PBX.md` §5, minus `/calls/active`, which is step 4's): full CRUD, JSON Merge Patch with `If-Match`/etag, `POST .../devices` and `POST /devices/{id}/reset-password` return the SIP login once (username, password, `sip.<domain>:5061/tls`, a plain-language settings block) and never again — only the digest hash is stored. Deleting an extension revokes every device under it and frees its number, all in one transaction. New scopes `devices:read`, `devices:write` (sensitive — a device login is as powerful as an API key), `calls:read` (declared now per `docs/PBX.md` §5, wired up when step 4 builds `/calls/active`). Webhook catalog gained `extension.created/updated/deleted` and `device.created/updated/revoked/registered/unregistered` (replacing the Phase 1A placeholder `device.enrolled`); every extension/device write fires its event and an audit entry in the same transaction. Control plane reads `LINX_DOMAIN` now too, for the SIP settings text. Tests: `internal/pbx` (credential generation, validation), `internal/store/pbx_docker_test.go` (CRUD, optimistic concurrency, the delete-cascade, events — `make test-docker`), `services/control-plane/{extensions,devices}_test.go` (HTTP, scopes, `If-Match`, bad input) via a new `fakePbxStore`. Next: step 4 (dialplan, ARI, device online state, call events, `/calls/active`, SIPp suite) — Opus: hardest integration.
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
