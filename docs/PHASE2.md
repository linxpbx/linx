# Linx — Phase 2: the iPhone and iPad app (design for approval)

*Drafted 2026-10-03; §8 and §11 updated the same day after checking Apple's releases (the Apple Developer membership is confirmed; the iPhone Duo simulator and fold APIs exist in the Xcode 27.1 beta). **Build-order steps 1 to 6 are built** (§12, each with an "as built" note below). **Build-order step 7 is built too** (2026-10-04). ADR-073 to ADR-079 are proposals; they go into `docs/DECISIONS.md` once you approve them (§11), with ADR-078 (how a call reaches a phone where CallKit may not be used) added on 2026-10-04 with step 6 and **ADR-079 (a picture is added to a call, never rung as one) with step 7**. Phase 2's goal in `docs/ROADMAP.md` is unchanged: enrollment with device certificates, a push gateway, and a native iOS/iPadOS app that rings reliably.*

## 1. In plain words
When Phase 2 is done:
- **Setting up a phone takes one scan.** In the admin portal (or your own Settings page) you press **Add phone**, and Linx shows a QR code. You scan it with the Linx app on the iPhone and the phone is set up: no server address to type, no password, nothing to copy. An emailed link does the same when the phone isn't in the room, and there's still a by-hand option.
- **No password lives on the phone.** The app makes its own key inside the iPhone's security chip (the Secure Enclave), which can never leave the phone, and Linx gives that key a certificate. The QR code carries no phone password — it's a one-time ticket that works for 10 minutes.
- **The phone rings when it's locked, asleep or the app has been closed.** That's what a native app is for: Apple wakes the app with a special "a call is coming" push, and the call appears on the lock screen like a normal phone call (same look, answer from CarPlay, in your call history).
- **Calls and 1:1 video work**, on Wi-Fi and mobile data, over TLS on 443 only, so they work on hotel Wi-Fi and in China (`ADR-042`).
- **Four tabs:** Calls, Team, Keypad, More (Voicemail, Settings; Meetings and Chat join them in Phase 3 and 4 — no "coming soon" placeholders, see §14). **On iPad**, a sidebar with the list and the call side by side.
- **A lost phone is one click to revoke**, and a phone that hasn't been in touch for 6 months has to be set up again.

## 2. What's in this slice, and what waits
**In:** enrollment (QR, emailed link, by hand) with device certificates; the `ios` device kind; the push gateway (Apple) and "hold the call until the phone is back"; the app itself (sign-in, calls in and out, 1:1 video, voicemail, call history, team list and presence, settings); CallKit and lock-screen ringing, with Apple's rules for China; iPad layout; the six-month idle expiry and revoking; `TEST_MATRIX.md`, `APPLE_SIGNING.md`, `STORE_SUBMISSION.md`; help guides for the new screens.

**Waits:**
- **Chat** (Phase 4), **meetings and guests** (Phase 3): their tabs arrive with the features, not before (§14).
- **Desk-phone provisioning** (Phase 4) — this slice's enrollment is for the app only.
- **Android, Mac and Windows apps** (Phase 7, your go-ahead only).
- **Keeping a call alive when you walk out of the office** — that's the Site Connector, after going live.
- **Voicemail from a desk phone (`*97`) and the message light**: Phase 1F parked these until Linx controls calls through ARI. Phase 2 adds exactly that for push, so they become a small follow-on — but they're not in Phase 2's exit test. *(Decision §11 item 6.)*

## 3. Before any of it: what only you can do (§11 items 1–3)
1. ~~**An Apple Developer Program membership**~~ — **you have it (confirmed 2026-10-03)**, so the ringing work (PushKit, CallKit, real-device push, TestFlight) is unblocked, and so is downloading the Xcode 27.1 beta in §8.
2. **Two test devices on iOS 27**: an iPhone (for ringing, push, CarPlay, mobile data) and an iPad (for the sidebar layout). The simulator covers layout and screenshots, never ringing. An **iPhone Duo** (from 23 October) is optional: the simulator does the layouts.
3. **Xcode 27.1 beta** installed beside Xcode 27.0 (§8) — a download from your developer account.
4. **Who publishes the app** — your own Apple account, under the bundle ID `com.linxpbx.app` that's already decided. This has a consequence for other people who self-host Linx, in §6.

## 4. Setting up a phone: enrollment and what identifies a device (ADR-073)
- **What you see.** **People → a person → Add phone** (and the same on your own Settings page): pick **iPhone / iPad**, name it ("Mohammed's iPhone"), and choose **Show a QR code**, **Email a link** or **Set it up by hand**. The screen shows the code and a countdown. When the phone finishes, the row turns into a normal device: online, last seen, **Revoke**.
- **What the QR code is.** A one-time ticket: the server's address, a signed token (10 minutes, usable once, tied to that one person and device name, `ADR-012`) and a fingerprint of Linx's own certificate authority. **No SIP password, ever** (security rules). Photographed and used by someone else, it would enrol *their* phone as that device — so it's short-lived, single-use, and shows in the audit log; the admin sees the new phone appear and can revoke it. An emailed link is the same token in a link, and by hand means typing the token's 8 characters.
- **What the phone does.** It makes a key pair inside the Secure Enclave (the private half can never be read, copied or backed up, not even by the app), asks Linx for a certificate for it, and keeps the certificate in the Keychain. That certificate *is* the phone's identity from then on.
- **How the phone proves who it is afterwards.** Not with TLS client certificates: through an HTTP-only front door (Caddy, Nginx Proxy Manager) the proxy decrypts and re-encrypts, so a client certificate never reaches Linx, and Linx must work behind every front door. Instead the app signs a short statement with the Secure Enclave key (a signed, one-minute, single-use proof, sent with the certificate) and gets a short-lived token back. **This works through every front door and inside China.** The Secure Enclave is still what the security rests on: the proof cannot be made anywhere but on that phone.
- **The phone line itself.** With that token the app asks for its SIP login (`POST /me/phone-line`, the same idea as the web client's), keeps it in memory, and talks to Asterisk through the existing `/sip` relay on 443 — the relay Phase 1C already built and hardened, with its method allowlist and strict `From` checks (`docs/WEB.md` §4). The relay learns one new way in: a device token instead of a browser cookie, with the same rules after that. **No new public surface.**
- **Server side:** device kind `ios` (the column exists), a step-ca provisioner the control plane alone can use, `POST /api/v1/enrollments` / `POST /v1/enroll`, device tokens, and webhook events `device.enrolled` / `device.expired`.

## 5. Ringing a sleeping iPhone (ADR-074, ADR-078)
- **The chain:** someone dials the extension → Asterisk asks Linx where to ring (it already does, `ADR-068`) → Linx sees an `ios` device that isn't connected right now → Linx sends Apple a **VoIP push** → Apple wakes the app → the app *immediately* tells CallKit "a call is coming" (Apple requires this, or it stops delivering pushes) → the app connects and registers → Asterisk delivers the call. Meanwhile **Asterisk holds the call ringing** for up to 10 seconds through the ARI app the control plane already runs, so the call is still there when the phone arrives.
- **What the push contains:** a call id and the caller's number, nothing else. No name lookup, no tokens, no SIP credentials. The lock screen shows the number at once and the name a moment later, from the call itself. Apple sees only what's in the push, and the push is the only part of a Linx call that touches a third party.
- **Reliability is the exit test**, not a nice-to-have: locked phone, app closed by the user, phone in Low Power Mode, overnight, bad network, two calls at once, a call that the caller gives up on (the ringing must stop). These are rows in `TEST_MATRIX.md` and are tested on real devices.
- **China (`ADR-042`, `ADR-078`):** Apple's push works in China; CallKit is not allowed there, and a VoIP push that doesn't report a call to CallKit gets the app killed (§14 item 1). So a phone whose region is China **sends no PushKit token at all** and tells Linx `call_alerts` instead (`POST /api/v1/me/phone-push`): a ringing call reaches it as an ordinary **time-sensitive notification** on the app's own topic, which the person taps, and the call is still held ringing for them. The app rings with its own full-screen screen whenever it is open, there and everywhere else. Linx waits for such a phone exactly as it waits for a woken one (`asterisk.linx_wake`, migration 0043). Everything stays on TLS 443.
- **Every device of the extension rings at once, and always has** (owner's question, 2026-10-03). Linx has no "make this one active": `linx_ring_targets` lists every enabled device of an extension and the dialplan dials them joined with `&`, so the browser line, the iPhone, the iPad and a desk phone all ring together and the first to answer takes the call — the rest stop ringing. The app is simply one more device in that list; nothing about this changes for Phase 2.
- **The devices that didn't take the call still have to hear about it** (owner, 2026-10-03, added to step 5). What a person missed is already kept per *person*, not per device — missed calls (`app_user.calls_seen_at`, `/me/missed-calls`), voicemail (the person's box), and chat when it comes in Phase 4 — so every device shows the same counts and they clear everywhere at once over the Team websocket. What is missing is the phone that was asleep: **step 5 therefore sends two kinds of push** — the VoIP push that rings a call, and a **quiet, ordinary notification for a missed call, a new voicemail and (Phase 4) a chat message**, which needs the person's permission the first time and carries the same bare facts as the VoIP one (who and when, nothing else). A phone that answered the call gets no such notification.
- **Cost (low-resource rule):** one long-lived outgoing HTTP/2 connection to Apple, opened only when the first `ios` device exists, and no new container. Measured and recorded in `docs/RESOURCES.md`.

## 6. The part that affects other self-hosters (decision §11 item 3)
Apple ties the "wake up and ring" push to the app's identity, with a key that belongs to whoever publishes the app. So pushes for `com.linxpbx.app` can only be sent with *your* Apple key — and that key is a secret that can't be shipped to other people's servers.

Three ways out, and this phase only needs the first:
- **(a) Your own server, your own key** *(recommended now)*: the key is a Docker secret on your server, like the mail password. Nothing goes through anyone else. Works in China. This is what Phase 2 builds.
- **(b) A Linx push relay later**: your server asks a small Linx-run service to send the push, which fans out to Apple. That's how open-source chat apps (Nextcloud, Matrix, Mattermost) solve it for self-hosters. It means call metadata passes through a Linx service, so it needs its own design, privacy note and a way to turn it off. Phase 5 or later, when the app goes to the App Store for other people.
- **(c) Each self-hoster publishes their own build** with their own Apple account — possible, documented in `APPLE_SIGNING.md`, but not something most people will do.

The push gateway is built so the destination is one setting: Apple directly now, a relay later, with no change to the app.

## 7. The app (ADR-075)
- **Swift 6 / SwiftUI**, one app for iPhone and iPad (`ADR-011`), English only (`ADR-021`), Apple's own controls, SF Symbols, Dynamic Type, light and dark from `design/tokens.json` (the colour set is already generated by `make tokens`).
- **Audio and video:** Google's WebRTC (BSD), the same engine the browser uses, pinned by version and checksum. Opus with in-band FEC and DTX, exactly the settings the web client already sends (`web/src/phone/sdp.ts`), so a bad network degrades the same way. Video is 1:1 only in this phase, capped, and drops to audio before audio suffers. **A picture is always added to a call that is already up** (a re-INVITE, either side, `ADR-079`): a Linx call rings as a phone call — which is what a lock screen, a car and a headset understand — and the camera goes into it afterwards, so turning video off leaves an ordinary call rather than ending anything.
- **SIP:** a small SIP-over-websocket client written in Swift — REGISTER, INVITE, ACK, BYE, CANCEL, OPTIONS, INFO, UPDATE, PRACK, MESSAGE, NOTIFY, SUBSCRIBE, REFER, and nothing else, because that's what the relay allows. Written rather than borrowed: every mature SIP library (linphone, PJSIP, Sofia) is GPL, AGPL or LGPL, and client bundles take permissive licences only.
- **Screens:** **Calls** (history, missed first, tap to call back), **Team** (the directory with presence, live over the websocket Phase 1C built), **Keypad**, **More** → Voicemail (listen, delete), Settings (ringtone, microphone, "use mobile data", sign out of this phone, Help), and Meetings later. Everything reuses the API the web client already uses — no new endpoints except enrollment, the device token and the phone line.
- **Battery:** no polling. One websocket while the app is in front; nothing at all in the background — that's what push is for.

## 8. iPad, and the foldable iPhone "iPhone Duo" (ADR-076)
**Where the tools are (checked 2026-10-03).** The installed Xcode is **27.0 (27A266a)**, the current release, and it has nothing foldable: no device in the simulator, and no fold, hinge or posture API in its iOS 27.0 SDK. The foldable support ships **only in Xcode 27.1 beta (27A9269, 18 September 2026)**, which carries the **iOS 27.1 SDK and the iPhone Duo simulator** with the phone's poses (open, folded, part-folded, rotated). **Xcode 27.2 beta 2 is newer but does not have it** — its own notes point back at the 27.1 beta — so "newest Xcode" is the wrong one here. iPhone Duo itself ships **23 October 2026 running iOS 27.1**.

**Installed and verified 2026-10-03.** Xcode 27.1 beta (27A9269) is in `/Applications/Xcode-27.1-beta.app` — Apple-signed (chain to Apple Root CA, Gatekeeper "Apple System"), **beside** Xcode 27.0, which is still the active toolchain (`xcode-select -p`) and still builds releases and CI. It carries the **iOS 27.1 SDK** (device and simulator) and the **iPhone Duo** simulator device (`com.apple.CoreSimulator.SimDeviceType.iPhone-Duo`). The iOS 27.1 **simulator runtime** was a separate 7.85 GB download (`xcodebuild -downloadPlatform iOS`, no admin password needed) and is installed — a Duo on the iOS 27.0 runtime is refused outright ("Incompatible device"). A **Linx iPhone Duo** simulator is created and boots.

**The APIs, read out of the installed SDK itself** (not the documentation site, not the press):
- **SwiftUI `ArrangementView`** with the **`ArrangementViewStyle`** protocol and two styles — **`.split`** (`SplitArrangementViewStyle`) and **`.overlay`** (`OverlayArrangementViewStyle`), each with `.axes(_:)`. They live in the **`SwiftUICore`** module that SwiftUI re-exports, not in `SwiftUI`'s own interface, which is worth knowing when a symbol looks missing.
- **SwiftUI `ReservedRegion`**: `kind` (**`.division`** for the fold, **`.occlusion`** for things like the camera), `QueryOptions` (`.includeInactive`), and the query `reservedRegions(kind:options:layoutDirectionBehavior:)`.
- **UIKit**: `UIArrangementViewController`, `UISplitArrangement`, `UIOverlayArrangement`, `UIArrangementViewState`, `UISplitArrangementDimension(Range)`, and `UIView.ReservedRegion` (`frame`, `kind`, `margins`, `isActive`, `Kind`, `QueryOptions`).
- Together these are what keep a call's buttons, the caller's name and the video tile off the fold and out from under the camera.

**The iPad must look like an iPad app, not a stretched iPhone one** (owner, 2026-10-04). A sidebar alone is not enough: step 8 gives each tab a **list beside a detail**, the way Mail and Messages do — Calls with the call's own details next to the list, Team with the person's card, Voicemail with the player, and the keypad laid out for a tablet rather than a phone's column floated in the middle. A call in progress sits beside the list rather than covering everything. Nothing in the app may look like a phone screen stretched to fill a 13-inch display.

**What this means for Phase 2:**
- **Done 2026-10-03:** the beta is installed as above. 27.0 stays the one that builds releases and CI until 27.1 is final, around the phone's launch. The app project will pin which Xcode it needs per build setting, and `make ios-screens` names the beta explicitly (`DEVELOPER_DIR`), so neither Xcode can be picked up by accident.
- Screens are built adaptive by size first (`NavigationSplitView`, size classes, no hard-coded widths), so they are right on every iPhone and iPad and when a window changes size *while* open.
- The fold-specific layout uses `ArrangementView` and `ReservedRegion` behind an availability check (`iOS 27.1+`), with the adaptive layout as the fallback below it. So that the project still builds on Xcode 27.0 in CI, the fold file is compiled only when the SDK supports it (a build setting keyed to the SDK version) until CI moves to 27.1.
- **Screenshots of every screen in the iPhone Duo simulator**, folded and unfolded, next to the iPhone and iPad shots, compared against `docs/ui/` — which is what your 2026-09-25 request asked for and is now possible.
- Known limits of the beta simulator: the first launch takes several minutes, app extensions mostly can't be run or debugged, StandBy is missing. None of those are in Phase 2's path.
- A **real iPhone Duo** is only needed for the ringing and camera rows of `TEST_MATRIX.md`; the simulator covers all the layout work. *(Decision §11 item 4.)*

## 9. Lifetime, losing a phone, and 6 months (ADR-077; owner, 2026-10-03)
- The device certificate lasts **6 months** — exactly as long as a phone may stay idle — and renews itself every time the app runs *or* wakes for a push, so the two always run out together and a phone in use never notices.
- **6 months with no contact at all** and the device expires: it has to be set up again with a new QR code. The person sees "Set this phone up again" in the app; the admin sees **Expired** in the list. A phone kept offline becomes useless by itself, without pestering someone who only uses theirs now and then.
- **Only two things make a phone need setting up again**: those 6 idle months, and a change of its person's password. Disabling the person or taking their extension away stops the phone while that lasts (and gives it back when it's undone); revoking it is for good.
- **A password change** (changed, reset or removed) expires the person's phones along with their browser sessions: a new QR code or emailed link sets the phone up again (owner, 2026-10-03; built in step 2).
- **Revoke** (which already exists) kills it at once and for good: the token stops, the certificate is refused, the SIP login dies, the call drops. Changing or disabling the person, or changing their password, does the same to their phones — as it already does to their browser lines.
- Each phone is its own device: revoking one leaves the others alone.

## 10. Security in this slice (added to `docs/THREAT_MODEL.md` at the review step)
- **Enrollment QR** has a row already; it gains the signed proof, the single-use `jti`, the audit entries and the "admin sees a new phone appear" check.
- **New rows:** the device token (proof-of-possession, one minute, single-use, replay refused, bound to one certificate); the Apple push key (a Docker secret, never in git, and what an attacker could do with it — spam pushes, not calls); the VoIP push payload (what Apple can see); the `/sip` relay's new way in (a device token instead of a cookie, same checks after that, same lockout and rate limits); a phone whose Keychain is extracted (the Secure Enclave key can't be, so a copied Keychain gives nothing usable).
- **Step 7's rows (video and the screens):** the picture goes the same way the sound does — DTLS-SRTP end to end, direct or through Linx's own relay, never anywhere else — and Asterisk passes it between the two WebRTC endpoints without looking inside it; a camera is only ever switched on by the person holding the phone (an offer with video in it is answered `recvonly`); the Team list's websocket now takes a device token, with the same rule as `/sip` (no `Origin`, closed the moment the phone is stopped); and a voicemail's audio is fetched for one message at a time, played out of memory, and never written to the phone.
- **Unchanged rules:** no public UDP/TCP 5060; TLS and encrypted audio always, no fallback; the QR carries no SIP password; Linx's outbound connection to Apple goes through the same private-address guard as webhooks.

## 11. Decisions I need from you
| # | Question | My recommendation |
|---|---|---|
| 1 | ~~Apple Developer Program~~ | **Answered 2026-10-03: you have the subscription.** Nothing blocked. |
| 2 | ~~**Test devices**~~ **Answered 2026-10-04: iPhone 18 Pro Max and iPad Pro 13-inch (M5).** The screenshots are taken on exactly those two from now on. | — |
| 3 | ~~**Push for other self-hosters**~~ **Answered 2026-10-04: (a), your own key only.** Your server, your Apple key, nothing passes through anyone else, works in China. A Linx-run relay is revisited only when the app goes to the App Store for other people (Phase 5+). | — |
| 4 | **Foldable iPhone** (§8): the simulator and APIs exist, but only in the **Xcode 27.1 beta**. Develop the app on a beta Xcode, or stay on 27.0 and add the fold layout when 27.1 is final (about 23 October)? | **Install the 27.1 beta and use it for the app**, keeping 27.0 for release builds and CI. Nothing else about Phase 2 depends on the beta, the fold layout gets designed from the start rather than retrofitted, and the only cost is one extra Xcode on disk (~10 GB). |
| 4b | **Minimum iOS version** for the app | **iOS 26.0.** One version back covers every phone people actually carry, keeps SwiftUI simple, and the fold APIs sit behind an `iOS 27.1` check anyway. |
| 5 | ~~**Where the app goes first**~~ **Answered 2026-10-03: TestFlight, but not until push and CallKit are in** (steps 5 and 6), so the first build the owner installs can ring. Signing and the App Store Connect record are set up then (they need the Apple Team ID), before step 7. | **TestFlight first.** The trademark check (`ADR-015`) and `STORE_SUBMISSION.md` get written in Phase 2; submitting can wait until Phase 3's meetings are in, so reviewers see the finished app. |
| 6 | ~~**`*97` and the message-waiting light**~~ **Answered 2026-10-04: fold them into Phase 2.** They become build-order step 9b below, before the finish. *(I had recommended keeping them separate; the owner decided otherwise, and that is the plan.)* | — |
| 7 | **Chat tab**: show it with a "coming later" card, or leave it out until Phase 4? | **Leave it out** — this reverses what I recommended earlier today. App Review's "app completeness" rule (2.1) rejects visible placeholder or "coming soon" content, and you asked for no rejections. Four tabs in Phase 2, Chat added in Phase 4. §14 has the rest. |

## 12. Build order (one session each, in this order)
1. ~~**The Xcode project skeleton**~~ — **done 2026-10-03** (`ios/README.md`). Swift 6 / SwiftUI, minimum **iOS 26.0** (§11 item 4b as recommended), bundle `com.linxpbx.app`, iPhone and iPad. The project file is checked in and uses Xcode's synchronised folders, so adding a Swift file never edits it — no project generator was added. `make tokens` now also writes `ios/Linx/Generated/DesignTokens.swift` (`LinxColor`, `LinxSpace`, `LinxRadius`) beside the colour sets, and `make lint` fails when either is stale; a unit test checks every colour set against `design/tokens.json` in both appearances. The first screen is the enrollment screen of `docs/ui/iOS · QR setup@1x.png` in Cobalt (its camera, and the two other ways in, arrive in step 4; the second button says **Set it up by hand**, the third way in §4). New: `make ios-lint`, `make ios-build`, `make ios-test`, `make ios-screens` (screenshots light and dark, `ios/screenshots/`), and the `ios` CI job on a `macos-26` runner — no Apple account, no signing, no secrets. The runner's newest Xcode is 26.x, so step 8's fold layouts (iOS 27.1 SDK) will need either a newer runner Xcode or a compiler check.
2. ~~**Server: enrollment**~~ — **done 2026-10-03**. Migration 0041: `device_enrollment` (the one-time ticket), `device_identity` (what a set-up phone is) and `device_proof` (each proof used once); `device_live` and Asterisk's `ps_endpoints` now take `ios` phones, with the browser's transport and encryption, live only while the phone is enabled, unexpired, and its person is still there with that extension. `internal/enroll`: tickets (10 minutes, one phone, a signed token for the QR code or link and 8 typed characters, ADR-012), `POST /v1/enroll` (a certificate request for the Secure Enclave key → a six-month certificate from the CA's **linx-devices** provisioner, which `ca-init.sh` already made), and `POST /v1/device-token` (certificate + a one-minute single-use ES256 proof → a 15-minute token, renewing the certificate when the phone sends a new request for the same key). Admin side: `GET/POST /api/v1/enrollments`, `DELETE /api/v1/enrollments/{id}` — anyone may set up their own phone, someone else's needs `devices:write`. A device token speaks for its person with **an ordinary person's scopes whatever their role is**, is refused the moment the phone is revoked or expires, and can never make another ticket. Audit: `device.enroll_ticket`, `device.enroll_cancel`, `device.enrolled`; webhooks: `device.enrolled`, `device.expired`. Phones that go six months without being in touch expire once an hour (ADR-077), and used proofs are swept with them. Only Linx's fixed name `linx-phone` is ever signed, so a phone's certificate can never stand in for a Linx service. Tests: `internal/enroll` (the rules, proofs, replay, renewal, expiry) and `internal/store` on real Postgres. **Not here, by design:** the `/sip` relay's new way in and `/me/phone-line` for a phone (step 4, where the app needs them), and a decision for the owner in "As built" below.
3. ~~**Admin and Settings screens**~~ — **done 2026-10-03**. **Add phone** (iPhone or iPad, a name, then **Show a QR code** / **Email a link** / **Set it up by hand**) on My account → **My phones** for yourself and on People → a person → **Phones** for an admin; the code box shows the QR picture, the 8 characters, a countdown and **Waiting for the phone…** until it finishes, and **Cancel** kills the code. The phone lists show online, when each was last in touch, **Expired** with **Set it up again**, and stopping one (**I've lost it** for yourself, **Stop it** for an admin); Extensions' Phones list says the same and no longer offers a SIP password for a phone, which hasn't got one. New: `GET /api/v1/me/phones` and `DELETE /api/v1/me/phones/{id}` (a person's own, no scope), `send_email` on `POST /api/v1/enrollments` (`email.KindPhoneSetup`), `phone` on a device (last seen, set up again, expired) from `device_identity`, and the public page **/set-up-phone** an emailed link opens on the phone itself (the code is in the link's `#fragment`, which browsers never send to a server). Help guide `iphone-and-ipad`, screenshots and the phone-width sweep updated.
4. **The app signs in and calls** — split in two, because the two halves are a session's work each (2026-10-03):
   - ~~**4a. The app signs in**~~ — **done 2026-10-03**: scan, Secure Enclave key, certificate request, Keychain, device token, and the phone line, with the `/sip` relay's new way in on the server. "Step 4a, as built" below.
   - ~~**4b. The app calls**~~ — **done 2026-10-03**: Google's WebRTC pinned by version and checksum, the Swift SIP-over-WSS user agent, the browser's Opus settings, `*43`, and calls in and out while the app is open. "Step 4b, as built" below.
5. ~~**Server: push gateway + hold the call**~~ — **done 2026-10-04**: the Apple client, the sealed key, per-phone rate limits, the dialplan's hold, the metrics, the quiet notifications, and the admin card. "Step 5, as built" below.
6. ~~**The app rings**~~ — **done 2026-10-04**: PushKit, CallKit, the lock screen, a killed app, a car, two calls, a caller who gives up, and China's in-app ringing. "Step 6, as built" below.
7. ~~**The rest of the app**~~ — **done 2026-10-04**: the four tabs, 1:1 video with a layout that follows the screen, Calls, Team with presence, Voicemail and Settings. "Step 7, as built" below.
8. ~~**iPad, adaptive and fold layouts**~~ — **done 2026-10-05**: every tab a list beside a detail, a call in its own column rather than over the top, and the fold's own geometry (`ReservedRegion`, iOS 27.1, ADR-082) in place of the near-square guess. "Step 8, as built" below.
9. ~~**Lifetime and loss**~~ — **done 2026-10-05**: renewal, the six-month idle expiry, revoke-everywhere, "set this phone up again", and the security review with its `THREAT_MODEL.md` rows. "Step 9, as built" below.
9b. ~~**`*97` and the message-waiting light**~~ — **done 2026-10-05** (ADR-083): dialling `*97` from a phone plays that extension's own new messages, and the light comes on when there is a new message and goes out when there isn't. "Step 9b, as built" below.
10. **Finish**: ~~`TEST_MATRIX.md`~~ (**written 2026-10-04 as `docs/TEST_MATRIX.md`**, when the first TestFlight build reached a real phone and the owner could start working down it), ~~`APPLE_SIGNING.md`~~ (**written 2026-10-04 as `docs/ops/APPLE_SIGNING.md`**, because the owner needed it before the signing session rather than after), `STORE_SUBMISSION.md`, resource and data-per-minute measurements in `docs/RESOURCES.md`, `docs/DEMO_PHASE2.md`, and the demo on the test VPS.

### Step 9b, as built (2026-10-05)

**A desk phone can hear its messages, and its light tells the truth.**
Both are things Phase 1F parked until Linx could control a call through
ARI (ADR-069, ADR-083); the owner folded them into this phase on
2026-10-04.

- **`*97` plays your own new messages.** The dialplan hands the call to
  the control plane (`Stasis(linx,voicemail)`), which answers it and
  plays. **Whose messages is decided in the control plane, from the
  endpoint the channel belongs to** — not from an argument the dialplan
  passes and not from anything the phone sent — so a phone can only ever
  reach its own extension's box. Each message is read out of the database
  into a folder only Asterisk reads (in memory, one message at a time,
  removed the moment it has played), announced as "Message from" and the
  caller's number read out a digit at a time, and played. **1** hears it
  again, **2** moves on, **3** deletes it, and with no key pressed the
  next follows in three seconds. A message heard to the end stops being
  new — the same rule the web app follows, so hearing it here clears the
  badge there — and one skipped stays new. A box that is off, or an
  extension that has none, hears so. With the control plane away the
  caller hears "nobody can take your call right now" rather than silence.
- **The light is a count Linx gives Asterisk, not one Asterisk works
  out.** The image now carries `res_mwi_external` (so `app_voicemail` is
  still not built and there is still one message store, ADR-069), and the
  control plane sets each box's new and old counts over ARI: at every
  change to anybody's messages, and again whenever Asterisk reconnects,
  because a phone engine that has just restarted knows nothing. Only what
  changed is sent, so a quiet system sends nothing at all, and a Linx that
  drifted puts itself right at the next change. Migration 0046 puts the
  box's name on that person's **desk phones and softphones** only: the web
  app and the iPhone app count their own messages and show a badge, and an
  unsolicited NOTIFY on a browser's line would be noise. The path that is
  proved end to end is the one real desk phones use — the phone
  **subscribes** when it signs in and Asterisk notifies it on every
  change (the call suite drives it with SIPp and reads the light out of
  the NOTIFY). A phone that never asks would need the unsolicited kind,
  which is configured but may not reach it, because Asterisk cannot
  enumerate endpoints that live in a database. Row 5b.1 of the test
  matrix is what settles it on the owner's own phones.
- **ARI is read-write now** (ADR-083). It is the one thing in this step
  that widens anything: a control plane someone had broken into could also
  hang up or redirect a call. It already holds the database, every secret
  and Asterisk's own password, so this widens what an attacker *does*
  rather than what they can reach — and Asterisk gains nothing: still four
  views and one `INSERT`. `docs/THREAT_MODEL.md` carries the row.
- **Eleven new recordings and no speech at call time**: `digit-0` …
  `digit-9` and `plus`, in Linx's own voice (ADR-065), so a caller's
  number can be read out; plus the seven sentences `*97` says. `make
  prompts` no longer re-records a message that already has a file — the
  voice is not bit-for-bit repeatable, and regenerating everything
  quietly re-recorded eight messages the owner had already heard.
- **In call history** a `*97` call reads "Listened to voicemail" and
  counts as **answered** — Linx answered it and played the messages, the
  same shape as the echo test. No new outcome was needed, so nothing
  changes for the API, the web app or the phone app.
- **A person's own box only.** A ring group's messages have no phone to
  light up and no one person to mark them heard; they stay in the web app
  and the phone app for every member. Offering them here would mean a menu
  before the messages. Worth revisiting if the owner wants it.
- **Tests:** `internal/voicemail` drives a whole call against a stand-in
  phone engine (no messages, no box, a box switched off, two messages
  played through, 3 deleting, 1 repeating, a caller hanging up mid-way, a
  call that isn't `*97` left alone, and the digits a number turns into),
  the light (what is sent, what is not sent twice, a box that went away,
  a reconnect, a refusal tried again), and that every sound `*97` can
  play is in the image. The **call suite** proves it on the real image
  end to end: a message inserted, the light going on, `*97` playing the
  intro, the caller's digits and the message itself from its own file,
  the message no longer new, the folder empty, and the light going out.
- **What it costs** (`docs/RESOURCES.md`): the Asterisk image **+0.81 MB**
  (six modules, 528 KB, and the new recordings, 300 KB), the control plane
  **+0.05 MB**, no new container and no new timer — the light waits on
  changes, it doesn't poll. One more in-memory volume, 16 MB, holding one
  message only while it plays.

### Step 9, as built (2026-10-05)

**What stops a phone, how fast, and what it leaves behind.** Most of this
step turned out to be already built — steps 2 and 4a put the rules in from
the start, where they belong — so the work was to **prove** it end to end,
close what was missing, and do the security review §10 reserves for this
step. The review is in `docs/THREAT_MODEL.md` ("Phones: identity, lifetime
and loss review"), with six new rows in the STRIDE table and three
corrected.

- **Five things stop a phone, and nothing else** (§9, ADR-077): revoking it,
  disabling its person, taking their extension away, changing their
  password, and six months with no contact. Each is read on **every**
  request and by Asterisk's own view on every registration and call. Two of
  them — revoking and the six-month expiry — also close the phone's open
  `/sip` connection the moment they happen, so a call on a lost phone drops
  mid-sentence; a password change closes it through the same path that ends
  that person's browser sessions; and the relay re-checks every line every
  15 seconds, so **the longest a stopped phone can hold a line is 15
  seconds**. Being disabled is reversible and the phone comes back; revoking
  is for good.
- **A stopped phone leaves nothing behind.** Its SIP password was only ever
  in the app's memory, its proofs are swept within the hour, and — new in
  this step — **the Apple tokens Linx held for it are cleared** when it is
  revoked or expires, so there is no way left to reach a phone nobody has. A
  *disabled* person's phones keep theirs, because they work again the moment
  the person does. Nothing was being sent to a stopped phone in any case
  (`device_wakeable` names only phones that are still set up); this is about
  not keeping it.
- **The pinned CA question is settled, by not pinning it.** §4 said the app
  "notices if Linx's own CA is ever replaced", and it never did. Looked at
  properly, it shouldn't: Linx knows a phone by the **fingerprint of the
  certificate it issued**, not by validating a chain, and the app's
  connection is already proved by the server's public certificate. Refusing
  a renewal from an unfamiliar internal CA would brick every phone on a
  server whose CA was legitimately replaced (a re-install, a restore) and
  stop nothing, since anyone able to answer for that server would hold its
  public certificate anyway. So the root is kept as given, unused, and the
  comment now says so. This also answers the open note left in §4 about a
  replaced internal CA, and the moved-server checklist in
  `docs/INSTALL.md` §8.
- **The phone says what it joined.** Settings → This phone's line now shows
  the **Person** it signs in as and the **Linx server** it was set up on,
  beside the extension and the date it would have to be set up again. A
  setup code decides both, so the phone should say them out loud.
- **Two stale lifetimes corrected**: the `linx-devices` provisioner is
  described as six months, not the 7 days of the first draft
  (`services/control-plane/enroll.go`, `docs/ARCHITECTURE.md`), which is
  what `linx setup` has actually been setting since 2026-10-03.
- **Tests:** `internal/store` on real Postgres walks the whole life of a
  phone in one test — set up, renewed, six months of silence, back again, a
  password change, disabled, revoked — and now watches the Apple tokens
  through all of it (gone on expiry, gone on a password change, **kept**
  while merely disabled, gone on revoke). `internal/enroll` already covered
  the rules themselves; nothing there needed changing.
- **What this step did *not* need:** no migration, no new endpoint, no app
  screen beyond the two lines in Settings, and no change to how a phone
  signs in. Nothing on the server has to be updated for it except in the
  ordinary way.

### Step 8, as built (2026-10-05)

**The iPad is an iPad app now, and a folding phone is asked where it folds.**
The owner's condition on ADR-076 — "the iPad must look like an iPad app, not
a stretched iPhone one" — is what this step is held to.

- **Every tab is a list beside a detail** (`NavigationSplitView` in each of
  the four), so an iPad shows the tab sidebar, the list and what you picked,
  the way Mail does. iOS collapses exactly the same screens back into one
  pushed column on a phone, so there is one set of screens and not a line of
  "which device is this" in any of them. **Calls** gained a call's own page —
  what happened, when, how long, which group rang, who answered, and **Call
  back** — which a phone now pushes when a row is tapped and an iPad shows
  beside the list. **Team** gained a person's card (extension, what they are
  doing this second, **Call**, **Video call**, **Add to favourites**).
  **More** opens Voicemail and Settings beside its list instead of over it.
- **The keypad is laid out for a tablet**, not floated in the middle of one:
  on a screen with room for it, the starred people stand beside it under
  **Favourites**, with the last number rung above them — the buttons down the
  side of a desk phone, which is what a tablet on a desk is. Tapping one
  *puts the number on the keypad* rather than ringing it, for the same reason
  redial does; the phone beside it rings. Starring moved to one list for the
  whole app (`HomeView` owns the `Favourites` and hands it to Team and the
  keypad), so the two can't drift apart.
- **A call stands beside the app rather than covering it** on a screen with
  the room (820 points across and a regular width: an iPad either way up, an
  opened-out Duo; an iPad mini upright or a narrow window is a phone again
  and the call covers it). The app itself never moves between containers as a
  call comes and goes — doing that would rebuild every open screen and take
  the Team websocket with it — so the list stays exactly where it was.
- **The fold, from the phone instead of from a guess (ADR-082).** iOS 27.1's
  `UIView.reservedRegions(kind: .division)` says where the crease really is;
  `Crease` turns that into a plain value (where the band is, which way it
  runs), and the two-panel layouts divide along it: the video call's picture
  and its buttons meet on the fold, and a call beside the app takes one leaf
  while the app keeps the other. `ScreenRotation` no longer guesses that a
  700-point screen is a tablet, either — a phone that reports a fold turns
  like one. With no fold to ask, every rule falls back to the shape of the
  screen exactly as step 7 left it.
- **It still builds on Xcode 26 and 27.0.** The fold code is compiled only
  when the SDK knows about it (`LINX_FOLD_SDK`, a build setting keyed to the
  SDK and written as "not these old ones", so a future Xcode needs no
  change) and sits behind `#available(iOS 27.1, *)` as well. Checked both
  ways on 2026-10-05 by reading the symbols out of `Crease.o`: 107 under the
  beta, none under 27.0. CI is untouched.
- **Opening the phone out, mid-use (the owner's point, 2026-10-05).** Unfolding
  is not one event the app is told about: the window grows, the size class
  changes, and only a moment later does iOS say where the crease is. So every
  rule is asked the same question at each step, and the steps agree — the
  near-square rule already says "two panels" before the fold is reported, and
  the crease then *confirms* it rather than changing it, so nothing jumps. The
  things that must survive the transition are deliberately not in the views
  that get rebuilt: the call itself lives in `PhoneModel` and cannot drop, the
  app never moves between containers as the call comes and goes, the typed
  number and the chosen row are model and view state that outlive the reflow,
  and the **in-call keypad and Call details moved into `PhoneModel`** for this
  reason — a keypad that shuts itself while somebody is typing a PIN into a
  menu is its own small disaster. `UnfoldingTests` walks shut → open → open
  with the fold known → shut again. **Nobody has watched it happen**: the
  simulator's fold can't be driven from the command line and no real Duo
  exists. The nearest thing that can be watched today is dragging an iPad's
  Split View divider while a call is up (`docs/TEST_MATRIX.md` row 4b.8a).
- **Tests:** `ios/LinxTests/BigScreenTests.swift` — what counts as a fold and
  what doesn't (a band across the view, one in a corner, one so near the edge
  that two panels would be pointless), a call dividing along the fold even on
  a screen the old rule called tall, the panels meeting on the crease, where
  a call goes on an iPhone 18 Pro Max and an iPad Pro 13-inch either way up,
  the call's column taking one leaf of a Duo, and a folded-out phone turning
  like a tablet. Plus the two-column screens rendering at iPad size in
  `ScreenTests`. None of them needs the 27.1 SDK, so CI runs the lot.

### Step 7, as built (2026-10-04)
**The app is the whole app now**: four tabs — **Calls**, **Team**, **Keypad**, **More** (Voicemail, Settings) — and 1:1 video. Four and no more: Meetings arrive with Phase 3 and Chat with Phase 4, and App Review refuses a tab that does nothing (§14 item 2).

- **Video is added to a call, never rung as one** (`ADR-079`, new). The call rings as an ordinary call and either side puts a camera into it with a **re-INVITE**; turning it off is another one, and the conversation never stops for either. So the lock screen, CarPlay and a headset see what they always see, the push still carries the same three facts, and a network that can't carry a picture costs the picture and nothing else. The **video button** in Team rings first and turns the camera on the moment they answer. App side: `Core/Media/Camera.swift` (640×480 at 24 frames, the smallest format the camera itself can give), the picture in `WebRTCMedia`, `SIPUserAgent.setVideo`, and a `rollback` (RFC 8829) that puts the call back untouched when the other side won't have it.
- **It is small on purpose.** H.264 first, because an iPhone encodes and decodes it in hardware and the battery pays for anything else; **600 kbit/s** at most, written into the SDP (`b=AS`) and into the sender's own settings; and when WebRTC says the link has under 150 kbit/s to spare for three readings running — a quarter of a minute — **the camera goes off by itself** and the person is told the call carries on. Sound comes first, every time (CLAUDE.md's low-bandwidth rule).
- **A picture is never switched on by somebody else.** When the other side adds video, this phone answers "I'll watch, I'm not sending" (`recvonly`) and shows theirs; its own camera waits for its own button, which is also why no permission prompt can ever appear mid-call without the person asking for it.
- **The screen follows the screen, not the device** (`CallLayout`, §8, the owner's ask on 2026-10-04): a phone upright (picture full-bleed, buttons along the bottom), a phone on its side (buttons in a column down the trailing edge, out of the picture and away from the camera), and **two panels** — the picture in one, everything else in the other — on an iPad and on an **iPhone Duo opened out**, side by side when the screen is square-ish so that nothing anyone presses sits on the crease, over-and-under on a tall iPad. The Duo's outer screen is simply a narrow phone and gets the first layout. It is one pure function with its own tests at the real device sizes, and **step 8 swaps the near-square rule for the fold's own geometry** (`ReservedRegion`, `ArrangementView`, iOS 27.1) without touching anything else.
- **Who can be on the other end.** Both ends have to do video, so today that means **app to app** — an iPhone to an iPad, or two iPhones. The web client still asks for sound only (`web/src/phone/line.ts`), so a video call to a browser is a call the browser answers without a picture, which is exactly what it should do; video in the browser is its own piece of work for a later phase. A desk phone is never offered video at all.
- **Server: 1:1 video through Asterisk** (migration 0044). The app's and the browser's endpoints allow `h264,vp8` and one video stream; a desk phone or a gateway keeps exactly the codecs it had, because offering video to a device that doesn't expect it is how a working phone call stops working. Asterisk passes the picture between the two WebRTC endpoints and never transcodes it.
- **Server: the Team list, live, for a phone.** `GET /api/v1/team/live` now takes a device token in the `Authorization` header with no `Origin`, exactly as `/sip` does, and closes when the phone is stopped. That is the whole server-side addition for this step: everything else the screens read — the team, call history, voicemail, the two badges, "my status" — are the endpoints the web client already uses, with a phone's ordinary read-only scopes.
- **The tabs.** **Calls**: the history from `/me/calls`, missed in red, All/Missed, tap to ring back, older pages on demand; opening it clears the badge for that **person** on every device they have. **Team**: the directory live over the websocket, presence dots with the words beside them, search, **my own status** (Available / Away / Do not disturb, which stops the extension ringing), and call or video-call anybody. **Keypad**: as before, now with a **Video** button in a call. **More**: **Voicemail** (play, which marks it heard, swipe to delete, ring back; the audio is fetched when play is pressed, played out of memory, and never written to the phone) and **Settings** (status, the line, the camera, how calls arrive here, and **Sign out of this phone**).
- **Five small things the owner asked for the same day** (2026-10-04), all of them this phone's own and none of them anybody else's business: **redial** (the green button with nothing typed brings the last number back, ready to ring — it fills it in rather than dialling, so a pocket can't redial anyone, and `*43` is never offered back because the sound test is not a number); **Appearance** in Settings (Light / Dark / Match this device, the same choice the web app has had since 2026-09-30); **search in Calls** (a *number* is searched for at the server, so it finds calls long off the screen; a *name* is matched against the calls in hand, because the server has no name search and pretending otherwise would be a lie); **favourites in Team** (swipe to star, starred people in their own section at the top, kept on this phone by extension number — `Features/Team/Favourites.swift`); and **"Show Linx calls in the Phone app"** (CallKit's `includesCallsInRecents`, on by default — off keeps work calls out of the iPhone's own Recents, and the Calls tab still has every one of them; the system reads it when a call starts, so it takes effect from the next call).
- **Still nothing in the background.** The Team websocket lives only while the app is in front, and nothing polls: Linx sends the list when it changes and says "a call ended" or "a voicemail arrived", and the app then asks for *its own* counts — so one person's numbers can never reach another's phone.
- **Tests:** `ios/LinxTests/VideoTests.swift` (the picture going in and coming out, a refusal rolled back with the call untouched, both sides asking at once, Asterisk challenging a re-INVITE, their camera arriving, a camera the person said no to, the link going thin, the Team list's video button, hanging up); `CallLayoutTests` at the real sizes of an iPhone upright and on its side, a Max-sized iPhone on its side, an iPad both ways, an unfolded Duo and a shrunken iPad window; `ScreensDataTests` against a stand-in Linx (every shape the screens decode, the methods and the merge-patch, the badges and what clears them, and which searches reach Linx); `SmallThingsTests` (redial and what it refuses to remember, favourites surviving a restart with nobody lost or doubled, the three appearances, and the Phone-app setting reaching the system); the Go side, `TestTeamLiveForAPhone` and migration 0044's row in `internal/db`. 94 tests in the app.
- **What it costs.** The app **13 → 15 MB** (`docs/RESOURCES.md`): Google's WebRTC is the same **12 MB** — the video codecs were already inside it and nothing of them runs until a camera is switched on — and Linx's own program grew **1.9 → 3.1 MB** for the new screens, most of that SwiftUI's generated code. Data per minute of a video call is measured on real devices at the demo; at the ceiling it works out at about 4.5 MB a minute each way, against 0.2 MB for sound alone.

**Screenshots:** `ios/screenshots/iPhone-17/`, `iPad-Pro-11-inch-M5/` and `iPhone-Duo/`, light and dark, with the call screens shot on their side as well. The simulator has no camera, so both video tiles show their "camera off" state — the layout is what these shots are for. **Needs the owner's look.**

**The iPhone Duo, as far as a simulator goes** (2026-10-04). Its **outer screen** is shot and is right: at 1398×2034 it is a wide phone, and the call takes the upright layout — the picture full-bleed, the caller top-left, this phone's own picture top-right, the buttons along the bottom (`LINX_IOS_DEVICE="iPhone Duo" DEVELOPER_DIR=/Applications/Xcode-27.1-beta.app LINX_IOS_DISPLAY=1 ios/tools/screens.sh`; a foldable has two screens and `simctl io … enumerate` lists them — the outer is 1, the inner 3). The **inner screen is dark until the simulated phone is opened out**, and opening it is Simulator's own Device menu with **no command behind it**, so that shot is taken by hand. What the inner screen will show is already decided and tested — 1024×1080 is square-ish and regular, so `CallLayout` gives it two panels side by side, with every button in one half and nothing on the crease (`CallLayoutTests`) — and **step 8 is where it is photographed and where the fold's own geometry replaces the near-square rule**.

### On a real phone, build 4 (2026-10-05)

What the owner found with build 4 on an iPhone 18 Pro Max, and what it changed
(`docs/HISTORY.md` "Build 4 on the phone"). All five are things only a real
phone, a real pair of AirPods and a real mobile network can show.

- **The camera must not touch the sound.** WebRTC's camera capturer gives its
  capture session an audio session of its own, and starting one takes the sound
  hardware off the call — the AirPods went the moment the camera came on. The
  camera now borrows the **call's** session and may not configure it
  (`Camera.usesTheCallsAudioSession`, held to it by a test). The rule for the
  whole app: while CallKit owns the session, nothing else configures it.
- **The sound button offers whatever is connected, when it is connected.** It
  becomes the system's own picker as soon as there is anywhere else to send the
  sound — read from `availableInputs`, which is how iOS says a headset is there
  at all — rather than only once the sound has already moved; and a device
  arriving (`newDeviceAvailable`) **clears an explicit loudspeaker override**,
  because iOS leaves the override where it was put and the new AirPods would
  otherwise never get the call.
- **Their picture can stop and come back** (`PictureWatch`): two quiet readings
  take it off the screen, one new frame puts it back, the track is kept either
  way, and WebRTC's silence about an existing track no longer means the picture
  is gone for the rest of the call.
- **A one-way video call looks like an ordinary call** (ADR-079's condition):
  with only this phone's camera on, **this phone's own picture takes the big
  screen**, with their name and "Their camera is off. This is yours — they can
  see you." beside it, and no second copy in the corner.
- **A picture going into a call takes a moment and says so.** Asterisk's answers
  carry no BUNDLE group, so the video stream gathers its own routes — on a
  mobile network that is a TURN allocation of its own, a second or two.
  `PhoneModel.changingVideo` turns the button into a spinner saying "Starting…",
  and a second press while the first is in the air does nothing.
- **Call details, for the call that connects and carries no sound.** The one
  thing still open is no sound on a call made from outside the owner's network
  (the echo test is heard over the home VPN; on 5G nothing), after the
  relay-credentials and gathering fixes of 2026-10-04. Instead of guessing a
  third time, the app now says **where the sound stops**: a call up for seven
  seconds with not a byte arriving shows a red line, and the "Encrypted · …"
  line opens **Call details** — whether a route was agreed at all, direct or
  relayed and over what, sound in and out in bytes, round trip, the routes this
  phone found, the relay's addresses and how long its credentials have left,
  and **what the relay answered if it refused the phone**, word for word from
  `didFailToGatherIceCandidate` (401 credentials it wouldn't take, 701 a relay
  it couldn't reach). **Copy these details** puts the lot on the clipboard. A
  relay that refused the phone *and* left it no route is said out loud during
  the call; a relay reached over TLS but not UDP says nothing, because that is
  ordinary. It is a diagnostic and nothing more: no decision in the app is made
  from it.

### The silent call from outside, found and fixed (2026-10-05, ADR-080)

**WebRTC wouldn't trust the relay's certificate, so there was never a relay.**
The owner's calls from outside their network connected and carried no sound,
while the same phone on the same Wi-Fi was perfect and **the web client on the
same mobile network was perfect too** — which was the clue that mattered: a
browser verifies certificates with the system's trust store, and Google's
WebRTC verifies them against a list compiled into the library.

Let's Encrypt began issuing from a new chain (ISRG Root YE) on 2026-09-29; the
owner's relay certificate is from 2026-09-29. WebRTC's bundled list predates
it, so every `turns:` connection failed before it began ("Failed to establish
connection"), no relay candidate was ever gathered, the offer went out after
waiting the full ten seconds with nothing but the phone's own private
addresses in it, and on a network where the relay is the only way through the
call connected and nobody heard anything.

Proved, not guessed, on 2026-10-05 against **two** Linx servers behind two
different front doors (`home.mym.ae` through its proxy, `vps.mym.ae` on 443
directly): the same failure on both, Apple's own TLS to the same host and port
`ready` in the same process, a full TURN allocation from the same Mac in
Python, and the gathering succeeding the moment certificate checking was taken
out of the picture. The fix (**ADR-080**) is `RelayCertificates`: iOS's own
trust store answers the question, pinned to the relay hostname Linx issued,
and WebRTC only asks after its own list has failed. Verification is never
disabled or weakened. After it, both relays gather a relay candidate in about
a second.

Worth knowing for the owner's own network: from the internet, **UDP 443 on
`turn.home.mym.ae` answers from a different machine** (it calls itself
`pbx.mym.ae`) which refuses this Linx's credentials, so the UDP relay URL can
never work from outside until that port forward is corrected. The TLS one does,
which is what every restrictive network needs anyway.

### Build 6 on the phone: the relay cap, and two screens telling the truth (2026-10-05)

With the certificate fix on the phone and the owner's old UDP 443 forward
switched off, **the call had sound from outside at last**. Three things left,
all three found:

- **A red line said "this phone couldn't reach Linx's call relay" on a call the
  owner could hear.** The warning was decided the moment a relay address
  failed, and the address that fails usually fails first: with UDP 443 no
  longer forwarded, every call now has one failure and one success. It is now
  decided only once the phone has **finished looking for routes**
  (`CallDiagnostics.settled`), and it comes off the screen if a route through
  the relay turns up after all.
- **The picture froze within seconds of coming on, and it was the relay's own
  bandwidth cap.** `turnconf.MaxBPS` was **64 kB/s** — sized in Phase 1C for
  Opus at 8 kB/s and never revisited when video arrived in step 7. A 1:1 video
  call is 600 kbit/s of picture plus 24 of voice, about **90 kB/s** with every
  header, and more for a moment at each keyframe. So every **relayed** video
  call was throttled and the picture stopped, while a video call on the same
  LAN was perfect — which is exactly what the owner saw. Now **160 kB/s**:
  twice what a video call needs, and still nowhere near enough to move data
  about with, which is what the cap is for. **A server has to be updated for
  this** (the cap is in coturn's config, which `linx-coturn` renders at start).
- **Stop video sometimes came back as a video call with nobody's camera on.**
  The frame watch, which brings a picture back when frames start arriving
  again, was treating the last frames of a call whose video had just been
  turned off as a picture returning. It now needs the other side's **SDP** to
  say they mean to send one as well: the SDP says whether a picture is meant to
  be there, the frames say whether it really is, and both have to agree before
  it goes back on the screen.

### A picture that follows the link, and a screen that stops flickering (2026-10-05, ADR-081)

The owner's video call worked once the relay's cap was raised, and they asked
for the next thing: **"adaptive based on connection speed — HD on a fast link,
less on a slow one, and when it is too low drop the video and switch to voice
only."** They also found the screen **alternating** between the video call and
the voice call after pressing Stop video.

- **Five steps** — 240p, 360p, 480p, 540p, 720p at 200, 350, 600, 900 and
  1500 kbit/s (`VideoQuality`). Every call starts at **480p/600 kbit/s**, which
  is what every Linx relay has always carried and what the first seconds of a
  mobile call can be trusted with, and climbs from there.
- **Down at once, up slowly.** A step down on the first reading that says the
  link can't hold the picture; a step up only after about ten seconds of room to
  spare, a third more than the next step needs. Readings come every three
  seconds while a picture is going out (five otherwise).
- **Never more than the road allows.** A relayed call is held to what Linx's
  relay will carry for one call: the server now hands the phone that number
  (`max_bitrate_bps` in the relay credentials, from coturn's `max-bps`), less
  the voice and the packets' own weight. With today's 160 kB/s that is 540p; a
  call straight to the other side may go to 720p. An older server that doesn't
  say stays at 480p, which is always safe.
- **Sound still comes first**, unchanged: under 150 kbit/s for three readings
  the camera goes off by itself and the call carries on as a phone call.
- **Inside a step, WebRTC decides** (`degradationPreference = balanced`), which
  is the fine work between our readings.
- **The screen stops flickering.** `CallVideo` now keeps "they **say** they are
  sending a picture" (their SDP) apart from "their frames are **arriving**". The
  first decides whether this is a call with a picture in it; the second decides
  only what is drawn inside that screen. Before, a stutter — or the last frames
  of a camera just switched off — flipped the whole app between the video screen
  and the voice screen every few seconds.
- **Call details** shows the step in the words people use for it ("540p").

### Build 8 on the phone: a picture nobody is sending leaves the call (2026-10-05)

The flicker was gone and the owner found the three things left in it.

- **A video screen with nothing in it isn't what the call is.** With this phone's
  camera off and their picture stopped, the app sat on the video screen saying
  "Their picture has stopped" — where the owner rightly expected the voice call
  back. A picture is in a call only while somebody is sending one, so when
  theirs stops and ours is off, Linx now takes the video **out of the call**
  with one more re-INVITE (`WebRTCMedia.dropVideo`, `SIPUserAgent.dropVideo`),
  both ends agree about it, and either side pressing the button puts a picture
  back exactly as it did the first time.
- **The big picture goes within a few seconds of Stop video,** not ten: the
  moment this phone's camera goes off, one quiet reading is enough to call their
  picture stopped (`PictureWatch.oneReadingIsEnough`), because what was arriving
  was very often this phone's own picture coming back — which is exactly what
  the sound test does.
- **A picture just switched on is no longer made worse while the link is still
  being measured** (owner: "it shows my actual camera with the same quality but
  then it mirrors it with lower quality"). The room on a link is measured from
  what is flowing on it, so for the first seconds of a picture the measurement
  is still catching up and reads low — and the ladder was acting on it. It now
  ignores the first two readings for going **down**, and needs **two** tight
  readings rather than one before making the picture smaller. Going up is
  unchanged: about ten seconds of real headroom.

In the sound test the big picture is the echo of this phone's own camera — the
far end's view, so not mirrored — and when the echo stops it becomes this
phone's own preview, which is mirrored. Both are right; they only look odd
because in that one call the two pictures are the same face.

### Either picture can have the big screen (owner's ask, 2026-10-05)

"I want to have the option to switch which camera takes the big screen (mine or
the other party), so tapping on my camera — the small screen — makes it the big
screen and the other party goes to the small screen, and vice versa."

Built as one piece of state for the call (`PhoneModel.myPictureIsBig`, swapped
by `swapPictures()`) and two views that can each draw either picture
(`Picture`, `SmallPicture`, `Whose`). A tap on **either** picture swaps them.
It means something only while both cameras are on — with one picture in the
call, that picture *is* the big screen — and it goes back to the ordinary way
round (the other person big) as soon as a camera leaves the call, so the next
video call never starts inside out. The two-panel layouts on an iPad and an
opened-out Duo swap the same way.

### The question that answered itself (2026-10-05)

On both the iPhone and the iPad, the question Linx asks when the other side
turns their camera on — "Turn mine on too / Not now" (ADR-079, the owner's own
condition) — **appeared and vanished within a second**, with no chance to
answer it, and the camera rightly stayed off.

Two faults in how it was shown, both mine:

- It was answered **"Not now" by any dismissal**. The alert's `isPresented`
  binding treated SwiftUI setting it to false as the person choosing "Not now".
  But a picture arriving brings several changes at once — the call screen
  changes, the sound moves to the loudspeaker, the system's own call is updated
  — and any one of them taking the alert down counted as an answer. Now only
  the two buttons answer: they clear the question themselves, which is what
  takes the alert away, and nothing else may.
- The alert was attached to a view whose **identity changed** at exactly that
  moment: `Group { if video.on { … } else { … } }` is two different views, so
  switching from the voice screen to the video screen took the alert with it.
  One `ZStack` now stays put while its child changes.

Their camera going off again while the question is still up clears it too:
there is nothing left to answer.

### A video call keeps the screen awake (owner's ask, 2026-10-05)

"The screen should not time out if I am on a video call/meeting, unless I press
the power button." So `PhoneModel` holds the idle timer off
(`UIApplication.isIdleTimerDisabled`) while a call is **active and has a
picture in it**, and lets it go the moment the picture or the call ends — a
phone left on a table after a video call sleeps as it should. A call with no
picture is left to iOS exactly as before: holding a phone to an ear is what the
proximity rule is for, and a call in a pocket has no business keeping a screen
alight. The two rules about the screen during a call now live side by side in
`theScreenDuringTheCall()`. **Meetings (Phase 3) must do the same** when they
arrive.

### A picture nobody is sending, on a call to the outside world (2026-10-05)

On the iPad, video on and then off again on a call to an **outside number** left
the split video screen up with neither picture in it; the same thing on an
internal call came back to the voice screen properly. Both halves of that are
explained by the far end being a phone line: Asterisk negotiates video per
channel, so it answers the app's video offer with a video stream of its own
even though the trunk leg has none — their camera is *announced* and not one
frame ever arrives.

Two rules were missing for that:

- **Stopping the camera when nothing is arriving takes the picture out of the
  call** in the same re-INVITE, instead of asking to go on watching a camera
  that has sent nothing (`stopCamera` now looks at `theirPicture`, not
  `theirs`).
- **A picture announced but never arriving is counted too**
  (`checkNobodyIsSending`): the frame watch only notices a picture that
  *stops*, so one that never starts needed its own count. After two readings
  with nothing arriving and this phone's camera off, the video leaves the call.
  It can never take a camera away from the person holding the phone — it only
  acts when **neither** side is sending.

### Step 6, as built (2026-10-04)
**A sleeping phone rings now.** Step 5 gave the server the push; this is the app's side of it, and the rule that shapes all of it is Apple's: a VoIP push must report a call to CallKit *at once, every time*, or iOS kills the app and stops delivering its pushes (§14 item 1).

- **Ready before there is a screen** (`ios/Linx/App/AppDelegate.swift`). PushKit only delivers a call to an app that was already listening, and a push to a closed app launches it with no window at all — so registering happens in the app delegate's first moment, not in a view's `task`. SwiftUI keeps the lifecycle; the delegate is there for this one thing. `AppModel.shared` is the one model both the delegate and the screens look at.
- **The order of things** (`AppModel.woken`, `PhoneModel.woken`). A push arrives → the system is told there is a call, with the caller's number from the push → Apple's completion handler is called → *then* the app signs in, asks for its phone line and registers, while the server holds the call ringing. Nothing slow or fallible comes first. A phone woken for a call that never arrives stops ringing after 20 seconds and closes its line again, because a registration left behind would make Linx think the phone is awake and skip the push next time.
- **The system owns the call** (`ios/Linx/Core/Call`). Every button goes one way round: the app **asks** the system (`CXCallController`), the system **tells** the app (`CXProviderDelegate`), and only then does the app act. So Answer on the lock screen, Answer in a car, a headset's button and the app's own screen are one path and can never disagree. Outgoing calls are reported too, so they show in the Phone app's Recents and a car can hang them up. `SystemCalls` is the seam: `CallKitCalls` is the real one, `NoSystemCalls` is China's and the tests'.
- **The sound starts when the system says so.** WebRTC is put in **manual-audio** mode whenever CallKit is in charge, and a call's sound is enabled only in `provider(_:didActivate:)` — which is what prevents the audio dropouts reviewers notice (§14, "Audio path"). Where CallKit may not be used the app activates the session itself, exactly as it did in step 4b.
- **Which call is which.** A push carries the call's id, but Asterisk's invitation doesn't, so the invitation is matched to the push by the caller's number (digits only: the two come by different roads) and otherwise to the one that has been waiting longest. There is only ever one call on this line, so there is never much to choose between: a second caller hears busy and goes to voicemail, as they do for the browser. The push for that second caller is still reported — iOS is watching — and ended at once.
- **China** (`ADR-078`). No PushKit and no CallKit where the region is `CN`: the app registers for ordinary notifications only, sends `call_alerts`, rings on its own screen, and says so once on the signed-in screen (it moves to Settings when step 7 builds that). A ringing-call notification is never shown as a banner while the app is open — it rings instead.
- **The two tokens.** `POST /api/v1/me/phone-push` carries the PushKit token, the notification token once the person allows notifications, which Apple the build belongs to, and `call_alerts`. Which Apple is read out of the app's **own provisioning profile** (`PushEnvironment`), so a TestFlight phone and a build straight from Xcode both ring without a setting. The app asks about notifications once, when the phone has just been set up, and never from the background. A token Apple calls dead is forgotten by the server and the app sends a new one next time it runs.
- **What the app asks iOS for** (§14 items 6 and 7): background modes **`voip`** and **`audio`** and nothing else (`ios/Info.plist`), and the Push Notifications entitlement (`ios/Linx.entitlements`). The purpose strings for the microphone, the camera and the local network were already there.
- **Server, small additions.** Migration 0043 adds `device_identity.call_alerts` and widens `asterisk.linx_wake` to wait for a phone that can only be told by notification; the gateway sends such a phone a time-sensitive alert push carrying the same three facts and nothing else, counted as `linx_push_sent_total{kind="call"}`; `POST /me/phone-push` takes `call_alerts` and refuses it together with a VoIP token.
- **Tests:** `ios/LinxTests/RingingTests.swift` — the push reports the call before the line is even open; the invitation that follows keeps the id the system was given and only adds the name; a call while the app is open is reported once; a call the system won't take is turned down rather than left ringing; a push that no call follows stops ringing and closes the line; two callers at once; a caller who gives up; every button through the system, including mute from both sides and the sound handed over; the system restarting; what a push carries and nothing more; the tokens, the hex, and which Apple. Go side: `internal/push` for the China path's topic, type, expiry, level and payload, and `internal/store` on real Postgres for `linx_wake` and the new column.
- **What it costs.** The app is still **13 MB** (`docs/RESOURCES.md`): CallKit, PushKit and UserNotifications are iOS's own. Nothing new runs in the background — the websocket still lives only while the app is in front or a call is up.

**Still to come:** a sleeping phone's ringing is tested on **real devices** at the demo (§13): the lock screen, a force-quit app, Low Power Mode, overnight, a bad network, and push → ringing under two seconds measured end to end.

### Step 5, as built (2026-10-04)
**The server can now ring a phone whose app is asleep.** Nothing of it runs until an Apple key is saved, and nothing is sent to Apple before then.

- **The gateway** (`internal/push`). One guarded HTTP/2 connection to Apple — the same private-address guard webhooks use, kept open (10 minutes idle) because opening one per call would show up as ringing late. The provider token is an ES256 JWT signed with the owner's `.p8`, made once and reused for 45 minutes (Apple refuses one older than an hour, and refuses being handed new ones more often than every 20 minutes). A wake push goes to the app's `.voip` topic with `apns-push-type: voip`, priority 10 and **expiry 0** — right now or not at all, because a call is no use late. A missed call and a new voicemail go as **ordinary notifications** on the app's own topic, never as VoIP pushes: Apple kills an app that takes a VoIP push without ringing a call (§14 item 1). A token Apple calls dead is forgotten at once and the app sends a new one next time it runs.
- **What Apple is told:** the call's id, the caller's number and the time. Nothing else, and a test fails if a fourth field is ever added.
- **Holding the call** (`internal/asteriskconf`, migration 0042). Before a step rings its phones, the dialplan gosubs `linx-wake`: `LINX_WAKE(targets)` names the app phones among them that can be woken at all (a key is saved, the phone sent a token, the phone is still set up), the dialplan drops any already registered, sends `UserEvent(LinxWake, …)`, starts ringback, and waits in quarter-second steps until they arrive or the wait runs out (**6 seconds** by default, at most 15, 0 turns it off). Then every phone of the step is dialled as before and the first to answer wins. A server without push never waits, because the lookup answers nothing.
- **Both kinds of notification** (owner, 2026-10-03). A missed call comes from the call history builder as each call is written (one per person who was rung and didn't answer, and none at all for a call someone answered); a new voicemail comes from the importer as the message is kept. Both go to the phones of whoever has that extension. Chat joins them in Phase 4.
- **Measuring what matters** (§14): the gateway remembers when each phone was pushed and the ARI app tells it when that phone registers, so **push → ringable** is a measured number, not a claim. `GET /metrics` on the control plane's own loopback carries `linx_push_sent_total`, `linx_push_failed_total`, `linx_push_dead_tokens_total`, `linx_push_limited_total`, `linx_push_wake_total` and `linx_push_wake_seconds_total`; the admin card shows the same in words.
- **The admin card** — System → Settings → **Calls to the app**: the team id, the key id, the `.p8` pasted in, which Apple service (the App Store and TestFlight, or a build from Xcode), and how long a call waits. A system admin only, after "confirm it's you"; the key is sealed (ADR-030) and never read back out. Each phone says which Apple its own build belongs to, so a TestFlight phone and an Xcode build can both work at once.
- **The app's side of it:** `POST /api/v1/me/phone-push`, with the phone's own device token, is where the app says which push tokens reach it. Step 6 is where the app started sending them.
- **Rate limits per phone:** 10 wake pushes a minute, 60 quiet notifications an hour. A caller redialling, or a loop in someone's routing, can't turn into a stream of pushes — or get the app cut off by Apple.
- **Tests:** `internal/push` against a stand-in Apple inside the test process (the JWT's shape, the topics and headers, the payload's three fields, a dead token forgotten, a passing failure that isn't, the per-phone limit, both quiet notifications, nothing at all while push is off, the system-admin and If-Match rules, and what a phone may send as a token); `internal/store` on real Postgres for the settings, the tokens and **Asterisk's own `linx_wake` lookup** (no key, no token, an expired phone, push turned off); the dialplan's golden test; and the call suite, which still passes with the wake step in the path.
- **What it costs** (`docs/RESOURCES.md`): the control plane **+0.13 MB**, no new dependency, no new container, nothing running until a key exists; the admin page **+1.6 KB** compressed.

- **Fixed 2026-10-04, found by row 2.4 of `docs/TEST_MATRIX.md` on a real iPhone:** the hold above asks `${DEVICE_STATE(PJSIP/…)}` which app phones are already connected and `${IF(…)}` builds the list to wake, and the Asterisk image had **neither function** — `func_devstate` and `func_logic` were never named in its menuselect step. Both came out empty, which read as “the phone is already here”, so no wake event and no push were ever sent and a force-quit app never rang (the call went to voicemail). Both modules are now built (+146 KB), the dialplan counts only a state Asterisk actually answered as “already here” (an empty answer wakes the phone), and the call suite now checks that **every** `${NAME(…)}` in the rendered dialplan exists in the image, and that a sleeping `ios` phone with a push token really produces the wake event. `docs/HISTORY.md` has the whole of it.

- **Found on a real iPhone, 2026-10-04 (four things, all fixed):** the loudspeaker button did nothing (the route has to be put back every time CallKit hands the session over and at every route change, and a headset arriving now wins and moves the button); a call ringing with the app open showed **twice**, the app's screen under CallKit's banner — the ring is the system's and the app's screen now waits for the answer (`PhoneModel.showsCallScreen`), except where CallKit may not be used (ADR-078); the keypad didn't use a big phone's screen and is now laid out like the Phone app's (owner's words: bigger keys, a margin down each side, centred); and Settings now says whether iOS is allowing notifications, with a way to ask, because the owner's phone had sent no notification token at all. `docs/HISTORY.md` has the whole of it.

- **Three more from the owner's phone, 2026-10-04:** the app's pages no longer **turn** with the phone — only a **video** call does, an iPad and an opened-out iPhone Duo are untouched, and the picture the other side sees is unaffected either way (`ScreenRotation`); a person whose app isn't running now counts as **reachable** in the Team list, because a push wakes them (migration 0045's `device_wakeable`, read by the wake step and the list alike); and **Show Linx calls in the Phone app** now really turns Recents off — iOS reads that setting when the CallKit provider is made, so the provider is made again when it changes. `docs/HISTORY.md` has the whole of it.

**Step 6 did the app's side of this** (above): the app registers for push, reports every VoIP push to CallKit at once, and rings on the lock screen.

### Step 4b, as built (2026-10-03)
**The app is a phone now.** It signs its line in to Asterisk over the `/sip` relay and carries the sound over WebRTC, so Asterisk sees the app and the browser as the same kind of endpoint (migration 0041 already gave `ios` phones the browser's transport, DTLS media, rtcp-mux and ICE). Nothing on the server changed in this step.

- **SIP, written here, not borrowed** (`ios/Linx/Core/SIP`, ADR-006). Every maintained SIP library is GPL or LGPL, which a client bundle may not carry, and the relay allows a small subset anyway. `SIPMessage` reads and writes the text — bodies byte for byte, so an SDP's line endings survive, one-letter header names and folded lines understood. `SIPDigest` answers Asterisk's challenge (MD5, the only scheme pjsip offers), checked in a test against RFC 2617's own worked example, and counts each nonce's uses. `SIPTransport` is one websocket to `GET /sip` with the device token in the `Authorization` header and the `sip` subprotocol the relay requires, with a blank keep-alive every 30 seconds. `SIPUserAgent` is the call: REGISTER with a refresh halfway through, INVITE/ACK/BYE/CANCEL, a 180 or 183 (early media played, as the browser plays it), the right words for every refusal, an incoming call answered with a 200 that carries the answer, a second caller told 486, a re-INVITE answered, and a dialog that keeps its route set so a BYE goes where it should. **Two goes at a challenge and no more**, because the relay closes a line after three failures.
- **The sound** (`ios/Linx/Core/Media`). One peer connection per call: DTLS-SRTP, rtcp-mux, ICE straight through where there is a route and Linx's own TURN where there isn't, and **the browser's Opus settings** — `SDPTweaks` is the port of `web/src/phone/sdp.ts` (in-band FEC, DTX, mono, 24 kbps), so a poor network degrades the same way in both clients. Candidates are gathered before the invitation goes out, with the browser's rule ("a relay candidate, then 300 ms, or three seconds whatever we have"), because Asterisk doesn't take them one at a time. Keypad tones are RFC 4733 in the media, which is what the endpoint is configured for. The ring is made in memory (no sound file in the app), the same two tones every three seconds the web client plays.
- **The screens** (`ios/Linx/Features/Phone`, `Features/Home/HomeView.swift`). The keypad from `docs/ui/iOS · Keypad@1x.png` in Cobalt, with the line's state in a pill, **Test my sound** for `*43`, and the person button for this phone's details; the call screen from `docs/ui/iOS · Active call@1x.png` with mute, the keypad for menus, the loudspeaker and hang up, the caller's initials, the timer, and **Encrypted · Direct/Relayed · n ms** read from WebRTC's own statistics every five seconds. A call coming in shows only Answer and Decline. Hold, transfer, park, record and video are **not** shown: they belong to later steps, and App Review refuses placeholders (§14 item 2). `make ios-screens` now shoots six screens, light and dark.
- **While the app is in front, and no longer** (§7). The websocket opens when the app comes forward and closes when it goes away, unless a call is up. Ringing a sleeping phone is steps 5 and 6, and the help guide says so plainly.
- **Getting WebRTC** (`ios/tools/webrtc.sh`, `make ios-deps`). The prebuilt **M154** XCFramework (BSD) is fetched by a script that checks it against a SHA-256 pinned in the repo and unpacks it into `ios/Vendor`, which git ignores. SwiftPM would have been the obvious way and was tried first: **its binary-artifact download hangs on this Mac** — Xcode and `swift package` both stall right after checkout while `curl` fetches the same URL in seconds, which looks like a firewall that has to be allowed per program. The script also makes the pin plain to read and lets CI cache the zip. Going back to a Swift package later is a small change.
- **What it costs.** The app for a real iPhone is **13 MB**: 12 MB of it is WebRTC, 1.8 MB is Linx (`docs/RESOURCES.md`). Data per minute of a call is measured on real devices at the demo.
- **Tests** (`ios/LinxTests`, 47 in all): the SIP text in both directions, the digest against RFC 2617's example, the Opus settings, and the whole call flow against a stand-in Asterisk inside the test process — signing in, a call out answered and hung up, a challenged call, a busy number, cancelling before they answer (and acknowledging the 487 that follows), a call in answered and ended, a caller who gives up leaving a *missed* call, a second caller told the phone is busy, mute/speaker/tones, and the connection dropping mid-call. One test walks everything the app ever sends and fails if it is a request the relay doesn't allow or if its `From` isn't this line's own username. CI now also builds the app in **Release for a real iPhone**, unsigned, which is the only build that compiles the release-only paths.

**Screens approved by the owner 2026-10-04** (keypad, in a call, a call coming in, light and dark, on an iPhone 17), with the missing tab bar and the missing call buttons understood as later steps.

**Still to come, in order** *(as it stood on 2026-10-03)***:** push and CallKit (steps 5 and 6) are what make a sleeping phone ring; until then the app has to be open. Video, the Calls/Team/More screens, call history and voicemail are step 7.

### Step 4a, as built (2026-10-03)
**On the server.**
- **`POST /api/v1/me/phone-line`** (`internal/enroll/phoneline.go`, `services/control-plane/api/webphone.go`): a set-up phone, and nothing else, gets the SIP login of the device it was given when it was set up — the same username every time, a **new password every time**, because the app keeps it in memory only. Anything that isn't a phone's device token is refused (400 `not_a_phone`; a browser has `/me/web-phone`), and the phone is read again on the way through, so one revoked, expired, disabled or moved to another extension a moment ago gets nothing (401 `device_inactive`). Relay (TURN) credentials come with it, as the browser's do. Audit: `device.phone_line`.
- **`GET /sip` takes a device token** (`services/control-plane/sip.go`, `internal/siprelay`): the app sends `Authorization: Bearer <device token>` instead of a cookie, and **no `Origin`** is required of it — the app is not a web page and sends none, and no web page can put an `Authorization` header on a websocket, so the browser rules are untouched. After that it is the same relay: the same method allowlist, the same byte-for-byte username checks, the same caps and the same three-failures rule. The 15-second recheck reads the phone (on, not revoked, not expired, its person still there with that extension, same username) instead of the session, and a line is tracked per phone, so a phone and a browser can never collide.
- **A phone that stops being one loses its line at once**: revoking it (an admin's **Stop it** or the person's **I've lost it**) and the six-month expiry both close the open websocket, so a call on a lost phone drops with it (`pbx.Service.OnDeviceRevoked`, `enroll.Service.OnPhoneStopped` → `relay.CloseUsername`).

**In the app** (`ios/Linx/Core`, `ios/Linx/Features`).
- **Three ways in, all the same ticket**: the camera reads the QR code (AVFoundation, one code and it stops; the viewfinder says what to do instead when there is no camera or the person said no); **Paste setup link** takes the link from the setup email on that phone; **Set it up by hand** takes the Linx address and the 8 characters. A scanned or pasted link is checked before anything is sent: https only, Linx's own `/set-up-phone` page, the ticket in the `#fragment`, no query, no user.
- **The key never leaves the phone**: one P-256 key in the Secure Enclave (CryptoKit, `kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly` and no Face ID, because a call has to be able to wake the app on the lock screen). A software key exists for the simulator only and cannot be reached in a release build.
- **The certificate request is written by hand** (`Core/DER.swift`): Apple has no API for one, so the app writes the DER itself — the name `linx-phone`, the same name again as a subject alternative name, the Secure Enclave's public key, and a signature from inside the Enclave. Unit tests check the bytes, the nesting, the lengths and the signature.
- **The proof** (`Core/Proof.swift`): a compact ES256 JWS, `sub` the device id, `aud` `linx-device`, a fresh `jti`, one minute long, signed by the same key — what `POST /v1/device-token` wants, and what no other phone can make.
- **What the phone keeps** (`Core/PhoneIdentity.swift`): the certificate, Linx's CA, the server's address and who the phone is for, all in the Keychain as `ThisDeviceOnly` (so a backup restored onto another phone carries nothing usable), plus the Enclave's own blob. **No password of any kind is stored** — not the person's, not the SIP line's.
- **It signs itself in on its own** (`Core/PhoneSession.swift`): a token is reused until a minute before it runs out, the certificate is renewed for the same key once it has less than a month left, and both the six-month date and the certificate move with it. Everything the person sees is one line: "Your phone line is ready".
- **Screens**: the setup screen from `docs/ui/iOS · QR setup@1x.png` with the camera behind the viewfinder, the two sheets, a **signed-in** screen (who the phone is for, its extension, the line, when it would have to be set up again, and **Sign out of this phone**), and a **Set this phone up again** screen for the three things that end a phone. `make ios-screens` now shoots all three, light and dark.
- **Tests**: `ios/LinxTests` — the DER and the signature, the proof's shape, every setup link and typed code the app accepts and refuses, Linx's own time format, and the whole chain (setup code → certificate → token → phone line, then a restart, then signing out) against a stand-in Linx inside the test process. A guide may now quote the app's buttons in **bold**, like the web app's (`internal/help`).

**Parked for the owner (nothing is blocked).** **No TestFlight build until push and CallKit are in** (owner, 2026-10-03; §11 item 5). An emailed link can't open the app directly yet: that needs **universal links**, which need the Apple **Team ID** and a small file served at `/.well-known/apple-app-site-association` on every Linx server. Until then the person pastes the link, which needs no Apple account and works on every self-hosted server. Worth doing in step 10 if the owner wants the tap-the-link path.

### Step 3, as built (2026-10-03)
- **The box asks Linx every 3 seconds whether the phone has finished**, and only while the code is on the screen (at most 10 minutes). An event would be cheaper still; it would mean a new kind of message on the Team websocket, which step 4 can add if the owner wants it.
- **An ordinary person can't read the email setting**, so their own **Email a link to me** is always offered; if email isn't set up the answer says so and the code on screen still works.

### Step 2, as built (2026-10-03)
- **Six months idle, or a password change, and nothing else** (owner, 2026-10-03). The idle window is 183 days, and the `linx-devices` provisioner signs certificates for exactly that long (`ca-init.sh`, 4392h), so the certificate and the window run out together; `linx setup` updates an existing server's CA, which signed 7-day certificates before this.
- **A password change asks for the phone to be set up again** (owner, 2026-10-03). Changing, resetting or removing a person's password ends their browser sessions and, in the same transaction, expires every phone of theirs with a `device.expired` event: the app says "set this phone up again" and the person asks for a new QR code or emailed link. Disabling them, taking their extension away, or revoking the phone still stop it at once as well. (Adding a password to a passkey-only account doesn't end sessions, so it doesn't touch the phones either.)
- **The app is a client, nothing else** (owner, 2026-10-03). A phone's token holds one written-out list of read-only scopes (`auth.deviceScopes`: `extensions:read`, `team:read`), not a role's ceiling, so widening a role can never widen an app. Running Linx — people, phone lines, routing, settings, backups, setting up another phone — stays with the web app and the command line, even on an admin's phone. A test fails if that list ever gains a `:write` or sensitive scope.
- **A phone can't change the account it signs in as.** A device token is a signed-in person for the things the app needs (its own calls, voicemail, team), but the account's own password, email, authenticator, passkeys and "confirm it's you" are a browser session's alone (`notASession` in `internal/auth/service.go`), and it can't make another setup code.
- **Rate limits** on the two phone endpoints: 10 a minute per address for setting up, 60 a minute for tokens. Guessing the 8 characters is stopped by that limit (32^8 codes), not by a per-ticket counter — a wrong code matches no ticket at all.
- **A restored server keeps its phones; a changed domain does not.** A phone is found by the
  fingerprint of the certificate Linx issued, which is in the database, so a restore onto another
  server keeps every set-up phone working — and so does replacing the internal CA, since nothing
  validates a chain and the app deliberately refuses no renewal for coming from a CA it hadn't
  seen (settled in step 9, 2026-10-05: "Step 9, as built" and `docs/THREAT_MODEL.md`). What a
  restore *does* break is a **changed domain**: the app keeps asking for the old address and has
  to be set up again. That is its own row in the moved-server checklist (`docs/INSTALL.md` §8),
  apart from desk phones, because a desk phone can be given a new address and an app cannot.
- **The certificate is the identity, not the name.** Every phone's certificate says `linx-phone`; Linx finds the phone by the fingerprint of the certificate it issued, and refuses a certificate request for any other name.

## 13. Demo exit (Phase 2)
Every lock-screen and killed-app ringing case in `TEST_MATRIX.md` passes **on real devices**: a QR scan sets up an iPhone with nothing typed; the locked phone rings and answers from the lock screen; the app force-quit still rings; a call works on mobile data and on a network that blocks UDP (TLS 443 only); 1:1 video works and falls back to audio on a throttled link; voicemail and call history match the web app; revoking the phone drops its call at once; and a phone whose clock is moved past six months of silence asks to be set up again. Data used per minute of a call and the app's download size are recorded in `docs/RESOURCES.md`.

## 14. Not getting rejected, and being fast (owner, 2026-10-03: "best performing, 100% compliant, no rejection")
These are the rules that actually sink apps of this kind. Each becomes a line in `STORE_SUBMISSION.md` and a row in `TEST_MATRIX.md`, and the review step (build order 9) checks every one before anything is submitted.

**The ones that get VoIP apps rejected or cut off**
1. **Every VoIP push must report a call to CallKit, immediately, every time.** Apple enforces this in the system, not just in review: an app that takes a VoIP push without reporting a call is killed, and repeat offenders stop receiving pushes. So Linx never uses a VoIP push for anything but a real, ringing call — no "sync now", no message badges. The China path (no CallKit) uses an ordinary push plus the in-app ringer, never a VoIP push.
2. **No placeholders.** Guideline 2.1 rejects "coming soon" screens and empty features — which is why the Chat tab waits for Phase 4 (§11 item 7).
3. **Reviewers must be able to use the app.** A self-hosted client with no server is the single most common rejection for apps like this. Submission needs a **reachable demo Linx server with a working extension, a second extension to call, and voicemail already in the box**, with the credentials in the review notes, kept alive through review. That demo server is a Phase 2 deliverable, not an afterthought.
4. **IPv6-only networks.** Apple tests on a NAT64/IPv6-only network. Sign-in, the websocket, SIP, TURN and the media path all have to work with no IPv4 anywhere — worth testing early, because it can reach down into how addresses are configured on the server.
5. **Privacy manifest and labels.** `PrivacyInfo.xcprivacy` with the required-reason APIs declared, matching App Store privacy labels, and the same for any third-party binary we ship (the WebRTC framework). A missing or mismatched manifest is an automatic rejection at upload.
6. **Purpose strings that say why**: microphone, camera, and — easy to miss — **local network**, because WebRTC's mDNS ICE candidates trigger that prompt on iOS. Each string says what Linx does with it, in plain words.
7. **Background modes**: `voip` and `audio` only, both justified by the app's actual behaviour. Nothing else requested.
8. **Account rules**: accounts are created by the company's admin, not in the app, so the in-app account-deletion rule doesn't apply the way it does to consumer sign-ups — but the review notes must say so plainly, and Settings still has "sign out of this phone" and a path to ask the admin to remove the account.
9. **Export compliance**: declare encryption in `Info.plist` (`ITSAppUsesNonExemptEncryption`). Linx uses standard TLS/SRTP, which is normally exempt, but the declaration is still required and France asks for its own.
10. **The basics that hold up a release**: privacy policy and support URLs, age rating, screenshots for every required size (now including iPhone Duo), and `APPLE_SIGNING.md` covering the bundle ID `com.linxpbx.app`, capabilities and provisioning.

**Performance, measured not claimed** (these go into `docs/RESOURCES.md` beside the server's numbers)
- **Push → ringing under 2 seconds** on mobile data, measured end to end; the server metric already planned (push sent → CallKit reported → registered → ringing) is what proves it.
- **Cold launch under 1 second** to a usable screen; no network call blocks the first paint.
- **Audio path**: WebRTC in manual-audio mode, the session activated only in CallKit's `provider(_:didActivate:)` — this is also what prevents the audio-dropout bugs reviewers notice.
- **Energy**: nothing runs in the background; the websocket lives only while the app is in front. A call's battery draw and data per minute are measured on a real device.
- **Size**: the download stays small (a phone app, not a framework dump); the WebRTC binary is the only large dependency and is stripped of simulator slices in the shipped build.
- **Instruments pass** (Time Profiler, Allocations, Energy) before the demo, with anything above budget fixed or written down.
