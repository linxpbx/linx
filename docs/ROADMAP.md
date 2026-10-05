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

## Phase 1 — Core PBX + web ✅ approved 2026-10-03 (Demo B, `docs/DEMO_PHASE1F.md`)
- Public REST API (OpenAPI), signed webhooks and admin alerts come first.
- Asterisk 22 + Postgres realtime; control plane with RBAC, OIDC and MFA.
- Admin portal:
  - extensions, devices and trunks (TLS/SRTP first, unencrypted fallback with warning, ADR-023)
  - WireGuard profiles (several) and a per-trunk Connection drop-down (ADR-024)
  - inbound, outbound and ring-group wizards with the call simulator
  - email sending
  - Domain & DNS
- Simple mode and the getting-started checklist.
- **Built for any admin, however new to phone systems** (owner direction 2026-09-26; applies to every admin and user screen, in every phase):
  - First-run web setup wizard includes the numbering plan: how many digits extensions have (e.g. 3 → 100–999), where numbering starts, with a plain explanation and a sensible default. It also sets up the first extensions.
  - Creating anything (extension, person, trunk, ring group, …) offers two paths: a guided step-by-step wizard, or **quick create** with only the fields that are truly required. Everything else gets a safe default and can be edited later.
  - Plain words, a recommended choice at each step, and no screen that needs telecom knowledge to finish.
- Web client audio calls over WSS/TURN-TLS; CDR; voicemail with email delivery.
- Slices so far: 1A API/webhooks/alerts, 1B phone engine, 1C web client, 1D phone lines (all approved). **1E** (approved 2026-09-26, `docs/ADMIN.md`): admin portal, web setup wizard with the numbering plan, passkeys, company sign-in. **1F** (owner-approved split): ring groups, office hours, the full inbound wizard, voicemail, email sending, call history, routing undo, Domain & DNS page.
- **Web-first install** (owner direction 2026-09-27, `docs/INSTALL.md`, ADR-057/058, approved 2026-09-28): the terminal prints one link, everything else in the browser; replaces most setup questions and the 1F Domain & DNS page.
- **Simpler phone lines, front doors and DNS** (owner request 2026-09-29, `docs/SIMPLER.md`, ADR-061 to 063, approved 2026-09-29): a PBX or gateway signs in to Linx like a phone, and providers are pasted and tested (with 1E step 7); one front-door method with instructions per proxy and "Check it", and any DNS company by hand or through libdns (1F, replacing its Domain & DNS item). **Public port (advanced)** (owner request and decisions 2026-09-29, `docs/SIMPLER.md` §2.5, ADR-064): Linx behind a router forwarding another public port (8443 suggested, customizable) to its 443, when nothing on 443 can pass it through, or on a rented server publishing that port itself; needs a DNS token; built in 1F after the front-door card, with its own demo (a normal mobile-data call works, a 443-only network is shown failing with the explanation).
- **Phase 1F design** (`docs/PHASE1F.md`, ADR-066 to 071, approved by the owner 2026-09-30, all as recommended): email, password reset, ring groups, office hours, the inbound wizard, voicemail, call history and undo, with SIMPLER §2–3 and §2.5 in one build order and two demos.
- **Exit:** a web-to-web and web-to-trunk call on profile A with UDP blocked, and the SIPp suite green.

## Phase 2 — Provisioning + first native client
*Design drafted 2026-10-03 in `docs/PHASE2.md` (ADR-073 to 078 proposed, decisions awaiting the owner).*
- QR, email-link and manual enrollment with device certificates (Secure Enclave).
- Push gateway (APNs), with the "hold INVITE until register" logic in the ARI app.
- iOS/iPadOS app: CallKit/PushKit, audio + 1:1 video, the 5 tabs, iPad split view (English only, ADR-021).
- Foldable iPhone ("iPhone Duo", owner request 2026-09-25): checked at design time 2026-10-03 — the device, simulator and fold APIs ship in the **Xcode 27.1 beta** (27A9269, iOS 27.1 SDK; *not* in the installed 27.0 release, and *not* in the newer 27.2 beta). APIs verified in Apple's documentation: SwiftUI `ArrangementView` (`arrangementViewStyle`), `UIArrangementViewController`, `UIView.ReservedRegion` (fold division, camera occlusion), all iOS 27.1. iPhone Duo ships 23 October 2026 on iOS 27.1. Plan in `docs/PHASE2.md` §8: 27.1 beta beside 27.0 (which keeps building releases and CI), adaptive layouts first, fold layout behind an iOS 27.1 check, and screenshots of every screen folded and unfolded in the Duo simulator.
- 7-day inactivity expiry.
- `APPLE_SIGNING.md`, `STORE_SUBMISSION.md`, `TEST_MATRIX.md`.
- *Custom tunnel is not built (ADR-007).* The "tunnel-only network" test cases run against WSS + TURN/TLS 443 and may trigger ADR-008.
- Linx name/trademark check completed before submission (ADR-015).
- Working from China (ADR-042): CallKit off when the device's region is China (in-app ringing instead); `TEST_MATRIX.md` has China rows (hotel Wi-Fi and a Chinese SIM: sign in, `*43`, calls both ways over TLS 443, push ringing).
- **Exit:** every lock-screen and killed-app ringing case in `TEST_MATRIX.md` passes on real devices.

## Phase 3 — Conferencing + guests
- LiveKit, meetings with E2EE on by default, lobby, and guest links/QR/email with `.ics`.
- Screen share, SIP dial-in (non-E2EE meetings), click-to-call links.
- Guest page under 300 KB.

## Phase 4 — 3CX parity (web + iOS)
- Queues, IVR builder, BLF/presence panel, park/pickup, recording with retention.
- Chat, CardDAV/LDAPS directory, desk-phone provisioning.
- Site-to-site agent. This is the likely point to build the ADR-008 tunnel.

## Desk phones from outside the office (owner requirement, 2026-10-05)

**A desk phone must work on the home or office network *and* from outside it, with or without an SBC in front.** Today only the first half is true: 5061 (TLS) and the audio ports are published on the LAN address alone and the firewall drops everything else, which is what "desk phones work on your home or office network" means in the help.

This is its own piece of design, not a setting, because going public with SIP is the thing that gets a phone system attacked. What it has to answer:

- **Which port the phone reaches.** Linx already multiplexes TLS on 443 by name (`linx-sni` routes the web app, the browser websocket and the relay), so `sip.<domain>:443` could carry a desk phone's SIP/TLS with nothing new opened — which also gets through hotel and office networks that allow only 443. Phones that insist on 5061 would need it published.
- **Registration lockout.** `docs/THREAT_MODEL.md` already names this as the precondition: a phone login on the LAN has no lockout today, and a password that only a LAN attacker could guess becomes one the whole internet can. The control plane sees every registration over ARI, so the count and the lockout belong there, with the same per-address and per-account shape as sign-in.
- **Where the audio goes.** A desk phone does not do ICE or TURN the way the app and the browser do; it sends SRTP straight to whatever address Asterisk gave it. From outside that means either publishing the RTP range (with `rtp_symmetric`/`force_rport`, which already handle the phone's own NAT) or putting something in front that terminates media.
- **With or without an SBC.** Both have to work: an SBC (or the Site Connector below) in front for people who have one, and nothing in front for people who don't.
- **What it must not cost.** The standing rules hold: no public 5060 of any kind, TLS and encrypted audio only, no weakening of what the LAN path does today.

**Where it goes:** not Phase 2, which is the app. Desk-phone **auto-provisioning** stays in Phase 4 as planned (owner, 2026-10-05) — this is a separate thing and may land earlier, since it is about reachability rather than convenience. Design for approval when it starts.

## Phase 5 — Hardening and ops
- Capacity page and benchmark, Pi Lite validation.
- ~~External storage and encrypted backups with restore tests.~~ Pulled forward ahead of going live (owner decision, 2026-09-27): see `docs/BACKUP.md`, ADR-055.
- Load tests and `tc netem` impairment matrix, observability dashboards.
- **Releases and updating from the web** (owner decision 2026-10-03: done here, close to production, not before Phase 2): see "Releases and updating from the web" below, steps 2 and 3. Full security review, operator runbook.

## Phase 6 — Migration, channels and intelligence
- 3CX v20 and UCM6304 import; minimum client version enforcement.
- Reports and wallboards; local Whisper/LLM.
- WhatsApp Business (messaging + calling) and Telegram bot.
- Calendar and contacts sync, CRM lookup, n8n/Home Assistant/MQTT, MCP server.
- Migration tools can be pulled forward if the 3CX cut-over is needed sooner.
- **Owner decisions 2026-09-30 (kept in this phase):** migration assistant for **Grandstream UCM, 3CX, Yeastar and FreePBX** (FreePBX covers most Asterisk-based systems); **spreadsheet (CSV) exports first**, full backup files later per product (3CX first); a preview before anything is saved, re-runnable without duplicates, a "done by hand" report, invites sent in one go; switching over gradually with the old system signed in to Linx (`docs/SIMPLER.md` §1). Design for approval when this phase starts.

## Releases and updating from the web (Phase 5; owner decisions 2026-10-03)
The owner asked why Linx can't check GitHub for an update and install it from the web interface. It can, in three steps, in this order. Step 1 was built at once; steps 2 and 3 wait for Phase 5, close to production (owner, 2026-10-03):
1. **Setup says when an update is waiting (done 2026-10-03, `docs/ops/UPDATING.md`):** `sudo linx setup` says when the `linx` program is newer than what's running and prints the command that installs it (`sudo linx setup --config /etc/linx/setup.yaml`), even while the Server settings page is open (it silently did nothing in Demo B); plus an "Updating Linx" note in `docs/ops/`. This stays the way to update when the web page is broken or GitHub can't be reached (China).
2. **Releases:** a version number, a short list of what changed, and signed files (images and the `linx` program, signed in CI as images are today) for each release, instead of whatever is newest on `master`. Nothing below works without these.
3. **Update from the web:** once a day the server asks GitHub whether a newer release exists (one small request; can be turned off: it tells GitHub the server's address). System → Status shows "Linx 1.1 is available" with what changed, and **Update**. The update is done by the server's helper with a fixed list of jobs (as **Restart** on System → Status is): the page can only press "go", never choose what is installed, and the helper installs only releases signed with Linx's release key, so an admin account still never means root on the host. It checks free disk space first, takes a backup, checks the signatures, downloads, restarts and checks every service is healthy; if anything fails it goes back to the old version and the backup. The page shows progress, reconnects after the restart and says "Updated to 1.1". Old versions are removed afterwards. Designed in a session of its own first (releases, the helper's new job, rollback), then built in steps.

## Last task before production — `linx watch` (owner decision, 2026-09-25)
A watcher on the server that spots problems and gets a fix proposed, which the owner approves. Built after everything else planned for the first production release.
- **Watch (on the server, no AI):** a small background service checks what `linx doctor` checks, open alerts, crashed or restarting containers and error lines in the Linx services' logs. Only a *new* problem counts (each is fingerprinted, so a repeat isn't reported twice), with a daily cap on reports.
- **Report:** a short summary with secrets removed (passwords, tokens, SIP logins, keys) and only the few log lines that matter. It is sent as a GitHub issue (a token that can only create issues) and as an alert through the admin's existing channel. Never raw logs; nothing inbound; no Docker socket; it changes nothing on the server.
- **Fix (Claude, on demand only):** a Claude run starts only when a report arrives (never on a timer), one at a time, and works from the issue, CLAUDE.md and the code index to keep it cheap. It opens a pull request with the fix and a test; CI runs.
- **Approve:** nothing changes until the owner merges. The fix reaches the server through the normal build and update.
- **Trigger it yourself:** the admin portal gets "Check now" (runs the checks at once) and "Report a problem" (a short description of your own, sent the same way); `linx watch --now` does the same from the server.

## After going live, second phase — Site Connector (owner idea, 2026-09-26)
An SBC for remote offices, done the way Pangolin adds a site with Newt. It's for Linx hosted in the cloud (or at one main office), with phones in other places.
- **Add a site in one step:** in the admin portal (or `linx site add`), the admin names the site. Linx shows a link with a one-time code and a single command to run it (`docker run …` or a compose snippet). The code works once and expires quickly. It only enrols the connector and never contains a SIP password (security rules).
- **Runs anywhere:** the connector is one small container on any machine at the remote site (a Raspberry Pi, a NAS, any Docker host). It connects **out** to the main Linx over an encrypted tunnel on **TCP 443**, through the same front door as everything else, so it gets past firewalls that block SIP or UDP and needs no port forwarding at the site.
- **Local phones use it:** desk phones and mobile clients on that site's network connect to the connector's local address, as if Linx were in the room. The connector carries their calls (signalling and audio) through the tunnel to the main Linx. Auto-provisioning (Phase 2) points a site's phones at their site's connector.
- **Softphones move without dropping calls:** a mobile or desktop app using the site's connector keeps working, and keeps its call, when the person walks out onto 4G/5G or another Wi-Fi without a connector. It switches straight to the main Linx over 443 (WSS + TURN, as today) and back again on returning. The app treats the connector and the direct route as two ways to reach the same line: the same login, one registration at a time. The switch happens during the call: ICE restart for audio, and SIP re-registration with the dialog kept, the way CallKit apps hand over between Wi-Fi and cellular. The person hears at most a short gap. The app finds the connector on the site's network by itself (provisioning, then a local discovery check), and never uses it anywhere else. Desk phones don't move, so this only concerns softphones (the iOS app in Phase 2, then the web client). The release test: walk out of the office during a call and it stays up.
- **Local phone lines use it too:** a trunk on the remote site's network (a UCM with landlines, an FXO gateway like the GXW4104, a local provider's line that only works from that office) is reached through the same connector. In the trunk setup it's one more "Connection" choice, next to "Internet" and a WireGuard profile (ADR-024): "Through site X". Outgoing calls can prefer the site's own line for that site's people (local caller ID and local rates), with the main lines as backup. Calls to that line's numbers ring whoever Linx routes them to, anywhere. The toll-fraud rules stay as they are (no trunk-to-trunk, permission levels, limits), and the connector only lets the main Linx reach the local trunk addresses the admin picked.
- **The admin sees and controls each site:** online or offline, how many phones, call quality, last seen. Site down or back up raises an alert. One click revokes a site, and its tunnel closes at once. The connector gets its own certificate from Linx's internal CA when it enrols (step-ca); the code itself is only a one-time ticket.
- **To decide when it's designed:**
  - It changes ADR-007 ("no custom tunnel for now"), so it needs a new ADR.
  - Pick the tunnel technology. Newt/Pangolin's own is AGPL, so it can't be reused as is. Other candidates: WireGuard carried over a TLS/WebSocket 443 wrapper, or a small Go relay. The rule is permissive licences and mature open source first.
  - Audio quality over TCP (a lost packet delays everything behind it): try UDP first and fall back to 443.
  - Per-site call limits for toll-fraud protection.
  - Whether mobile apps on the site's Wi-Fi use the connector or keep connecting directly.
- **Fits the owner's needs:** everything works on TLS 443 alone (the China travel requirement), and nothing needs opening at remote sites.

## Phase 7 — Additional clients (owner go-ahead only)
- Android, macOS and Windows. Each must pass the same ringing and low-bandwidth release blockers.
