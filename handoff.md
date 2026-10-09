# Handoff

**What this file is for.** The first thing to read when a session starts (or after `/clear`), and the last thing to update before one ends. It carries only what a fresh session needs to pick the work up: where we are, what is in play, what has already been tried and failed, and what comes next. The long record lives in `docs/HISTORY.md`; the rules live in `CLAUDE.md`. If this file and those disagree, `CLAUDE.md` and `docs/HISTORY.md` win and this file should be corrected.

Last updated: **2026-10-09** (see §6 "2026-10-09 — start here"); before that **2026-10-05 (late)**, after Phase 2 was built out to **step 10**, **TestFlight build 14** was uploaded, **release management** was settled (ADR-084), and a **full pre-production security pass** was done — static review, fuzzing, image scans, and active testing against a local throwaway — with every finding fixed. The next move is the owner's: update `home.mym.ae`, run the Phase 2 close-out demo, then tag **1.0.0** and go live.

---

## 1. Goal

**Linx** is a self-hosted, open-source (Apache-2.0) 3CX-style phone system: PBX, video meetings, guest links, presence. One owner, who is not a developer: explain in plain language and always recommend an answer. Full brief in `linx-build-prompt.md`, design in `docs/`.

**The goal right now** is **going live with the server at 1.0.0 first, then handling the app.** The owner decided (2026-10-05) to **cut `v1.0.0` and bring it up on a fresh server now, and handle the iPhone/iPad app (TestFlight → App Store) and its hands-on demo rows afterwards** — the server side of Phase 2 is tested by CI; the app-dependent close-out checks (ringing, video, fold) move to the app stage. Then add each later phase gradually. Phase 1 (server, web client, trunks, admin, email/voicemail/history) and Phase 2's build (steps 1–10) are done.

The standing constraints that shape every change: smallest possible processor, memory, disk and bandwidth (measure, record in `docs/RESOURCES.md`); never weaken TLS or fall back to plaintext; plain-language copy; design tokens only.

---

## 2. Current state

**Phase 2 steps 1–10 are all built.** What stands between here and **Linx 1.0.0 live** is the owner's close-out demo (`docs/DEMO_PHASE2.md`), which needs `home.mym.ae` updated first. Steps 8–10, release management and the whole security pass all landed on 2026-10-05; CI green throughout (last push `e500ad0`, CI running at session end — check it).

**The app is on TestFlight as build 14** (0.1.0 (14), delivery `dd4647ed`, signed *Apple Distribution*, `aps-environment = production`). It is the first build carrying step 8's iPad/fold screens, step 9's Person/Linx-server lines in Settings, the privacy manifest, and step 10's VoIP-push fix. **The next upload is build 15** (a number is used once, ever). Standing permission to upload in this round still holds; the App Store listing and Submit stay the owner's.

**`home.mym.ae` (192.168.1.213) is still on `25587b4`, schema 45 — NOT updated for anything after step 8.** It lacks `*97`, the message light, the perl-free Asterisk image and the 413 fix. **Updating it is the gate for the close-out demo and for going live** (back up first, `docs/ops/UPDATING.md`). `linx doctor` green apart from two standing warnings (CA root-key backup on the server; no API key).

**What was built/decided this session (all pushed):**
- **Step 9 — lifetime & loss, + its security review** (`docs/PHASE2.md` "Step 9, as built", `docs/THREAT_MODEL.md`). Proved the five things that stop a phone; a stopped/expired phone now loses its Apple tokens; the moved-server checklist counts app phones apart from desk phones; Settings shows Person + Linx server; the CA is deliberately **not** pinned.
- **Step 9b — `*97` and the message-waiting light** (ADR-083). Dial `*97` to hear your own messages (1 again / 2 next / 3 delete); the desk-phone light is a count Linx gives Asterisk over ARI. **ARI is read-write now.** Needs the new Asterisk image.
- **Step 10 — the finish**: `docs/ops/STORE_SUBMISSION.md`, `docs/RESOURCES.md` §3b (phase cost + data-per-minute), `docs/DEMO_PHASE2.md` (the close-out demo, every owner-facing check in one sitting). Gap closed: the app had no `PrivacyInfo.xcprivacy` — added, ships at bundle root.
- **Release management (ADR-084, `docs/ops/RELEASES.md`)**: one branch, tags make releases, three channels (stable/beta/edge), semver, **first live release 1.0.0**, **a patch never adds a migration**. `.github/workflows/release.yml` re-tags + signs the tested images on a tag; `installer.ImageTag` follows a release version.
- **Security pass — four parts, all findings fixed:**
  - *Pre-production static review* (`docs/THREAT_MODEL.md`): all 218 API ops by scope, CSRF, Help isolation, logging, ports, deps — clean. Owner decisions: CA root key off the server "when it's time"; no second restore rehearsal; admin-from-anywhere stays (resting on 2FA/passkeys — note an admin can still drop to password-only via a warning; a "require a second step" setting is recommended, not built).
  - *Fuzzing* (`internal/siprelay/fuzz_test.go`, `internal/voicemail/fuzz_test.go`): found + fixed a `uriUser` parser differential; seed corpora run in CI.
  - *Image scans (Trivy, full)*: **purged perl from the Asterisk image** — killed a CI time-bomb (seven fixed-but-unavailable perl CVEs) and its recurring CVE stream; all images now scan clean of fixed HIGH/CRITICAL.
  - *Active testing against a local throwaway*: scope/lockout/TLS/headers/resilience all held; found + fixed oversized requests returning **401 instead of 413**. Also fixed a real iOS compliance bug — a VoIP push that couldn't be read as a call **reported nothing to CallKit** (which gets an app killed); it now reports-and-ends.
- **Leanness review** (`docs/RESOURCES.md` §3c): Linx is already lean; sized the DB pool (`internal/db/config.go`); everything else examined is documented with why not to cut it.
- **Scope decisions**: the remote-office box is **"Linx Connector", now Phase 4**; desk-phone **auto-provisioning is Phase 4**; **desk phones working from outside the network (with or without an SBC)** is an unscheduled requirement in `docs/ROADMAP.md`.

---

## 3. Active files

The ones in play this round. `codegraph explore "<names>"` is faster than grep for any of them.

**The app's call path** — `ios/Linx/Core/Media/WebRTCMedia.swift` (ICE, SDP, the picture, diagnostics, the audio session), `VideoQuality.swift` (the quality ladder and the relay ceiling, ADR-081), `Camera.swift` (capture + `CallVideo`), `RelayCertificates.swift` (ADR-080), `SDPTweaks.swift`, `Core/SIP/SIPUserAgent.swift` (re-INVITEs, `dropVideo`), `Core/Call/*` (CallKit), `Core/Push/*`.

**The app's screens** — `ios/Linx/Features/Phone/PhoneModel.swift` (the one place a call's state and the screen rules live), `CallView.swift`, `VideoCallView.swift`, `CallDetailsView.swift`, `AudioRouteButton.swift`, `CallLayout.swift`.

**Step 8's screens (new, 2026-10-05)** — `ios/Linx/App/Crease.swift` (the fold, and the only file with iOS 27.1 in it), `Features/Home/BigScreen.swift` (the shared two-column pieces), `Features/Phone/CallAlongside.swift` (a call beside the app), and the split views inside `CallsView.swift` (`CallDetailView`), `TeamView.swift` (`PersonView`), `HomeView.swift` (`SpeedDial`, `MoreView`).

**Tests** — `ios/LinxTests/SoundOnTheRoadTests.swift` (this round's rules), `VideoTests.swift`, `CallTests.swift` (holds `FakeMedia`), `RingingTests.swift`, `BigScreenTests.swift` (step 8's rules). 137 app tests.

**Server side touched this round** — `internal/turnconf/turnconf.go` (`MaxBPS`), `internal/turn/turn.go` (`MaxBitrate` → `max_bitrate_bps`), `api/openapi.yaml` + `services/control-plane/api/webphone.go`, `internal/asteriskconf/config.go` (the dialplan, unchanged today but read often).

**Step 9b's files (2026-10-05)** — `internal/voicemail/listening.go` (`*97`: the session, the keys, writing a message out for Asterisk) and `light.go` (the mailbox counts), `internal/ari/ari.go` (`Do`, the write side), `internal/pbx/calls.go` (`Stasis` and `Registered` hooks), `internal/asteriskconf/config.go` (`*97` → Stasis, `read_only = no`, `PlayDir`), migration `0046_message_light.sql`, `deploy/docker/asterisk.Dockerfile` (six modules), `tools/prompts/prompts.tsv` + `generate.sh`, `deploy/compose/compose.yaml` (`voicemail-play`).

**Step 9's files (2026-10-05)** — `internal/store/enroll.go` (`forgetPushTokensTx`, called from `ExpireIdentities`, `expirePhonesTx` and `RevokeDevice` in `internal/store/pbx.go`), `internal/moved/moved.go` + `internal/store/moved.go` (`AppPhones` apart from `DeskPhones`), `ios/Linx/Features/Settings/SettingsView.swift`, `ios/Linx/Core/PhoneIdentity.swift` (the unused CA root, and why). The lifetime rules themselves are in `internal/enroll/` (`device.go`, `enroll.go`), `services/control-plane/sip.go` (`checkPhoneLine`, the 15-second re-check) and `services/control-plane/main.go` (`stopPhoneLine`) — none of which needed changing.

**Release & security files (2026-10-05)** — `.github/workflows/release.yml` + `internal/installer/stack.go` (`ImageTag`/`releaseVersion`, a tag sets the stack's version) for releases (ADR-084, `docs/ops/RELEASES.md`); `internal/siprelay/fuzz_test.go` + `internal/voicemail/fuzz_test.go` (seed corpora run in CI); `internal/siprelay/sipmsg.go` (`uriUser` hardened); `deploy/docker/asterisk.Dockerfile` (perl purged); `internal/apihttp/bodylimit.go` (413 up front); `internal/db/config.go` (pool sized); `ios/Linx/Core/Push/PushService.swift` + `Features/Phone/PhoneModel.swift` (`wokenByNothing`); `ios/Linx/PrivacyInfo.xcprivacy`. New docs: `docs/ops/RELEASES.md`, `docs/ops/STORE_SUBMISSION.md`; big additions in `docs/THREAT_MODEL.md` and `docs/RESOURCES.md`.

**Copy and docs that must keep up** — `web/src/screens/PhoneLines.tsx` (the Caller ID hint), `web/src/lib/gatewayHowTo.ts`, `docs/help/phone-system-or-gateway.md`, `docs/help/iphone-and-ipad.md`, `docs/TEST_MATRIX.md`, `docs/PHASE2.md`, `docs/DECISIONS.md`, `docs/TRUNKS.md`, `docs/RESOURCES.md`, `docs/HISTORY.md`, `CLAUDE.md`.

---

## 4. Changes made (the whole project)

- **Every country (ADR-085), 2026-10-07.** Any of libphonenumber's 245 regions as the home country. All 245 were proved on 2026-10-07: about 7.5 M numbers, with the database and the library agreeing on every one. The Docker test now checks every country (about 3 min). `GET /api/v1/countries`. `call_permission_level.abroad_countries` (migration 0047, schema **47**) means calls abroad go everywhere or only to the listed countries; widening needs "confirm it's you", including in an undo. The wizard's country step saves country-suited ranges (200–699 in North America). Outgoing calls can change the country in place and offers "Every country" / "Only the countries I choose". 38 busy tones (37 from Asterisk 22.11). Country names take "the" in sentences.
- **Website: help guides, changelog, TestFlight requests, privacy — 2026-10-07.** `docs/help` is the single source; `site/scripts/sync-help.mjs` publishes it at /docs/<guide>. The /changelog page is written by the Release workflow. /beta and /privacy. The site is on **Astro 7.3.6**, and `make security` now audits `site/` too.
- **Security, 2026-10-07:** source-map-js 1.2.2 in `web/` (GHSA-68fv-2mgg-jv7q had turned CI's security job red); the site's Astro/esbuild/sharp advisories fixed (sharp held at 0.35.5 by an npm override).

- **Product website (`site/`, Astro) for linxpbx.com — 2026-10-07.** Landing + Download + a docs/help section (Markdown under `site/src/content/docs/`). Brand from `design/tokens.json`; static, near-zero JS; no sideways scroll checked at 390px; deps pinned; telemetry off. Deploys on **Cloudflare Pages** (root `site`, build `npm run build`, output `dist`) via git-push auto-deploy — updating content = a commit. `site/README.md` has the one-time Cloudflare connect + DNS steps. **Pending (owner, one-time):** connect the repo in Cloudflare Pages and add `linxpbx.com` + `www`. `npm run dev` / `npm run build` in `site/`.

Phase by phase, each finished with a demo the owner approved. **Every step's detail is in `docs/HISTORY.md`** — this is the map, not the territory.

| | What was built | Where |
|---|---|---|
| Phase 0 | Skeleton, CI, `linx setup`, certd, step-ca, `linx doctor` | `docs/DEMO_PHASE0.md` |
| 1A | Database, API, auth, webhooks, admin alerts | `docs/DEMO_PHASE1A.md` |
| 1B | The phone engine: Asterisk, realtime views, devices, calls | `docs/DEMO_PHASE1B.md`, `docs/PBX.md` |
| 1C | Web client, accounts, `/sip` relay, coturn, front doors | `docs/DEMO_PHASE1C.md`, `docs/WEB.md` |
| 1D | Trunks, numbering and outbound routing, WireGuard, firewall | `docs/DEMO_PHASE1D.md`, `docs/TRUNKS.md` |
| — | Pre-launch security audit; backup and restore; web-first install; help pages | `docs/THREAT_MODEL.md`, `docs/DEMO_BACKUP.md`, `docs/INSTALL.md`, `docs/HELP.md` |
| 1E | Admin portal, setup wizard, passkeys, company sign-in, phone-line screens | `docs/DEMO_PHASE1E.md`, `docs/ADMIN.md` |
| 1F | Front doors, DNS, public port, email, password reset, ring groups, office hours, voicemail, call history, undo — **and Phase 1's exit test** | `docs/DEMO_PHASE1F.md`, `docs/PHASE1F.md` |
| After 1 | Busy-tone detection on gateway lines (ADR-072); update notice in `linx setup` | `docs/HISTORY.md` "After Phase 1" |
| 2, steps 1–4a | App skeleton, server enrollment (Secure Enclave device certificates), the Add-phone screens, the app signs in | `docs/PHASE2.md` §12 |
| 2, step 4b | The app makes and takes calls: its own SIP over `/sip`, Google WebRTC, the browser's Opus settings | |
| 2, step 5 | The push gateway (`internal/push`), holding a call while a phone wakes, quiet notifications | |
| 2, step 6 | A sleeping phone rings: PushKit → CallKit first, manual audio, ADR-078 for China | |
| 2, step 7 | The whole app: Calls, Team, Keypad, More; 1:1 video added to a call (ADR-079); the adaptive call screen | |
| 2, on devices (2026-10-04/05) | Everything the owner found on real hardware, builds 4–13: see §5 and `docs/HISTORY.md` | |
| 2, step 8 | The iPad and fold layouts: every tab a list beside a detail, a call beside the app, the keypad's speed dial, and the fold's own geometry (ADR-082) | `docs/PHASE2.md` "Step 8, as built" |
| 2, step 9 | Lifetime and loss proved end to end; a stopped phone leaves no Apple token; the moved-server checklist tells the truth about app phones; the security review and its STRIDE rows | `docs/PHASE2.md` "Step 9, as built", `docs/THREAT_MODEL.md` |
| 2, step 9b | `*97` plays your own messages from a desk phone, and the message-waiting light follows them; ARI becomes read-write | `docs/PHASE2.md` "Step 9b, as built", ADR-083 |

**Decisions of record:** `docs/DECISIONS.md`, ADR-001 … **ADR-082**. The newest matter most here — 078 (China/CallKit), 079 (a picture is added to a call), 080 (iOS's trust store for the relay's certificate), 081 (the adaptive picture).

---

## 5. Failed attempts (the whole project)

- **A long batch of commits can hide a red CI (2026-10-07).** A new npm advisory turned the security job red on a docs/website commit, and later pushes cancelled that run before anyone looked. Check `gh run list` for *failure*, not just the newest run's status. Also: `make security` didn't cover `site/` until that day.
- **Astro 7 doesn't run remark plugins by default** (its new Markdown engine). It needs `@astrojs/markdown-remark` installed, or the build stops at config time.
- **A context7 version can lag.** It gave Astro 6.3.1 when 7.3.6 was current. Check `npm view <pkg> version` before pinning.
- **YAML reads guide front matter differently from the server.** `*43` is an alias and `999` a number. The website rewrites front matter to strict YAML in `sync-help.mjs` rather than changing the guides.

- **cosign v3 `sign-blob` broke the first `v1.0.0` release run (2026-10-05).** The `program` job failed: *"must specify --bundle with --new-bundle-format"*. cosign v3 makes `--new-bundle-format` the default for `sign-blob` and drops the old `--output-signature`/`--output-certificate` pair. Fixed in `.github/workflows/release.yml` to emit one `<file>.sigstore.json` bundle per binary (verify-blob example in the step comment). The image-signing step uses `cosign sign` (OCI) and was fine. Lesson for any future cosign bump: `sign-blob` and `sign` have different flag surfaces.

The useful half of the record: **do not try these again.**

**Guesses that cost a build each (the "no sound from outside" hunt, 2026-10-04/05)**
- *Stale TURN credentials* — real bug, fixed, **not** the cause of the silence.
- *A longer wait for ICE candidates* (3 s → 10 s) — same: a real improvement, not the cause.
- The actual cause was **WebRTC's own bundled root certificates**, which are older than Let's Encrypt's 2026 chain, so every `turns:` connection failed and no relay route was ever found. Fixed by **ADR-080** (iOS's trust store, pinned to the relay hostname). **Lesson, now a standing rule: for anything only a real device shows, ship a diagnostic first** — the Call details screen is what finally named it.
- *Setting the audio session's category from CallKit's `didActivate`* (a guessed loudspeaker fix) **silenced calls in both directions** on a real iPhone. Never touch the category or mode while CallKit owns the session. The same rule caught the camera later: WebRTC's capturer gives its capture session an audio session of its own, which took the AirPods off the call — the camera must borrow the call's session and never configure it.
- *`tlsCertPolicy = .insecureNoCheck`* — used once, locally, as the experiment that proved the certificate was the cause. It must never ship, in any form.

**Designs tried and rejected**
- *Driving the call screen from the frame counter* → the app flipped between the voice and video screens every few seconds. *Driving it from the SDP alone* → it stuck on a video screen with nothing in it. The answer was **both, separately**: the SDP says whether a picture is meant to be there, the frames say whether it really is (`CallVideo.theirs` vs `theirPicture`), and a picture nobody is sending leaves the call by re-INVITE.
- *A fixed 600 kbit/s video ceiling* — a good link never got a better picture and a relay with a smaller cap froze. Replaced by ADR-081's ladder. Within the ladder, *"down at once"* was also wrong: the link's spare room reads low for the first seconds of a picture, so it made every new picture worse. Now two tight readings, and a settling period.
- *`bundlePolicy = .maxBundle`* for the app — Asterisk's answers carry no BUNDLE group, so the negotiation would fail outright. That is why a second video stream gathers its own relay route (and why turning video on takes a moment on mobile data).
- *Inferring "Not now" from a SwiftUI alert's dismissal* — a picture arriving changes the screen, the sound route and the CallKit call at once, and any of those closed the alert and answered the question for the person. Only the buttons may answer. And an alert attached to a view whose identity changes (`Group { if … }`) goes away with it: use one stable container.
- *Asserting `UIApplication.isIdleTimerDisabled` in a unit test* — iOS honours it only while the app is in front. Keep the app's own decision and assert that.
- *No Docker control in the admin UI* (owner's standing decision): no embedded Portainer, no socket for the control plane. System → Status does health, logs and Restart through an allowlisted host helper.
- *Resetting 2FA with a password reset* — asked for, not done: it would defeat 2FA.

**Server-side things that looked like Linux bugs and weren't**
- *"Extension 201 isn't allowed to call out"* — Linx allowed every one of those calls. The **UCM** refused them, by caller ID: Linx presents a person's **own number** when they own one of the line's numbers, so ext 200 (which owns the DID) got out and 201 did not. Setting the line's Caller ID to `042340100` **did not work** either — that UCM's pattern is `_+97142340100`, and the two are one number to a person and two patterns to a gateway. `+97142340100` fixed it. Tell the two refusals apart in Calls: **not permitted** is Linx's own permission level, **no answer** is a gateway refusing with early media and a 603.
- *Row 2.4, a force-quit phone not ringing* — not the app: the **Asterisk image** lacked `func_devstate` and `func_logic`, so the wake step read empty and no push was ever sent.
- *The relay froze video* — coturn's `max-bps` was sized for Opus in Phase 1C and never revisited. 64 → 160 kB/s.
- *UDP 443 answering from the wrong machine* — the owner's router forwarded it to an older Linx; TLS 443 was always fine. They have since switched that forward off.

**Step 8's own dead ends (2026-10-05)**
- *`$(inherited)` inside a conditional build setting* — `SWIFT_ACTIVE_COMPILATION_CONDITIONS[sdk=…] = "DEBUG $(inherited)"` does **not** inherit from the parent level: it expands to the **unconditional value at the same level**, so the flag it was meant to drop came straight back. The conditional variants spell out what they want and use no `$(inherited)`. Check it with `xcodebuild -showBuildSettings`, never by eye.
- *Looking for a way to unfold the simulator from the command line* — there isn't one on the 27.1 beta: no fold verb in `simctl`, nothing in `simctl ui`, no fold strings in CoreSimulator or SimulatorKit, and **Xcode 27 has no Simulator.app at all** (simulators live in DeviceHub now), so there is nothing to drive with AppleScript either. The Duo's inner screen is photographed by hand; `BigScreenTests` is what holds that layout to its rules.
- *The `-sideways` shots on the iPad* come out upright: `Screen.turnTheScreenForTheShot`'s `requestGeometryUpdate` doesn't take on that simulator, so `in-call-sideways` and `video-call-sideways` in `iPad-Pro-13-inch-M5/` are duplicates of the upright ones. Pre-existing, not step 8. The landscape layouts are covered by `CallLayoutTests` and `BigScreenTests`; turn the simulator by hand if a picture is wanted.
- *Claiming the unfold transition works* — it is **designed** for (the steps agree at each stage, the call lives in `PhoneModel` and cannot drop, the app never moves between containers, and the in-call keypad and Call details were moved into the model so a reflow can't shut them), and `UnfoldingTests` walks shut → open → open-with-the-fold-known → shut. But **nobody has watched it happen**: the simulator's fold can't be driven from the command line and no real Duo exists. The nearest thing that can be watched today is dragging an iPad's Split View divider while a call is up — `docs/TEST_MATRIX.md` row 4b.8a. Don't write it up as proven.
- *Reading anything into the iPhone Duo simulator's **outer**-screen shots* — the app's own content comes out upright and correct, but iOS's status bar and the tab bar are drawn turned 90° in the capture. Nobody has a real Duo, so whether that is the simulator's presentation of that display or something the app should answer differently is **unknown**; it is not something step 8 changed, and the app is portrait-locked on a phone by the owner's own ask. Don't "fix" it blind.
- *`#expect(x == 540.0 / 1080.0)` in swift-testing* — failed against a value that is exactly 0.5. Division of literals inside the macro's expansion doesn't compare as you'd expect; bind the value and compare with a plain literal.

**Step 9b's own dead ends (2026-10-05)**
- *Assuming `Stasis()` is there because `res_stasis` is* — the dialplan **application** `Stasis` is its own module, `app_stasis`, and the image enables applications one by one. Without it the dialplan says "No application 'Stasis'" and the caller gets a 603 decline. The call suite now checks every **application** the dialplan calls as well as every function, which is the general form of this.
- *Writing the message file 0640, as the greetings code does* — passed on this Mac (Docker Desktop ignores uids on a bind mount) and failed in CI, where Asterisk's own uid genuinely couldn't read it: `res_stasis_playback: Playback failed for sound:/var/lib/linx/voicemail-play/…`. It is now 0644, because the **folder** is what keeps a message private (2750, only the control plane and Asterisk), not the file's mode. **Anything the control plane writes for Asterisk to read has this trap**; the local run can't show it.
- *Reusing `MarkVoicemail(…, heardBy nil, …)` to mean "heard"* — its nil means the **opposite**: mark it new again, which is what the web app's **Mark as new** does. On an extension with no person (`*97` passes nil then) every message played was quietly un-heard, so the light never went out. `HeardVoicemail` is the verb that means heard, by somebody or by nobody in particular; the store's docker test now holds both meanings apart.
- *Playing a message by the path the control plane wrote it to* — the name goes to **Asterisk**, so what matters is the folder's name in *its* container. In Linx both are `/var/lib/linx/voicemail-play` and it works by accident; the call suite mounts a folder of its own and it didn't. `Listening.DirForAsterisk` now says the name to play by, and the unit test uses a different one from the write path so this can't come back.
- *Routing playback events by `ev.Channel`* — a `PlaybackFinished` carries **no channel at all**, only the playback and `target_uri: "channel:<id>"`. Everything played, the session then waited for an event that never came, and the call sat there until SIPp gave up at 90 s. The fake phone engine in the unit tests now sends playback events the way Asterisk does.
- *Asserting the mailbox count with `asterisk -rx "mwi show mailbox …"`* — nothing matched. The count is read back over ARI instead (`GET mailboxes/<id>`), which is the same interface that set it and has a shape worth asserting on.
- *Running `make prompts` and expecting only the new messages to change* — the voice is **not** bit-for-bit repeatable, so it quietly re-recorded all eight existing messages, including ones the owner has heard and approved. The generator now leaves any message that already has a file; to change one, delete its `.g722` first. If a diff ever shows an untouched message changed, that is this.
- *Reaching for `app_voicemail` because it already has `*97` and MWI* — it would mean a second message store, its own folders and its own idea of what is new, directly against ADR-069. `res_mwi_external` gives the light alone, with the count coming from Linx, and the `*97` menu is a few seconds of ARI call control.
- *Letting the dialplan play the messages* — it would need the `linx_asterisk` role to write to voicemail (mark heard, delete), the opposite of ADR-070's "a taken-over phone engine can rewrite nothing". The control plane answers the call instead.
- *Marking a message heard whenever its playback call returned* — pressing 2 halfway counted as hearing it. `play` now says whether the sound actually reached its end, and only that marks it heard.
- *Inserting a test voicemail with a made-up `source`* — `voicemail_message.source` is checked against Asterisk's call-id shape (`^[0-9]{1,12}\.[0-9]{1,10}$`), so "test-<uuid>" is refused by the database.

**Step 9's own dead ends (2026-10-05)**
- *Making the app check the CA it was given* — the obvious reading of §4 ("the app notices if Linx's own CA is ever replaced"), and wrong. Linx finds a phone by the **fingerprint of the certificate it issued**, never by validating a chain, and the app's connection is already proved by the server's public certificate; so the check would only ever fire when the internal CA legitimately changed, bricking every phone on a re-installed or restored server, and would stop nothing, because anyone able to answer for that server would hold its public certificate. Don't add it. The root stays in `PhoneIdentity`, unused, with the reason written next to it.
- *Treating a stopped phone's Apple token as harmless to keep* — nothing is sent to it (`device_wakeable` names only phones that are still set up), so it isn't a bug, but holding a way to reach a phone somebody has lost is not defensible. Cleared on revoke and on expiry; **kept** when the person is merely disabled, because their phones come back with them.
- *Assuming a review of built code finds nothing* — it found that the moved-server checklist told admins to give app phones the new address, which cannot be done. The code was right about everything it enforced and wrong about what it told a person to do.

**Security pass dead ends (2026-10-05)**
- *Active-testing external hosts is off the table.* `home.mym.ae` is the live phone system (no floods) and the hosted VPS can't be scanned without the provider flagging it; the harness also blocks pointing tools at external hosts. Active testing uses a **local throwaway** full stack (loopback Docker) — stand it up via the browser-suite harness, not by hand.
- *`uriUser` must refuse what it can't read identically to Asterisk.* The fuzzer found a differential (`sip:>@`); it is fixed by rejecting any user part with a structural/odd byte. Don't loosen that charset.
- *Purge perl **after** `adduser`.* `adduser`/`addgroup` are perl scripts; the `dpkg --purge perl-base` must be the last step of the runtime `RUN` or the build fails 127.
- *Oversized request → 401, not 413, was a validator artifact.* The OpenAPI validator reads the body during its security phase, so `MaxBytesReader` surfaced as "unauthorized". Fixed by rejecting an over-cap Content-Length up front in `LimitBody`.
- *A VoIP push that can't be read as a call must still report one to CallKit* — reporting nothing gets the app killed by iOS. `PushService.onNothingToRing` → `PhoneModel.wokenByNothing` reports-and-ends.

**Environment traps**
- SwiftPM's binary download of WebRTC **hangs on this Mac** (a per-program firewall, most likely): `make ios-deps` fetches it with `curl`, pinned and checksummed.
- `TestProbePlain` fails locally whenever the dev stack's Asterisk is publishing `127.0.0.1:5061`. CI is fine. Not a regression.
- The browser suite's passkey test flaked once on the `public-port` door; a rerun passed.
- A TestFlight **build number can only be used once, ever** — always bump.
- Xcode can lose its Apple ID (it did today): `exportArchive: No Accounts` / no "iOS Distribution" certificate. Only the owner can sign in again; after that `make ios-archive && make ios-upload` works.
- Help guides: **bold** in a guide must name a real on-screen label — use *italics* for emphasis, or `make test` fails.
- `make lint` is not enough. Run `make test` too (CI caught a guide once because that was skipped).

---

## 6. Next step

**▶ 1.0.0 re-released 2026-10-07 (owner's decision: replace it while unused).** Downloads showed only Claude's own test fetches, so the old `v1.0.0` release was deleted and re-cut at **`2ff6506`** (every country, ADR-085; help guides on the website; coming-soon labels). Every Release job succeeded, including the first run of the new **`changelog`** job, which committed `99022ea Changelog: Linx 1.0.0`; linxpbx.com/changelog shows it. The GitHub release uses `docs/release-notes/v1.0.0.md`. **The next release is 1.1.0 or a patch:** write `docs/release-notes/<tag>.md` first (`docs/ops/RELEASES.md`). The owner's fresh install of 1.0.0 (step 3 below) is still to do.

**▶ 2026-10-09 — start here.** (1) **Build 16 is on TestFlight** (the owner uploaded it from Xcode's Organizer). It fixes build 15's two faults on home Wi-Fi: a working call was moved onto the relay 2 s after the answer, before its sound had started, onto a relay that refuses the server's LAN address, leaving it silent. The speaker also switched itself off at the answer (WebRTC re-set the category with its own options). `docs/PHASE2.md` "Build 15 at home…". **Next upload: build 17.** (2) **Signing without Xcode's sign-in:** `make ios-archive` now uses an App Store Connect key with the **Admin** role when `LINX_ASC_SIGN_KEY_ID` is set in the git-ignored `ios/signing.local.mk` (that file already holds the upload key's IDs). The existing upload key can't sign ("Cloud signing permission error"). **Done 2026-10-09:** key `C9ZAGUP2N8` (Admin) is in `~/.appstoreconnect/private_keys/` (chmod 600) and in `ios/signing.local.mk`. `make ios-archive` signed build 16 with it and nobody signed in to Xcode, so uploads need no owner step any more (still run outside Claude's sandbox). (3) Go moved to **1.27.2** for a govulncheck finding (net/http HTTP/2). Check CI on it. Two flaky CI jobs were seen on unrelated commits (browser suite nginx passkey, call suite echo test); both passed on re-run or the next push. (4) Then the owner retests: two Wi-Fi calls, one with the speaker on before the answer, and Call details' timeline now lists every sound-route change.

**▶ Session ended 2026-10-08 — start here.** (1) **Check CI on `95f765f`** (`gh run list --limit 3`); it was running at session end, after earlier runs were cancelled by newer pushes. Report it, and fix anything red before anything else. Then push this local handoff commit. (2) **The owner is testing build 15** on their iPhone: `docs/TEST_MATRIX.md` §5c (Tailscale on/off, Wi-Fi off mid-call, and the "How long it took" numbers from Call details). Their results decide whether the relay should also be held open in advance (only if "Routes ready" is still slow). (3) Still owner-side: the fresh 1.0.0 server install (`home.mym.ae` stays on `25587b4` until then), and the /beta TestFlight-request setup in `site/README.md` (an external TestFlight group, an App Manager API key, Email Routing, Turnstile, Worker secrets). (4) Signing a build: Xcode → Settings → Accounts must hold the owner's Apple Account, and `make ios-archive` must run outside Claude's sandbox. Next upload: **build 16**.

**▶ 2026-10-08: calls through a VPN, mended (app build 15, `docs/PHASE2.md` "Calls through a VPN…").** On 5G with Tailscale (exit node abroad), an outside call had no sound. Two causes: a direct route through the Synology subnet router that Asterisk rightly refused, plus the phone failing to look up the relay's name. The app now looks the relay up through iOS, moves a silent call onto the relay without hanging up (2–3 s), waits at most 3 s for routes, and shows a set-up timeline in Call details. The web phone's stuck "Reconnecting…" after a network change is fixed too. The browser suite proves a live mid-call move to the relay behind all five front doors. **Build 15 (version 1.0.0) is uploaded to TestFlight (2026-10-08)**, signed Apple Distribution, `aps-environment = production`; the next upload is **build 16**. Signing needs the owner's Apple Account in Xcode (it had dropped out; they signed in again), and the export must run **outside Claude's sandbox**, which otherwise can't read the keychain ("No Accounts"). Owner's tests then: `docs/TEST_MATRIX.md` 5c. `home.mym.ae` is still on `25587b4`, which is fine for this (no server change).

**Also built 2026-10-07 (website, not tied to a release):** the changelog page and its automation (`tools/changelog/entry.sh`, Release job `changelog`, notes in `docs/release-notes/`); the TestFlight request page **/beta** (`site/worker/index.ts`: signed review link emailed to the owner, Approve adds the tester via the App Store Connect API) and **/privacy**. **/beta is closed until the owner does the setup in `site/README.md` "TestFlight requests"**: an *external* TestFlight group submitted for Beta App Review (needs a demo server + sign-in details for Apple), an App Store Connect API key with the App Manager role, Cloudflare Email Routing with their address as a verified destination, a Turnstile widget, and the variables/secrets in the Worker's settings. App Store Connect today has only the "Internal Testing" group (app id 6818948775, "Linx UC").

**Going live at 1.0.0 now (owner, 2026-10-05: server first, app after), in order:**

1. ✅ **DONE — CI green and `v1.0.0` released** (2026-10-05). The tag sits at **`c3e61d6`** (not the original `f5bc985`: the first release run failed in the `program` job on a cosign v3 change — see §5 — so the fix landed and the tag was moved before anyone had pulled it). GitHub release: https://github.com/linxpbx/linx/releases/tag/v1.0.0 — the five `ghcr.io/linxpbx/linx-*` images carry `1.0.0`/`1.0`/`stable` and are cosign-signed; `linx-linux-amd64`/`-arm64` + `SHA256SUMS` + `.sigstore.json` bundles attached.
2. ✅ **DONE** (folded into 1).
3. **Fresh install of 1.0.0 on a new server** (owner, 2026-10-05: *"start with a fresh server, no backup"* — not an in-place update of `home.mym.ae`, and no data carried over, so everything is reconfigured: people, extensions, trunks + the UCM landline, phones, front door, the Apple push key). Provision a clean Ubuntu 24.04 / Debian box, then **one command** — `curl -fsSL https://raw.githubusercontent.com/linxpbx/linx/master/install.sh | sudo sh` (`install.sh` at the repo root, also on every release; detects amd64/arm64, downloads the signed binary, checks SHA256 + cosign-if-present, installs, runs `linx setup`). The web-first install prints one link → TLS-ALPN-01 → HTTPS (`docs/INSTALL.md`). A fresh server gets a clean internal CA and clean schema (no migration history), which is the cleanest possible 1.0.0.
4. **Harden the new server's CA when settled** (`docs/ops/INTERNAL_CA.md`): take the CA root key off the box — the task the owner said to do "when it's time". Not a go-live blocker; done *with* the owner once the server is up.
5. **Then handle the app:** the owner signs in to Xcode (only interactive step left), archive + upload build 14 → TestFlight, then the App Store path (`docs/ops/STORE_SUBMISSION.md`: reachable demo server + live setup code is the thing that sinks self-hosted clients) and the hands-on Phase 2 demo rows (`docs/DEMO_PHASE2.md`: ringing incl. force-quit, video + data cost, iPad/fold, `*97` on a desk phone, the 1-core feel).

Then later phases gradually: Phase 3 (meetings/guests), Phase 4 (3CX parity + **Linx Connector** + desk-phone auto-provisioning), 5, 6.

**Done (owner's ask, 2026-10-05):** the interactive installer no longer offers a **test certificate** on a release build — `askCertificates` skips the "Use test certificates for now?" question and forces trusted when `installer.IsRelease(version)` is true; a dev build still asks. (The web install already forced trusted; the `cert.go:358` "(test certificate)" line is the automatic staging reachability probe, left as-is.) The `--config` path is explicit YAML, with the sample comment now saying staging is development-only.

**Standing, for the owner at go-live (none blocks the build order):**
- **Take the CA root key off the server** (`docs/ops/INTERNAL_CA.md`) — owner said "when it's time"; go-live is that time. Clears a standing `doctor` warning.
- **A scoped third-party pen test** before real traffic — offered; the scope pack can be written on request. (The local active test is done; it can't cover creative chaining or the real Pangolin front door.)
- **TestFlight build 14 is up** — owner can test the iPad/fold screens and the Settings Person/Linx-server lines.

**Worth putting to the owner (small, not blocking):**
- Discoverability: a line on the **Voicemail** page — "or dial `*97` from your desk phone" (one web change + `make screens`). Left out rather than slipped in.
- The app has **no lock of its own**; a Face ID lock is a small separate feature if wanted.
- A **"admins must have a second step"** setting would make the owner's own 2FA/passkey assumption a rule rather than a habit (recommended in the security review, not built).
- Ring-group messages are deliberately **not** on `*97`; say if that should change.

**Recorded, not started:** desk phones working **from outside the network, with or without an SBC** (`docs/ROADMAP.md`, "Desk phones from outside the office"); registration lockout is its precondition.

**Still waiting on the owner:** the §11 answers in `docs/PHASE2.md` (push for other self-hosters, TestFlight vs App Store), and whether to forward UDP 443 to `192.168.1.213` for slightly smoother audio.
