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
- DNS-01 check (after the first live test, 2026-09-23): before asking the CA to validate, certd waits until the record is visible at public resolvers (1.1.1.1, 8.8.8.8, 9.9.9.9; `LINX_DNS_RESOLVERS` overrides), up to 10 minutes, then waits one more minute. The server's own resolver had reported the record while Let's Encrypt still got NXDOMAIN for a domain registered the same day.

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
