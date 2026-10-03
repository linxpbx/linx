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
- `docs/WEB.md` web client, accounts/sign-in, `/sip` relay, coturn, front doors, Opus (Phase 1C)
- `docs/TRUNKS.md` trunks, numbering/outbound routing, WireGuard, toll fraud (Phase 1D)
- `docs/ADMIN.md` admin portal, web setup wizard, numbering plan, passkeys, company sign-in (Phase 1E)
- `docs/RESOURCES.md` what Linx needs to run (measured), minimum hardware, what's been made smaller
- `docs/PHASE1F.md` Phase 1F: email, password reset, ring groups, office hours, voicemail, call history, undo; one build order with SIMPLER (ADR-066 to 071, approved 2026-09-30)
- `docs/PHASE2.md` Phase 2 design (iPhone/iPad app, enrollment, push) — drafted 2026-10-03, decisions awaiting the owner
- `docs/SIMPLER.md` simpler phone lines (gateways sign in to Linx), one front-door method, any DNS company (ADR-061 to 063, approved 2026-09-29)
- `docs/HELP.md` help pages and search (guides in `docs/help/`)
- `docs/INSTALL.md` web-first install: one link from the terminal, HTTP 6464 → TLS-ALPN-01 → HTTPS, moved-server checklist
- `docs/ui/DESIGN_TOKENS.md` colours/type/status; mockup PNGs in `docs/ui/`

## Current state
Step-by-step history of every phase (what was built, fixes, demo results): `docs/HISTORY.md`. Read it only when you need the detail.

Finished (each demo passed and approved by the owner):
- Phase 0 (skeleton, CI, `linx setup`, certd, step-ca, doctor): 2026-09-23, `docs/DEMO_PHASE0.md`, `ff145ad`
- Phase 1A (database, API, auth, webhooks, admin alerts): 2026-09-24, `docs/DEMO_PHASE1A.md`, `a6effa3`
- Phase 1B (phone engine: Asterisk, realtime, devices, calls, phone ports): 2026-09-24, `docs/DEMO_PHASE1B.md`, `ae653f1`
- Phase 1C (web client, accounts, `/sip` relay, coturn, front doors): 2026-09-25, `docs/DEMO_PHASE1C.md`, `6310c39`
- Phase 1D (trunks, numbering, WireGuard, firewall): 2026-09-26, `docs/DEMO_PHASE1D.md`, `b863e71`
- Pre-launch security audit: 2026-09-27, `docs/THREAT_MODEL.md`, `da9246b`
- Backup and restore: 2026-09-27, `docs/DEMO_BACKUP.md`, `e0107b5`
- Web-first install: 2026-09-29, `docs/DEMO_INSTALL.md`, `d5fd31f`
- Help pages (guides, search, written answers): 2026-09-30, `docs/DEMO_HELP.md`, `d347363`
- Phase 1E (admin portal, setup wizard, passkeys, company sign-in, phone-line screens): 2026-09-30, `docs/DEMO_PHASE1E.md`, `e9a9795`
- Phase 1F (front doors, DNS, public port, email, password reset, ring groups, office hours, voicemail, call history, undo) and **Phase 1's exit test**: Demo A 2026-10-01, Demo B 2026-10-03, `docs/DEMO_PHASE1F.md`, `68e71fa`

Now (2026-10-03):
- **Phase 1F and Phase 1 done** (Demo B approved 2026-10-03). Its step-by-step history is in `docs/HISTORY.md` "Phase 1F and the end of Phase 1".
- **Busy tone done 2026-10-03** (ADR-072, `docs/PBX.md` §4, `docs/HISTORY.md` "After Phase 1"): gateways' lines listen for 4 bursts of the country's busy tone and hang up; voicemail trims it. Check on the owner's UCM landline at the next demo (needs the new Asterisk image there).
- **Update notice in setup done 2026-10-03** (`docs/ops/UPDATING.md`, `docs/HISTORY.md` "After Phase 1"): `sudo linx setup` on an installed server prints the update command when the program isn't the version running, even while the Server settings page is open. Check at the next demo. A help guide only once releases for users exist.
- **Phase 2 step 2 done 2026-10-03** (`docs/PHASE2.md` §12 step 2 + "Step 2, as built", `docs/HISTORY.md`): the server side of setting up an iPhone/iPad — migration 0041, `internal/enroll`, `POST /v1/enroll` and `POST /v1/device-token` (Secure Enclave key, 7-day certificate from the CA's linx-devices provisioner, single-use proofs), `/api/v1/enrollments` for admins and for yourself, device tokens that always hold an ordinary person's scopes, 7-day expiry, audit and webhooks. Owner decisions 2026-10-03, both built: the app is a **client only** (its token holds a fixed read-only scope list; admin stays on web/CLI), and **changing a password expires that person's phones** (new QR code or emailed link to set up again). Next: step 3 (Add phone screens, Sonnet).
- **Phase 2 step 1 done 2026-10-03** (`ios/README.md`, `docs/PHASE2.md` §12 step 1, `docs/HISTORY.md` "After Phase 1"): the app's skeleton — `ios/Linx.xcodeproj` (checked in, synchronised folders, no project generator), Swift 6 / SwiftUI, **minimum iOS 26.0**, iPhone + iPad; `make tokens` also writes `ios/Linx/Generated/DesignTokens.swift`; the enrollment screen from `docs/ui/iOS · QR setup@1x.png` in Cobalt; `make ios-lint|ios-build|ios-test|ios-screens`; an `ios` CI job on `macos-26` with no Apple account or secrets. **Needs the owner's look** at `ios/screenshots/iPhone-17/` (run `make ios-screens`). Next: step 2 (server enrollment, Opus).
- **Phase 2 (iOS) design drafted 2026-10-03** (`docs/PHASE2.md`, not committed yet, nothing built): enrollment with Secure Enclave device certificates, the push gateway, the app, iPad and adaptive layouts, 7-day expiry, a 10-step build order. **Apple Developer membership confirmed (owner, 2026-10-03)**, so nothing is blocked. Still waiting on the owner's other §11 answers: test devices, push for other self-hosters (§6), TestFlight vs App Store, `*97`, the Chat tab. **Foldable (checked 2026-10-03):** the iPhone Duo simulator and the fold APIs (`ArrangementView`, `UIArrangementViewController`, `UIView.ReservedRegion`, all iOS 27.1) are in the **Xcode 27.1 beta** only — not in the installed 27.0 release, not in the newer 27.2 beta; the owner downloads 27.1 beta (needs their account) and it sits beside 27.0, which keeps building releases and CI (`docs/PHASE2.md` §8).
- Releases and **Update** from the web moved to Phase 5, close to production (`docs/ROADMAP.md` "Releases and updating from the web").

Work queue (owner, 2026-09-29; keep it current so a fresh session can start from it):
1. Help pages (`docs/HELP.md`, ADR-060, design approved 2026-09-28; build order §7). Steps 1 (guides + checks), 2 (Help screen, search, **?** button, public sign-in help) and 3 (written answers: Anthropic/OpenAI-compatible/Ollama, sealed key, limits, System → Settings card) done 2026-09-30; step 4 review done 2026-09-30; **demo passed and approved 2026-09-30**. Done.
2. Phase 1F. **Done 2026-10-03** (Demo B approved).
3. Portainer note on the rented extras page. **Done 2026-09-30** (`8453509`): install extras and Server settings say why it isn't offered and point at System → Status.
5. Phase 2 (iOS). Design drafted 2026-10-03 (`docs/PHASE2.md`). **Steps 1 (the app's skeleton) and 2 (server enrollment) done 2026-10-03.** Still **waiting on the owner's §11 answers** (test devices, push for other self-hosters, TestFlight vs App Store, `*97`, the Chat tab); steps 2–4 can go on without them.
4. Light/dark toggle on every page (owner, 2026-09-30, during the Help demo). **Done 2026-09-30**: **Appearance** button (Light / Dark / Match this device) in every page's header or top-right corner, kept per browser (`web/src/lib/theme.ts`, `components/ThemeMenu.tsx`; own small menu, +1.4 KB on sign-in instead of +24 KB, `docs/RESOURCES.md`); `make screens` fails a screen without it. Needs the owner's look.

Open notes:
- **Test VPS (64.177.45.141, `vps.mym.ae`, 1 core 1.9 GB):** kept for the next phases' demos; **Debian 13**, user `linx`, SSH key and passwordless sudo checked 2026-10-01; Linx running there (Linx takes 443, no token).
- **Public port (advanced)** designed 2026-09-29 at the owner's request (`docs/SIMPLER.md` §2.5, ADR-064), owner decisions made 2026-09-29 (TURN/TLS shares the port; UDP asked separately; 8443 suggested, customizable; rented servers too, Docker publishing the port). Build in Phase 1F after the front-door card.
- Owner's observations still open from the Phase 1E demo (step 12): Tab doesn't move to the next field on some pages (which pages/browser: asked, no answer yet). Reusing a still-good certificate on a re-install or restore is proposed for the owner's decision (`docs/INSTALL.md` §15).

Standing owner decisions (word for word from the history; the full context is in `docs/HISTORY.md`):
- (2) **No Docker control in the admin UI** (no embedded Portainer, no socket for the control plane: an admin account must never mean root on the host). Instead System → Status (Phase 1E step 8) gets per-service health, recent log lines and **Restart**, done by a host helper with a fixed allowlist (restart a Linx service, read its log), the linx-backup-agent pattern. Portainer stays separate, LAN-only.
- Password reset: **nothing now** (owner, 2026-09-27) — all of it in Phase 1F with email: "Forgot your password?" → emailed reset link, still needing the second step after it, no account enumeration, rate limits.
- owner asked about resetting 2FA with a password reset: not done, it would defeat 2FA
- Scope: server + Web + iOS/iPadOS only. No Android/macOS/Windows code.
- **The app is a client, nothing else** (owner, 2026-10-03): a phone's token never holds admin scopes (`auth.deviceScopes` is a written-out read-only list). Running Linx — people, lines, routing, settings, backups, setting up another phone — is the web app's and the command line's.
- **A password change expires that person's phones** (owner, 2026-10-03): they ask for a new QR code or emailed link and set the phone up again, as they sign in again in the browser.

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
- Only permissive licences (MIT/BSD/Apache/ISC) in client bundles; OFL-1.1 for font files (`@fontsource/*`) only. No GPL SDKs in apps.
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
- `make screens` also saves the pictures help guides use into `docs/help/pictures/` (WebP, committed; only when a screen really changed): commit them with the change.
- `make ios-lint`, `make ios-build`, `make ios-test`, `make ios-screens` (the iPhone/iPad app; need Xcode. `ios/README.md`)
- `make test-calls` (phone call suite; needs `make image SERVICE=asterisk` and `SERVICE=wireguard`), `make test-browser` (browser call suite: needs `make image SERVICE=` control-plane, asterisk, coturn), `make screens` (web screenshots into `web/e2e/screenshots/`)
- `make security` (govulncheck, npm audit, licence allowlist), `make image SERVICE=control-plane`
- CI: `.github/workflows/ci.yml` (amd64+arm64 tests, gitleaks, SBOM, Trivy, cosign-signed images to ghcr.io on master). Actions pinned by SHA; Dependabot updates them. First external Go dep must add a Go licence check.
- `linx setup [--config FILE] [--dry-run]` (code: `cmd/linx/setup.go`, `internal/installer`, `internal/hostinfo`), `linx doctor` (code: `cmd/linx/doctor.go`, `internal/doctor`: certificates in `doctor.go`, everything else in `platform.go`), `linx api-key` (code: `cmd/linx/apikey.go` → `docker exec` → `services/control-plane/apikey_cmd.go`)

## Security rules (never break these)
- Never disable cert verification, never use `--insecure`, never fall back to plaintext. Exceptions: the install's first page on HTTP 6464 (ADR-057: nothing secret, one-time link, closed for good after install); and a provider trunk whose provider can't encrypt (ADR-023): TLS/SRTP tried first, admin confirms a warning; no warning over WireGuard.
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
- Help guides (`docs/help/`, owner design 2026-09-28): a change people can see updates its guide in the same commit (`make test` fails on a renamed **bold** label, a new screen with no guide, a broken link or picture).
- Batch related small fixes (owner, 2026-09-30): commit each separately described and tested, push them together, one CI run. Pushing a new commit while a run is going is fine: the new run tests everything.
- Session hygiene (owner, 2026-09-30): suggest `/clear` after each finished item and `/compact` before context grows large; update "Now" and the work queue above whenever an item finishes, so a fresh session starts from CLAUDE.md alone.

## Which local tests a change needs (owner, 2026-09-30)
CI still runs everything on every push, unchanged, and a step is done only when CI is green and reported. Locally, run what the change touches (each line adds to the ones above it):
- **Every change:** `make lint` and `make test`.
- **Web screen or web copy:** `make screens` (look at the changed shots; it also fails any sideways scroll).
- **Database, migrations, store, anything with a `_docker_test.go`:** `make test-docker`.
- **Asterisk image, dialplan, `internal/asteriskconf`, trunks, WireGuard, call prompts:** `make image SERVICE=asterisk` (and `wireguard` if touched), then `make test-calls`.
- **`/sip` relay, TURN/coturn, the web phone, front doors, sign-in flows the browser suite covers (passkeys, sign-out), System → Status helper:** `make image SERVICE=control-plane` (+ `asterisk`/`coturn` if touched), then `make test-browser` (`LINX_FRONT_DOORS=<door>` to rerun one door while fixing).
- **Install page, `internal/install`, `internal/installer`'s web install, certd bootstrap:** `make image SERVICE=control-plane` and `SERVICE=certd`, then `make test-install`.
- **The iPhone/iPad app (`ios/`), or `design/tokens.json`:** `make ios-lint` and `make ios-test`; `make ios-screens` for a changed screen (look at the shots, compare with `docs/ui/`).
- **Dependencies:** `make security`.
- Not sure which applies: run the wider set. Docs-only changes need none locally (CI still runs).

## Low-resource and low-bandwidth rule (owner, 2026-09-29, applies to every step)
Linx must run well on the smallest servers (1 core, 1–2 GB, a few GB of disk). Every change keeps processor, memory and disk use at a bare minimum, both idle and under load (calls, video calls, meetings, and all other server activity):
- **Measure, don't guess.** Anything that adds a service, a dependency, a background loop or a bigger image gets its idle and busy cost measured (`docker stats`, image size, disk) and recorded in `docs/RESOURCES.md`. Numbers go up only with a reason written there.
- **Nothing runs that isn't needed:** optional features start only when turned on (meetings, recordings, AI); no polling where an event will do; timers as slow as the job allows.
- **Small images:** smallest safe base (distroless/Alpine/slim), stripped binaries, no build tools or docs in runtime images; old versions cleaned up after updates.
- **Tuned defaults for small servers:** memory caps per container, services' own settings sized for a small office (connection pools, buffers, worker threads), logs capped.
- **Never at the cost of security or correctness:** say what a saving trades away and ask when it's not free.
- **Low bandwidth too** (owner, 2026-09-29): calls, video and meetings must work on slow or poor links (mobile data, hotel Wi-Fi, China): Opus with a low, adaptive bitrate and forward error correction before anything else; video that scales down (simulcast, bitrate caps, audio kept first); the web app small and cached (compressed, split, long-cached assets); no chatty polling (websockets/events, small payloads). Measure data used per minute of a call and per page load, and record it in `docs/RESOURCES.md`.
- Each phase's demo includes the 1-core 2 GB VPS (`docs/RESOURCES.md` §4).

## Front-end rules (added by owner)
- Use docs/ui/linx-tokens.json as the single source for colours, fonts, radii and the logo. Never invent colours.
- Match the screenshots in docs/ui for layout. Brand colour is Linx Cobalt, replacing the teal in the mockups.
- After building or changing any screen, use Playwright (web) or XcodeBuild simulator screenshots (iOS) to capture it, compare with docs/ui, and fix differences before reporting done.
- Use the frontend-design skill for all UI work.
- If the web UI uses shadcn/ui, set up the shadcn MCP server before building components.
- iOS work: Xcode's own MCP server is in `.mcp.json` as `xcode` (`xcrun mcpbridge`; needs Xcode **open**, prefers Xcode 27.1 beta when it's running). Use it for Apple templates, previews, build issues and simulator interaction; XcodeBuildMCP for headless builds and screenshots. Details in `ios/README.md`.
- No page ever scrolls sideways, at any width, admin or not (owner, 2026-09-27): long text wraps in its column (the shared table cell wraps), and lists drop secondary columns on phones (`DataTable` column `meta: { wide: true }`). `make screens` fails any screenshot whose page or any part of it scrolls sideways, and sweeps every signed-in page at phone width.
