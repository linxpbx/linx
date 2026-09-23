# Linx — Public API, webhooks and admin alerts (Phase 1 foundation)

Status: **approved by the owner 2026-09-23** (ADR-025 to ADR-030).
The admin portal, the web client, `linx` CLI and every integration use this API. Nothing gets a private back door.

## 1. In plain words
- **API**: other programs talk to Linx over HTTPS at `https://api.<domain>/api/v1/`. What it offers is written down in one file, `api/openapi.yaml` (the "menu"). Server code and the web app's client are generated from it, so they can't disagree.
- **API keys**: long random passwords for programs. Each key only opens the doors (scopes) it was given, can be revoked instantly, and is shown once. Linx stores only a fingerprint of it.
- **Webhooks**: Linx calls *your* URL when something happens (a missed call, a trunk going down). Each message is signed so the receiver can prove it came from Linx. Failed deliveries are retried for about a day and can be re-sent from a log.
- **Admin alerts**: short messages to you on ntfy, Gotify, Slack, Teams, Telegram or a webhook (email later in Phase 1) when something needs attention. The same problem is not repeated every minute, and quiet hours are respected except for critical problems.

## 2. API conventions (ADR-025)
| Topic | Rule |
|---|---|
| Spec | OpenAPI **3.1**, hand-written in `api/openapi.yaml` (split files allowed under `api/`). Webhook payloads are documented in the spec's `webhooks:` section. |
| Server | `oapi-codegen` strict server for `net/http` (Go 1.22+ `ServeMux`, no web framework). Generated code in `services/control-plane/api/gen.go`; `make api` regenerates; lint fails if stale (same as `make tokens`). |
| Validation | Every request is checked against the spec before a handler runs (kin-openapi middleware). Unknown JSON fields are rejected. Body limit 1 MiB. |
| Web client | `openapi-typescript` + `openapi-fetch` (MIT) generate types into `web/src/api/`. |
| Base path | `/api/v1`. Breaking changes mean `/api/v2`; additive changes don't. |
| Format | JSON, `snake_case`, timestamps RFC 3339 UTC, IDs are UUIDv7 strings. |
| Errors | RFC 9457 `application/problem+json` with a stable `code` (e.g. `scope_missing`) and a plain-language `detail`. Never stack traces. |
| Lists | Cursor pagination: `?limit=` (default 50, max 200) and `?cursor=`; response has `items` and `next_cursor`. |
| Writes | `PATCH` is JSON Merge Patch. Resources carry `etag`; `If-Match` is honoured (412 on mismatch). `POST` accepts `Idempotency-Key` (kept 24 h). |
| Audit | Every write, key use for writes, replay and failed auth goes to `audit_log` (actor, key/client id, IP, action, target, result). |
| Spec served | `GET /api/v1/openapi.json` (public, no secrets in it). |

## 3. Who can call the API (ADR-027)
Three kinds of caller, all ending in the same **principal** (tenant, role ceiling, scopes):

1. **API key** — `Authorization: Bearer linx_<id>_<secret>`.
   - `<id>`: 12 chars, public, used to look the key up. `<secret>`: 32 random bytes, base64url. The `linx_` prefix lets GitHub/gitleaks secret scanning spot leaked keys.
   - Stored: `id`, `SHA-256(secret)`, name, scopes, created_by, expires_at (default 1 year, max 2), last_used_at/ip, revoked_at. Compared in constant time. Shown **once** at creation.
   - Scopes can never exceed the creating user's role. Optional IP allowlist per key.
2. **OAuth 2.0 client credentials** — `POST /oauth/token` with client id + secret (secret stored like an API key). Returns a JWT access token: EdDSA only (alg pinned), 15 min, `aud=linx-api`, `jti` checked against the revocation list (ADR-012). No refresh tokens.
3. **Signed-in people** (admin portal, web client) — session cookie from OIDC/local login + MFA (built later in Phase 1): `HttpOnly`, `Secure`, `SameSite=Strict`, plus a CSRF header on writes. Specified now so the API doesn't change later.

**Scopes** are `resource:read` / `resource:write` (e.g. `extensions:write`, `webhooks:write`, `alerts:write`, `audit:read`). Sensitive scopes are never included in "all": `recordings:read`, `transcripts:read`, `calls:control`, `api_keys:write`.

**Roles** (brief): `system_admin`, `admin`, `user`, `reporter`. A role maps to the scopes it may hold; the API checks both role ceiling and key scopes.

**Rate limits** (`golang.org/x/time/rate`, in memory; Valkey-backed when multi-node): 600 requests/min per key or client, burst 100. Failed authentication: 20/min per IP, then 429. Responses carry `RateLimit-*` headers and `Retry-After`.

**First key.** Before the portal exists, the owner creates the first key on the server:
`linx api-key create --name "My laptop" --role admin` → runs inside the control-plane container, prints the key once, writes an audit entry.

## 4. Webhooks (ADR-028)
**Format: Standard Webhooks** (standardwebhooks.com), so receivers can use ready-made libraries.
- Headers: `webhook-id`, `webhook-timestamp`, `webhook-signature: v1,<base64 HMAC-SHA256>` over `id.timestamp.body`. Secret format `whsec_<base64>` (32 bytes).
- Body envelope: `{ "type": "call.missed", "timestamp": "...", "data": { ... } }`. `data` is documented per event in the spec.
- **Secret rotation**: a new secret is issued; the old one keeps signing alongside it for 24 h (both signatures in the header).

**Events** (brief list; each ships with the feature that produces it):
`call.started`, `call.answered`, `call.ended`, `call.missed`, `voicemail.created`, `recording.ready`, `presence.changed`, `meeting.started`, `meeting.ended`, `device.enrolled`, `device.revoked`, `trunk.down`, `trunk.up`, plus `webhook.test` and `alert.fired`/`alert.resolved`.
Endpoints subscribe to a list of event types (or all). Recording and transcript contents are never in payloads, only IDs and links needing a scoped key.

**Delivery**
- **Outbox**: the event row is written in the same database transaction as the change, so an event is never lost or sent for a change that rolled back. A worker claims rows with `FOR UPDATE SKIP LOCKED`.
- Timeout 10 s, success = any 2xx. No redirects followed. `410 Gone` disables the endpoint.
- Retries: immediately, 5 s, 5 min, 30 min, 2 h, 5 h, 10 h, 10 h (≈ 27 h total), with jitter. Then marked failed.
- An endpoint failing every delivery for 5 days is disabled and an admin alert fires.
- Order is not guaranteed; receivers de-duplicate on `webhook-id` (documented).
- **Delivery log**: each attempt (status, duration, first 4 KB of response, error) kept 30 days. **Replay** one delivery, or all failed deliveries for an endpoint since a time.

**Safe outbound connections (SSRF guard)** — shared by webhooks, alert senders and later CRM/storage:
- HTTPS only, with normal certificate checks. This includes allowlisted LAN targets: there is no `http://` exception (owner decision). Home tools need HTTPS, e.g. via their own reverse proxy.
- The host is resolved once, every address is checked, and the connection goes to the checked address (stops DNS-rebinding tricks). Blocked unless allowlisted: loopback, private (RFC 1918, `fc00::/7`), link-local and cloud metadata (`169.254.0.0/16`, `fe80::/10`), CGNAT `100.64.0.0/10`, multicast, `0.0.0.0/8`, and Linx's own Docker networks.
- Admin allowlist entries are exact CIDRs or host names for LAN targets (NAS, Home Assistant).
- Senders run on `linx-egress`; they never reach `linx-private`.

## 5. Admin alerts (ADR-029)
**Channels now**: ntfy, Gotify, Slack (incoming webhook), Microsoft Teams (Workflows webhook with an Adaptive Card), Telegram (bot token + chat id), generic webhook (Standard Webhooks signed). **Email** is added when email sending is built later in Phase 1. Each is a small built-in sender (one HTTPS POST each), all through the SSRF guard. Uptime Kuma connects via generic webhook or ntfy.

**Alert** = `key` (e.g. `trunk.down:<trunk-id>`), `severity` (`info`, `warning`, `critical`), title, plain-language message, link to the admin page.
- **De-duplication**: an alert key fires once while the problem lasts. Reminders every 24 h (configurable) while still open. A "resolved" message when it clears. A problem that flaps is held back until it has been stable for 5 minutes.
- **Per channel**: minimum severity; quiet hours (window + time zone). Critical alerts ignore quiet hours unless the admin turns that off. Held alerts are sent as one summary when quiet hours end.
- **Test** button per channel. Every send is in a delivery log, with the same retries as webhooks.

**Alert sources** (each ships with the feature it watches): trunk down; certificate renewal failure (`linx-certd` reports to the control plane); disk/storage nearly full (80 % warning, 90 % critical); backup failure; DDNS update failure; registration attack / toll-fraud detection; capacity limits; email sending broken; webhook endpoint disabled.

## 6. Data (ADR-026, ADR-030)
PostgreSQL 18 (pinned digest) on `linx-private`, migrations built into the control plane and run at start (forward-only, one transaction each, advisory lock so two instances can't race).
New tables in this slice: `tenant`, `audit_log`, `api_key`, `oauth_client`, `token_revocation`, `idempotency_key`, `event_outbox`, `webhook_endpoint`, `webhook_delivery`, `alert`, `alert_channel`, `alert_delivery`, `outbound_allowlist`.

Secrets Linx must be able to use again (webhook signing secrets, Telegram bot tokens, Slack/Teams URLs that contain tokens) are **encrypted in the database** with AES-256-GCM. The key is a Docker secret (`linx_db_encryption_key`) generated by the installer, never in the database or backups of it. API keys and OAuth client secrets are only hashed.

## 7. Endpoints in this slice
```
GET    /api/v1/openapi.json
GET    /api/v1/me                          who am I, which scopes
GET    /api/v1/event-types
POST   /oauth/token

GET/POST          /api/v1/api-keys          GET/DELETE /api/v1/api-keys/{id}   (DELETE = revoke)
GET/POST          /api/v1/oauth-clients     GET/DELETE /api/v1/oauth-clients/{id}
GET/POST          /api/v1/webhooks          GET/PATCH/DELETE /api/v1/webhooks/{id}
POST   /api/v1/webhooks/{id}/rotate-secret | /test | /replay
GET    /api/v1/webhooks/{id}/deliveries     POST /api/v1/webhook-deliveries/{id}/replay
GET/POST          /api/v1/alert-channels    GET/PATCH/DELETE /api/v1/alert-channels/{id}
POST   /api/v1/alert-channels/{id}/test
GET    /api/v1/alerts                       open and recent alerts
GET/POST/DELETE   /api/v1/outbound-allowlist
GET    /api/v1/audit-log
```

## 8. Build order (one session each)
1. **Database**: Postgres + control plane in compose, installer secrets, migration runner, `tenant` + `audit_log`, encryption helper. *(Sonnet)*
2. **API skeleton**: `api/openapi.yaml`, `make api` codegen + stale check, validation middleware, problem+json, pagination, `/me`, `/openapi.json`, `/event-types`. *(Sonnet)*
3. **Authentication**: API keys, scopes/roles, rate limits, `linx api-key create`, OAuth client credentials. *(Opus: security)*
4. **Webhooks**: outbox, SSRF guard, signer, retries, delivery log, replay, `webhook.test`. *(Opus: security)*
5. **Admin alerts**: engine, six channels, first sources (certificate renewal, disk, DDNS, webhook disabled). *(Sonnet)*
6. **Review + docs**: security review, threat model, `linx doctor` checks, `docs/DEMO_PHASE1A.md`. *(Opus)*
