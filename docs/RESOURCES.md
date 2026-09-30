# Linx — What it needs to run, and making it smaller

*Started 2026-09-28 during the install demo, at the owner's request: "4 GB of memory and 10 GB of disk is an expensive VPS; 3CX runs on half of that." Measurements are from the demo's server (`home.mym.ae`, Ubuntu 24.04, 2 cores, 4 GB, Docker 29.8 with its containerd image store), Linx fully running at rest, Portainer on.*

## 1. What Linx really uses (measured)

**Memory at rest** (`docker stats`, `ps`):

| Part | Memory |
|---|---|
| Database (Postgres) | 61 MB |
| Phone engine (Asterisk) | 40 MB |
| Control plane (web app, API) | 18 MB |
| Internal certificate authority (step-ca) | 13 MB |
| Call-audio relay (coturn) | 8 MB |
| WireGuard agent, certd | 8 + 5 MB |
| **Linx's own services** | **about 155 MB** |
| Docker itself (dockerd + containerd) | about 170 MB |
| Portainer (optional) | 16–76 MB |
| Ubuntu Server's own programs (fwupd, multipathd, ModemManager, udisks2, …) | 300–450 MB |

The server used 812 MB in all out of 3.9 GB. **An echo call (measured 2026-09-29, browser on the home network, Opus end to end):** Asterisk 38 → 45 MB and about 1.2% of one core; the control plane about 0.2%; the whole server 97% idle. A call that needs converting between audio formats, or that goes through the audio relay from outside, costs more processor time.

**Disk:** images 1.8 GB, stored twice by Docker's containerd image store (0.46 GB compressed plus 1.3 GB unpacked), volumes 69 MB, Docker's programs about 250 MB. Setup added about 2.3 GB to the disk in all.

**Processor:** under 2% of one core at rest.

## 2. Done (2026-09-28)

| Change | Saves | Compromise |
|---|---|---|
| Setup's minimums from the measurements: **1 GB memory** (was 4 GB), **5 GB free disk** (was 10 GB); 2 GB and 20 GB recommended (a warning below) | lets a 1–2 GB VPS run Linx | none: the phone system fits with room to spare |
| **1 GB swap file** on servers under about 2 GB without swap (swappiness 10: used only when memory is really short); never in a container | stops a busy moment or an update from killing a service | a little disk |
| Call-audio relay image on coturn's official **Alpine** variant (same coturn release) | 294 → 101 MB | none (tested with the real TURN test; browser suite in CI) |
| Asterisk's programs **stripped of debugging data** (`--strip-debug`: function names kept, so its own backtraces still read) | 358 → 279 MB | only matters to someone attaching a debugger to production |
| After every setup or update, **remove the previous versions' Linx images** (only images labelled as Linx's, only unused ones) | about 450 MB per update that would otherwise pile up | none |
| Internal names never completed with the host's search domain (`dns_search: .`) | (a correctness fix found while measuring) | none |
| **Database on Alpine for new installs** (2026-09-30, item 1 below): `postgres:18-alpine` with Postgres's own built-in C.UTF-8 locale (`POSTGRES_INITDB_ARGS`), both images Postgres 18.6. Setup records the choice in setup.yaml (`database.image`): a new install gets Alpine; an install that already has a database keeps Debian (`installer.DecideDatabaseImage`) and moves by restoring a backup onto a fresh install (checked: a Debian dump restores onto Alpine and its indexes pass `bt_index_check`) | image 666 → 425 MB (measured, unpacked); memory at rest 65 → 27 MB | the few name-sorted queries use `COLLATE "unicode"` (sorting only, never in an index), since the built-in locale orders by character code ("Zoë" before "alice") |
| **Go services' memory** (2026-09-30, work queue item 3): each password check (Argon2id, 64 MB by design) hands its memory back right after (`auth.idKey`, `debug.FreeOSMemory` after the answer, one at a time); `GOMEMLIMIT` at about 3/4 of each Go container's `mem_limit` (control plane 200 MiB, step-ca 96, certd 48, WireGuard agent 48; tested below each limit) | measured on the home server: after one password check the control plane stayed at **92 MB** before, **25 MB** after (at rest 25–28 MB); at rest step-ca 11 MB, certd 6, WireGuard agent 7. (The 88/57 MB seen on the first VPS was the same effect: memory kept after sign-ins) | one garbage collection per sign-in, after the answer; Argon2's 64 MB stays (the owner may lower it to OWASP's 19 MB minimum: not recommended) |
| **Admin pages load only when opened** (2026-09-29, Phase 1E step 7): the admin area and the setup wizard are separate files fetched on demand, not part of what every signed-in person downloads | a signed-in page load: 232 → 168 KB compressed (app 107 → 84 KB, signed-in part 125 → 84 KB, *before* the step's five new pages, which alone would have added 18 KB); an admin opening Phone lines fetches 12 KB more | a moment's "loading" the first time an admin page opens |

## 3. Proposed (decisions noted per item)

1. **Database image: `postgres:18-alpine` instead of `postgres:18`** (650 → about 280 MB). *The catch:* Debian's Postgres sorts text with the system's C library (glibc), Alpine's with another (musl). Moving an existing database between them silently breaks the order its text indexes were built in, which Postgres can't detect. So:
   - New installs: create the database with Postgres's own built-in locale (`--locale-provider=builtin --builtin-locale=C.UTF-8`, Postgres 17+), which doesn't depend on the C library at all, then use the Alpine image. Safe for good, and future image changes can't hit this again.
   - Existing installs (the lab server, this demo server): keep the Debian image until they're moved over with Linx's own backup and restore (dump and reload, which rebuilds every index), or keep it for good. Both images would be supported; setup picks by what the database was created with.
   - *Recommendation:* yes, for new installs now; existing ones move on their next restore. Saves 370 MB for every new server.
   - **Owner decision (2026-09-29): yes, as recommended. Built 2026-09-30** (§2).
2. **Docker's image store:** Docker 29 keeps each image twice (compressed and unpacked). Switching a server to the classic storage (`"features": {"containerd-snapshotter": false}` in daemon.json) saves about 460 MB here. *The catch:* switching hides the images already downloaded (they're downloaded again), and it goes against where Docker is heading. *Recommendation:* not now; revisit if disk is the limit on real small servers.
3. **Trimming Ubuntu Server's own background programs** (fwupd, ModemManager, udisks2, upower, multipathd: 100–150 MB of memory, none of which a server needs). *Recommendation:* not by setup (it's the owner's operating system, not Linx's); a one-paragraph "make a small VPS smaller" note in the help guides instead.
4. **Profiles that actually change something:** the "lite/standard/performance" size chosen in setup is saved but changes nothing yet. Once meetings (LiveKit) and recordings arrive, it should decide what runs and with how much memory. Nothing to gain from it today.

## 4. Minimum and recommended hardware (as built)

| | Minimum (setup refuses less) | Recommended |
|---|---|---|
| Memory | 1 GB (setup adds 1 GB swap under about 2 GB) | 2 GB |
| Free disk | 5 GB | 20 GB (backups; later recordings and voicemail) |
| Processor | 1 core, 64-bit (amd64 or arm64) | 2 cores (Opus calls that need converting use processor time) |

Measured, not guessed: the owner's 1-core, 2 GB VPS is the demo's second server (Part A of `docs/DEMO_INSTALL.md`).
