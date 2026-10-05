# Putting Linx UC on the App Store

*Written 2026-10-05, Phase 2 step 10. For the owner, who has never submitted an app. `docs/ops/APPLE_SIGNING.md` is the one to read first — it covers the Apple account, the Team ID, the bundle ID, the push key and getting a build to TestFlight. This one picks up where that ends: everything App Review asks for, the answers Linx gives, and the one thing that sinks apps like this.*

**Nothing here is urgent.** TestFlight already works without any of it, and Linx can stay on TestFlight for as long as you like. This document exists so that the day you decide to submit, nothing is a surprise.

---

## 1. The one that sinks apps like this: a reviewer can't use it

Linx UC is a client for a phone system **you** run. A reviewer in Cupertino opens it and sees a screen asking to scan a setup code they haven't got. That is Guideline 2.1 ("we were unable to review your app") and it is far and away the most common rejection for self-hosted clients.

So a submission needs, before anything else:

- **A Linx server that is reachable from the internet and stays up through the whole review** (a week is a safe assumption, longer if they come back). The test VPS is the obvious candidate, not your own office system.
- **Two extensions on it**: one for the reviewer, one to call. The second can be the web client left signed in, or a second phone — something that actually answers.
- **A voicemail already in the reviewer's box**, so Voicemail isn't empty, and **a few calls in the history**, so Calls isn't empty. An empty app looks unfinished even when it isn't.
- **A setup code that doesn't expire during review.** An ordinary code lasts ten minutes (ADR-012). Make a fresh one and put it in the review notes *the same day you submit*, and watch for the "Waiting for review" → "In review" change so you can replace it if it lapses. If review runs long, the notes should also say "ask us and we'll send a new code within the hour".
- **Review notes** that say, in order: what Linx is, that the server is yours and not a service, how to set the phone up (paste the link or type the 8 characters — they will not have a QR code on a screen), which extension to call, and that calls need a real network, not the simulator.

A suggested note is in §7.

---

## 2. What Apple asks, and what Linx answers

### Privacy — "data collected"

**Nothing.** Linx's makers receive no data at all: calls, voicemail, the directory and call history live on the company's own server. In App Store Connect's privacy form, every category is **not collected**, and **no tracking**.

The app ships `ios/Linx/PrivacyInfo.xcprivacy` saying exactly that, plus the one required-reason API it uses — user defaults (`CA92.1`), for the handful of choices a phone remembers for itself: light or dark, whether Linx calls appear in the iPhone's own Recents, which camera a video call starts on, and who is starred. Google's WebRTC carries its own manifest inside the framework.

**The answers in App Store Connect must match that file.** A mismatch is a rejection, and it is an easy one to cause by clicking through the form quickly.

One thing worth saying plainly in the notes, because a careful reviewer will ask: **Apple's push service sees the caller's number and the time** for a call that reaches a sleeping phone. That is between the company's server and Apple (`docs/PHASE2.md` §5) and is the minimum a push can carry; it is not something the app collects.

### Encryption

Already declared: `ITSAppUsesNonExemptEncryption` is `false` in `ios/Info.plist`, so TestFlight builds don't stop to ask. Linx uses standard encryption only — TLS, DTLS-SRTP, and Apple's own Secure Enclave — which is exempt. **France asks separately** in App Store Connect; the same answer applies.

### Accounts, and deleting one

Apple requires in-app account deletion for apps that let people *create* accounts. Linx doesn't: an admin creates the person and the extension, and the app only ever joins an existing one with a setup code. Say that in the review notes. What the app does have, and what you point at:

- **Settings → Sign out of this phone** takes Linx off the device.
- **My phones → I've lost it** on the website stops the phone at Linx's end, for good.
- Removing the *person* is the admin's, on the website, as it is for any company phone system.

### Age rating and category

**Business** (primary), **Productivity** (secondary) fits what it is. Age rating **4+**: no user-generated content between strangers, no advertising, no web browsing — it calls the people in one company's directory. Chat arrives in Phase 4, and when it does the rating should be looked at again.

### The ordinary boxes

- **Privacy policy URL** — required, and must be reachable. One page saying Linx collects nothing and the data is the company's.
- **Support URL** — somewhere that answers a question. The help pages or a contact page.
- **Screenshots** for every required size, including iPad. `make ios-screens` produces them on the test devices; Apple wants them cropped and without a status bar that says anything silly.
- **Name**: **Linx UC** (owner, 2026-10-03). Home screen stays **Linx**, bundle id `com.linxpbx.app`.
- **Subtitle**: plain words, not "UC" — something like *Your office phone, on your phone*.

---

## 3. The rules that get VoIP apps killed after review

These are enforced by the system, not just at review, and Linx is already built to them. Check each still holds before submitting — the call suite and `docs/TEST_MATRIX.md` cover them.

| Rule | How Linx keeps it |
|---|---|
| **Every VoIP push reports a call to CallKit, at once, every time.** An app that takes one without ringing is killed, and stops receiving pushes. | A wake push reports to CallKit **before anything else happens** — before signing in, before the line opens (ADR-074). A push with no call id is dropped without reporting one. Missed calls and voicemail go as ordinary notifications, never VoIP pushes. |
| **No CallKit in mainland China.** | ADR-078: a phone whose **Region** is China sends no VoIP token, tells the server `call_alerts`, and rings on its own screen from an ordinary notification. Decided by the setting, not by location, so a phone behaves the same wherever it travels. |
| **No placeholders** (Guideline 2.1). | There is no Chat tab until Phase 4 builds chat, and no greyed-out "coming soon" anywhere. |
| **Background modes must be justified.** | `voip` and `audio`, nothing else, both for what the app actually does. |
| **Purpose strings must say why.** | Microphone, camera and — the one people forget — **local network**, which iOS prompts for because WebRTC looks for a short route. All three are in plain words in the build settings. |
| **IPv6-only networks.** Apple tests on NAT64. | Not yet proved. See §5. |

---

## 4. Before you press Submit

A list to walk down, once.

1. CI green on the commit you are shipping, and `make ios-build-device` clean.
2. The build number is one nobody has used — **they can never be reused**, so always go up.
3. `aps-environment` came out **production** in the signed export (`docs/ops/APPLE_SIGNING.md` says how to check).
4. The demo server is up, with the two extensions, a voicemail and some history, and a **fresh setup code** in the notes.
5. The privacy answers in App Store Connect match `PrivacyInfo.xcprivacy`.
6. Privacy policy and support URLs load.
7. Screenshots for every size, from the real devices.
8. `docs/TEST_MATRIX.md` section 2 passes on a real phone — the ringing cases. If a locked phone doesn't ring, nothing else matters.
9. The app has been run once on a **network that allows only 443** (a hotel, a phone's hotspot with UDP blocked) and once on **mobile data**.

---

## 5. What is not proved yet

Written down rather than claimed, so nobody is surprised by a rejection.

- **IPv6-only (NAT64).** Apple reviews on one. Everything Linx does should work — it is TLS over 443 and TURN — but no part of it has been tested with no IPv4 anywhere, and the server's own addressing may need attention. **Do this before submitting**, not after: macOS can share an IPv6-only NAT64 network for exactly this test.
- **Instruments.** Time Profiler, Allocations and Energy haven't been run over the app (`docs/PHASE2.md` §14). Nothing suggests a problem — a call is WebRTC's work, not Linx's — but "measured" is better than "expected".
- **Push → ringing under 2 seconds**, measured end to end on mobile data. The server counts it (`/metrics`, `linx_push_wake_seconds_total`); the number hasn't been read off a real phone yet. `docs/DEMO_PHASE2.md` does it.
- **A long review.** If Apple takes longer than the demo server's certificate or a backup window, the server has to survive it untouched.

---

## 6. If it comes back rejected

Normal, and usually one line. The two likely ones here:

- **"We were unable to review your app"** — the setup code expired, the server was down, or the notes weren't clear enough. Answer in Resolution Center with a fresh code and keep the server up; this does not need a new build.
- **"Guideline 5.1.1 — account deletion"** — an automated reading of the sign-in screen. Answer that accounts are created by the company's administrator, never in the app, and point at Sign out of this phone and the admin's own tools. Again, no new build.

Neither costs a review cycle if the answer is ready.

---

## 7. A review note you can paste

> Linx UC is the client for Linx, an open-source phone system that each company runs on its own server. There is no Linx service and no account to create in the app: an administrator adds a person on their own server and the app joins it with a one-time setup code.
>
> We have left a server running for you.
>
> **To set the phone up:** open the app, tap **Set it up by hand**, and enter the code `XXXXXXXX` with the address `https://DEMO.EXAMPLE.COM`. (Or tap **Paste setup link** and paste `LINK`.) The code is one-time; if it has expired, contact us and we will send another within the hour.
>
> You are extension 101. **To test a call:** dial **102** on the keypad — it answers automatically. Dial **\*43** for an echo test if you would rather hear your own voice. There is already a voicemail in **More → Voicemail** and some calls in **Calls**.
>
> **Please test on a real device over Wi-Fi or mobile data**: calls need a real network, and the Simulator has no microphone or push.
>
> **Push and CallKit:** every VoIP push reports a call to CallKit immediately; the app never uses a VoIP push for anything else. Where Apple does not permit CallKit (mainland China, by the device's Region setting) the app sends no VoIP token and rings on its own screen instead.
>
> **Privacy:** the app sends nothing to us. Calls, voicemail and the directory stay on the company's own server. Apple's push service sees only a call's id, the caller's number and the time, for a call arriving at a sleeping phone.
>
> **Accounts:** people are created by the company's administrator, not in the app, so there is no in-app sign-up. **Settings → Sign out of this phone** removes Linx from the device, and the administrator removes the person on their own server.
