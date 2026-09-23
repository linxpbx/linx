# Linx — Threat Model (STRIDE)

Version: Phase 0 (2026-09-23), installer, certd and step-ca rows updated in Phase 0b. This document is updated at the end of every phase.

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
| Public API (`api.`) | **S** stolen/guessed API key or client secret | 256-bit random secrets, stored only as SHA-256; `linx_` prefix for secret scanning; expiry; optional IP allowlist; instant revocation; failed-auth rate limit per IP (ADR-027) | 1 |
| Public API | **E** key does more than intended | Scopes capped by the creator's role; sensitive scopes (recordings, transcripts, call control, key management) never granted by "all"; checked on every request | 1 |
| Public API | **T**/**D** malformed or huge requests | Spec validation before handlers, unknown fields rejected, 1 MiB body limit, per-key rate limits, problem+json errors without internals | 1 |
| Portal sessions | **S** CSRF / cookie theft | `HttpOnly; Secure; SameSite=Strict` cookies plus CSRF header on writes | 1 |
| OAuth tokens | **S** forged or replayed JWT | EdDSA only (alg pinned), 15-minute lifetime, `aud` checked, `jti` revocation (ADR-012) | 1 |
| API writes | **R** "I didn't do that" | Append-only `audit_log` with actor, key/client id, IP, action, target, result | 1 |
| Webhooks / alert senders / later CRM | **I**/**E** SSRF into `linx-private`, the LAN or cloud metadata | HTTPS only; resolve, check every address, dial the checked address; private, loopback, link-local, metadata, CGNAT, multicast and Linx networks blocked unless the admin allowlists them; no redirects; senders only on `linx-egress` (ADR-028) | 1 |
| Webhooks | **S** receiver fooled by fake or replayed events | Standard Webhooks HMAC-SHA256 with id + timestamp; receivers told to reject old timestamps and de-duplicate ids; secret rotation with 24 h overlap | 1 |
| Webhooks | **I** personal data in payloads | Only what each event needs; never recording/transcript contents; delivery log kept 30 days | 1 |
| Stored integration secrets | **I** database dump leaks tokens | AES-256-GCM with a key held as a Docker secret, bound to the row (ADR-030) | 1 |
| Admin alerts | **D** alert flood hides real problems | De-duplication by key, flap hold-back, reminders at most every 24 h, one summary after quiet hours | 1 |
| AI/MCP (later) | **E** prompt injection via untrusted content | Untrusted-data marking; write tools require human confirmation | 6 |

## Residual risks and open items
- API rate limits are per control-plane instance until a Valkey-backed limiter exists (single node now).
- Losing `linx_db_encryption_key` makes stored integration secrets unreadable; backups must hold it separately from the database dump.
- Asterisk and coturn can't see real client IPs on passthrough profiles. This is mitigated by pushing clients through WSS (where the control plane sees the IP) and by credential quotas on TURN.
- Server-decrypted call types (PBX-anchored calls, recordings, trunks) are documented honestly in the user guide.
- Dynamic IP with IP-auth trunks is unsupported (the wizard warns about this).
- CAA records aren't created automatically yet. Until the Domain & DNS page does it (Phase 1), the owner adds `CAA 0 issue "letsencrypt.org"` and `CAA 0 issue "sectigo.com"` (ZeroSSL) by hand.
- The device provisioner is protected by its password (a Docker secret for the control plane only), not by mTLS as first planned (accepted by the owner, 2026-09-23). step-ca has no per-provisioner client-certificate gate; revisit with an X5C provisioner in Phase 2 if needed.
- If the CA secrets in `/etc/linx/secrets` are lost, step-ca can't unlock its intermediate key. Rebuilding needs the root backup and passphrase (`docs/ops/INTERNAL_CA.md`).
