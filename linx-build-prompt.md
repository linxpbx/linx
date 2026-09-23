# Claude Code Prompt — Linx: Self-Hosted Unified Communications Platform

> Paste everything below into Claude Code at the root of an empty git repo. Start in **plan mode**.

---

## Role and working rules

You are the lead engineer building **Linx**, a production-grade, self-hosted, open-source unified communications platform that replicates the 3CX feature set and user experience. I am the owner and operator. I will compile, sign, and distribute the Apple apps myself in Xcode using my own Apple Developer account.

Rules for how you work:

1. **Plan first.** Before writing code, produce `docs/ARCHITECTURE.md` and `docs/ROADMAP.md` from this brief and ask me any blocking questions in one batch. Do not start Phase 1 until I approve.
2. **Create a `CLAUDE.md`** at the repo root capturing the architecture, conventions, commands, and security rules below, and keep it current.
3. **Work in phases** (defined at the end). Each phase must end with: passing automated tests, a `docker compose up` that works on a clean Ubuntu 24.04 VM, updated docs, and a short demo checklist I can run by hand.
4. **Never weaken security to make something work** (no disabling cert verification, no plaintext fallbacks, no `--insecure`, no hardcoded secrets). If something is blocked, stop and tell me.
5. **Prefer mature open-source components** over custom code. Write custom code only for glue, the control plane, the tunnel, provisioning, and the clients.
6. Pin all dependency versions. Generate an SBOM. No abandoned libraries.
7. When a decision below is marked **(recommend)**, evaluate the options briefly in `docs/DECISIONS.md` (ADR format) and pick one with justification.
8. **Token efficiency (mandatory):**
   - Keep `CLAUDE.md` under ~200 lines. Put detail in `docs/` and read it only when needed.
   - One task per session. Suggest `/clear` when a task is done, and `/compact` before context grows large.
   - Read files with targeted ranges or grep, never whole large files. Never dump full logs: filter to errors and the last ~50 lines.
   - Test and build commands must print summaries only (quiet reporters, failures only).
   - Use subagents for broad codebase searches, so only conclusions enter the main context.
   - Use Sonnet for routine implementation. Use Opus only for architecture, security review, and hard debugging, and tell me when to switch.
   - Fetch library docs via the docs MCP instead of guessing APIs and iterating on errors.
   - Don't restate plans or summarise diffs unless I ask. Keep replies short.
   - Turn repeated procedures (deploy, run test matrix, cut a release) into project skills or `make` targets instead of re-explaining them.

---

## Environment

- **The product must not assume my setup.** It must work for any home or small-business network, with or without a reverse proxy, domain, static IP or port forwarding (see **Deployment profiles**). My environment below is the first **test target**, not the default.
- My test environment: Ubuntu 24.04 LTS VM on VMware ESXi (dedicated VM), Docker + Docker Compose, a single dynamic public IP behind a UniFi UCG Fiber, with 443 already owned by Pangolin (Traefik).
- My DNS: Cloudflare, including the product domain **linxpbx.com**, which my test install uses. The product supports any DNS provider through the DNS library (see **Domain and DNS management**).
- I run Pangolin (Traefik-based, **without** Newt/Gerbil, Cloudflare DNS-only records). The platform must coexist with it, but must equally work with no proxy at all.
- Existing gear that must interoperate later: Grandstream UCM6304 and GXW4104 FXO gateway (as SIP trunk peers), and Grandstream/Yealink desk phones (provisioning).
- Lesson learned from a previous attempt: a Kamailio + rtpengine + coturn relay stack caused client crashes on call answer. Keep the media path simple, test call answer on every client platform, and treat "answer from locked/background state" as a release blocker.

---

## Product goals (3CX parity)

### Telephony core
- Extensions, multiple devices per extension (fork to all registered devices + push wake).
- Ring groups (simultaneous, hunt, round-robin), call queues with agent login/logout, IVR / auto-attendant with office hours and holidays.
- Call transfer (blind + attended), hold with MOH, park/pickup, call forwarding rules (always / busy / no answer / unregistered), DND, BLF/presence.
- Voicemail with email delivery (audio attachment + optional transcription hook).
- Call recording (per extension / per queue, with retention policy).
- SIP trunks (register and IP-auth), inbound DID routing, outbound rules, caller ID policies.
- Call history (CDR) per user and system-wide, with search and export.
- Internal chat/presence between users (phase 4, nice-to-have).

### Video
- 1:1 video calls between any clients (app-to-app and app-to-web).
- Multi-party video conferencing via an SFU: screen sharing, mute/unmute, camera toggle, active-speaker layout, grid layout, raise hand, moderator controls (mute all, remove, lock room), waiting room/lobby.
- Audio dial-in to conferences from PSTN/extensions (SIP bridge into the SFU room).

### External guests
- A user can create a meeting (instant or scheduled) and invite external parties via:
  - a shareable link,
  - a QR code (rendered in app and in the email),
  - email invitation with `.ics` calendar attachment.
- Guests join from a browser with **no install and no account** (WebRTC), entering a display name; lobby admission by host is optional per meeting.
- Guest links are signed, time-bound tokens (JWT or PASETO) scoped to one room, revocable, optionally PIN-protected.
- Also support "click-to-call" guest links that ring a specific extension/queue (like 3CX "talk" links).

### Provisioning (QR)
- Admin or user portal shows a QR code per extension/device.
- QR contains **only** a short-lived (e.g. 10 min), single-use enrollment token and the server's HTTPS URL + certificate pin — **never the SIP password**.
- Client scans QR → calls the enrollment API over TLS → generates a device keypair on-device (Secure Enclave / Android Keystore / TPM where available) → receives a device-bound client certificate (mTLS) plus full account config (SIP identity, tunnel endpoint, TURN credentials method, directory endpoint, codec prefs, push registration).
- Provisioning also available by email link (deep link / universal link) and manual login (username + password + MFA).
- Admin can revoke a device (revokes its cert and kills its sessions).
- Desk phone auto-provisioning: generate Yealink and Grandstream config files served over HTTPS with per-MAC auth (phase 4).

### Tunnel (3CX-tunnel equivalent)
- A single encrypted **TCP** connection (TLS 1.3, port 443 capable) that carries **SIP signalling, RTP/SRTP media, and directory/API traffic** so clients work behind restrictive firewalls, hotel Wi-Fi, and carrier NAT with only outbound 443 allowed.
- Implement as a multiplexed tunnel: client-side tunnel library ↔ server-side tunnel gateway, mutual TLS using the device certificate from provisioning, with logical streams for SIP, each RTP/RTCP flow, and HTTPS/directory.
- **(recommend)** protocol: custom framing over TLS vs WebSocket-over-TLS vs HTTP/2 or yamux/smux multiplexing. Must traverse HTTP proxies where possible.
- Client tunnel core written once in **Rust or Go** and exposed to all native clients via FFI (e.g. `uniffi` or `gomobile`); web clients use the equivalent (SIP over WSS + TURN over TLS 443).
- Automatic mode selection: direct (UDP, SRTP/DTLS) when available → tunnel when not; manual "always tunnel" toggle.
- Handle network changes (Wi-Fi ↔ cellular) with fast reconnect and in-call media recovery (ICE restart / re-INVITE).
- Document the latency/jitter trade-offs of RTP-over-TCP and implement mitigation (small send buffers, TCP_NODELAY, jitter buffer tuning, head-of-line awareness).
- Optional site-to-site mode: a tiny tunnel agent (Docker/Raspberry Pi) that lets a remote office's desk phones register through one tunnel (like the 3CX SBC).

### Directory services
- Company directory (users, extensions, departments, presence) and shared/personal contacts.
- Exposed via a REST/GraphQL API over the tunnel, plus **CardDAV** and read-only **LDAPS** for desk phones.
- Optional sync from an external source (LDAP/Entra ID) — design the interface now, implement later.

### Presence and extensions panel (all clients)

Every client has a **Team / Extensions** view, 3CX-style, listing all extensions the user is allowed to see.

**Status per person, updated in real time:**

- Available
- On a call
- Ringing
- In a meeting
- Away (idle)
- Do Not Disturb
- Reachable via push
- Offline

"Reachable via push" matters because a sleeping mobile app has no live registration but will still ring. It must **not** be shown as offline. Offline means no registered devices, no valid push tokens, or a session expired by the 7-day inactivity rule.

**Per-person detail:**

- extension number, department and title
- custom status message
- which devices are active (desktop / mobile / web)
- "last seen" time for offline users
- optional "back at" time

**Organisation:**

- search by name, number or department
- group by department
- favourites pinned to the top
- queue and ring-group membership, with agent logged-in/out state visible to supervisors
- shared parked calls with a park-slot view

**Actions from any entry:**

- audio call and video call
- chat
- invite to a meeting
- transfer a live call to them (blind or attended)
- pick up their ringing call (if permitted)
- listen/whisper/barge for supervisors (permission-controlled)

**Status sources:**

- manual setting
- automatic idle detection on desktop/web
- on-call and ringing from the PBX (Asterisk device state via ARI/AMI)
- "in meeting" from the SFU
- calendar sync (see **Integrations**)
- DND synchronised across all of a user's devices

**Transport:**

- presence is pushed over the existing authenticated channel (tunnel or WSS) as small delta events, never polled
- desk phones get BLF via SIP `SUBSCRIBE`/dialog events

**Privacy and permissions:**

- admins control who can see whom (whole company, own department, or custom groups)
- admins decide whether "last seen" and device details are shown
- a user can hide their status message
- the directory is served only to authenticated devices

**Low-bandwidth behaviour:**

- presence updates are batched and throttled on poor links
- on mobile, the list refreshes on app open and via push instead of keeping a persistent connection just for presence

### Home and small-business simplicity
- The installer asks: **"Home"** or **"Small business"**. This picks sensible defaults and a **Simple mode** admin console.
  - Simple mode hides queues, reports, CRM, supervisor tools and advanced routing until switched on. Nothing is removed; it's one toggle away.
- **Getting-started checklist** on the admin home page:
  1. connect a phone line
  2. add people
  3. set business hours
  4. choose where calls ring
  5. install the app with a QR code
  6. make a test call
- **Home extras:**
  - family-friendly naming ("Mum's phone", "Kitchen")
  - an intercom/paging group
  - doorbell integration through Home Assistant
  - blocking of spam and withheld callers, with a personal block list
- Plain-language help text and examples on every setting. No telecom jargon in default views.

### Admin & user portals (web)
- Admin console: extensions, devices, groups, queues, IVR builder, trunks, routing, office hours, recordings, CDR, live call monitor, system health, backup/restore, certificate status, audit log.
- User portal: settings, forwarding rules, voicemail, recordings, call history, meeting scheduling, QR provisioning for own devices.
- Admin auth: OIDC (support Authentik/Keycloak/Entra), local accounts with TOTP/WebAuthn MFA, RBAC (system admin, admin, user, reporter).

---

## Client applications

**Scope for this build: Web and iOS/iPadOS only.** Get these two rock-solid before anything else. Android, macOS and Windows are deferred to a later phase that starts only when I give the go-ahead. Design the server APIs, provisioning, push gateway and presence protocol to be client-agnostic, so adding those platforms later needs no server redesign. Do not build Android, macOS or Windows code, stores, or CI now.

- **Web client:** TypeScript (**(recommend)** React vs Svelte), WebRTC, SIP over WSS, TURN over TLS/UDP 443. It is an installable PWA and also serves as the guest meeting/click-to-call page and the admin/user portals.
- **iOS/iPadOS client:** **(recommend)** native Swift/SwiftUI vs Flutter. Default expectation: **native Swift/SwiftUI**, because PushKit/CallKit reliability and first-pass App Store approval matter most and there is no cross-platform benefit with a single native platform. Build it as a universal app with iPad layouts (split view, keyboard shortcuts).
- **(recommend)** iOS SIP/media engine: evaluate native WebRTC with SIP over WSS (same stack as web), Linphone SDK, and PJSIP. Criteria:
  - video support
  - SRTP/DTLS
  - Opus / H.264 / VP8
  - stability when answering from the lock screen or a killed app
  - App Store licensing (Linphone/PJSIP are GPL unless commercially licensed)
  - behaviour on poor networks
  - how it satisfies the tunnel requirement

  If the WebRTC approach is chosen, evaluate in `DECISIONS.md` whether SIP over WSS + TURN over TLS on 443 fully meets the tunnel requirement, making the custom tunnel unnecessary for this scope.
- **Mandatory native integrations**
  - iOS/iPadOS: **PushKit VoIP pushes + CallKit** (incoming call UI from locked/killed state, reporting to CallKit immediately on push as Apple requires), audio session handling, Bluetooth/CarPlay routing, Siri/recents integration.
  - Web: installable PWA, Web Push for incoming call notifications when tab is backgrounded.
  - *(Later phase)* Android: FCM high-priority push + ConnectionService/Telecom; macOS/Windows: tray/menu-bar app with background helper.
- **Push wake architecture:** server-side push gateway (APNs token auth now; design the interface so FCM HTTP v1 can be added later). On inbound call to an extension with sleeping mobile devices: send push → device wakes → registers (via tunnel if needed) → call is delivered. Implement the "hold INVITE until device registers" logic in the PBX layer (ARI app or SIP proxy), with a timeout.
- Features in every client: dial pad, contacts/directory search, presence/BLF, call history, voicemail (visual), transfer/hold/park/conference, video calls, join/host meetings, share meeting link/QR/email, chat (phase 4), settings, QR scan provisioning, tunnel status indicator.
- Accessibility (VoiceOver/TalkBack), dark mode, localisation-ready (**English and Arabic with full RTL**).
- Provide Xcode projects/workspaces that I can open, sign with my team ID, and archive — document every entitlement (push, VoIP background mode, camera, mic, local network) and provisioning-profile step in `docs/APPLE_SIGNING.md`.

---

## UI / UX design

- **Approved mockups:** the design canvas "PBX App Screens" (placeholder branding; apply Linx) (7 screens: iOS keypad, Team, active call, QR setup; web console, meeting with invites, guest join) is the visual reference. I will export it into `docs/ui/`. Match its layout, colours and typography.
- **Design first:** before building UI in each phase, produce low-fidelity screen specs in `docs/ui/` (layout, states, navigation) and get my approval. Don't iterate on visuals in code.
- **Shared design system** for web and iOS: design tokens in one source file (colors, typography, spacing, radii, status colors), exported to CSS variables and a Swift asset catalog. Status colors are consistent everywhere: available = green, busy/on call = red, ringing = amber, away = yellow, DND = purple, push-reachable = grey-blue, offline = grey.
- **iOS:** follow Apple's Human Interface Guidelines. Native SwiftUI controls, SF Symbols, system fonts with Dynamic Type, tab bar navigation with 5 tabs: Calls, Team, Keypad, Chat, More (Meetings, Voicemail and Settings live under More), CallKit for incoming calls, and iPad split view with sidebar.
- **Web:** clean, professional, 3CX-like layout. Left sidebar navigation, main content area, right-hand panel for call/contact details. Responsive down to phone width. Use a mature accessible component library (**(recommend)** shadcn/ui + Radix vs Mantine).
- **Key screens:**
  - dialer
  - active call (audio/video, with transfer/hold/park/conference controls)
  - incoming call
  - Team/presence panel
  - call history
  - visual voicemail
  - meetings (schedule, lobby, grid/speaker view, share link/QR/email)
  - guest join page (minimal, no account)
  - QR enrollment flow
  - settings
  - admin console
  - WhatsApp/Telegram inbox
- **Required everywhere:**
  - light and dark mode
  - English and Arabic with full RTL mirroring (including call controls and timelines)
  - WCAG 2.2 AA contrast, with VoiceOver and keyboard navigation
  - a visible network-quality indicator and low-data mode toggle in calls
  - clear empty, loading and error states
- **Product name: Linx.** Use it consistently:
  - app display name, App Store listing, web title, installer, docs and emails
  - repo `linx`, CLI `linx` (e.g. `linx setup`, `linx doctor`), container and network prefix `linx-`
  - product domain: **linxpbx.com** (owned). Bundle ID `com.linxpbx.app`; universal links, the website and the support/privacy pages live on linxpbx.com
  - linxpbx.com is the **product/project** domain. Each installation still uses its own domain chosen in the setup wizard, and my own test install uses subdomains of linxpbx.com
  - I'll supply the logo later; until then use a simple text wordmark
  - Check that the name is free to use in the App Store and flag any clash (trademark or existing app) in `DECISIONS.md` before Phase 2.
- **White-label:** admins can override the display name, logo and accent colour from the admin console. "Powered by Linx" stays in the About screen.
- **Asset efficiency:** no heavy animations or large images on low-bandwidth paths. The guest join page must load in under 300 KB.

## Server architecture

- **(recommend)** PBX core: **Asterisk 22 LTS (chan_pjsip, ARI)** vs FreeSWITCH. Default expectation: Asterisk with realtime config from PostgreSQL, ARI-based control app for advanced call logic (push wake, queues extensions, recording control).
- **(recommend)** Video conferencing SFU: **LiveKit** (with LiveKit SIP bridge) vs Jitsi vs Asterisk ConfBridge SFU mode. Pick the one that best supports large rooms, guests, screen share, recording, and SIP dial-in.
- TURN/STUN: **coturn** with TLS on 443 and time-limited REST credentials.
- Control plane / API: **(recommend)** Go or TypeScript (NestJS). Responsibilities: tenants (single-tenant now, multi-tenant-ready schema), users, extensions, devices, provisioning & enrollment, meeting links, push gateway, directory API, CardDAV/LDAP, CDR ingestion, webhooks.
- Tunnel gateway: separate service (same language as the client tunnel core), terminates mTLS, demultiplexes streams to Asterisk (SIP/TLS + RTP), SFU, and API.
- Database: PostgreSQL. Cache/pubsub: Redis/Valkey. Object storage for recordings/voicemail: local volume with optional S3/MinIO.
- Ingress (reverse proxy, SNI routing, ports) is not hardcoded. It is generated from the selected **deployment profile** (next section).
- Observability: Prometheus metrics, Grafana dashboards (registrations, active calls, MOS/jitter/packet loss per call, tunnel sessions), Loki logs, alerting. Homer/HEP SIP capture optional.
- Deployment: `docker compose` for single-node; clearly separated volumes; `make` targets for install, upgrade, backup, restore. Design so it can later move to Kubernetes.

---

## Migration from existing systems

- Build import tools (CLI + admin console wizard) with a **dry-run preview** and a conflict report before anything is written. Imports are idempotent: re-running updates records and never duplicates them.
- **3CX v20:** import from a 3CX backup file or its PostgreSQL dump:
  - extensions (names, numbers, emails, forwarding rules, DND)
  - ring groups, queues, IVRs and digital receptionists, office hours and holidays
  - inbound and outbound rules, trunks (secrets re-entered or re-generated, never silently copied), phonebook/contacts
  - call history (CDR)
  - voicemail greetings and prompts where accessible
- **Grandstream UCM6304:** import from its config backup and/or API: extensions, ring groups, queues, IVR, trunks, inbound/outbound routes, phonebook, CDR export (CSV).
- **Generic CSV import/export** for users, extensions, contacts, and DIDs.
- **After migration:**
  - generate a report of everything imported, skipped, or needing manual action (e.g. unsupported features)
  - bulk-send QR/email enrollment invitations to all migrated users
  - desk phones: re-point provisioning URLs automatically where possible
- Support a **parallel-run period**: the new platform runs alongside the old PBX via a SIP trunk between them, so extensions can migrate in batches.

## End-to-end encrypted meetings (on by default)

- **E2EE is on by default for meetings**; hosts can turn it off per meeting when they need recording, dial-in or AI summaries.
- Admins can make E2EE mandatory for specific users or groups. Use the SFU's insertable-streams / frame-encryption support (e.g. LiveKit E2EE) so the server forwards media it cannot decrypt.
- Key management: per-meeting keys, rotated when participants leave, delivered only to authenticated participants and admitted guests. Guests receive the key through the signed guest link flow, never via email content.
- Clear UI indicator (lock icon + verification code participants can compare) when E2EE is active.
- The UI must make trade-offs explicit and enforce them automatically when E2EE is on:
  - server-side recording is disabled, with an optional client-side recording by the host
  - SIP/PSTN dial-in is disabled
  - live transcription/AI summaries are disabled
  - unsupported clients (browsers without insertable streams) are blocked with an explanation, and the host is told why a guest can't join and can switch E2EE off for that meeting
- 1:1 app-to-app calls already use DTLS-SRTP. Document which call types are E2EE and which are server-decrypted (e.g. calls bridged through Asterisk for recording, transfer, or trunks).

## Client updates and version control

- **Store builds** (App Store, Play Store, Microsoft Store) update through the stores.
- *(Phase 7, deferred)* **Direct-download desktop builds** (optional, for users outside the stores):
  - auto-update via **Sparkle** (macOS) and **Squirrel/WinSparkle or MSIX App Installer** (Windows)
  - updates are signed (EdDSA for Sparkle, code signing on Windows) and served from the platform over HTTPS
  - release channels: stable and beta
- **Minimum supported version:** the server advertises a minimum and a recommended client version per platform.
  - Clients below the recommended version show an update prompt.
  - Clients below the minimum are blocked from connecting, with a clear "update required" screen and a store link. Use this to force upgrades after security fixes.
- The admin console shows client versions per device and flags outdated ones.
- Server upgrades are versioned with database migrations, automatic pre-upgrade backup, and one-command rollback.

## Reports and analytics

- Dashboards and scheduled reports (emailed as PDF/CSV):
  - call volume by hour/day/extension/trunk
  - missed and abandoned calls
  - average wait and talk time
  - queue SLA (answered within X seconds)
  - agent performance (calls handled, login time, pause time)
  - ring group performance
  - inbound DID usage
  - call quality (MOS, loss, jitter by network/client type)
  - meeting usage (count, duration, participants, guests)
- Wallboard view for queues: live waiting callers, longest wait, agents available, SLA today.
- Role-based access: supervisors see only their queues/departments. All reports are exportable.

## Local AI features (private, self-hosted, optional)

- All AI runs **on my own hardware**. No audio or text leaves the server unless I explicitly configure an external provider.
- **Transcription:**
  - self-hosted Whisper (e.g. faster-whisper / whisper.cpp, GPU optional)
  - Arabic and English support, including mixed-language calls
  - used for voicemail-to-text (included in the email and the app) and for recorded calls and meetings
- **Summaries:** a pluggable local LLM (e.g. via Ollama or an OpenAI-compatible endpoint I configure) produces:
  - meeting summaries with action items
  - call summaries for recorded calls
  - voicemail one-line previews
- Consent and policy:
  - transcription/summaries only on calls and meetings where recording is permitted
  - announce to participants
  - per-user and per-meeting opt-out
  - retention rules match recordings
  - disabled automatically for E2EE meetings
- Runs as a background job queue so it never affects live call quality. It can run on a separate machine (e.g. a GPU box) via an authenticated API.

## WhatsApp and Telegram integration

Use only **official APIs**. Unofficial libraries that automate personal accounts (e.g. whatsmeow, Baileys, Telegram userbots) are **not allowed**: they break terms of service and risk bans.

### WhatsApp (WhatsApp Business Platform, Cloud API)

- **Messaging:**
  - a shared team inbox inside all clients, with conversations assigned to users, queues, or departments
  - contact matching with the directory and call history
  - media and attachments
  - approved message templates for messages outside the 24-hour customer-service window
  - canned replies
  - business hours auto-replies
- **Sending invites:** send meeting links / guest call links and QR codes via WhatsApp templates.
- **WhatsApp Business Calling:**
  - Connect inbound user-initiated WhatsApp calls to the PBX via Meta's **SIP** integration (preferred) or the Graph API calling webhooks + WebRTC.
  - Treat WhatsApp as a trunk type, so calls route to queues, ring groups, IVR, and extensions like any other inbound call.
  - Support business-initiated calls within Meta's permission rules: call-permission requests, limits per 24 h / 7 days, and call windows. Enforce these limits in the platform and surface permission status in the UI.
  - Technical constraints to design for:
    - Opus audio (transcode if needed)
    - no re-INVITEs from our side
    - WhatsApp calls cannot bridge to PSTN
    - allowlist Meta's published IP ranges, auto-updated, for the SIP/media endpoints
    - verify SIP and media reachability works with the selected deployment profile (including profile A behind Pangolin with a dynamic IP)
  - Check and document current availability for my business number's country, including the UAE, before enabling. Keep the feature switchable per country.
- **Setup:**
  - document the Meta Business verification, phone number registration, app/webhook configuration, and per-message/per-minute pricing
  - store tokens as Docker secrets
  - verify webhook signatures (`X-Hub-Signature-256`)

### Telegram (Bot API)

- **A company Telegram bot:**
  - customer conversations land in the same shared inbox as WhatsApp, with assignment and history
  - send meeting and guest-call links / QR codes
  - a "call me back" / click-to-call button that creates a callback request or opens the web guest-call page
- **Personal notifications:** users can link their Telegram account (via a one-time deep link) to receive missed-call, voicemail (with transcription), and meeting-reminder notifications.
- **Voice/video calls:** Telegram's Bot API does not support calls. Do not implement Telegram calling; redirect to the web guest-call link instead.
- **Webhooks:**
  - use the secret token header for verification
  - rate-limit handling

### Common requirements

- Webhooks must be reachable over HTTPS on the selected deployment profile. Behind any authenticating proxy (e.g. Pangolin in profile A), the webhook paths only must **bypass the proxy's authentication**, with provider signature verification and IP/rate limits in the platform.
- Build a channel abstraction (inbox, conversation, message, participant) so more channels (SMS, email, web chat widget) can be added later.
- Retention, export, and deletion rules apply to channel messages like any other user data. Log all channel activity in the audit log.

## Call routing wizard (no telecom knowledge needed)

The admin must **never have to write or read a dial pattern** (like `_9XXXXXXX` or `^00[1-9]`). Build inbound and outbound routing as plain-language, step-by-step wizards. Someone who has never used a phone system must be able to finish them without help.

### Inbound wizard: "When someone calls you, what should happen?"

1. **Which number?** Pick from a list of numbers the connected phone lines provide, shown in the usual local format (e.g. "04 000 0123"), or choose "Any number".
2. **When?** Choose from:
   - "Always"
   - "During business hours" and "Outside business hours" (a visual weekly schedule)
   - "Public holidays" (a country holiday calendar that is pre-filled and editable)
3. **Send the call to:** a picker with icons and one-line explanations:
   - a person
   - a group that rings together
   - a waiting line (queue)
   - a menu ("Press 1 for Sales…")
   - voicemail
   - a recorded message
   - a mobile or outside number
   - hang up
4. **If nobody answers after [20] seconds:** pick the next step from the same options.
5. **Review:** a plain-English summary plus a simple flow diagram. For example: "Calls to 04 000 0123 during business hours ring the Support group. If nobody answers in 20 seconds they go to Support voicemail. Outside business hours callers hear the closed message."

### Outbound wizard: "How should your people make calls?"

1. **Country:** chosen once. The system loads that country's numbering rules from a maintained numbering-plan library (e.g. libphonenumber) and generates all patterns automatically.
2. **Call types:** ticked in plain words, each with example numbers:
   - Local
   - Mobile
   - National
   - International
   - Toll-free
   - Premium/expensive numbers: blocked by default
   - Emergency: always allowed, can't be turned off
3. **Which phone line to use:** primary and backup, with provider templates so trunk setup is also a guided form.
4. **Who can call what:** permission levels such as "Staff: local & mobile" and "Managers: + international", assigned to users or groups.
5. **Caller ID:** which number people see when you call them.
6. **Review:** a plain-English summary, e.g. "Mobile numbers like 050 123 4567 go out on Line A, and on Line B if Line A is down. International calls are allowed only for Managers."

### Ring group wizard: "Who should answer these calls?"

1. **Name it** in plain words (e.g. "Front desk", "Support team").
2. **Pick people:** a searchable list showing each person's photo and live status. Drag to reorder.
3. **How should their phones ring?** Three picture cards with a one-line explanation and a small animation:
   - **Everyone at once:** first to answer gets it.
   - **One after another:** in the order you set.
   - **Take turns:** spreads calls evenly.
4. **Ring for how long?** A slider in seconds, with a friendly hint ("about 5 rings").
5. **If nobody answers:** the same destination picker as the inbound wizard (voicemail, another group, a mobile number, a message).
6. **Extras** (collapsed by default):
   - announce the group name to the answering person
   - skip people who are busy or on Do Not Disturb
   - let members log in/out of the group from their app
7. **Review:** a plain-English summary. Offer to test it right away by ringing the group from the admin's browser.

Every ring group gets an internal number automatically (editable). It can be picked by name from any other wizard.

### Common to all wizards

- **"Test a call" simulator:** type a number, plus a date and time for inbound. It shows step by step which rule matches and where the call goes, before anything is saved.
- **Automatic checks:** overlapping or conflicting rules, unreachable destinations, and numbers that match no rule are detected and explained in plain words with a suggested fix.
- **Templates:**
  - "Small office"
  - "Shop with opening hours"
  - "Support desk with queue"
  - country presets
- **Advanced toggle** for experts only: shows the generated patterns read-only, with an option to add custom patterns (validated, with examples).
- **Undo and change history** for every routing change. Changes apply without restarting the system and without dropping live calls.
- The wizard applies to trunks, ring groups, queues, IVR menus and office hours too: every setup screen uses the same guided, plain-language style.

## Small hardware and Raspberry Pi support

- The server must run on **Raspberry Pi 5 (8 GB recommended, 4 GB minimum)** and other ARM64 boards, as well as x86-64.
  - Supported OS: Raspberry Pi OS 64-bit (Bookworm or later), Debian 12+, Ubuntu 24.04. 32-bit is not supported.
  - All container images are multi-arch (amd64 + arm64), built and tested in CI.
- **Resource profiles** are chosen automatically from the detected hardware, and can be changed in the admin console:

  | Profile | Hardware | Behaviour |
  |---|---|---|
  | **Lite** | Pi / ≤4 cores / ≤8 GB | Audio-first, small conferences, AI off, lighter monitoring stack |
  | **Standard** | Mini PC / small VM | All features, medium conferences, AI only if pointed at another machine |
  | **Performance** | Server / large VM | All features, including local AI and large conferences |

- **Lite-profile optimisations:**
  - Avoid transcoding: Opus end to end, G.711 only at the trunk edge.
  - Cap video resolution and conference size.
  - Replace the Prometheus/Loki/Grafana stack with a lightweight built-in metrics page (full stack optional).
  - PostgreSQL tuned for low memory.
  - Optional Redis/Valkey (in-process cache when absent).
  - Whisper/LLM disabled, or pointed at another machine.
- **SD-card protection:**
  - Warn if running from an SD card and recommend a USB/NVMe SSD.
  - Keep logs in RAM with periodic flush, or on external storage.
  - Minimise disk writes.
- The Pi must pass the same ringing and security tests. Document its measured capacity.

## System capacity page ("What can this server handle?")

- Admin console page and `linx doctor --capacity` command. It detects CPU model, cores, RAM, disk type and free space, and architecture.
- It runs a short **benchmark**, at install time and on demand:
  - SRTP audio call load with SIPp
  - SFU forwarding load
  - disk write speed
  - **measured upload/download bandwidth** (often the real limit on home internet)
- It shows **recommended limits**, each as a traffic-light bar against current usage:
  - number of extensions
  - concurrent audio calls (with and without recording and transcoding)
  - concurrent 1:1 video calls
  - largest conference and total conference participants
  - recording storage in days/hours at current retention
- **Explanations:** says which resource is the bottleneck (e.g. "Your internet upload speed limits you to about 6 simultaneous video calls") and what upgrade would help.
- **Enforcement:** optional soft limits that warn admins, and optional hard limits that refuse new calls or meetings with a friendly message instead of degrading everyone's quality.
- Estimates are labelled as estimates. Store benchmark history so changes after upgrades are visible.

## External storage (logs, backups, recordings)

- An admin console **Storage** page plus a setup-wizard step. Each data type can go to a different place:
  - logs
  - configuration backups
  - call recordings
  - voicemail
  - meeting recordings
  - transcripts
- **Supported targets:**
  - local disk or USB drive
  - NAS via **SMB/CIFS** or **NFS**
  - **SFTP**
  - **WebDAV**
  - **S3-compatible** (MinIO, Backblaze B2, Wasabi, AWS S3)
  - **Google Drive**, **OneDrive**, **Dropbox** and others via **rclone**, with OAuth sign-in in the browser; tokens stored as secrets
- **Configuration backups:**
  - scheduled (daily default) and before every upgrade
  - include database, settings, certificates metadata and prompts, but never private keys unless explicitly chosen
  - encrypted client-side with a passphrase I keep (e.g. restic or rclone crypt)
  - retention rules (e.g. 7 daily, 4 weekly, 12 monthly)
  - one-click restore, plus a restore test that verifies backups actually work
- **Recordings and logs:**
  - retention per type
  - encrypted at rest on the remote target
  - a local buffer queue if the target is unreachable, with automatic retry
  - an alert when the target is down or nearly full
- Show a **connection test** and free space for each target. Never block live calls because remote storage is slow.

## Email sending (voicemail, invites, alerts, reports)

- An admin console **Email** page plus a setup-wizard step: "How should the system send email?" Options:
  - **Gmail / Google Workspace:** OAuth sign-in (XOAUTH2); app password as a fallback.
  - **Outlook / Microsoft 365:** OAuth sign-in (Microsoft is phasing out basic SMTP authentication).
  - **Yahoo Mail:** app password.
  - **My own mail server:** host, port, STARTTLS/TLS, username and password, with auto-detect of common settings.
  - **Transactional email service** (recommended for reliability): Amazon SES, Mailgun, SendGrid, Postmark, Brevo, via API or SMTP.
- **Deliverability:**
  - Warn that most home internet connections block outbound port 25, so sending directly from the home lab is not supported.
  - When a custom domain is used, the DNS feature (below) adds SPF, DKIM and DMARC records automatically where the provider allows.
- A **"Send test email"** button and a delivery log with bounces and failures.
- Admin alerts go out when email sending breaks.
- Credentials and OAuth tokens are stored as secrets. Tokens refresh automatically, and the admin is alerted before they expire.
- **Templates:** editable, bilingual (English/Arabic) templates for:
  - voicemail
  - missed calls
  - meeting invites with `.ics`
  - QR enrollment
  - backup reports
  - security alerts

## Domain and DNS management

- A **Domain** step in the setup wizard and a **Domain & DNS** page in the admin console. Two paths (buying or registering domains is out of scope: the user registers a domain themselves at any registrar):
  1. **"I already have a domain":** connect the DNS provider with an API token. Support the common providers through a maintained DNS library (e.g. lego providers or libdns), covering:
     - Cloudflare (default)
     - Route 53
     - DigitalOcean
     - Hetzner
     - Porkbun
     - Namecheap
     - GoDaddy
     - deSEC
     - others the library supports
  2. **"I don't have a domain":** use a free dynamic-DNS subdomain that supports DNS-01 certificates (e.g. deSEC or DuckDNS) so the system still gets trusted certificates. Show its limits clearly.
- **Automatic records:**
  - The system creates and maintains every record it needs: A/AAAA for all hostnames, CAA restricting certificate issuance to the chosen CA(s), and email SPF/DKIM/DMARC when email uses the domain.
  - Show each record before creating it. Never touch records it didn't create: track ownership with a tag or comment.
- **Dynamic IP:** the built-in updater from Deployment profiles:
  - checks the public IPv4/IPv6 every 1–5 minutes via multiple sources
  - updates all owned records and verifies propagation
  - reloads Asterisk/coturn/SFU with the new IP
  - logs every change and alerts the admin
- **Health:**
  - check nameservers, and records that were accidentally orange-clouded or deleted
  - `linx doctor` includes all of these

## Integrations and public API

All integrations are optional, switched on per organisation from an admin console **Integrations** page, with credentials stored as secrets and every call logged in the audit log.

1. **Public REST API and webhooks (foundation):**
   - An OpenAPI-documented REST API covering everything the admin console can do.
   - Scoped, hashed API keys, plus OAuth client credentials. Keys are rate-limited and revocable.
   - Webhooks for these events:
     - call started, answered and ended
     - missed call
     - new voicemail
     - recording ready
     - presence changed
     - meeting started and ended
     - device enrolled or revoked
     - trunk down or up
   - Payloads are HMAC-signed, retried with backoff, and replayable from a delivery log. All other integrations build on this.
2. **Calendar sync (Google Calendar, Microsoft 365):**
   - Presence becomes "In a meeting" during events.
   - Meetings scheduled in the app appear in the user's calendar with the join link.
   - Optional automatic Do Not Disturb during events.
   - Per-user OAuth; read free/busy only unless the user enables event creation.
3. **Contacts sync (Google Contacts, Microsoft 365, CardDAV):**
   - Caller names on incoming calls, contact search in the apps, and a shared company phonebook.
   - Delta sync, and the user chooses which address books to include.
4. **CRM and helpdesk caller lookup:**
   - Incoming calls pop up the matching customer record (web and iOS).
   - Calls, recordings and voicemail are logged against the contact automatically, with click-to-call from the CRM.
   - Templates for HubSpot, Zoho CRM, Odoo, Zendesk and Freshdesk, plus a generic HTTP lookup template for any other system.
   - Lookups are cached, and time out fast so they never delay ringing.
5. **Automation (n8n, Home Assistant, MQTT):**
   - Publish call and presence events to MQTT and webhooks, and accept authenticated commands: originate a call, set DND, play an announcement, open a door relay via a SIP intercom.
   - Document ready-made recipes, e.g.:
     - doorbell/intercom rings a ring group
     - missed-call automation
     - presence on a Home Assistant dashboard
6. **MCP server (AI assistant control):**
   - An MCP server exposing the platform to Claude or other assistants.
   - **Read-only tools** by default: directory, presence, call history, reports, system health, capacity.
   - **Write tools**, only when an admin enables them per API key: create extension, change routing, manage ring groups. Every write requires explicit human confirmation and is recorded in the audit log.
   - Scoped keys and RBAC apply exactly as for the REST API. Recording and transcript access is off unless granted.
7. **Admin alerts:**
   - Send to email plus ntfy, Gotify, Slack, Microsoft Teams, Telegram and generic webhooks. This lets me connect it to my existing Uptime Kuma.
   - Alerts for:
     - trunk down
     - certificate renewal failure
     - disk or storage nearly full
     - backup failure
     - DDNS update failure
     - registration attack or toll-fraud detection
     - capacity limits reached
   - Severity levels, quiet hours, and de-duplication so the same problem doesn't spam.

## Deployment profiles (configurable network setups)

The installer (`linx setup`, interactive wizard plus non-interactive `setup.yaml`) asks a few questions and generates everything: Docker Compose overrides, proxy/router config snippets, Asterisk/coturn/SFU network settings, firewall rules, DNS records, and step-by-step router port-forward instructions. Changing setup later means re-running the wizard. No manual editing of service configs is needed.

### Prerequisites step (first screen of the installer)

- Detect OS, architecture (amd64/arm64), RAM, disk and whether Docker is installed.
- **Docker missing:** ask for permission, then install Docker Engine and the Compose plugin from Docker's official repository (not distro packages). Add the service user to the docker group and verify with a test container.
- **Docker present but too old:** offer an upgrade.
- **Optional container management interface:** offer a choice, defaulting to None:
  - **Portainer CE**
  - **Dockge**
  - **Cockpit** (with its container plugin)
  - **None**

  If one is chosen:
  - install it with a generated admin password
  - bind it to the LAN only, never exposed publicly by default
  - show its address at the end
- Run the capacity benchmark and suggest the resource profile (Lite / Standard / Performance).
- Ask where logs, backups and recordings should be stored (see **External storage**). The default is local.

### Setup questions

1. **Public IP:** auto-detected as static, dynamic, or behind CGNAT. If behind CGNAT, warn that inbound access won't work and suggest profile F (LAN + VPN) or a cloud VPS relay. If dynamic, DDNS uses the connected DNS provider.
2. **Server location:** directly on a public IP, or behind NAT (home or office router).
3. **Existing reverse proxy on 443:**
   - **Auto-detected:** check what listens on 80/443 and inspect running containers for Traefik, Pangolin, nginx, Nginx Proxy Manager, Caddy or HAProxy.
   - Show the finding and the suggested profile, and let the user confirm or override.
   - Options: none, Pangolin, Traefik, nginx, Nginx Proxy Manager, Caddy, HAProxy, "other / I'll configure it myself".
   - **"Other":** generate a plain-language checklist plus example snippets of what the proxy must do.
4. **Port mode:**
   - *443 only* (TCP 443 + UDP 443)
   - *443 TCP only* (UDP blocked: all media over TCP, with a warning about reduced quality)
   - *standard ports* (5061 SIP/TLS, 3478/5349 TURN, RTP range): best performance
5. **Certificates:** ACME DNS-01 (Cloudflare default, other providers via lego/acme.sh), ACME HTTP-01 (only for profiles D/E with port 80 open; not possible for passthrough hostnames), or bring your own certificates.
6. **Domain:** existing domain or free subdomain (see **Domain and DNS management**); then hostnames, defaults are `admin.`, `meet.`, `api.`, `sip.`, `turn.`, `tunnel.`, and `provision.` under one base domain.

### Profiles to implement and test

- **A — Shared 443 behind Pangolin/Traefik, single dynamic IP, behind NAT (my test setup; one of several equal profiles):**
  - Web hostnames are added as normal Pangolin HTTP resources:
    - `meet.` (guest join pages), `api.` and the WSS endpoints must be set to **public**, bypassing Pangolin SSO, or guests and apps can't connect. The platform does its own authentication.
    - `admin.` stays behind Pangolin SSO as an extra layer.
  - The same public-vs-protected split applies to every proxy profile: guest, API and WSS hostnames must be public at the proxy.
  - `sip.`, `turn.`, `tunnel.` use a generated Traefik dynamic-config file with `HostSNI` TCP routers and `tls.passthrough: true` on Pangolin's existing `websecure` entrypoint. The file goes in Pangolin's Traefik dynamic-config directory, survives Pangolin upgrades, and must not break existing Pangolin sites or its auth.
  - UDP 443 is port-forwarded on the router directly to coturn, not through Traefik. Verify HTTP/3 is disabled in Traefik.
  - Assumes no Newt/Gerbil: the PBX VM must be reachable from the Pangolin VM over the LAN.
- **B — Shared 443 behind an existing nginx:** generate an nginx `stream` block with `ssl_preread` SNI mapping, plus `http` server blocks for web hostnames. Same mapping for HAProxy and Caddy (Caddy via layer4 plugin).
- **C — Standalone, 443 only:** the platform brings its own SNI router on 443 (**(recommend)** HAProxy vs Traefik), with PROXY protocol towards services that support it.
- **D — Standalone, standard ports:** each service listens on its native port with no SNI router. This is best for cloud VPS or static-IP deployments.
- **E — Cloud VPS with static IP:** C or D without DDNS or NAT handling. This is a future option for moving the server off the home lab.
- **F — LAN only (simplest home setup):**
  - no port forwarding, no public exposure
  - DNS records point to the server's LAN IP, with trusted certificates still issued via DNS-01 (own domain or free subdomain)
  - phones and apps work at home, and remotely only through the user's own VPN (WireGuard, Tailscale, UniFi Teleport, etc.)
  - push-wake still works over the internet, because Apple's push service reaches the phone directly
- **G — Behind a proxy that can't do TLS passthrough** (e.g. Nginx Proxy Manager, or Cloudflare Tunnel for web). The wizard offers either:
  - (a) web traffic through that proxy and SIP/TURN/tunnel on dedicated ports, or
  - (b) the platform's own SNI router takes 443 and forwards the other web sites to the existing proxy.

  Explain that Cloudflare Tunnel and Cloudflare's orange-cloud proxy can't carry SIP or media, and never route those through them.
- Every profile can be changed later by re-running the wizard, with a preview of what will change and an automatic config backup first.

### Dynamic IP handling (all profiles with a dynamic IP)

- A DDNS updater container updates all hostnames with a low TTL (60–120 s). Records for `sip.`, `turn.`, `tunnel.` must never be proxied by a CDN (Cloudflare: grey cloud).
- An IP-watcher service detects public IP changes. It then rewrites Asterisk `external_signaling_address`/`external_media_address`, coturn `external-ip`, and the SFU's advertised IP, and reloads them without restarting the whole stack.
- Clients detect server IP changes and reconnect automatically. Push-wake still works because it doesn't depend on the server IP.
- Warn in the wizard that IP-authenticated SIP trunks are incompatible with a dynamic IP, and default trunks to registration mode.

### NAT and media rules

- When behind NAT with 443-only ports, route all remote media through TURN (coturn) so no RTP port ranges need forwarding. Asterisk and the SFU reach coturn over the LAN.
- Generate the exact list of router port forwards for the selected profile (e.g. profile A: 443/TCP → Pangolin VM, 443/UDP → PBX VM coturn).

### Real client IP and abuse protection per profile

- Where PROXY protocol reaches the service (tunnel gateway, web services via `X-Forwarded-For`), enforce per-IP rate limits and bans.
- Asterisk and coturn don't support PROXY protocol, so on passthrough profiles:
  - native clients connect via the tunnel gateway, and web clients via WSS;
  - direct SIP/TLS on `sip.` is restricted to allowlisted trunks and desk phones;
  - TURN relies on time-limited credentials and quotas, not IP bans.

### `linx doctor`

A diagnostics command that validates the chosen profile end to end:

- DNS records match the current public IP.
- Certificates are valid for every hostname.
- SNI routing reaches each backend.
- UDP 443 reaches coturn from outside (via an external check or a paired client).
- Existing proxy sites are still reachable.
- Push services (APNs and FCM) are reachable.
- There is no double NAT.

It prints clear pass/fail results with a fix for each failure.

## Automatic certificate lifecycle (zero manual steps)

All certificates are issued, renewed, deployed, and monitored automatically from the moment `linx setup` finishes. No certificate should ever need a manual step or expire unnoticed.

### 1. Public certificates (what clients and browsers see)

- A dedicated `cert-manager` container (**(recommend)** lego vs acme.sh vs certbot) issues from Let's Encrypt, with ZeroSSL as an automatic fallback CA if Let's Encrypt fails or rate-limits.
- **DNS-01 is the default and required for passthrough profiles.** When an existing proxy terminates the web hostnames (e.g. Pangolin's Traefik in profile A), it issues certificates only for those. The passthrough hostnames (`sip.`, `turn.`, `tunnel.`) never reach Traefik's ACME, so the platform must issue them itself. Use a single wildcard certificate (`*.base-domain`) or a SAN certificate for the platform's hostnames, as selected in the wizard.
- DNS provider credentials: the least-privilege token the provider offers (Cloudflare: `Zone:DNS:Edit` on the one zone only), stored as a Docker secret, never in `.env` or logs.
- **Renewal:** check daily and renew when 30 days or less remain, with retries and exponential backoff. Always test against the Let's Encrypt **staging** environment first on fresh installs and in CI, to avoid production rate limits.
- **Deployment after renewal:** atomically write certificates to a shared read-only volume, then hot-reload each consumer and verify it is serving the new certificate:
  - Asterisk: PJSIP TLS/WSS transports
  - coturn
  - LiveKit/SFU
  - tunnel gateway
  - web services

  Where a component can't hot-reload, do a graceful restart that waits for active calls to end, or schedule it at a low-traffic time within the renewal window. Document the reload method per component.
- **Pinning safety:** clients pin the chosen CA's **root** keys (e.g. ISRG Root X1 and X2) plus the fallback CA's roots, never the leaf or intermediates, because Let's Encrypt rotates intermediates without notice. Renewals and intermediate changes must never break pinned clients. Pin-set updates are delivered through the authenticated config channel before any CA change.

### 2. Internal PKI (service-to-service mTLS)

- step-ca runs as the internal CA with an offline root and an online intermediate. The root key is generated at install, exported encrypted for offline backup, and removed from the server.
- Every internal service gets short-lived certificates (24 h) through step-ca's ACME or provisioner, renewed automatically at two-thirds of their lifetime by a sidecar or renewer, with hot reload.
- Intermediate rotation is documented and scripted.

### 3. Device (client) certificates

- Issued automatically at QR/email/login enrollment. The keypair is generated on-device and never leaves it; only a CSR is sent.
- Lifetime equals the inactivity window (default 7 days). The certificate is renewed silently in the background while the user keeps interacting with the app, or always for users exempted from inactivity expiry. This implements the **7-day inactivity rule** naturally: a device with no interaction stops renewing, its certificate expires, and it is signed out.
- Revocation takes effect immediately at the tunnel gateway (deny-list checked on connect and pushed to active sessions), in addition to expiry.

### Monitoring and alerts

- Export expiry metrics for every public, internal, and device CA certificate to Prometheus. Alert at 21 days and 7 days before expiry, and on any renewal failure, by email and push to admins.
- The admin console has a **Certificates** page showing each certificate, its issuer, expiry, last renewal result, and a "renew now" button.
- `linx doctor` checks the full chain and expiry for every hostname and warns if any hostname is proxied (orange cloud) where it shouldn't be.

## Security requirements (non-negotiable)

- **Certificates**
  - Public-facing endpoints (web, API, SIP/TLS, WSS, TURN/TLS, tunnel) use **publicly trusted certificates** from Let's Encrypt (ACME DNS-01 via the user's DNS provider with a least-privilege token), auto-renewed, with services reloaded on renewal.
  - Internal service-to-service traffic uses an **internal PKI** (**step-ca** or equivalent) with short-lived certs and automatic rotation — mTLS between every internal component (Asterisk ↔ control plane ↔ SFU ↔ tunnel gateway ↔ DB where supported).
  - Client devices get device-bound certificates at enrollment; revocation via short lifetimes + a revocation list checked by the tunnel gateway.
  - Clients **pin** the server CA/SPKI (delivered via QR) with a documented pin-rotation plan.
- **Transport:** TLS 1.3 preferred (TLS 1.2 minimum with strong ciphers only). SIP over TLS/WSS only — **no UDP/TCP 5060 exposed publicly**. Media always encrypted: **DTLS-SRTP** for WebRTC, SRTP (SDES only over TLS, or DTLS) for native; reject unencrypted media. Plaintext allowed only for LAN desk phones if I explicitly enable it per network.
- **Auth:** strong generated SIP secrets (never shown after creation), device certificates for app clients, OIDC + MFA for portals, signed short-lived tokens for guests and enrollment, API keys scoped and hashed.
- **Abuse protection:** rate limiting and lockout on registration/auth, fail2ban or native equivalent, geo/IP allowlists for admin and trunks, toll-fraud controls (international dialling off by default, per-extension call limits, spend alerts), meeting-link brute-force protection.
- **Secrets:** no secrets in git; `.env` generated by an installer with random values; support Docker secrets / Vault later. Encrypt recordings and voicemail at rest.
- **Hardening:** containers run as non-root, read-only filesystems where possible, minimal images, seccomp defaults, host firewall rules (nftables) generated by the installer, only required ports published.
- **Supply chain:** dependency scanning (e.g. Trivy, `govulncheck`/`npm audit`/`cargo audit`), SBOM, signed container images (cosign), CI pipeline that blocks on high/critical findings.
- **Privacy/audit:** audit log of admin actions, recording consent announcement option, data-retention settings, GDPR-style export/delete per user.
- **Network isolation:**
  - Database, Redis, ARI/AMI, step-ca, metrics and the internal APIs sit on private Docker networks and are never published.
  - The admin console is LAN/VPN-only by default. When exposed publicly, it requires MFA and optionally the existing proxy's SSO in front (e.g. Pangolin, Authentik, Authelia).
- **TURN abuse and SSRF:**
  - coturn uses `denied-peer-ip` for all private, loopback and link-local ranges, except the exact PBX/SFU addresses, so it can't be used to reach the home LAN.
  - TURN credentials are short-lived and quotas are applied per user.
- **Outbound request safety:**
  - Webhooks, the generic CRM lookup, calendar/contacts sync and storage targets block private/internal IP ranges by default, with an explicit admin allowlist for LAN targets such as a NAS or Home Assistant.
  - Timeouts and response-size limits on all outbound requests.
- **Web security:**
  - strict Content Security Policy with no inline scripts
  - HSTS
  - Secure/HttpOnly/SameSite cookies
  - CSRF protection
  - output encoding
  - all external content (WhatsApp/Telegram messages, CRM data, caller names, file uploads) treated as untrusted and sanitised
- **AI/MCP safety:** content returned to AI assistants (messages, notes, transcripts, caller names) is marked as untrusted data so it can't act as instructions. Write tools always need human confirmation.
- **Container management UI:** warn that Portainer/Dockge/Cockpit have root-equivalent access through the Docker socket. Keep them LAN-only with MFA where supported.
- **Updates:**
  - the installer enables automatic OS security updates
  - the admin console shows available platform updates (signed images, verified with cosign) with release notes and a one-click update with automatic backup and rollback
- **Emergency calls and regulation:**
  - The outbound wizard states plainly that emergency calls work only if the connected provider supports them, and shows the same warning in the apps.
  - The README notes that VoIP use is regulated in some countries (e.g. the UAE) and that users must check local rules.
- Produce `docs/THREAT_MODEL.md` (STRIDE) in Phase 0 and update it each phase.

---

## Low-bandwidth and high-latency optimisation (non-negotiable)

Target: usable audio at **24 kbps with 400 ms RTT and 10% packet loss**, and usable video at **150 kbps**. Degrade gracefully instead of dropping the call.

- **Audio:** Opus as the preferred codec everywhere, with adaptive bitrate (6–40 kbps), in-band FEC, DTX, packet-loss concealment, and RED (redundant audio) for WebRTC. Keep G.711/G.722 only for trunks and desk phones. Use an adaptive jitter buffer tuned for high-latency links.
- **Video:** use congestion control (transport-cc / GCC bandwidth estimation, NACK, PLI/FIR). In conferences, use simulcast or SVC (VP9/AV1 SVC where supported, H.264/VP8 simulcast otherwise). The SFU forwards the layer each receiver can handle. Clients adapt resolution and frame rate automatically, and fall back to audio-only when bandwidth drops below a threshold. Video resumes when it recovers. Screen share uses a content-optimised mode: prioritise sharpness, reduce frame rate.
- **Transport selection:** prefer UDP (direct or TURN/UDP) for media because RTP over TCP suffers head-of-line blocking. Use the TCP tunnel only when UDP is blocked. Inside the tunnel, give media streams priority over SIP/directory traffic, and drop stale media frames instead of queueing them.
- **Signalling efficiency:** SIP header compaction where supported, a minimal registration keep-alive interval, and batched presence/directory updates. Directory sync is delta-based, never a full re-download.
- **User controls:** a "low data mode" toggle that caps video and prefers audio. Show a live call-quality indicator (MOS estimate, RTT, loss) during calls, and warn the user before quality becomes unusable.
- **Server placement:** document how to add regional TURN/SFU nodes later so media can take the shortest path.
- **Measurement:** record per-call quality stats (MOS, jitter, loss, RTT, bitrate, transport used) to the CDR and Grafana.

## Incoming calls when the device is locked or the app is killed (release blocker)

- **iOS/iPadOS:** VoIP push via PushKit. On every push, report the call to CallKit immediately (Apple terminates apps that don't). The app then registers or connects the tunnel in the background and answers. The call must ring on the lock screen, with the app force-quit, in Low Power Mode, and in Focus modes that allow calls.
- *(Phase 7, deferred)* **Android:** FCM high-priority data messages, a self-managed `ConnectionService`, a `phoneCall` foreground service type, and a full-screen intent for the incoming-call screen. The call must ring from Doze, app standby, and after the app was swiped away. Handle OEM battery killers (Samsung, Xiaomi, Huawei, Oppo): detect them and guide the user through battery-optimisation exemption during onboarding.
- *(Phase 7, deferred)* **macOS/Windows:** a lightweight background helper keeps the registration or tunnel alive, launches at login, and shows a native incoming-call notification with answer/decline buttons even when the main window is closed.
- **Web:** Web Push with a service worker shows an incoming-call notification when the tab is backgrounded or closed. Document the browser limitations.
- **Server side:** when a call arrives for an extension with sleeping devices, the PBX sends pushes, holds the INVITE while the devices wake and register, rings all devices at the same time, and cancels the push on the other devices when one answers. Track push delivery success and time-to-ring in metrics.
- Add each of these scenarios to `docs/TEST_MATRIX.md` as mandatory pass criteria on real devices.

## Session expiry after 7 days of inactivity

- If a client has had **no user interaction for 7 days**, the server ends its session. The server revokes the device's session token, stops sending it pushes, and marks it "inactive" in the admin console. Interaction means the user opening the app, making or answering a call, or any authenticated user action. Background registrations and push wakes do not count.
- The 7-day window is configurable per organisation and per user, and admins can exempt specific users (for example on-call staff).
- On the client, the app shows a clear "signed out due to inactivity" state and offers a quick re-login or re-scan of the QR code. It must not fail silently.
- Send a warning push/email to the user 24 hours before expiry.
- Note: an expired device cannot ring. Document this clearly in the admin and user guides.

## App store approval readiness (design for first-pass approval)

Build with each store's review rules in mind from Phase 0. Produce `docs/STORE_SUBMISSION.md` with a checklist per store.

- **Apple App Store (iOS, iPadOS, macOS):**
  - Use PushKit only for real incoming calls, always paired with CallKit. Apple rejects apps that misuse it.
  - Include a privacy manifest (`PrivacyInfo.xcprivacy`) declaring the required-reason APIs, with accurate privacy nutrition labels.
  - Every permission gets a clear purpose string: microphone, camera, local network, contacts, notifications.
  - Provide in-app account deletion, a reachable privacy policy, and support URLs.
  - No private APIs. The macOS build is sandboxed with minimal entitlements, hardened runtime, and notarisation.
  - Provide a reviewer demo account and a test extension that answers automatically, so reviewers can place a real call. Include reviewer notes that explain the VoIP and background behaviour.
  - Note that CallKit is not available in China: disable CallKit there or exclude that storefront.
- *(Phase 7, deferred — the Google Play, Microsoft Store and Mac App Store items below apply only when those clients are built.)*
- **Google Play:**
  - Target the latest required API level.
  - Declare the foreground service types (`phoneCall`, `microphone`, `camera`) with justification.
  - Request `USE_FULL_SCREEN_INTENT` only as a calling app, with a runtime fallback if the user revokes it.
  - Complete the Data Safety form accurately and follow the permissions policy (no unnecessary SMS or call-log permissions).
  - Support in-app and web account deletion. Include a reviewer account.
- **Microsoft Store (Windows):** MSIX packaging, code signing, and a declared microphone/camera/background capability.
- **Licensing:** all bundled SDKs must be compatible with app store distribution. GPL SIP engines (Linphone, PJSIP) need a commercial licence or must be avoided. Flag this in `DECISIONS.md`.
- **Automation:** CI builds signed, store-ready packages (Fastlane for iOS and Android). Screenshots, descriptions, and privacy text are generated from templates in the repo.

## Testing

- Unit tests for control plane, provisioning, token logic, tunnel framing.
- Integration tests in Docker: SIPp scenarios (register, call, transfer, hold, queue), WebRTC call via headless browser (Playwright), tunnel end-to-end (SIP + RTP + API through one TCP connection with UDP blocked by iptables).
- Security tests: TLS config scan (e.g. testssl.sh), cert pinning failure cases, expired/revoked device cert rejection, guest-token tampering.
- Mobile: manual test matrix in `docs/TEST_MATRIX.md` covering answer from locked screen, killed app, Wi-Fi→LTE handover mid-call, Bluetooth headset, tunnel-only network.
- Network impairment tests with `tc netem` in CI. Run calls and conferences under each of these profiles and assert minimum MOS and no dropped calls:
  - 3G: 384 kbps, 200 ms RTT, 2% loss
  - poor mobile: 64 kbps, 400 ms RTT, 10% loss
  - satellite: 600 ms RTT
  - burst loss
  - TCP-tunnel-only
- Inactivity expiry test: simulate 7 days of inactivity, then verify the session is revoked, pushes stop, and re-enrolment works.
- Load test targets: 200 concurrent calls, 50 concurrent video participants on one node (document actual results).
- Raspberry Pi 5 test run: install, capacity benchmark, ringing tests and a 24-hour soak test. Document the measured limits.
- Routing wizard tests: generated patterns validated against numbering-plan sample numbers for each supported country. The simulator's result must match real call routing.
- Backup tests: backup to each storage type (NAS, S3, Google Drive via rclone), then restore to a clean machine.

---

## Phases

- **Phase 0 — Design:** ARCHITECTURE, DECISIONS (all "recommend" items), THREAT_MODEL, ROADMAP, repo skeleton, multi-arch CI (amd64 + arm64), installer with Docker/container-UI prerequisites step that provisions certs (Let's Encrypt + step-ca).
- **Phase 1 — Core PBX + web:** public REST API + signed webhooks and admin alerts (these come first, because the admin console and other integrations are built on them), Asterisk + Postgres realtime, control plane, admin portal (extensions, devices, trunks, the inbound/outbound routing and ring group wizards with call simulator, email sending, domain & DNS management), WebRTC web client making audio calls over WSS/TURN-TLS, CDR, voicemail.
- **Phase 2 — Provisioning + tunnel + first native client:** QR enrollment with device certs, tunnel gateway + client tunnel core, iOS/iPadOS app with CallKit/PushKit push wake, audio + 1:1 video.
- **Phase 3 — Conferencing + guests:** SFU integration, meetings with E2EE on by default, guest links/QR/email/`.ics`, lobby, screen share, SIP dial-in, click-to-call links.
- **Phase 4 — 3CX parity (web + iOS):** queues, IVR builder, BLF/presence, park/pickup, recording, chat, directory (CardDAV/LDAPS), desk-phone provisioning, site-to-site tunnel agent.
- **Phase 5 — Hardening & ops:** capacity page and benchmark, Raspberry Pi Lite profile validation, external storage targets and encrypted backups with restore tests, load tests, observability dashboards, backup/restore drills, upgrade path, full security review against THREAT_MODEL, operator runbook.
- **Phase 6 — Migration, channels & intelligence:**
  - 3CX v20 and Grandstream UCM import tools
  - minimum-version enforcement
  - reports and wallboards
  - local Whisper/LLM transcription and summaries
  - WhatsApp (messaging + Business Calling) and Telegram bot integration
  - integrations: calendar and contacts sync, CRM caller lookup, n8n/Home Assistant/MQTT, MCP server

  Migration tools can be pulled earlier if I need to cut over from 3CX sooner.
- **Phase 7 — Additional clients (only on my explicit go-ahead):**
  - Android (FCM + ConnectionService, Google Play submission)
  - macOS and Windows (background helper, desktop auto-update, Mac App Store / Microsoft Store)

  Each platform must pass the same ringing and low-bandwidth release blockers before release.

## Quality bar

The goal is the best self-hosted phone system for homes and small businesses:
- as easy to set up as a consumer app
- as reliable as a carrier service
- secure by default
- no subscription

When a requirement here conflicts with security or reliability, security and reliability win. Flag the conflict to me.

Start now with Phase 0 in plan mode: read this brief, list your blocking questions, and propose the architecture and technology decisions for my approval.
