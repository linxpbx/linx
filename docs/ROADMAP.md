# Linx — Roadmap

Every phase ends with:
- passing automated tests
- `docker compose up` working on a clean Ubuntu 24.04 VM
- updated docs (including `THREAT_MODEL.md`)
- a hand-run demo checklist `docs/DEMO_PHASE<N>.md`

UI work in each phase starts with low-fidelity screen specs in `docs/ui/`, approved by the owner before coding.

## Phase 0 — Design and foundations ✅ approved 2026-09-23
- **0a (docs):** `CLAUDE.md`, `ARCHITECTURE.md`, `DECISIONS.md`, `THREAT_MODEL.md`, `ROADMAP.md`, `ui/DESIGN_TOKENS.md`. ➡ Owner approval.
- **0b (code):**
  - repo skeleton and design-token pipeline (CSS vars + Swift assets)
  - multi-arch CI (amd64 + arm64, lint/test, Trivy/govulncheck/npm audit, SBOM, cosign, licence allowlist, gitleaks)
  - `linx setup` prerequisites step (OS/arch/RAM/disk detection, Docker install/upgrade, optional Portainer, resource-profile suggestion, `setup.yaml`)
  - `linx-certd` (lego, LE staging default, ZeroSSL fallback) and a step-ca bootstrap
  - `linx doctor` certificate checks
- **Exit:** on a fresh VM, `linx setup` → staging certificate for a linxpbx.com test subdomain → step-ca healthy → `linx doctor` all green.

## Phase 1 — Core PBX + web
- Public REST API (OpenAPI), signed webhooks and admin alerts come first.
- Asterisk 22 + Postgres realtime; control plane with RBAC, OIDC and MFA.
- Admin portal:
  - extensions, devices and trunks (TLS/SRTP first, unencrypted fallback with warning, ADR-023)
  - WireGuard profiles (several) and a per-trunk Connection drop-down (ADR-024)
  - inbound, outbound and ring-group wizards with the call simulator
  - email sending
  - Domain & DNS
- Simple mode and the getting-started checklist.
- Web client audio calls over WSS/TURN-TLS; CDR; voicemail with email delivery.
- **Exit:** a web-to-web and web-to-trunk call on profile A with UDP blocked, and the SIPp suite green.

## Phase 2 — Provisioning + first native client
- QR, email-link and manual enrollment with device certificates (Secure Enclave).
- Push gateway (APNs), with the "hold INVITE until register" logic in the ARI app.
- iOS/iPadOS app: CallKit/PushKit, audio + 1:1 video, the 5 tabs, iPad split view (English only, ADR-021).
- 7-day inactivity expiry.
- `APPLE_SIGNING.md`, `STORE_SUBMISSION.md`, `TEST_MATRIX.md`.
- *Custom tunnel is not built (ADR-007).* The "tunnel-only network" test cases run against WSS + TURN/TLS 443 and may trigger ADR-008.
- Linx name/trademark check completed before submission (ADR-015).
- **Exit:** every lock-screen and killed-app ringing case in `TEST_MATRIX.md` passes on real devices.

## Phase 3 — Conferencing + guests
- LiveKit, meetings with E2EE on by default, lobby, and guest links/QR/email with `.ics`.
- Screen share, SIP dial-in (non-E2EE meetings), click-to-call links.
- Guest page under 300 KB.

## Phase 4 — 3CX parity (web + iOS)
- Queues, IVR builder, BLF/presence panel, park/pickup, recording with retention.
- Chat, CardDAV/LDAPS directory, desk-phone provisioning.
- Site-to-site agent. This is the likely point to build the ADR-008 tunnel.

## Phase 5 — Hardening and ops
- Capacity page and benchmark, Pi Lite validation.
- External storage and encrypted backups with restore tests.
- Load tests and `tc netem` impairment matrix, observability dashboards.
- Upgrade and rollback, full security review, operator runbook.

## Phase 6 — Migration, channels and intelligence
- 3CX v20 and UCM6304 import; minimum client version enforcement.
- Reports and wallboards; local Whisper/LLM.
- WhatsApp Business (messaging + calling) and Telegram bot.
- Calendar and contacts sync, CRM lookup, n8n/Home Assistant/MQTT, MCP server.
- Migration tools can be pulled forward if the 3CX cut-over is needed sooner.

## Last task before production — `linx watch` (owner decision, 2026-09-25)
A watcher on the server that spots problems and gets a fix proposed, which the owner approves. Built after everything else planned for the first production release.
- **Watch (on the server, no AI):** a small background service checks what `linx doctor` checks, open alerts, crashed or restarting containers and error lines in the Linx services' logs. Only a *new* problem counts (each is fingerprinted, so a repeat isn't reported twice), with a daily cap on reports.
- **Report:** a short summary with secrets removed (passwords, tokens, SIP logins, keys) and only the few log lines that matter. It is sent as a GitHub issue (a token that can only create issues) and as an alert through the admin's existing channel. Never raw logs; nothing inbound; no Docker socket; it changes nothing on the server.
- **Fix (Claude, on demand only):** a Claude run starts only when a report arrives (never on a timer), one at a time, and works from the issue, CLAUDE.md and the code index to keep it cheap. It opens a pull request with the fix and a test; CI runs.
- **Approve:** nothing changes until the owner merges. The fix reaches the server through the normal build and update.
- **Trigger it yourself:** the admin portal gets "Check now" (runs the checks at once) and "Report a problem" (a short description of your own, sent the same way); `linx watch --now` does the same from the server.

## Phase 7 — Additional clients (owner go-ahead only)
- Android, macOS and Windows. Each must pass the same ringing and low-bandwidth release blockers.
