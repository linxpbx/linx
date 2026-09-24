# Linx — Architecture Decision Records

Format: each ADR has **Status**, **Context**, **Options**, **Decision**, **Consequences**.
Status values: `Proposed` (awaiting owner approval), `Accepted`, `Superseded by ADR-x`.

| # | Topic | Decision | Status |
|---|---|---|---|
| 001 | PBX core | Asterisk 22 LTS (chan_pjsip, ARI, PostgreSQL realtime) | Accepted (owner, 2026-09-23) |
| 002 | Video SFU | LiveKit + LiveKit SIP | Accepted (owner, 2026-09-23) |
| 003 | Control plane / CLI language | Go | Accepted (owner, 2026-09-23) |
| 004 | Web framework + components | React + TypeScript + Vite, shadcn/ui + Radix | Accepted (owner, 2026-09-23) |
| 005 | iOS client stack | Native Swift/SwiftUI | Accepted (owner, 2026-09-23) |
| 006 | iOS SIP/media engine | Google WebRTC (BSD) + Linx SIP-over-WSS user agent | Accepted (owner, 2026-09-23) |
| 007 | Tunnel requirement | WSS + TURN/TLS 443 for this scope; custom tunnel deferred | Accepted (owner, 2026-09-23) |
| 008 | Tunnel protocol (when built) | WebSocket-over-TLS + yamux streams, Go core via gomobile | Accepted (owner, 2026-09-23) |
| 009 | Standalone SNI router (profile C) | HAProxy | Accepted (owner, 2026-09-23) |
| 010 | Public certificate manager | lego (library, embedded in `linx-certd`) | Accepted (owner, 2026-09-23) |
| 011 | Internal PKI | step-ca | Accepted (owner, 2026-09-23) |
| 012 | Guest / enrollment tokens | JWT (EdDSA) + server-side revocation | Accepted (owner, 2026-09-23) |
| 013 | Cache / pub-sub | Valkey (optional on Lite) | Accepted (owner, 2026-09-23) |
| 014 | Licence | Apache-2.0 | Accepted (owner, 2026-09-23) |
| 015 | Product name "Linx" clash check | Proceed with "Linx" in app metadata; flagged risks below | Accepted (owner, 2026-09-23) |
| 016 | Brand colour | Linx Cobalt `#1F5FD6` replaces mockup teal | Accepted (owner, 2026-09-23) |
| 017 | Web SIP library | JsSIP | Accepted (owner, 2026-09-23) |
| 018 | Code hosting / CI | Private GitHub repo, GitHub Actions (amd64 + arm64 runners) | Accepted (owner, 2026-09-23) |
| 019 | Optional container management UI | Portainer CE or none (Dockge and Cockpit dropped) | Accepted (owner, 2026-09-23) |
| 020 | setup.yaml parser (first external Go dependency) | go.yaml.in/yaml/v3 | Accepted (owner, 2026-09-23) |
| 021 | Languages | English only (CLI, server UI, apps); Arabic/RTL dropped | Accepted (owner, 2026-09-23) |
| 022 | Hidden terminal input | golang.org/x/term | Accepted |
| 023 | Unencrypted trunk fallback | Only for provider trunks that can't encrypt, with warning | Accepted (owner, 2026-09-23) |
| 024 | Trunk VPN profiles | WireGuard only, split tunnel, per-trunk Connection | Accepted (owner, 2026-09-23) |
| 025 | API description and server | OpenAPI 3.1 written first; oapi-codegen (Go), openapi-typescript/openapi-fetch (web) | Accepted (owner, 2026-09-23) |
| 026 | Database access and migrations | PostgreSQL 18, pgx v5, built-in forward-only migration runner | Accepted (owner, 2026-09-23) |
| 027 | API authentication | Hashed scoped API keys, OAuth client credentials (EdDSA JWT), x/time/rate | Accepted (owner, 2026-09-23) |
| 028 | Webhooks | Standard Webhooks signing, transactional outbox, retries, SSRF guard | Accepted (owner, 2026-09-23) |
| 029 | Admin alerts | Built-in senders (ntfy, Gotify, Slack, Teams, Telegram, webhook; email later), de-dup, quiet hours | Accepted (owner, 2026-09-23) |
| 030 | Secrets stored in the database | AES-256-GCM, key held as a Docker secret | Accepted (owner, 2026-09-23) |
| 031 | Asterisk image | Built by Linx CI from signed 22 LTS source, minimal modules, non-root | Accepted (owner, 2026-09-24) |
| 032 | Asterisk realtime | ODBC over read-only views in an `asterisk` schema, no cache, registrations kept in Asterisk | Accepted (owner, 2026-09-24) |
| 033 | Device SIP logins | Random 128-bit password shown once, stored as SIP digest hash (`md5_cred`) | Accepted (owner, 2026-09-24) |
| 034 | Call control split | Dialplan rings devices; ARI app (outbound websocket) watches calls; hand-written ARI client + coder/websocket | Accepted (owner, 2026-09-24) |
| 035 | First test phones | LAN-only SIP-TLS + SRTP softphones in Phase 1B; public SIP waits for registration lockout (1C) | Accepted (owner, 2026-09-24) |
| 036 | People accounts and sign-in | Local accounts: Argon2id passwords, TOTP (required for admins), cookie sessions; OIDC/passkeys in 1E | Accepted (owner, 2026-09-24) |
| 037 | One HTTPS entry | Control plane serves the web app, API and `/sip` on one port; web app built into its image | Accepted (owner, 2026-09-24) |
| 038 | Browser SIP path | JsSIP over WSS relayed by the control plane (session-bound, lockout) to Asterisk on an internal network; 5061 stays LAN-only | Accepted (owner, 2026-09-24) |
| 039 | TURN relay | Official coturn image, HMAC credentials (1 h), UDP/TLS 443, relays to Asterisk only | Accepted (owner, 2026-09-24) |
| 040 | Front doors | Every profile supported, NAT-friendly, 443 only; Pangolin and Linx-takes-443 built first | Accepted (owner, 2026-09-24) |
| 041 | Opus transcoding | Wazo's open-source `codec_opus` built into the Asterisk image; G.722 fallback | Accepted (owner, 2026-09-24) |

---

## ADR-001 — PBX core: Asterisk 22 LTS

**Context.** Need extensions, queues, IVR, voicemail, recording, trunks, park/pickup, BLF, WebRTC endpoints, and programmable call control (push-wake "hold INVITE").

**Options.**
- **Asterisk 22 LTS**: chan_pjsip has native WebRTC (WSS, DTLS-SRTP, ICE). ARI gives full call control over REST + WebSocket. Realtime config from PostgreSQL. Queues, voicemail, MOH, parking and BLF are mature. LTS support runs to about 2028 with security fixes to about 2029.
- **FreeSWITCH**: strong media and conferencing. The event socket works well. However, the 1.10 community edition's release cadence depends on SignalWire, and WebRTC/queue features are split across modules.

**Decision.** Asterisk 22 LTS. Configuration comes from PostgreSQL through realtime tables (endpoints, AORs, auths). The control-plane ARI application ("Stasis app") owns advanced logic: push-wake, forking, recording control and park-slot presence. Plain dialplan is used only for simple routing that the control plane generates.

**Consequences.** Asterisk is GPLv2. It runs as a separate container and process, and Linx talks to it only over network protocols (SIP/ARI/AMI), so the Apache-2.0 licence for Linx code is unaffected. Asterisk doesn't support PROXY protocol, so real client IPs are handled per profile (see ARCHITECTURE §Ingress).

## ADR-002 — Video SFU: LiveKit + LiveKit SIP

**Options.**
- **LiveKit**: Apache-2.0, written in Go. Supports simulcast and SVC (VP9/AV1), E2EE via insertable streams (built into its client SDKs), JWT access tokens, egress recording, and a SIP bridge (`livekit-sip`) for dial-in. It also has a native Swift SDK.
- **Jitsi**: mature, but it's a heavier stack (JVB, Jicofo, Prosody). E2EE is its own design, and SIP dial-in (Jigasi) is less maintained.
- **Asterisk ConfBridge SFU**: limited. There is no simulcast layer selection, lobby or E2EE.

**Decision.** LiveKit. Lobby/waiting room, guest admission and PIN checks live in the control plane, which only mints a LiveKit token after admission. E2EE is on by default through LiveKit's frame encryption. The control plane manages per-meeting keys and rotates them when participants leave.

**Consequences.** LiveKit uses its own TURN or an external one. We point it at the shared coturn so there is a single 443/UDP listener. Recording and dial-in are disabled automatically for E2EE meetings.

## ADR-003 — Control plane, installer and CLI: Go

**Options.** Go vs TypeScript (NestJS).

**Decision.** Go (latest stable, pinned in `go.mod` and CI).
- It builds a single static binary for amd64 and arm64, with small images and a light memory footprint that suits Raspberry Pi Lite.
- It is the same language as the future tunnel core and gateway (ADR-008), as `lego` (ADR-010) and step-ca's client libraries, and as LiveKit's server SDK.
- Its standard library has strong crypto and TLS 1.3.
- The installer `linx` is the same codebase, so there's no extra runtime on the host.

**Consequences.** The web frontend stays TypeScript. The API contract is OpenAPI 3.1, generated from Go types, with TypeScript and Swift clients generated from it.

## ADR-004 — Web: React + TypeScript + Vite, shadcn/ui + Radix

**Options.** React vs Svelte; shadcn/ui + Radix vs Mantine.

**Decision.** React + TypeScript + Vite, with shadcn/ui (Radix primitives + Tailwind).
- React has the largest ecosystem for WebRTC, PWA and i18n/RTL (e.g. `livekit-client` + `@livekit/components-react`).
- Radix primitives are WAI-ARIA compliant.
- shadcn components are copied into the repo, so we control them fully and can apply the design tokens directly.

**Guest-page budget (under 300 KB).** The guest join page is a separate Vite entry with code-splitting. It skips the admin component bundle and uses a system-font fallback until the fonts load.

## ADR-005 — iOS client: native Swift/SwiftUI

Decision: native Swift 6 / SwiftUI universal app (iPhone + iPad). The reasons are PushKit/CallKit reliability, Apple HIG compliance and first-pass App Store review. With a single native platform in scope, Flutter offers no benefit.

## ADR-006 — iOS SIP/media engine

**Criteria** (from the brief): video, SRTP/DTLS, Opus/H.264/VP8, stability when answering from lock or killed state, App Store licensing, poor-network behaviour, and meeting the tunnel requirement.

| Option | Video | DTLS-SRTP | Codecs | Licence for App Store | Poor network | 443-only |
|---|---|---|---|---|---|---|
| **Google WebRTC (libwebrtc) + SIP over WSS** | Yes | Yes | Opus, H.264, VP8/VP9 | BSD ✅ | Best-in-class (GCC, NACK, FEC, RED, NetEQ) | WSS + TURN/TLS 443 ✅ |
| Linphone SDK | Yes | Yes | Yes | GPLv3 or paid commercial ❌ | Good | Needs custom tunnel (paid) |
| PJSIP | Yes | Yes | Yes | GPLv2 or paid commercial ❌ | Fair | Needs custom tunnel |

**Decision.** Google WebRTC (prebuilt, pinned XCFramework, BSD licence) plus a small Linx SIP-over-WSS user agent written in Swift. The user agent covers REGISTER, INVITE/re-INVITE, BYE/CANCEL, REFER, NOTIFY/SUBSCRIBE (dialog/BLF) and INFO/DTMF. Web and iOS use the **same media stack as the browser**, so Asterisk sees identical WebRTC endpoints. For meetings, iOS uses the LiveKit Swift SDK, which is also built on libwebrtc.

**Why a small custom SIP user agent is acceptable.** There's no maintained, App-Store-compatible (non-GPL) native SIP stack for Swift. The SIP subset we need is small, and it is signalling only (no media). It is covered by unit tests and SIPp interop tests.

**Stability (lesson from the previous attempt).**
- There is no Kamailio/rtpengine layer. The media path is iOS libwebrtc ↔ (coturn if needed) ↔ Asterisk.
- The CallKit audio session is activated only in `provider(_:didActivate:)`, and WebRTC's `RTCAudioSession` uses manual audio.
- "Answer from lock/killed state" is a release blocker in `docs/TEST_MATRIX.md`.

## ADR-007 — Is a custom tunnel needed for this scope?

**Requirement.** Clients must work where only outbound TCP 443 is allowed, carrying signalling, media and directory/API traffic.

**Analysis.** With WebRTC on both clients:
- Signalling uses SIP over **WSS on 443**.
- API, directory and presence use **HTTPS/WSS on 443**.
- Media uses ICE to try UDP direct first, then TURN/UDP 443, then **TURN over TLS on TCP 443**.
- All three look like ordinary HTTPS to firewalls. TURN/TLS on 443 also works through most restrictive networks.

The gaps compared with a custom tunnel are:
1. **Explicit HTTP CONNECT proxies.** Browsers handle these for WSS/TURN automatically. iOS libwebrtc does not proxy TURN/TLS.
2. **One connection instead of three.** Both approaches are encrypted, so this isn't a security gap.
3. **mTLS with device certificates.** Device authentication moves to the WSS layer: the device certificate is presented on the WSS connection to the edge, with SNI routed to the control-plane edge, which verifies the certificate and the deny-list.

**Decision (owner-approved).** Use WSS + TURN/TLS 443 for Web and iOS in Phases 1–4. The tunnel gateway and client tunnel are **deferred**. Their interfaces (config keys `transport.mode = auto|direct|tunnel`, and the hostname `tunnel.`) are reserved now so adding them later needs no server redesign.

**Trigger for building the tunnel.** Any `TEST_MATRIX` "tunnel-only network" case fails on iOS (e.g. an HTTP-proxy-only network), or the site-to-site agent for desk phones is scheduled (Phase 4).

## ADR-008 — Tunnel protocol (when built)

**Options.** Custom framing over TLS; WebSocket-over-TLS; HTTP/2 streams; yamux/smux over TLS.

**Decision.** WebSocket over TLS 1.3 (with mTLS and device certificates) carrying yamux streams. The core is in Go, exposed to iOS through gomobile and served by a Go gateway.
- WebSocket traverses HTTP CONNECT proxies and passes most deep-packet inspection.
- yamux is a mature library with flow control.
- Media streams get priority queues, and stale frames are dropped.
- `TCP_NODELAY` and small send buffers are set.

## ADR-009 — SNI router for profile C (standalone 443): HAProxy

**Options.** HAProxy vs Traefik.

**Decision.** HAProxy (LTS branch, pinned). Its TCP-mode `ssl_preread`/`req.ssl_sni` routing is mature, it can **send** PROXY protocol v2, its memory use is very low (good for a Pi), and its config is simple to generate from a template. Traefik is used only where it already exists (Pangolin, profile A).

**Consequences.** HAProxy is GPLv2 and runs as a separate container, with no linking. Profiles B and G reuse the same SNI map generator.

## ADR-010 — Public certificates: lego

**Options.** lego, acme.sh, certbot.

**Decision.** lego, embedded as a **Go library** in a small `linx-certd` service.
- It supports 150+ DNS providers (Cloudflare, Route 53, DigitalOcean, Hetzner, Porkbun, Namecheap, GoDaddy, deSEC, DuckDNS …), and the same library powers the Domain & DNS page.
- It supports external account binding (EAB) for the ZeroSSL fallback.
- It's MIT-licensed and actively maintained.

`linx-certd` handles:
- Let's Encrypt staging first on fresh installs and in CI
- daily renewal checks (at 30 days or less remaining) with exponential backoff
- ZeroSSL fallback
- atomic writes to a read-only shared volume
- reload hooks for each consumer
- Prometheus expiry metrics

**Implementation notes (Phase 0b, owner, 2026-09-23).**
- DNS providers for now: **Cloudflare and DuckDNS**. deSEC is deferred: lego's deSEC client pulls in MPL-2.0 modules (`nrdcg/desec`, `hashicorp/go-retryablehttp`, `go-cleanhttp`), which the licence allowlist rejects. More providers arrive with the Domain & DNS page, and each one's module licences are checked first.
- Default certificate: one wildcard `*.<domain>`. It keeps the Linx hostnames out of Certificate Transparency logs. A named certificate (admin, api, meet, provision, sip, turn) is an option. `tunnel.` isn't issued until ADR-008 is built.
- Certificate keys are ECDSA P-256. ACME accounts are kept per CA in a volume only certd can read.
- Staging mode uses Let's Encrypt staging only. ZeroSSL has no staging environment, so the fallback only applies in production. ZeroSSL's EAB credentials are fetched with the ACME contact email, so production needs an email.
- DNS-01 check (after the live staging tests, 2026-09-23): certd checks only the domain's own authoritative name servers (lego's default), for up to 10 minutes, then waits 90 seconds before asking the CA to validate. The first test failed because Let's Encrypt reached a Cloudflare location that didn't have the record yet, 7 seconds after the check passed. Public resolvers are never queried. Querying them seconds after creating the record made Quad9 remember "no such record" for the zone's 30-minute negative TTL, which stalled the second test.

## ADR-011 — Internal PKI: step-ca

Decision: Smallstep step-ca (Apache-2.0).
- **Root.** Generated at install, exported encrypted for offline backup, then removed from the server.
- **Intermediate.** Online.
- **Service certificates.** 24 h lifetime, renewed at two-thirds of their lifetime by `step ca renew --daemon` sidecars (or natively in Go services).

step-ca also issues **device certificates**. It uses a dedicated provisioner that only the control plane can use after enrollment checks, and the certificate lifetime equals the inactivity window (default 7 days).

**Implementation notes (Phase 0b, 2026-09-23).**
- Image `smallstep/step-ca:0.30.2`, pinned by digest. `linx setup` bootstraps it once, inside a throwaway container with no network. Re-running setup never replaces an existing CA.
- Root and intermediate keys are ECDSA P-256, valid 10 years. The root key is created already encrypted with a generated passphrase (six groups of four characters), shown to the owner once and never saved. Its only copy goes to `/etc/linx/ca-backup` for the owner to take off the server.
- Provisioners are JWK: `linx-services` (24 h max and default) and `linx-devices` (7 d max and default). Each has its own password as a Docker secret. Remote management (the admin API) is off, so provisioners can only change through `ca.json`.
- The CA listens on `https://linx-step-ca:9000` on `linx-private` only.

## ADR-012 — Guest and enrollment tokens: JWT (EdDSA)

**Options.** JWT vs PASETO v4.

**Decision.** JWT signed with Ed25519 (`alg: EdDSA`), with strict algorithm pinning (no `none`, no algorithm negotiation). LiveKit access tokens are already JWTs, so this keeps one validation stack.

Every token has:
- `jti`, `exp` and `nbf`
- `aud` scoped to one room, extension or enrollment
- a single-use flag for enrollment tokens

The server keeps a revocation/used-`jti` table. PINs are checked server-side with attempt limits.

## ADR-013 — Cache/pub-sub: Valkey

Redis 7.4+ is no longer under a BSD licence. Valkey (BSD, Linux Foundation) is a drop-in replacement. It's optional on the Lite profile, where the control plane falls back to in-process pub-sub.

## ADR-014 — Licence: Apache-2.0 (owner decision)

It is permissive and App-Store-compatible, and it includes a patent grant. The GPL components (Asterisk, HAProxy) run as separate processes that talk over the network, with no linking, so there's no licence conflict. Every bundled iOS/web dependency must be permissively licensed (MIT/BSD/Apache/ISC). CI enforces this with a licence allowlist check on the SBOM.

## ADR-015 — Name "Linx": clash check

Findings (web search, 2026-09-23):
- **Ooma Linx** is a wireless VoIP phone-extension *hardware* device sold by Ooma. This is the closest overlap: same field (VoIP telephony) and same name.
- **LINX Communications, Inc.** has held US trademarks for telecom services (LINX, LINXOFFICE, LINXCONNECT) since 1983.
- **Linx Networks** sells trader voice/UC solutions.
- **FactoryTalk Linx** (Rockwell) is industrial communications software.
- **LINX**, reg. #5987193 (Linx Global MFG), is registered under computer and software services.

**Risk.** A **moderate** trademark risk in the telecom category (class 9 software / class 38 telecom), especially in the US. App Store name collisions are handled by using a distinct listing name.

**Recommendation.**
1. Use the App Store listing name **"Linx PBX"** or **"Linx Phone"**. Keep the in-app display name "Linx".
2. Before Phase 2 (first App Store submission), get a quick trademark search in your main markets (UAE, and US/EU if you plan to distribute there) from a trademark professional. I can't give legal advice.
3. Keep the bundle ID `com.linxpbx.app` and the domain linxpbx.com, which are distinctive.

## ADR-016 — Brand colour (owner decision)

Linx Cobalt `#1F5FD6` is the primary/accent colour and replaces the mockup teal. Call green `#1E8E4E` is reserved for answer/available and End red `#C53030` for hang-up and alerts. On dark surfaces, Signal `#7FB0FF` is used for accent text and links to keep WCAG AA contrast. The X logo from `docs/ui/A · Meeting point@1x.png` is the logo (vector source needed before release).

## ADR-017 — Web SIP library: JsSIP

**Options.** JsSIP (MIT, actively maintained) vs SIP.js (MIT, slower release cadence recently).

**Decision.** JsSIP, pinned. It is used with Asterisk WSS, and its maintenance status will be re-verified at the start of Phase 1. The iOS Swift user agent (ADR-006) mirrors the same SIP feature subset so both clients behave identically.

## ADR-018 — Hosting and CI (owner decision)

Private GitHub repository with GitHub Actions. Multi-arch builds run on native `ubuntu-24.04` (amd64) and `ubuntu-24.04-arm` (arm64) runners, not QEMU emulation. All actions are pinned by commit SHA.

## ADR-019 — Optional container management UI (owner decision)

**Context.** The brief offered Portainer CE, Dockge or Cockpit during setup, each LAN-only with a generated admin password.

**Findings.** Dockge serves its login over plain HTTP and can't be given an admin password in advance, so whoever opens it first becomes admin. Cockpit's container add-on (cockpit-podman) manages Podman, not Docker, so it can't see Linx containers.

**Decision.** Setup offers **Portainer CE** or **None** (default None). Portainer runs from a generated Compose file, image pinned by digest, bound to the server's private LAN address only (127.0.0.1 plus SSH-tunnel instructions when the server has no private address). The admin password is generated, kept in a root-only file and mounted as a Compose secret.

**Consequences.** Portainer mounts the Docker socket, so it's root-equivalent. Setup says so, and never binds it to a public address.

## ADR-020 — setup.yaml parser: go.yaml.in/yaml/v3

**Context.** `linx setup --config setup.yaml` needs a YAML parser. The standard library has none.

**Options.** `go.yaml.in/yaml/v3` (MIT + Apache-2.0, maintained by the YAML organisation; continuation of the archived `gopkg.in/yaml.v3`); `sigs.k8s.io/yaml` (wraps the same parser, adds a JSON step); `go.yaml.in/yaml/v4` (still release candidates).

**Decision.** `go.yaml.in/yaml/v3`, pinned. Unknown keys are rejected so typos don't pass silently.

**Consequences.** It's the first external Go dependency, so `make security` now checks Go module licences too (`tools/licensecheck/gomod.go`).

## ADR-021 — Languages: English only (owner decision)

**Decision.** The `linx` CLI, the web/admin UI and the iOS app ship in English only. The brief's Arabic translation and right-to-left layout requirement is dropped.

**Consequences.** No i18n string catalogues or RTL layout work in this scope. User-facing copy still stays plain-language and in one place per client, so adding languages later is a refactor, not a rewrite.

## ADR-022 — Hidden terminal input: golang.org/x/term

**Context.** `linx setup` asks for the DNS provider's API token. It must not appear on screen or in terminal scrollback.

**Options.** `golang.org/x/term` (BSD-3-Clause, maintained by the Go team); calling `stty -echo` from Go (fragile, leaves the terminal broken if setup is killed).

**Decision.** `golang.org/x/term`, pinned. It restores the terminal on return.

**Consequences.** The token never touches `setup.yaml` or `.env`; in `--config` mode it must already be saved in `/etc/linx/secrets/linx_dns_token`.

## ADR-023 — Unencrypted trunk calls as a fallback (owner decision, 2026-09-23)

**Context.** Many phone providers (ITSPs) and gateways don't support SIP over TLS or SRTP. The original rule ("SIP only over TLS/WSS, reject unencrypted media") would stop Linx connecting to them.

**Decision.** Unencrypted SIP and RTP are allowed **only for provider trunks, and only when the provider doesn't support encryption**.
- The trunk wizard always tries TLS + SRTP first. It offers unencrypted only after that test fails, and the admin must confirm a plain-language warning ("Calls to and from this provider can be listened to on the way"). The trunk list shows these trunks as unencrypted.
- A trunk using a WireGuard profile (ADR-024) is encrypted by the tunnel, so the wizard doesn't warn.
- Apps, web, meetings, guests and device enrollment stay encrypted always, with no fallback. LAN desk phones keep the existing rule (plaintext only on explicitly enabled LAN networks).
- Port 5060 is never public: registration trunks are outbound only; IP-auth trunks open 5060 only to the provider's addresses (nftables), or only on the WireGuard interface.

**Consequences.** Admins can connect any provider. The threat model records the eavesdropping risk on unencrypted trunks; `linx doctor` lists them.

## ADR-024 — Trunk VPN profiles: WireGuard only (owner decision, 2026-09-23)

**Context.** Some providers offer trunks over a VPN, and a VPN makes an unencrypted trunk safe to use.

**Decision.** Linx supports **WireGuard only** (no OpenVPN or IPsec).
- Admins can add several WireGuard profiles (import the provider's `.conf` or fill in the fields). Private keys are secrets, never shown again after saving.
- Each SIP trunk has a **Connection** drop-down: "Internet (this server's network)" or one of the WireGuard profiles.
- **Split tunnel only.** A profile carries only the traffic of its trunks, to the provider's addresses. A profile that routes everything (`AllowedIPs = 0.0.0.0/0` or `::/0`) is narrowed to the addresses of its trunks, with a note to the admin. If a VPN drops, only its trunks go down; apps, meetings and updates keep working.
- Kernel WireGuard (in Ubuntu 24.04), managed with `wgctrl-go` (MIT; licence checked when added). Creating interfaces needs `NET_ADMIN`: that goes to one small dedicated service, never to the control plane or Asterisk.

**Consequences.** Built with trunks in Phase 1. Health checks show each profile's last handshake; a trunk on a VPN with no recent handshake is flagged.

## ADR-025 — API description and server: OpenAPI 3.1 written first (owner chose the approach, 2026-09-23)

**Context.** The admin portal, web client, CLI and all integrations use one public REST API (brief: "OpenAPI-documented, covering everything the admin console can do"). Design: `docs/API.md`.

**Options.** (a) Spec first: hand-write `api/openapi.yaml`, generate Go server interfaces and the TypeScript client. (b) Code first: Go handlers produce the spec (Huma, MIT). (c) Hand-written both sides.

**Decision.** (a).
- Go: `oapi-codegen` v2 (Apache-2.0) strict server for `net/http`, standard library `ServeMux`, no web framework. Runtime helper `github.com/oapi-codegen/runtime` (Apache-2.0).
- Request validation against the spec: `oapi-codegen/nethttp-middleware` + `kin-openapi` (MIT).
- Web: `openapi-typescript` + `openapi-fetch` (MIT).
- `make api` regenerates; lint fails on stale generated code. All pinned; the generator is pinned as a Go tool dependency.

**Consequences.** Spec and code can't drift, and the spec is reviewable in plain YAML. Each endpoint needs a spec edit before code. First external Go dependencies beyond ADR-020/022: the Go licence check covers them.

## ADR-026 — Database access and migrations

**Context.** Phase 1 needs PostgreSQL (ADR-001 realtime views, API data).

**Options.** Drivers: `pgx` v5 (MIT, most used, native Postgres features) vs `lib/pq` (maintenance mode). Migrations: `goose` (MIT; its go.mod pulls many database drivers), `golang-migrate` (MIT; same issue), or a small built-in runner.

**Decision.** PostgreSQL 18 image pinned by digest, on `linx-private` only. `pgx` v5 with `pgxpool`; plain SQL, no ORM. Migrations: a built-in runner (~100 lines) over SQL files embedded in the binary — forward-only, each in one transaction, a `schema_migrations` table, a Postgres advisory lock. Runs when the control plane starts; `linx doctor` reports the schema version.

**Consequences.** One small dependency. No down-migrations: a bad migration is fixed with a new one; backups (Phase 1 later) cover disasters.

## ADR-027 — API authentication

**Context.** Brief: scoped, hashed, rate-limited, revocable API keys plus OAuth client credentials.

**Decision.** (Details in `docs/API.md` §3.)
- API keys `linx_<id>_<secret>`, 256-bit secret, stored as SHA-256 (the secret is random, so a slow password hash adds nothing), shown once, default expiry 1 year, optional IP allowlist, scopes capped by the creator's role.
- OAuth client credentials issue 15-minute EdDSA JWTs (alg pinned, `aud=linx-api`, `jti` revocable, ADR-012). No refresh tokens.
- Portal sessions (OIDC/local + MFA, later in Phase 1) use `HttpOnly; Secure; SameSite=Strict` cookies plus a CSRF header.
- Rate limits with `golang.org/x/time/rate` (BSD-3, Go team), in memory now.
- First key via `linx api-key create` on the server.
- Libraries (step 3): `github.com/go-jose/go-jose/v4` (Apache-2.0; already in the module graph via lego) signs and verifies JWTs, with EdDSA passed as the only allowed algorithm on every parse; `github.com/google/uuid` (BSD-3) for UUIDv7. Scopes are declared per operation in `api/openapi.yaml` and enforced through kin-openapi's `AuthenticationFunc`, so the spec is the single source.

**Consequences.** A leaked database gives no usable API keys. Rate limits are per instance until a Valkey limiter is added for multi-node.

## ADR-028 — Webhooks: Standard Webhooks (owner chose the format, 2026-09-23)

**Context.** Brief: HMAC-signed payloads, retries with backoff, replay from a delivery log; outbound HTTP blocks private IPs unless allowlisted.

**Options.** Standard Webhooks spec; a custom `X-Linx-Signature` header.

**Decision.** Standard Webhooks (`webhook-id`, `webhook-timestamp`, `webhook-signature`, HMAC-SHA256, `whsec_` secrets), implemented with the standard library (no SDK needed). Transactional outbox + `SKIP LOCKED` worker; retry schedule ≈ 27 h; 5 days of failures disables the endpoint and alerts. Shared SSRF-guarded HTTP client: HTTPS only, including allowlisted LAN targets (owner decision: no `http://` exception), resolve-check-then-dial the checked address, private/metadata ranges blocked unless allowlisted.

**Consequences.** Receivers verify with off-the-shelf libraries. Delivery is at-least-once and unordered; receivers de-duplicate on `webhook-id`.

**Built (step 4, 2026-09-23).** No new dependencies. Signing checked against the Standard Webhooks reference test vector. SSRF guard in `internal/safehttp`: any refused address refuses the whole name; loopback, metadata, multicast and the container's own networks can't be allowlisted; allowlist entries must lie inside one private range. `outbound_allowlist:write` became a sensitive scope.

## ADR-029 — Admin alerts (owner chose the channels, 2026-09-23)

**Options.** Shoutrrr (MIT; covers most services, but maintenance has moved between forks and it has its own HTTP client, bypassing our SSRF guard) vs built-in senders.

**Decision.** Built-in senders for ntfy, Gotify, Slack, Microsoft Teams (Workflows webhook), Telegram and generic webhook, each a single HTTPS POST through the SSRF-guarded client. Email joins when email sending is built. Severity levels, per-channel minimum severity, quiet hours (critical bypasses by default), de-duplication by alert key with 24 h reminders, flap hold-back of 5 minutes, "resolved" messages.

**Consequences.** No new dependency; each sender is small and tested against a fake server. New channels are a small file each.

**Built (step 5, 2026-09-23).** No new dependencies. Channel settings sealed as one JSON object per channel (ADR-030), same as a webhook secret; a `webhook` channel's signing secret is Linx-generated, like a webhook endpoint's. `alert.fired`/`alert.resolved` also emitted as webhook events, in the transaction that marks the alert notified. Certificate renewal failure wired up by polling `linx-certd`'s existing `/metrics` over `linx-private` (not the SSRF-guarded client: that guard is for admin-supplied URLs, not Linx's own services, and would in fact refuse `linx-private`). Disk/storage and DDNS update failure deferred: the control plane has no filesystem of its own to check and no DDNS-updater feature exists yet (`docs/THREAT_MODEL.md`).

## ADR-030 — Secrets stored in the database

**Context.** Some secrets must be usable again, not just checked: webhook signing secrets, Telegram bot tokens, Slack/Teams URLs with tokens inside, later SMTP and OAuth tokens.

**Decision.** Encrypt them with AES-256-GCM (Go standard library), random nonce, the row id as additional data (a ciphertext can't be moved to another row). The 32-byte key is a Docker secret `linx_db_encryption_key`, generated by the installer, never stored in the database. Key id is stored with each ciphertext so the key can be rotated later.

**Consequences.** A stolen database dump alone doesn't reveal these secrets. Losing the key file loses them: backups (later in Phase 1) must include it, stored separately from the database backup.

## ADR-031 — Asterisk image

**Context.** ADR-001 chose Asterisk 22 LTS. Asterisk publishes source releases, not an official Docker image. Design: `docs/PBX.md` §2.

**Options.** (a) A community image (e.g. `andrius/asterisk`): quick, but one maintainer, unknown patch speed and module set. (b) Build our own from the release tarball.

**Decision.** (b). Multi-stage Dockerfile: `debian:bookworm-slim` pinned by digest, Asterisk 22.x (≥ 22.4, for ARI outbound websockets) tarball verified by GPG signature and pinned SHA-256, `menuselect` limited to the modules Linx uses, runtime stage without compilers. Non-root `asterisk` user, read-only root filesystem, capabilities dropped. Built, scanned (Trivy) and cosign-signed in CI like the other images; Dependabot can't bump it, so a scheduled CI job checks for new 22.x releases and opens an issue.

**Consequences.** We own security updates: a new Asterisk security release means a Dockerfile bump (the check makes it visible within a day). Asterisk stays GPLv2 in its own container (ADR-001). Builds take several minutes; CI caches the build stage.

## ADR-032 — Asterisk realtime

**Context.** Asterisk must read extensions and devices from PostgreSQL (ADR-001), through views the control plane owns (ARCHITECTURE §7).

**Options.** Driver: `res_config_pgsql` (native, but "extended" support level, single connection) vs ODBC (`res_odbc` + psqlODBC, pooled, the path Asterisk's PJSIP realtime docs use). Cache: sorcery memory cache (fast, but changes wait for expiry or a cache flush over AMI) vs none. Registrations: Postgres `ps_contacts` (needs write access) vs Asterisk's local database.

**Decision.** ODBC. Views only, in schema `asterisk`, readable (`SELECT`) by a dedicated role `linx_asterisk` and nothing else. No sorcery cache for now. Registrations stay in Asterisk's local database on its own volume. psqlODBC and unixODBC are LGPL, server-side only (no client bundle).

**Consequences.** Revoking a device or changing an extension applies to the next request. A database read per registration and call is fine at home/small-office size; Phase 5 load tests decide on caching. A compromised Asterisk can't read or change anything outside the PBX views.

## ADR-033 — Device SIP logins

**Context.** Each device needs its own SIP login. Asterisk must be able to check it, so it can't be hashed with a slow password hash, and it can't be sealed with ADR-030's key (Asterisk reads the views directly and must never hold that key).

**Options.** (a) Plaintext password in the database. (b) SIP digest hash `MD5(username:realm:password)` (`md5_cred`). (c) SHA-256 digest (supported by newer Asterisk, not by JsSIP or most phones).

**Decision.** (b). Linx generates a 128-bit random password (never chosen by a person, so never reused elsewhere), shows it once, stores only the digest hash. Usernames are random too (`d_` + 8 characters), so knowing an extension number doesn't reveal a login. Move to (c) when clients support it. iOS devices switch to certificate-based enrollment in Phase 2.

**Consequences.** A database dump lets someone register as a device on this server (the hash works as that device's login) but reveals no password. Resetting or revoking is instant. Documented in the threat model.

## ADR-034 — Call control split (owner decision, 2026-09-24)

**Context.** ADR-001 gives the ARI app the advanced logic (push-wake). Ordinary ringing shouldn't stop working while the control plane restarts.

**Options.** (a) Every call goes into the ARI app. (b) The dialplan rings devices itself (`func_odbc` over a view); the ARI app watches and takes over only the calls that need it (push-wake in Phase 2). ARI client: `CyCoreSystems/ari` (Apache-2.0, large dependency tree incl. NATS) vs a small hand-written client. Connection: control plane connects in, or Asterisk connects out (ARI outbound websocket, Asterisk ≥ 22.4).

**Decision.** (b); hand-written client (REST with `net/http`) plus `github.com/coder/websocket` (ISC; also used later for the client WSS events); Asterisk connects out to the control plane on `linx-private`. ARI REST listens on `linx-private` only, TLS with a step-ca certificate, password in Docker secret `linx_ari_password`. mTLS isn't possible (Asterisk's HTTP server doesn't check client certificates); accepted like step-ca's provisioner password.

**Consequences.** Calls keep ringing through a control-plane restart; only call webhooks pause. One new Go dependency. Phase 2 moves extension calls into the ARI app for push-wake, with the dialplan as the fallback when the app isn't connected.

**As built (Phase 1B step 4, 2026-09-24).** Asterisk 22.5+ also carries ARI's REST requests over the same outbound websocket ("REST over websocket"), so Asterisk's HTTP server stays **off**: nothing in the Asterisk container listens for ARI at all, which removes the "ARI REST listener" this ADR planned. The TLS certificate therefore sits on the other end: the control plane serves `wss://linx-ari:8089/ari` with a 24 h certificate from step-ca's `linx-services` provisioner (`internal/stepca`, a small JWK-provisioner client on go-jose, renewed at two-thirds of its lifetime), and Asterisk checks it against the internal CA's root and the hostname. `linx-ari` is a network alias on `linx-private` only, and the listener binds that network's address only. Asterisk proves itself with `linx_ari_password` (HTTP Basic over that TLS). Its REST access is a read-only ARI user: the control plane can look at calls but not change them in this slice. Asterisk ≥ 22.5, not 22.4, is when outbound websockets arrived.

## ADR-035 — First test phones (owner decision, 2026-09-24)

**Context.** The web client and TURN come in the next slice. The phone engine needs to be tested with real phones now.

**Decision.** In Phase 1B, devices of kind `softphone` connect over SIP-TLS (5061) with SDES-SRTP (allowed by the brief: SDES only over TLS), from the LAN only (Asterisk ACL and nftables). Test apps (e.g. Linphone) are used by the owner only and never shipped with Linx. Nothing SIP-related is public until registration lockout exists (Phase 1C).

**Consequences.** The owner can hear real calls before the web client exists. `softphone` devices stay useful later for desk phones and third-party apps.

## ADR-036 — People accounts and sign-in (owner decision, 2026-09-24)

**Context.** The web client (Phase 1C) must know who is using it. Accounts were planned with the admin portal (1E). Design: `docs/WEB.md` §4.

**Options.** (a) Simple local accounts now, company sign-in later. (b) A one-time link per browser, no accounts. (c) Build 1E's sign-in first.

**Decision.** (a). Local accounts: Argon2id (`golang.org/x/crypto/argon2`, BSD), TOTP written with the standard library (required for admins, optional for users), 10 recovery codes, `__Host-` session cookies stored hashed, CSRF header, per-address and per-account lockout with an admin alert. First account through `linx user create` (one-time set-password link). OIDC and passkeys (WebAuthn) join in 1E on the same sessions.

**Consequences.** The session model from `docs/API.md` §3 gets built now. `golang.org/x/crypto` becomes a direct dependency. Until email sending exists, set-password links are handed over by the admin.

## ADR-037 — One HTTPS entry

**Context.** Browsers need the page, the API and a SIP websocket. Every front door (ADR-040) must forward them easily. ARCHITECTURE listed a separate `linx-web` static server.

**Options.** (a) Separate `linx-web`, API and Asterisk WSS each behind the front door. (b) The control plane serves all three on one port; the web build is copied into its image.

**Decision.** (b). One HTTPS backend (8443, certd certificate) for all HTTP hostnames. The page talks to `/api/v1` and `/sip` on its own origin. Strict CSP, HSTS.

**Consequences.** Any HTTP proxy works with one route per name. Same-origin cookies, no CORS. A web-only change rebuilds the control-plane image. A separate static server can return later (e.g. for the <300 KB guest page) without changing URLs.

## ADR-038 — Browser SIP path

**Context.** Browsers speak SIP over websockets. Public SIP needs lockout first (ADR-035). Asterisk can't see real client addresses behind a proxy.

**Options.** (a) Publish Asterisk's WSS through the front door and read Asterisk's security events for lockout. (b) The control plane relays `/sip` to Asterisk after checking the session, and enforces lockout itself.

**Decision.** (b). Each browser session gets its own `web` device with a fresh random SIP password (in memory only). The relay requires a valid session cookie and same-origin `Origin`, allows only that device's username, closes after 3 failed authentications, and caps rate and size. Asterisk's HTTP server is enabled only for the websocket transport, bound to a new internal network `linx-sipws` (control plane and Asterisk only), TLS with a step-ca certificate. ARI stays on its outbound websocket.

**Consequences.** No unauthenticated SIP reaches the internet; 5061 stays LAN-only. Signing out or disabling a person drops their line at once. The relay is a small, tested piece of Linx code in the call path (byte relay with light SIP parsing; no SIP rewriting). Enabling Asterisk's HTTP server also exposes ARI's HTTP routes on `linx-sipws`, reachable only by the control plane.

## ADR-039 — TURN relay

**Context.** Browsers outside the LAN need a media path that works behind NAT and on 443-only networks (ADR-007), without forwarding audio ports.

**Options.** coturn (BSD, the standard; official Docker image) vs LiveKit's built-in TURN (for meetings only) vs eturnal (Apache-2.0, smaller community).

**Decision.** coturn, official image pinned by digest, on UDP 443 and TLS 443 (certd certificate). HMAC "TURN REST" credentials (1 h) from the control plane, secret `linx_turn_secret`. Relays only to Asterisk's address on internal network `linx-media`. Non-root, read-only. LiveKit (Phase 3) will share it (ADR-002).

**Consequences.** Remote audio always relays through the server; no public IP or audio ports to configure; a dynamic IP only affects DNS. coturn can't see real client addresses behind a passthrough, so quotas and short credentials do the work.

## ADR-040 — Front doors (owner decision on scope, 2026-09-24)

**Context.** The owner wants Linx to work behind any reverse proxy (Pangolin, nginx, …) or plain port forwarding, and NAT-friendly in every case. ARCHITECTURE §3 lists profiles A–G.

**Decision.** `linx setup` asks what sits in front (Pangolin, Linx takes 443, nginx/HAProxy, HTTP-only proxy, home only) and generates config, router forwards and DNS records. Only 443 is needed (TCP, plus UDP when possible); TURN/TLS uses 443 by name passthrough where the proxy supports it, or its own port. Pangolin (the owner's demo setup) and Linx-takes-443 are built first, the rest right after. certd gains a DNS updater for changing home IPs. Backends are HTTPS only; proxies check Linx's certificate.

**Consequences.** One design, several small generators, each with a Docker test. Pangolin's TURN/TLS-on-443 depends on a Traefik passthrough file routing to a raw TCP resource, to be proved in the build; if it can't be done, TURN/TLS gets its own port there and the owner is told.

## ADR-041 — Opus transcoding (owner decision, 2026-09-24)

**Context.** Asterisk 22 ships no Opus encoder or `.opus` file format. Sangoma's `codec_opus` is a closed binary for x86-64 only. Without an encoder, Linx's messages are silent to Opus-only callers, and web calls would fall back to G.722 (migration 0009).

**Options.** (a) Wazo's open-source `codec_opus` (fork of `traud/asterisk-opus`, GPLv2; libopus BSD). (b) Sangoma's binary. (c) No Opus via Asterisk (G.722 for web calls).

**Decision.** (a), compiled into the Asterisk image from a pinned commit (SHA-256 checked), for amd64 and arm64. Prompts stored in formats Asterisk can play to Opus callers. Opus first in the codec order again. If it doesn't build or pass the call suite on Asterisk 22, fall back to (c) and tell the owner.

**Consequences.** Asterisk stays GPLv2 in its own container (ADR-001). The module has a small maintainer base; we own its updates like the rest of the image. Transcoding costs CPU only when a call actually converts (messages, later trunks); browser-to-browser calls pass Opus through unchanged.

