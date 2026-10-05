# Handoff

**What this file is for.** The first thing to read when a session starts (or after `/clear`), and the last thing to update before one ends. It carries only what a fresh session needs to pick the work up: where we are, what is in play, what has already been tried and failed, and what comes next. The long record lives in `docs/HISTORY.md`; the rules live in `CLAUDE.md`. If this file and those disagree, `CLAUDE.md` and `docs/HISTORY.md` win and this file should be corrected.

Last updated: **2026-10-05**, after the day's iPhone/iPad testing round (app build 13).

---

## 1. Goal

**Linx** is a self-hosted, open-source (Apache-2.0) 3CX-style phone system: PBX, video meetings, guest links, presence. One owner, who is not a developer: explain in plain language and always recommend an answer. Full brief in `linx-build-prompt.md`, design in `docs/`.

**The goal right now** is **Phase 2 — the iPhone/iPad app** (`docs/PHASE2.md`), taken to its exit test: a sleeping phone rings reliably, calls sound right on any network, and the app looks native on iPhone, iPad and the iPhone Duo. Phase 1 (server, web client, trunks, admin, email/voicemail/history) is finished and approved.

The standing constraints that shape every change: smallest possible processor, memory, disk and bandwidth (measure, record in `docs/RESOURCES.md`); never weaken TLS or fall back to plaintext; plain-language copy; design tokens only.

---

## 2. Current state

**Phase 2 steps 1–7 are built and on real devices.** The owner is working down `docs/TEST_MATRIX.md` on an **iPhone 18 Pro Max** and an **iPad Pro 13-inch (M5)**.

- **App: TestFlight build 13** is uploaded (0.1.0). Builds 4→13 all shipped today, each fixing what the owner found on the real devices. The owner has standing permission to upload builds in this round of testing ("don't wait for my order"); the App Store listing and Submit for Review stay theirs.
- **Server `home.mym.ae` (192.168.1.213) is on `25587b4`**, schema 45. Updated twice today, each time after a backup (snapshots `482c4735`, `63e4dcd3`): the relay's per-call cap (64 → **160 kB/s**, so a relayed video call isn't throttled) and **`max_bitrate_bps`** in the relay credentials (so the app can keep a relayed video call inside what the relay carries). `linx doctor` green apart from two standing warnings (CA root-key backup still on the server; no API key).
- **Confirmed working on both devices today:** calls with sound from outside the owner's network, adaptive video quality, Stop video, the picture swap, the camera question, the screen staying awake in a video call, and calling out through the UCM landline from any extension.
- **Owner's own config fix today:** the UCM Landlines line's **Caller ID shown to others = `+97142340100`**. Without it, only the extension that owns that DID could call out (see §5).
- **CI** on the last two commits (`975f00b`, `09f57ca`) was running when the session ended — check it first (`gh run list --limit 3`).

Not started: **step 8** (iPad and fold layouts), step 9 (lifetime/loss + security review), 9b (`*97` and the message-waiting light), 10 (store submission docs, resource measurements, `docs/DEMO_PHASE2.md`, the demo on the 1-core VPS).

---

## 3. Active files

The ones in play this round. `codegraph explore "<names>"` is faster than grep for any of them.

**The app's call path** — `ios/Linx/Core/Media/WebRTCMedia.swift` (ICE, SDP, the picture, diagnostics, the audio session), `VideoQuality.swift` (the quality ladder and the relay ceiling, ADR-081), `Camera.swift` (capture + `CallVideo`), `RelayCertificates.swift` (ADR-080), `SDPTweaks.swift`, `Core/SIP/SIPUserAgent.swift` (re-INVITEs, `dropVideo`), `Core/Call/*` (CallKit), `Core/Push/*`.

**The app's screens** — `ios/Linx/Features/Phone/PhoneModel.swift` (the one place a call's state and the screen rules live), `CallView.swift`, `VideoCallView.swift`, `CallDetailsView.swift`, `AudioRouteButton.swift`, `CallLayout.swift`.

**Tests** — `ios/LinxTests/SoundOnTheRoadTests.swift` (this round's rules), `VideoTests.swift`, `CallTests.swift` (holds `FakeMedia`), `RingingTests.swift`. 125 app tests.

**Server side touched this round** — `internal/turnconf/turnconf.go` (`MaxBPS`), `internal/turn/turn.go` (`MaxBitrate` → `max_bitrate_bps`), `api/openapi.yaml` + `services/control-plane/api/webphone.go`, `internal/asteriskconf/config.go` (the dialplan, unchanged today but read often).

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

**Decisions of record:** `docs/DECISIONS.md`, ADR-001 … **ADR-081**. The newest matter most here — 078 (China/CallKit), 079 (a picture is added to a call), 080 (iOS's trust store for the relay's certificate), 081 (the adaptive picture).

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

1. **Check CI** for `975f00b` and `09f57ca` (`gh run list --limit 3`), and report it.
2. **Let the owner test build 13** and work down `docs/TEST_MATRIX.md`. The rows that matter now: 2.x (ringing — locked, backgrounded, force-quit, Low Power Mode, overnight, mobile data), 3.8a/3.8b (adaptive quality: 540p relayed, up to 720p direct), 3.8c (the screen staying awake), 3.9a (tapping to swap the pictures), 3.10 (video in the sound test).
3. **Then Phase 2 step 8 — the iPad and fold layouts** (`docs/PHASE2.md` §12 step 8): every tab a list beside a detail, Mail-style, not a phone column floated in the middle of a 13-inch screen (**the owner's explicit condition**, ADR-076); a call beside the list rather than covering it; `ArrangementView` / `ReservedRegion` behind an iOS 27.1 check; and the iPhone Duo screenshots, folded and unfolded (the inner screen needs Xcode 27.1 beta and Simulator's Device menu by hand).
4. Then step 9 (lifetime and loss, security review, `THREAT_MODEL.md` rows), **9b** (`*97` and the message-waiting light), step 10 (the finish: `STORE_SUBMISSION.md`, resource and data-per-minute measurements, `docs/DEMO_PHASE2.md`, the demo on the 1-core VPS).

**Still waiting on the owner** (none of it blocks the build order): the §11 answers in `docs/PHASE2.md` (push for other self-hosters, TestFlight vs App Store), their look at `ios/screenshots/`, the two standing `doctor` warnings, and whether to forward UDP 443 to `192.168.1.213` for slightly smoother audio.
