# Linx — Backup and restore

*Status: design drafted 2026-09-27, pulled forward from Phase 5 (`docs/ROADMAP.md`) at the owner's request, ahead of going live. ADR-055 records the owner decisions below; not yet approved.*

Two things: getting a working copy of Linx off the server regularly (**backup**), and getting a whole system back after loss — a dead disk, a destroyed VPS, a mistake — onto a fresh one (**restore**). `docs/ARCHITECTURE.md` already lists `backup`/`restore` as `linx` CLI subcommands; this is their design.

## 1. In plain words

- **What's backed up:** everything needed to bring Linx back exactly as it was — every person, extension, phone line, setting and the activity log — plus the keys that protect it. Nothing about your calls' audio; there isn't any stored.
- **Where it goes:** on the server itself, on another machine or storage service you point it at, or straight to your own computer as a single file you download from the admin portal. All three at once is fine — the schedule can push to more than one place.
- **On a schedule, or by hand:** turn on daily, weekly or monthly backups at a time you choose, or turn them off. "Back up now" always works too, from System → Backups.
- **Always encrypted, always asked for before anything sensitive:** the backup file is useless without its password, which Linx generates and shows you once — the same "you won't see this again" box as a device's login. Setting up where backups go, or downloading one, needs "confirm it's you" (ADR-053) first.
- **Getting back onto a new server:** run `linx setup` on the new machine as usual (domain, DNS, the Linx stack) — that part can't come from a backup, since it's how the new machine reaches the internet at all. Once it's up, the browser's setup wizard offers **"Restore from a backup"** as well as "Start fresh". Most of it happens right there; one step needs a single command typed once over SSH (§4 explains why, in plain words on the screen itself, not just here).

## 2. What's in a backup

Two files, always made together and always used together — mixing an encryption-keys file from one backup with a database file from another silently breaks every stored secret (recovery codes, webhook secrets, everything sealed with ADR-030's key), so restore refuses to proceed unless their pair-id matches:

1. **The database** — a `pg_dump` of the whole Postgres database (control plane's own tables and the `asterisk` realtime schema): people, extensions, devices (only their digest hashes, never a password), phone lines (same), settings, the audit log, alert history. Sealed secrets (webhook signing secrets, TOTP secrets) are in here still encrypted with ADR-030's key — this file alone, on its own, doesn't reveal them.
2. **The keys** — the small set of Docker secrets a restored system can't regenerate and still be the same system: `linx_db_encryption_key` (ADR-030 — without it every sealed secret in the database file is unrecoverable garbage), `linx_jwt_signing_key` (ADR-027 — without it, every previously-issued API key and OAuth access token stops verifying). Kept in its own encrypted file, same as the database file, so a copy of just the database on its own never exposes them.

Both are wrapped into one [restic](https://restic.net) (BSD-2-Clause) repository, one snapshot per backup: restic already does exactly what this needs — content-addressed encryption (ChaCha20-Poly1305/AES-256, your repository password only, nothing readable without it), deduplication (so daily backups of a mostly-unchanged database cost little extra space or transfer), and back ends for a local path, SFTP and any S3-compatible service (AWS S3, Backblaze B2, MinIO, Cloudflare R2, …) without Linx writing its own upload code or its own crypto for this. It's a single static binary; `linx setup`'s prerequisites step gets it the same way it already handles Docker (download, checksum-verified, pinned version).

**Not included, on purpose:** call audio (never stored to begin with), TLS certificates (they renew themselves inside 90 days; a restored system gets fresh ones), the step-ca root key (ADR "Root... exported encrypted for offline backup" already covers it separately — that key protects every device's own certificate too, and deserves to live apart from a routine backup an admin might store somewhere more casually).

## 3. Where it goes (owner decision — restic backend, ADR-055)

One repository, one or more of:
- **Local** — a path on the server's own disk (a second drive is worth pointing this at; the same disk as Postgres isn't real protection).
- **Remote** — SFTP (any server or NAS you can SSH to) or an S3-compatible bucket (access key, secret, bucket, region/endpoint — restic's own flags, entered once and sealed the same way a webhook secret is).
- **Download** — "Back up now" always offers a direct download of that snapshot restored to a single `.tar` the browser saves, no repository needed for this path; useful for a one-off copy before a risky change, without setting up recurring remote storage at all.

More than one destination just means more than one restic repository configured; the schedule runs the backup once and pushes the resulting snapshot to each.

## 4. Restore (owner decision — web setup wizard, ADR-055)

Restoring puts back *everything*: every person, every session ended, every setting overwritten. It has to happen before there's a normal admin session to protect — which is exactly the state the web setup wizard already runs in (docs/ADMIN.md §4), so that's where it lives, as its first screen: **"Set up fresh" or "Restore from a backup"**.

Two steps, because of what's in §2:

1. **The keys, once, over SSH.** `linx setup` already needs one terminal session on the new server; restoring the encryption keys is one more command there: `linx restore-secrets <repository> <snapshot>` (asks for the repository password once, writes the two secret files, nothing else). This can't be a browser action: Docker secrets are files on the host the control plane's own container is deliberately never given write access to or a way to change on the fly (`docs/THREAT_MODEL.md`; confirmed in the pre-launch review, §9) — that boundary is exactly what keeps a compromised control plane from being able to touch its own credentials, and restoring is not an exception worth punching a hole for.
2. **The database, in the browser.** Back in the setup wizard: point it at the same repository (or upload a downloaded snapshot file), give the same repository password, and the control plane restores the database itself, over its own ordinary connection to Postgres — no new privilege needed for this part, since talking to its own database is all it ever does anyway. It refuses if the keys it finds already in place don't match this snapshot's pair-id (an old backup after a newer install, wrong repository, etc.), rather than restoring a database no one can safely decrypt.

The wizard's screen says both steps in plain words, in order, with the exact command to copy for step 1 (docs/ui/ADMIN_SCREENS_PHASE1E.md gets this screen added when the UI step below is built).

## 5. Schedule (owner decision, 2026-09-27)

Off by default. When on: **daily, weekly or monthly**, a time of day the admin sets (weekly also picks a day of week, monthly a day of month), each destination configured tried in turn with the others still attempted if one fails. Keeps the last 14 daily / 8 weekly / 6 monthly snapshots per repository by default (restic's own `forget --prune`, run right after each backup) — changeable later, not exposed as its own setting yet. A failed scheduled backup fires the existing "backup failure" alert (already in the catalog, `docs/API.md` §2, unused until now).

## 6. "Confirm it's you" and the repository password (owner decision, 2026-09-27)

The repository password is generated by Linx (like a device's SIP password, ADR-033) — never typed in by an admin, never weak, never reused between repositories — and shown exactly once, in a "you won't see this again" box, when a destination is first set up. Every action that would show it again, add a new destination, or produce a downloadable backup needs a fresh "confirm it's you" (ADR-053) first: these are exactly as sensitive as a device's login or an API key, for the same reason (whoever holds the password holds the whole system).

## 7. Security

- Restic's own encryption is the only thing that ever protects a backup at rest; Linx doesn't add or substitute its own on top (one well-reviewed scheme, not two).
- A remote destination's own credentials (SFTP password/key, S3 access key) are sealed with ADR-030's key like any other secret Linx must reuse — and are themselves covered by the next backup, so losing the *keys* file (§2) loses the ability to reach a remote repository too, not just to read it.
- Outbound connections to a remote destination go through the same SSRF-guarded client every other admin-supplied URL does (`internal/safehttp`) where restic's own backend allows it (S3-compatible over HTTPS); SFTP is a direct SSH connection to a host the admin named, the same trust level as a WireGuard peer endpoint.
- `linx restore-secrets` is destructive and irreversible (it overwrites the running system's keys); it refuses to run without an explicit `--yes` and prints exactly what it's about to overwrite first.
- THREAT_MODEL.md gets rows for: a stolen backup file (useless without the repository password), a compromised remote destination (can read old snapshots, can't reach the live system), and the restore-secrets step (root-run, host-side, same trust tier as `linx setup` itself).

## 8. Build order (one session each)

1. **Core engine, local only:** `internal/backup` (dump + secrets bundle → restic repository, pair-id, restore reversing it), `linx backup`/`linx restore-secrets` CLI, restic added to `linx setup`'s prerequisites. *(this session)*
2. **Remote destinations:** SFTP and S3-compatible repository configuration, sealed credentials, the outbound guard.
3. **Schedule and admin API:** `pbx_setting`-style backup settings, `GET/PUT /backup-settings`, `GET /backups` (history), `POST /backups` (back up now), `GET /backups/{id}/download`, the "backup failure" alert wired up, "confirm it's you" on the sensitive ones.
4. **Setup wizard restore screen:** the "Set up fresh or restore" first screen, upload/point-at-repository flow, the database-only restore call, the pair-id refusal.
5. **Admin screen:** System → Backups (history, destinations, schedule, "back up now", "download").
6. **Security review + demo.**
