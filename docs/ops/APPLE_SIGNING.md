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
3. **Team ID** is ten characters, like `A1B2C3D4E5`. Copy it.

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
3. **Description:** `Linx`. **Bundle ID:** Explicit, `com.linxpbx.app`.
4. In the capability list, tick **Push Notifications**.
5. **Continue → Register.**

### 4. The App Store Connect record (needed before any TestFlight build)

1. <https://appstoreconnect.apple.com> → **Apps → +  → New App.**
2. **Platform:** iOS. **Name:** `Linx UC` (your choice, 2026-10-03 — the
   home-screen name stays `Linx`). **Primary language:** English.
   **Bundle ID:** the one you just registered. **SKU:** anything unique, e.g.
   `linx-uc-1`.
3. **Create.**

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

**What I can do from this Mac**, once Part 1 is done and your Apple ID is
added in Xcode (Xcode → Settings → Accounts → +):

- build and archive the app;
- export it, signed with your distribution certificate (Xcode creates and
  stores that itself the first time, under "Automatically manage signing");
- upload it to App Store Connect, which is what puts it in TestFlight.

With the API key from step 5 that is one command and needs nothing typed by
you. **I will not run it without you saying so for that particular build** —
it publishes something under your name to a service outside this machine.

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
