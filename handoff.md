# Handoff

**What this file is for.** The first thing to read when a session starts (or after `/clear`), and the last thing to update before one ends. It carries only what a fresh session needs to pick the work up: where we are, what is in play, what has already been tried and failed, and what comes next. The long record lives in `docs/HISTORY.md`; the rules live in `CLAUDE.md`. If this file and those disagree, `CLAUDE.md` and `docs/HISTORY.md` win and this file should be corrected.

Last updated: **2026-10-05**, after Phase 2 **step 9b** (`*97` and the message-waiting light), which followed step 9 (lifetime, loss and the security review) and step 8 (the iPad and fold layouts), on top of the day's iPhone/iPad testing round (app build 13).

---

## 1. Goal

**Linx** is a self-hosted, open-source (Apache-2.0) 3CX-style phone system: PBX, video meetings, guest links, presence. One owner, who is not a developer: explain in plain language and always recommend an answer. Full brief in `linx-build-prompt.md`, design in `docs/`.

**The goal right now** is **Phase 2 — the iPhone/iPad app** (`docs/PHASE2.md`), taken to its exit test: a sleeping phone rings reliably, calls sound right on any network, and the app looks native on iPhone, iPad and the iPhone Duo. Phase 1 (server, web client, trunks, admin, email/voicemail/history) is finished and approved.

The standing constraints that shape every change: smallest possible processor, memory, disk and bandwidth (measure, record in `docs/RESOURCES.md`); never weaken TLS or fall back to plaintext; plain-language copy; design tokens only.

---

## 2. Current state

**Phase 2 steps 1–9b are built**, and 1–7 are on real devices. The owner is working down `docs/TEST_MATRIX.md` on an **iPhone 18 Pro Max** and an **iPad Pro 13-inch (M5)**.

**The owner looked at the iPad screens on 2026-10-05 and said they look right**, with one rule to follow: **a swipe that deletes is red, and blue down the side of a row means "mark read or unread" and nothing else.** The Favourite swipe in Team was brand blue and is now amber (`color.star`, a new token); Delete in Voicemail now uses Linx's own red rather than whichever red the system picks. The rule is written into `docs/ui/DESIGN_TOKENS.md` so it isn't re-decided. They also asked about **the transition when a folding phone is opened out** — see §5 and `docs/PHASE2.md`.

**Step 9b (`*97` and the message-waiting light) was built on 2026-10-05, and the call suite proves both on the real image** — including a phone subscribing the way a desk phone does and being sent a NOTIFY that says the light is on. (ADR-083, `docs/PHASE2.md` "Step 9b, as built", `docs/PBX.md` §4.) Dialling `*97` from a phone hands the call to the control plane, which answers it and plays that extension's new messages — the caller's number read out digit by digit, then the message, with **1** again, **2** next, **3** delete, and the next following by itself after three seconds. Whose messages is decided from the channel's endpoint, never from a dialplan argument. A message heard right through stops being new; one skipped partway stays new. The **light** is a count Linx gives Asterisk over ARI (`res_mwi_external`; `app_voicemail` is still not built), set at every change and again whenever Asterisk reconnects, on desk phones and softphones only. **ARI is read-write from now on** — the one thing in the step that widens anything, and it is in the threat model. **This needs the new Asterisk image on any server that wants it** (six modules and eighteen recordings, +0.81 MB); nothing else about the server changes, and no app build is involved.

**Step 9 (lifetime, loss and the security review) was built on 2026-10-05** (`docs/PHASE2.md` "Step 9, as built"). Most of it was already there — steps 2 and 4a put the lifetime rules in at the start — so the work was proving it end to end, closing three gaps and doing the security review §10 reserves for this step (`docs/THREAT_MODEL.md`, "Phones: identity, lifetime and loss review": six new STRIDE rows, three corrected, and an open-and-accepted list). The gaps closed: a phone stopped for good or expired now **loses the Apple tokens** Linx held for it; the **moved-server checklist** no longer tells an admin to give app phones a new address (impossible — they are set up again, and only a changed *domain* does it to them), counting and listing them apart from desk phones; and Settings now shows the **Person** and **Linx server** this phone joined. The pinned-CA question is settled by **not** pinning: Linx knows a phone by the fingerprint of the certificate it issued, so replacing the internal CA breaks nothing and refusing a renewal from an unfamiliar CA would brick a legitimately re-installed server for no gain. **No migration, no new endpoint, nothing for the owner's server to be updated for.**

**Step 8 (the iPad and fold layouts) was built on 2026-10-05 and is not yet in a TestFlight build.** Every tab is a list beside a detail, a call stands in its own column instead of covering the screen, the keypad has the starred people beside it, and a folding phone is asked where its fold is (ADR-082) instead of the near-square guess. All app tests pass, `make ios-build-device` is clean, and the fold code is proved to compile in under the 27.1 beta and out under Xcode 27.0. **The next TestFlight upload is build 14** and would be the first with these screens on it.

- **App: TestFlight build 13** is uploaded (0.1.0). Builds 4→13 all shipped today, each fixing what the owner found on the real devices. The owner has standing permission to upload builds in this round of testing ("don't wait for my order"); the App Store listing and Submit for Review stay theirs.
- **Server `home.mym.ae` (192.168.1.213) is on `25587b4`**, schema 45. Updated twice today, each time after a backup (snapshots `482c4735`, `63e4dcd3`): the relay's per-call cap (64 → **160 kB/s**, so a relayed video call isn't throttled) and **`max_bitrate_bps`** in the relay credentials (so the app can keep a relayed video call inside what the relay carries). `linx doctor` green apart from two standing warnings (CA root-key backup still on the server; no API key).
- **Confirmed working on both devices today:** calls with sound from outside the owner's network, adaptive video quality, Stop video, the picture swap, the camera question, the screen staying awake in a video call, and calling out through the UCM landline from any extension.
- **Owner's own config fix today:** the UCM Landlines line's **Caller ID shown to others = `+97142340100`**. Without it, only the extension that owns that DID could call out (see §5).
- **CI** was green on `41199b5` (all 24 jobs, including the iOS app) at the start of the step 9 session. Check the step 9 commits the same way (`gh run list --limit 3`).

**Step 10's written half is done (2026-10-05):** `docs/ops/STORE_SUBMISSION.md`, `docs/RESOURCES.md` §3b (Phase 2's whole cost, and what a minute of call costs in data), and **`docs/DEMO_PHASE2.md`** — the close-out demo, which carries every check that needs the owner's hands. A real gap closed on the way: the app had no `PrivacyInfo.xcprivacy`, which Apple refuses an upload without; it exists now and ships at the bundle root. **What is left of Phase 2 is the demo itself**, run with the owner, with the 1-core VPS inside it.

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

**Copy and docs that must keep up** — `web/src/screens/PhoneLines.tsx` (the Caller ID hint), `web/src/lib/gatewayHowTo.ts`, `docs/help/phone-system-or-gateway.md`, `docs/help/iphone-and-ipad.md`, `docs/TEST_MATRIX.md`, `docs/PHASE2.md`, `docs/DECISIONS.md`, `docs/TRUNKS.md`, `docs/RESOURCES.md`, `docs/HISTORY.md`, `CLAUDE.md`.

---

## 4. Changes made (the whole project)

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

1. **Check CI** for the step 9 commits (`gh run list --limit 3`), and report it.
2. **Let the owner test build 13** and work down `docs/TEST_MATRIX.md`. The rows that matter now: 2.x (ringing — locked, backgrounded, force-quit, Low Power Mode, overnight, mobile data), 3.8a/3.8b (adaptive quality: 540p relayed, up to 720p direct), 3.8c (the screen staying awake), 3.9a (tapping to swap the pictures), 3.10 (video in the sound test), and section 5, which gained five rows in step 9 (a call dropping when a phone is stopped mid-call, disable-and-enable, a stopped phone never ringing again, what Settings names, and sign-out versus "I've lost it").
3. **Still for the owner from step 8:** look at `ios/screenshots/iPhone-18-Pro-Max/`, `iPad-Pro-13-inch-M5/` and `iPhone-Duo/` (the Duo's *inner* screen has to be photographed by hand — open the simulated phone out in its window, then `xcrun simctl io booted screenshot --display 3 …`).
4. **Step 9b is done.** What it needs from the owner: **the new Asterisk image on `home.mym.ae`** before `*97` or the light can be tried there (back up and ask first, `docs/ops/UPDATING.md`), and then `docs/TEST_MATRIX.md` **section 5b**, which is ten rows on a real desk phone — the UCM's extensions are the obvious place.
5. **Then step 10, the last of Phase 2** (`STORE_SUBMISSION.md`, resource and data-per-minute measurements, `docs/DEMO_PHASE2.md`, the demo on the 1-core VPS).

**Worth putting to the owner** (from step 9b): nobody dials a code they have never heard of. A line on the **Voicemail** page — "or dial `*97` from your desk phone" — would make it discoverable; it is a one-line web change plus `make screens`, and it was left out rather than slipped in. Also from 9b: a ring group's messages are deliberately **not** on `*97` (no phone to light up, no one person to mark them heard); say if that should change.

**Worth putting to the owner** (from step 9's review, not blocking anything): the app has **no lock of its own** — anyone holding the unlocked phone can see the team, the call history and voicemail, and ring anyone, exactly as they could with the phone's own Phone app. iOS's passcode and Face ID are what protect it today. A Face ID lock on the app itself is small and separate; ask whether they want it.

**Nothing is asked of the owner until the close-out demo (owner, 2026-10-05).** Every device check — the ringing rows, `*97` and the light on a desk phone, the iPad and fold screens — belongs in `docs/DEMO_PHASE2.md` and is done in one sitting. Don't send them off to run test-matrix rows as each step lands.

**Recorded, not started (owner, 2026-10-05):** a desk phone must work **on the network and from outside it, with or without an SBC**. Today it is LAN-only by design. The requirement and what it has to answer — the port, registration lockout, where the audio goes, and both the SBC and no-SBC cases — are in `docs/ROADMAP.md` "Desk phones from outside the office". Auto-provisioning stays in Phase 4.

**Still waiting on the owner** (none of it blocks the build order): the §11 answers in `docs/PHASE2.md` (push for other self-hosters, TestFlight vs App Store), their look at `ios/screenshots/`, the two standing `doctor` warnings, and whether to forward UDP 443 to `192.168.1.213` for slightly smoother audio.
