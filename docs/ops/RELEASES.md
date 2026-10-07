# Releases: live, beta and development

*Written 2026-10-05 for going live after Phase 2 (ADR-084). For the owner. `docs/ops/UPDATING.md` is how a server is updated; this is how there comes to be something to update to.*

## The short version

**One branch. Tags make releases. Three channels.**

```
master ──●──●──●──●──●──●──●──●──●──●──▶   every green commit = edge
            │              │
            └ v1.0.0       └ v1.1.0-beta.1
              = stable       = beta
```

| Channel | What it is | Who runs it |
|---|---|---|
| **stable** | A tag like `v1.2.0`. What a business runs. | Your office, and anyone else's |
| **beta** | A tag like `v1.3.0-beta.1`. A release being tried first. | The test VPS; you, when you want to be early |
| **edge** | Every green commit on `master`. | Development only. Never a working phone system |

Nothing is rebuilt when you tag: the images for that commit already exist and were tested by CI, so a tag gives them the release's names and signs them again. **What ships is literally what was tested.**

## Version numbers

Plain semver — `MAJOR.MINOR.PATCH` — and **the first live release is `1.0.0`**. Not 0-point-something: the day your business runs its phones on Linx, the number should say so.

- **A phase is a minor.** 1.1 meetings, 1.2 3CX parity, and so on.
- **A fix is a patch.** 1.1.1, 1.1.2.
- **A major** is for a change that breaks something people depend on. Avoid needing one.

**The one rule that keeps a release safe to undo: a patch never adds a database migration.** Linx's migrations only go forward, so going back a version is only safe when the schema didn't move. A minor may migrate; a patch may not. That means "put yesterday's version back" always works for the releases most likely to need it — the quick fixes.

If a fix genuinely needs a schema change, it is a minor, not a patch. Say so in the notes.

## Cutting a release

1. **Be on a green commit.** `gh run list --limit 1` says success, and the images for that commit are published. The release workflow refuses a tag whose images aren't there.
2. **Decide the number** by the rules above. Check whether anything since the last release added a migration: `git diff --name-only v1.1.0..HEAD -- internal/db/migrations/`. Anything listed means it cannot be a patch.
3. **Write what it means for people** in `docs/release-notes/vX.Y.Z.md` — a few plain sentences or bullets (new, better, fixed), committed before the tag. This is what linxpbx.com's changelog shows. Without it, the changelog lists the release's commit subjects as *Improvements* and *Fixes*, which is accurate but reads like a developer's notes.
4. **Tag and push.**
   ```
   git tag -a v1.2.0 -m "Linx 1.2.0"
   git push origin v1.2.0
   ```
5. **Watch it** (`gh run list --limit 1`). The Release workflow re-tags and signs every image, builds `linx` for amd64 and arm64 from the tag, signs those too, opens a GitHub release with them attached, and then **posts the release to linxpbx.com/changelog** by itself (below).
6. **Try it on the test VPS before your own system**, even for a patch. That is what the VPS is for.
7. **Then update your own** (`docs/ops/UPDATING.md`), after a backup.

A beta is the same with `v1.3.0-beta.1`, and it is marked as a pre-release on GitHub so nobody installs it by accident.

## The changelog on linxpbx.com

Every release, stable or beta, appears at **linxpbx.com/changelog** without anyone doing anything. Once the release is published, the Release workflow's last job runs `tools/changelog/entry.sh`, which writes `site/src/content/changelog/<version>.md` from `docs/release-notes/<tag>.md` (or, if there isn't one, from the commits since the previous release: fixes and improvements, leaving out repository housekeeping), and commits it to `master`. That commit is what makes Cloudflare rebuild the site, so the entry is live a minute or two after the release. A beta is marked **Beta**. To correct an entry later, edit its file and push.

## What a server actually runs

A server **pins one exact version** in `/etc/linx/.env` (`LINX_VERSION=1.2.0`) and never resolves a moving tag at start. A channel decides what an update *offers*; it never changes what is running underneath you.

The `linx` program is the version selector: a `linx` built from the tag `v1.2.0` sets a stack up on `1.2.0` images, and a `linx` built from master sets one up on that commit's. So **download the `linx` from the release you want**, and the rest follows:

```
curl -LO https://github.com/linxpbx/linx/releases/download/v1.2.0/linx-linux-amd64
# check it against SHA256SUMS, then:
sudo install -m 0755 linx-linux-amd64 /usr/local/bin/linx
sudo linx setup --config /etc/linx/setup.yaml
```

`sudo linx setup` on an installed server already notices when the program is newer than what is running and prints the command.

## Putting the previous version back

1. Install the previous release's `linx` (above).
2. `sudo linx setup --config /etc/linx/setup.yaml`.

Safe when **no migration ran** between the two — which the patch rule guarantees for patches. Going back across a minor means restoring the database backup taken before the update (`docs/ops/UPDATING.md`), because the schema moved. This is why the update procedure takes a backup first and why you should not skip it.

## A fix for a live version while master has moved on

Only when it happens, not before:

```
git checkout -b release-1.2 v1.2.0
git cherry-pick <the fix from master>
git push -u origin release-1.2
git tag -a v1.2.1 -m "Linx 1.2.1" && git push origin v1.2.1
```

CI runs on the branch; the release workflow runs on the tag. Delete the branch when 1.3 is out and nobody is on 1.2.

The fix lands on `master` **first**, always, so it can never be lost when the branch goes.

## The app

The iPhone and iPad app has two numbers, and they do different jobs:

- **Version** (`CFBundleShortVersionString`) is the Linx release it belongs to — `1.0.0` alongside the server's 1.0.0.
- **Build** (`CFBundleVersion`) only ever goes up, and **a number can never be reused**, not once, not ever. Build 14 went up on 2026-10-05; the next is 15.

**TestFlight is the beta channel. The App Store is stable.** A beta of the server and a TestFlight build of the app go together by name, which is the point of using the same number.

`docs/ops/APPLE_SIGNING.md` is how a build gets out; `docs/ops/STORE_SUBMISSION.md` is what the App Store asks.

## What to keep an eye on

- **A tag is permanent.** Deleting one that people may have pulled is worse than releasing a fix. If a release is bad, release the next one.
- **Dependabot keeps opening pull requests** against master. They land as ordinary commits and arrive in the next release; nothing special happens for a release.
- **The release workflow signs with cosign**, keylessly, like CI. Anyone can check an image came from this repository's workflow.
- **Pre-1.0 tags do work** (`v0.9.0`) if you want to rehearse the machinery before committing to 1.0.0.
