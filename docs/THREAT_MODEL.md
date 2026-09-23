# Linx — Threat Model (STRIDE)

Version: Phase 0 (2026-09-23), installer, certd and step-ca rows updated in Phase 0b; API authentication rows updated in Phase 1 step 3 (2026-09-23). This document is updated at the end of every phase.

## Assets
- Call and meeting media and signalling
- Recordings, voicemail and transcripts
- SIP secrets and trunk credentials
- Device private keys (these stay on the device)
- CA keys (offline root, online intermediate)
- DNS provider token
- APNs key
- Admin accounts
- API keys, OAuth client secrets, webhook signing secrets, alert channel tokens
- Database encryption key (`linx_db_encryption_key`)
- API token signing key (`linx_jwt_signing_key`)
- CDR and directory data (personal data)
- Toll balance: fraudulent calls cost real money

## Trust boundaries
1. **Internet ↔ edge** (proxy/SNI router, coturn UDP 443, trunk SIP)
2. **Edge ↔ `linx-public` services**
3. **`linx-public` ↔ `linx-private`** (DB, Valkey, step-ca, ARI/AMI)
4. **Server ↔ third parties** (APNs, ACME CAs, DNS provider API, SMTP, later Meta/Telegram)
5. **Device ↔ server** (enrolled device, guest browser, desk phone)
6. **Host ↔ containers** (Docker socket, container management UIs)

## STRIDE by component

| Component | Threat (STRIDE) | Mitigation | Phase |
|---|---|---|---|
| SIP registration (Asterisk WSS/TLS) | **S** credential brute force, registration scanning | No public 5060; SIP only over WSS/TLS; long random secrets that are never displayed; per-IP and per-user rate limit and lockout at the edge (control-plane auth on WSS upgrade), with a fail2ban-equivalent ban list | 1 |
| Outbound calling | **E**/financial toll fraud | International dialling off by default; per-extension concurrency and spend limits; premium numbers blocked; alerts | 1 |
| Trunks | **S** spoofed INVITEs from the internet | Registration trunks by default (dynamic IP); IP-auth trunks allowlisted; `sip.` restricted to allowlisted sources | 1 |
| Media | **I** eavesdropping | DTLS-SRTP mandatory for WebRTC; unencrypted RTP rejected except on ADR-023 trunks; E2EE for meetings by default | 1/3 |
| Unencrypted trunks (ADR-023) | **I**/**T** calls overheard or altered between Linx and the provider | Only after TLS/SRTP fails the wizard test; admin confirms a plain-language warning; shown as unencrypted in the trunk list and `linx doctor`; recommended fix is a WireGuard profile; IP-auth 5060 limited to provider addresses | 1 |
| WireGuard profiles (ADR-024) | **I**/**D** leaked private key; a VPN capturing or cutting all traffic | Keys are secrets, never re-displayed; split tunnel enforced (full-tunnel `AllowedIPs` narrowed to trunk addresses); only a dedicated service has `NET_ADMIN`; handshake health per profile | 1 |
| coturn | **E** relay abuse / SSRF into LAN | `denied-peer-ip` for RFC1918/loopback/link-local, except exact PBX/SFU IPs; HMAC credentials with short TTL; per-user quotas | 1 |
| Guest links | **S**/**E** forged or replayed tokens, PIN brute force | EdDSA JWT with pinned algorithm; `aud`=room; `exp`; revocation table; PIN attempt limits + backoff; lobby | 3 |
| Enrollment QR | **S** QR photographed and reused | 10-min, single-use `jti`; bound to one extension; CSR from the device key; QR carries no SIP secret; admin can revoke | 2 |
| Device certificates | **S** stolen device | Secure Enclave keys (non-exportable); 7-day lifetime; deny-list checked on connect and pushed to live sessions | 2 |
| Pinning | **D** CA change bricks clients | Pin root SPKIs of primary + fallback CA; pin-set updates over the authenticated channel before any change | 2 |
| Admin console | **S**/**E** account takeover | LAN/VPN only by default; OIDC or local + TOTP/WebAuthn; RBAC; audit log; optional proxy SSO (Pangolin) | 1 |
| REST API / webhooks | **T**/**R** forged webhooks, key leak | Hashed and scoped API keys; HMAC-signed outbound webhooks; replay-safe delivery log; audit | 1 |
| Outbound requests (webhooks, CRM, storage) | **I** SSRF | Private ranges blocked by default with an explicit allowlist; timeouts and size limits | 1 |
| Web app | **T**/**I** XSS/CSRF | Strict CSP (no inline scripts), HSTS, Secure/HttpOnly/SameSite cookies, CSRF tokens, output encoding; external content treated as untrusted | 1 |
| step-ca | **E** CA key compromise | Offline root: generated inside a network-less bootstrap container, written only encrypted with a generated passphrase that is shown once and never saved; the CA volume never holds the root key (tested). Intermediate key encrypted, its password a group-restricted Docker secret; CA only on `linx-private`; non-root, read-only container; no remote admin API | 0 |
| step-ca | **E** rogue certificate issuance | Separate provisioners: `linx-services` (24 h max) and `linx-devices` (7 d max), limits enforced by the CA (tested); the device provisioner's password is mounted only into the control plane; no SSH certificates | 0 |
| Root key backup | **I** encrypted backup left on the server | Owner told to copy `/etc/linx/ca-backup` off the server and delete it; folder root-only; `linx doctor` warns while it is still there | 0 |
| linx-certd | **I** DNS token leak → domain hijack | Least-privilege token (Cloudflare: `Zone:Read` + `DNS:Edit` on the one zone), stored as a Docker secret, never logged (tested); CAA records restrict issuance. Setup reads it without echo, never writes it to `setup.yaml` or `.env` (tested), and saves it root-owned, group 65532, mode 0440 in a root-only folder | 0 |
| Service images | **T** a re-pushed image tag runs different code | Setup pulls `sha-<commit>` tags matching the `linx` binary, from CI's cosign-signed builds. Open: tags aren't digest-pinned or signature-checked on the server yet; a release manifest with digests and a cosign check arrive with `linx upgrade` | 0 |
| linx-certd | **I** hostname discovery via CT logs | Wildcard certificate by default, so `sip.`/`turn.`/`admin.` never appear in public logs | 0 |
| linx-certd | **T**/**D** half-written or stolen certificate files | New versions written to a fresh directory, then an atomic symlink swap; key `0640` (shared group only); ACME account keys `0600` in a separate volume; non-root, read-only container, all capabilities dropped | 0 |
| DNS automation | **T** clobbering user records | Only touches records it created (ownership tag); preview before write | 1 |
| Push gateway | **S**/**D** push spam, APNs key leak | APNs key as a Docker secret; VoIP pushes only for real calls (Apple policy); push rate limits per device | 2 |
| Docker host | **E** container escape, socket abuse | Non-root containers, read-only FS, no-new-privileges, dropped capabilities, seccomp; the Docker socket is never mounted into Linx services; container UIs warned as root-equivalent and bound to LAN | 0 |
| Installer | **T** supply chain | Docker's apt signing key is embedded in the `linx` binary (fingerprint checked by tests), not downloaded; packages come only from Docker's official repo; images pinned by digest and cosign-verified; SBOM; CI blocks high/critical CVEs | 0 |
| Installer | **E** privilege | `linx setup` runs as root and shows every step before applying it (`--dry-run` shows the exact commands); Docker group membership only for the `linx` system account, which has no login shell | 0 |
| Container management UI (Portainer, optional) | **E** root-equivalent via the Docker socket | Off by default; bound to the private LAN address only (loopback + SSH tunnel if the server has none); generated admin password in a root-only file mounted as a secret; image pinned by digest; setup warns never to port-forward it (ADR-019) | 0 |
| Backups | **I** data exposure off-site | Client-side encryption (restic) with an owner-held passphrase; private keys excluded by default | 5 |
| Logs | **I** secret or personal data leakage | Structured logging with redaction of tokens/secrets; retention limits | 0+ |
| Presence/directory | **I** over-sharing | Server-side visibility filtering by RBAC scope; served only to authenticated devices | 1/4 |
| Session inactivity | **S** abandoned devices | 7-day expiry via certificate lifetime + token revocation; pushes stop | 2 |
| Public API (`api.`) | **S** stolen/guessed API key or client secret | 256-bit random secrets, stored only as SHA-256 and compared in constant time (an unknown id costs the same hash); `linx_`/`linxcs_` prefixes with gitleaks rules in CI (`.gitleaks.toml`); expiry (default 1 year, max 2); optional IP allowlist; instant revocation; 20 failed attempts/min per IP (IPv6 per /64), then 429, every failure audited. Revoked/expired is only revealed to a caller who proved the secret (ADR-027, built) | 1 |
| Public API | **E** key does more than intended | Each operation's scopes are declared in `api/openapi.yaml` and enforced by the validator before the handler (a test fails if a new operation forgets them); effective scopes = key scopes ∩ role ceiling, re-checked on every request; a new key/client can't exceed its creator's role or scopes (no minting a stronger key); sensitive scopes (recordings, transcripts, call control, key and client management) never granted by "all" | 1 |
| Public API | **T**/**D** malformed or huge requests | Spec validation before handlers, unknown fields rejected, 1 MiB body limit, per-key rate limits, problem+json errors without internals | 1 |
| Portal sessions | **S** CSRF / cookie theft | `HttpOnly; Secure; SameSite=Strict` cookies plus CSRF header on writes | 1 |
| OAuth tokens | **S** forged or replayed JWT | EdDSA only (alg pinned; `none`, HS256 alg-confusion and other keys refused, tested), `kid`, `iss`, `aud`, `exp`/`nbf`/`iat` all required, lifetime ≤ 15 min, `jti` revocation (ADR-012). The client is looked up on every call, so revoking it kills its live tokens at once; a token can only narrow the client's current scopes. Signing key is an installer-generated Docker secret | 1 |
| Rate limits / audit | **S**/**D** spoofed `X-Forwarded-For` to dodge limits or pass IP allowlists | Header only believed from `LINX_TRUSTED_PROXIES` (empty by default), right-most untrusted hop used | 1 |
| `linx api-key` | **E** creating keys without the API | Runs as root on the host via `docker exec` into the control plane; that is already root-equivalent (Docker), so it adds no new path; every key it makes is audited as `system:cli` | 1 |
| API writes | **R** "I didn't do that" | Append-only `audit_log` with actor, key/client id, IP, action, target, result | 1 |
| Webhooks / alert senders / later CRM | **I**/**E** SSRF into `linx-private`, the LAN or cloud metadata | `internal/safehttp` (built): HTTPS only with normal certificate checks; no redirects; no environment proxy; the host is resolved once, **every** address checked (one private answer refuses the lot), and the dial goes to a checked address, re-checked in the dialer's `Control` hook (no DNS-rebinding window). IPv4-mapped, NAT64 and 6to4 addresses are checked as the IPv4 they reach. Private, CGNAT, link-local, documentation and similar ranges are refused unless on the outbound allowlist; loopback, `0.0.0.0/8`, multicast, reserved, cloud metadata addresses and the container's own networks (read from its interfaces at start) are refused even then. The allowlist is read per connection and fails closed. URLs are also checked when saved, so the admin hears at once (ADR-028) | 1 |
| Outbound allowlist | **E** allowlist used to open up the server itself | Entries must lie inside one private range (no `0.0.0.0/0`, no public ranges, no loopback or metadata) or be an exact host name; `outbound_allowlist:write` is a sensitive scope (never in "all"); every change audited | 1 |
| Webhook "Test" | **I** response reading turns the test button into a fetch tool | Only to addresses the guard allows (public, or the admin's own allowlisted LAN hosts); only the first 4 KB is kept; needs `webhooks:write`; audited | 1 |
| Webhooks | **S** receiver fooled by fake or replayed events | Standard Webhooks HMAC-SHA256 over id + timestamp + body (tested against the reference vector); receivers told to reject old timestamps and de-duplicate ids; secret rotation with 24 h overlap (both signatures sent) | 1 |
| Webhooks | **T** event sent for a change that rolled back, or lost | Transactional outbox: the event row commits or rolls back with the change; workers claim with `FOR UPDATE SKIP LOCKED` and a 2-minute lease, so a crashed worker's delivery is retried (at-least-once) | 1 |
| Webhooks | **D** slow or hostile receiver ties up the server | 10 s total timeout, 64 KB response headers, 4 KB kept + 64 KB drained, 8 concurrent sends, retries spread with jitter; 410 or 5 days of failures turns the endpoint off and cancels its queue | 1 |
| Webhooks | **I** personal data in payloads | Only what each event needs; never recording/transcript contents; delivery log kept 30 days | 1 |
| Webhook secrets | **I** secret leaks from the API or logs | Shown once (create, rotate); stored sealed (AES-256-GCM bound to the endpoint row); never in list/get responses, audit details or log lines; URLs with a user name or password refused | 1 |
| Stored integration secrets | **I** database dump leaks tokens | AES-256-GCM with a key held as a Docker secret, bound to the row (ADR-030) | 1 |
| Admin alerts | **D** alert flood hides real problems | De-duplication by key, flap hold-back (5 min stable before the first send), reminders at most every 24 h, one digest after quiet hours (built: `internal/alert`) | 1 |
| Alert channel settings | **I** channel URLs/tokens leak (Slack/Teams URLs, Telegram bot tokens, ntfy access tokens) | Sealed as one JSON object per channel (AES-256-GCM, ADR-030), same as a webhook secret; never returned by the API once saved, only usable through Test | 1 |
| Alert channel URLs (ntfy/Gotify server, Slack/Teams/webhook URL) | **I**/**E** SSRF via an alert channel | Same `internal/safehttp` guard as webhooks: checked at save time and every send | 1 |
| Certificate renewal alert source | **T** control plane trusts a spoofed `/metrics` response | Fixed hostname (`certd`, compose service DNS) on `linx-private` only, not admin-configurable; if wrong, the worst case is a missed or spurious alert, not a security bypass | 1 |
| AI/MCP (later) | **E** prompt injection via untrusted content | Untrusted-data marking; write tools require human confirmation | 6 |

## Residual risks and open items
- API rate limits are per control-plane instance until a Valkey-backed limiter exists (single node now).
- Failed-auth limiting is per IP: an attacker behind the same NAT as a real user can hold that user off for about a minute. Accepted; the alternative (per-key lockout) lets anyone lock out a known key id.
- One token-signing key, no rotation yet. Losing or replacing `linx_jwt_signing_key` only ends access tokens early (they last 15 minutes), so it needs no backup. Rotation with two `kid`s arrives when needed.
- `token_revocation` is checked on every token but nothing writes to it yet (revoking a client already stops its tokens). It is used by guest and enrollment tokens later (ADR-012).
- Portal sessions (cookies + CSRF) are specified but not built; they arrive with the admin portal login later in Phase 1.
- Losing `linx_db_encryption_key` makes stored integration secrets unreadable; backups must hold it separately from the database dump.
- Webhook URLs are stored in plain text. A receiver that puts a token in the query string (rather than verifying the signature) has that token readable by anyone with `webhooks:read` or a database dump. Slack/Teams-style URLs for admin alerts are sealed (step 5).
- The outbound allowlist is server-wide. With multi-tenancy it must become `system_admin` only.
- LAN receivers need a certificate from a public CA (e.g. Let's Encrypt via their own reverse proxy); a custom CA per endpoint isn't supported yet.
- Idle outbound connections are reused for up to 30 s, so removing an allowlist entry can take that long to affect a receiver already connected.
- An endpoint turned off for failing only writes a log line and an audit entry until admin alerts arrive (step 5).
- Standard Webhooks receivers reject messages whose timestamp is more than ~5 minutes off, so the server's clock must be right (NTP).
- `Idempotency-Key` isn't implemented yet; no step-4 endpoint needs it (creating a webhook returns its secret, like API keys, so it isn't idempotent by design).
- Disk/storage-nearly-full and DDNS-update-failure alert sources (docs/API.md §5) are not wired up: the control plane container has no filesystem of its own to check (`read_only: true`, no volumes) and no host-disk or DDNS-updater signal reaches it without either a Docker socket mount (against the "no Docker socket mounts" rule) or a new host mount / DDNS-updater feature. Revisit when either is designed.
- The certificate renewal alert only reads `linx-certd`'s expiry and failure-count metrics; it can't tell a slow ACME provider from a broken DNS provider apart. The message points the admin at `linx doctor` and the certd logs rather than guessing further.
- Alert channels are server-wide, like the outbound allowlist; with multi-tenancy they'll need a tenant scope.
- Asterisk and coturn can't see real client IPs on passthrough profiles. This is mitigated by pushing clients through WSS (where the control plane sees the IP) and by credential quotas on TURN.
- Server-decrypted call types (PBX-anchored calls, recordings, trunks) are documented honestly in the user guide.
- Dynamic IP with IP-auth trunks is unsupported (the wizard warns about this).
- CAA records aren't created automatically yet. Until the Domain & DNS page does it (Phase 1), the owner adds `CAA 0 issue "letsencrypt.org"` and `CAA 0 issue "sectigo.com"` (ZeroSSL) by hand.
- The device provisioner is protected by its password (a Docker secret for the control plane only), not by mTLS as first planned (accepted by the owner, 2026-09-23). step-ca has no per-provisioner client-certificate gate; revisit with an X5C provisioner in Phase 2 if needed.
- If the CA secrets in `/etc/linx/secrets` are lost, step-ca can't unlock its intermediate key. Rebuilding needs the root backup and passphrase (`docs/ops/INTERNAL_CA.md`).
