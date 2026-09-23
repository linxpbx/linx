# Linx — Threat Model (STRIDE)

Version: Phase 0 (2026-09-23). This document is updated at the end of every phase.

## Assets
- Call and meeting media and signalling
- Recordings, voicemail and transcripts
- SIP secrets and trunk credentials
- Device private keys (these stay on the device)
- CA keys (offline root, online intermediate)
- DNS provider token
- APNs key
- Admin accounts
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
| Media | **I** eavesdropping | DTLS-SRTP mandatory for WebRTC; unencrypted RTP rejected; E2EE for meetings by default | 1/3 |
| coturn | **E** relay abuse / SSRF into LAN | `denied-peer-ip` for RFC1918/loopback/link-local, except exact PBX/SFU IPs; HMAC credentials with short TTL; per-user quotas | 1 |
| Guest links | **S**/**E** forged or replayed tokens, PIN brute force | EdDSA JWT with pinned algorithm; `aud`=room; `exp`; revocation table; PIN attempt limits + backoff; lobby | 3 |
| Enrollment QR | **S** QR photographed and reused | 10-min, single-use `jti`; bound to one extension; CSR from the device key; QR carries no SIP secret; admin can revoke | 2 |
| Device certificates | **S** stolen device | Secure Enclave keys (non-exportable); 7-day lifetime; deny-list checked on connect and pushed to live sessions | 2 |
| Pinning | **D** CA change bricks clients | Pin root SPKIs of primary + fallback CA; pin-set updates over the authenticated channel before any change | 2 |
| Admin console | **S**/**E** account takeover | LAN/VPN only by default; OIDC or local + TOTP/WebAuthn; RBAC; audit log; optional proxy SSO (Pangolin) | 1 |
| REST API / webhooks | **T**/**R** forged webhooks, key leak | Hashed and scoped API keys; HMAC-signed outbound webhooks; replay-safe delivery log; audit | 1 |
| Outbound requests (webhooks, CRM, storage) | **I** SSRF | Private ranges blocked by default with an explicit allowlist; timeouts and size limits | 1 |
| Web app | **T**/**I** XSS/CSRF | Strict CSP (no inline scripts), HSTS, Secure/HttpOnly/SameSite cookies, CSRF tokens, output encoding; external content treated as untrusted | 1 |
| step-ca | **E** CA key compromise | Offline root (exported encrypted, removed from host); intermediate only on `linx-private`; provisioner restricted to the control plane via mTLS | 0 |
| linx-certd | **I** DNS token leak → domain hijack | Least-privilege token (`Zone:DNS:Edit`, one zone), stored as a Docker secret, never logged; CAA records restrict issuance | 0 |
| DNS automation | **T** clobbering user records | Only touches records it created (ownership tag); preview before write | 1 |
| Push gateway | **S**/**D** push spam, APNs key leak | APNs key as a Docker secret; VoIP pushes only for real calls (Apple policy); push rate limits per device | 2 |
| Docker host | **E** container escape, socket abuse | Non-root containers, read-only FS, no-new-privileges, dropped capabilities, seccomp; the Docker socket is never mounted into Linx services; container UIs warned as root-equivalent and bound to LAN | 0 |
| Installer | **T** supply chain | Docker installed from the official repo with a verified GPG key; images pinned by digest and cosign-verified; SBOM; CI blocks high/critical CVEs | 0 |
| Backups | **I** data exposure off-site | Client-side encryption (restic) with an owner-held passphrase; private keys excluded by default | 5 |
| Logs | **I** secret or personal data leakage | Structured logging with redaction of tokens/secrets; retention limits | 0+ |
| Presence/directory | **I** over-sharing | Server-side visibility filtering by RBAC scope; served only to authenticated devices | 1/4 |
| Session inactivity | **S** abandoned devices | 7-day expiry via certificate lifetime + token revocation; pushes stop | 2 |
| AI/MCP (later) | **E** prompt injection via untrusted content | Untrusted-data marking; write tools require human confirmation | 6 |

## Residual risks and open items
- Asterisk and coturn can't see real client IPs on passthrough profiles. This is mitigated by pushing clients through WSS (where the control plane sees the IP) and by credential quotas on TURN.
- Server-decrypted call types (PBX-anchored calls, recordings, trunks) are documented honestly in the user guide.
- Dynamic IP with IP-auth trunks is unsupported (the wizard warns about this).
