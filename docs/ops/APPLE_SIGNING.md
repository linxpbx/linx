# Apple: the things only you can do

Everything in Linx is yours and runs on your own server. Apple is the one
exception: an app can only reach an iPhone through Apple, and Apple will only
deal with the person whose account it is. This is the short list of what that
means, in order, with where to click.

You need: your Apple Developer account (you have it), a Mac with Xcode (this
one), and about half an hour the first time.

---

## Part 1 — Two numbers and two keys

**There are two different `.p8` key files in this document and they are not
interchangeable.** One lets Linx ring your phones. The other lets a build be
uploaded. Keep them apart and labelled; Apple lets you download each of them
**once, ever**.

### 1. Your Team ID (a number to look up, not something you make)

1. Sign in at <https://developer.apple.com/account>.
2. Click **Membership details** in the sidebar.
3. **Team ID** is ten characters. **This account's is `AY75S2Z9UK`** (found
   2026-10-04); it is already set in `ios/Linx.xcodeproj` as
   `DEVELOPMENT_TEAM`, so Xcode can make the signing profiles itself. A Team
   ID is not a secret — it is visible inside every published app — but it is
   the one number everything else here is tied to.

That one number is needed for: the push key below, signing the app, and the
`/.well-known/apple-app-site-association` file that makes an emailed setup
link open the app directly (`docs/PHASE2.md` §12, parked until you have it).

### 2. The push key — the `.p8` that rings your phones

This is the one Linx asks for.

1. <https://developer.apple.com/account/resources/authkeys/list>
2. Press **+** (Create a key).
3. **Key Name:** something you'll recognise later, e.g. `Linx push`.
4. Tick **Apple Push Notification service (APNs)**. If it asks for an
   environment, choose **both Sandbox and Production** — the same key then
   works for a build from Xcode and for a TestFlight build.
5. **Continue → Register → Download.**
6. You get `AuthKey_XXXXXXXXXX.p8`. The `XXXXXXXXXX` is the **Key ID** — note
   it down. **Apple will not let you download this file again.** Keep it
   somewhere safe (a password manager, not email, not a chat).

### 3. The bundle ID, registered once

1. <https://developer.apple.com/account/resources/identifiers/list>
2. **+ → App IDs → App → Continue.**
3. **Description:** `Linx` — letters, numbers and spaces only; Apple refuses
   punctuation in this field.
4. **Bundle ID:** Explicit, `com.linxpbx.app`.
5. In the capability list tick **Push Notifications**, and tick **Associated
   Domains** while you are there: that is what later lets an emailed setup
   link open the app itself instead of the browser (`docs/PHASE2.md` §12,
   "Parked for the owner"). Ticking it now saves a second visit.
6. **Continue → Register.**

### 4. The App Store Connect record (needed before any TestFlight build)

1. <https://appstoreconnect.apple.com> → **Apps → +  → New App.**
2. **Platform:** iOS. **Name:** `Linx UC` (your choice, 2026-10-03 — the
   home-screen name stays `Linx`). **Primary language:** English.
   **Bundle ID:** the one you just registered. **SKU:** anything unique, e.g.
   `linx-uc-1`.
3. **Create.** The bundle ID only appears in that dropdown once step 3 above
   is done.

If **Business → Agreements** shows "Action needed", accept it there. Without
it a build uploads and then never appears in TestFlight, with no useful
message about why.

### 5. Optional, and only if you want me to do the uploading: an App Store Connect API key

This is the **second** `.p8`, and it is not the push key.

1. App Store Connect → **Users and Access → Integrations → App Store Connect
   API → Team Keys.**
2. **+**, name it `Linx uploads`, **Access: App Manager**.
3. **Generate**, then **Download** — again, once only.
4. Note the **Issuer ID** (a long one at the top of that page) and the
   **Key ID**.

---

## Part 2 — Where the push key goes in Linx

In the Linx web app, as a system admin:

**System → Settings → Calls to the app → Set it up.**

- **Team ID** — from step 1.
- **Key ID** — the ten characters in the file's name.
- **The key itself** — open `AuthKey_XXXXXXXXXX.p8` in TextEdit and paste the
  whole thing, `-----BEGIN PRIVATE KEY-----` line and all.
- **Which Apple** — leave it as it is; each phone tells Linx which Apple its
  own build belongs to, so a TestFlight phone and a build straight from Xcode
  both ring without you choosing.

Linx seals the key (it is never shown again, not even to you) and only then
does anything reach Apple. Until this is saved, **nothing is sent to Apple at
all** and a sleeping phone simply doesn't ring.

---

## Part 3 — Who can put a build on your phone

**Two `make` targets do it** (added 2026-10-04):

```
make ios-archive   # archive + export a signed build into ios/build/archive/export
make ios-upload    # send that build to App Store Connect (TestFlight)
```

`make ios-archive` refuses with a plain message if there is no signing
identity on this Mac, which is the state until the step below is done.
`make ios-upload` needs the App Store Connect API key from Part 1 step 5:
put the `.p8` in `~/.appstoreconnect/private_keys/` and run it with
`LINX_ASC_KEY_ID=... LINX_ASC_ISSUER=... make ios-upload`.

**The one thing only you can do, and it is interactive:**

> **Xcode → Settings → Accounts → +** → Apple ID → sign in (it asks for your
> password and a code on your phone). Once, on this Mac.

That is what lets Xcode make the distribution certificate and the
provisioning profile. Until then `security find-identity -v -p codesigning`
says "0 valid identities found" and no signed build is possible by any route.

**I will not upload a build without you saying so for that particular
build** — it publishes something under your name to a service outside this
machine.

**A trap worth knowing (hit 2026-10-04).** Archiving with
`CODE_SIGNING_ALLOWED=NO` and letting the export step sign it *looks* like it
works — the `.ipa` comes out signed by **Apple Distribution**, with
`beta-reports-active` set, and it would upload. But an unsigned archive has
no entitlements baked in, so the exported build comes out **with no
`aps-environment` at all**, and an app without that cannot register for push:
the phone would never ring, which is the one thing the build exists for.
Always check before uploading:

```
unzip -q ios/build/archive/export/Linx.ipa -d /tmp/ipa && \
  codesign -d --entitlements :- /tmp/ipa/Payload/Linx.app | grep aps-environment
```

Nothing printed means do not upload it.

**Why a device has to be registered first.** Xcode signs the archive itself
with a *development* profile and only applies the *distribution* one when
exporting — and Apple refuses to make a development profile for a team with
no devices ("Your team has no devices from which to generate a provisioning
profile"). So register one iPhone, once:

<https://developer.apple.com/account/resources/devices/list> → **+** →
Platform **iOS**, any name, the **UDID**. Plugging the phone into this Mac
and unlocking it does the same thing automatically. The owner's phone is
`00008150-00026C4A36C0401C` (iPhone 17 Pro Max), known to this Mac already.

**Worth checking on the first signed archive:** the entitlement
`aps-environment` must come out as `production` in the exported build (it says
`development` in `ios/Linx.entitlements`, and Xcode is expected to substitute
it when exporting for the App Store). If the upload is refused over it, the
fix is a Release-only entitlements file. The app itself reads which Apple it
belongs to out of its own profile, so it behaves correctly either way.

**What stays yours, because Apple requires a person:**

- the first time, agreeing to Apple's agreements in App Store Connect;
- adding testers to TestFlight (internal testers — you — get each build
  automatically once it has processed);
- for the App Store proper: the listing, screenshots, the privacy answers,
  the age rating, and pressing **Submit for Review**. The app's review notes
  must also point at a reachable demo Linx server with two extensions and a
  voicemail already in the box (`docs/PHASE2.md` §14 item 3) — a reviewer who
  can't sign in is the most common rejection for an app like this.

**CI never does any of this.** The `ios` job on GitHub builds the app
unsigned, with no Apple account and no secrets, and that stays true.

---

## What the first upload actually looked like (2026-10-04)

```
make ios-archive
LINX_ASC_KEY_ID=<key id> LINX_ASC_ISSUER=<issuer id> make ios-upload
```

Linx **0.1.0 (1)**, 6.7 MB, signed *Apple Distribution: Mohammed AlMudharreb
(AY75S2Z9UK)*, `aps-environment = production`, `beta-reports-active` set. The
upload itself took about two minutes. **A build number can only be used once,
ever**: the next upload must be build 2, whatever its version number.

Apple then processes the build for a few minutes before it can be given to
testers; it appears in **TestFlight → iOS Builds** when it is ready, and
App Store Connect emails if it is rejected (usually a missing icon size or a
privacy manifest).

## Letting other people test it

**Internal testers — up to 100, no review, builds appear in minutes.** They
must be people on your App Store Connect team.

1. App Store Connect → **Users and Access → +**.
2. Their **Apple ID email address**, a name, and a role — **Developer** is
   right for someone who should see builds but not change the listing;
   **Customer Support** or **Marketing** is enough for a pure tester. Tick
   **Access to Certificates, Identifiers & Profiles** only if they need it
   (most testers don't).
3. They get an email and have to accept it before they appear anywhere else.
4. Then **TestFlight → Internal Testing → +** (a group, e.g. "Us"), add those
   people, and tick which builds the group gets.

They install Apple's **TestFlight** app from the App Store, open the invite
email on the phone, and the build is there.

**External testers — up to 10,000, no App Store Connect account needed, but
the first build goes through Beta App Review** (usually a day or so).

1. **TestFlight → External Testing → +** for a group.
2. Add people by email address, or generate a **public link** that anyone can
   open.
3. Fill in **Test Information** first — what to test, your email, a privacy
   policy URL — and **the review notes must give a reachable Linx server with
   a working extension, a second extension to call, and a voicemail already
   in the box** (`docs/PHASE2.md` §14 item 3). A reviewer who cannot sign in
   is the commonest rejection for an app like this one.
4. Submit for review; once it passes, invitations go out and later builds of
   the same version don't need reviewing again.

**Which to use.** Internal for you and anyone at the company: instant, no
review, and enough for the Phase 2 demo. External only when people outside
need it.

## If something goes wrong

- **"Missing push key"** on the Calls to the app card: the `.p8` wasn't
  pasted whole, or the Key ID doesn't match the file name.
- **A phone rings in the app but not on the lock screen:** that phone's
  Region is set to China (`ADR-078`), which is deliberate.
- **You lost the `.p8`:** make a new key and revoke the old one on the same
  page. Nothing else is affected; phones re-send their tokens by themselves.
- **"No profile for team ... matching"** when building for a device: your
  Apple ID isn't in Xcode's Accounts, or the bundle ID in step 3 wasn't
  registered.
