# What to try on a real phone

The app is on a real iPhone now (TestFlight, 2026-10-04). A simulator can
never test any of this: ringing, push, CallKit, the camera and a bad network
only exist on a real device.

Work down the list. **Stop at the first thing that fails and say what
happened** — each row below the one that failed usually depends on it.

Before anything: set the phone up as a Linx phone — in the web app,
**My account → My phones → Add phone → iPhone**, name it, **Show a QR code**,
and scan it with the app. Nothing is typed, and no password exists.

---

## 1. It works at all

| | What to do | What should happen |
|---|---|---|
| 1.1 | Open the app after setting it up | "Ready · Ext ___" at the top of the keypad |
| 1.2 | **Test my sound** on the keypad | You hear your own voice back, about a second behind |
| 1.3 | Dial your own landline from the keypad | It rings; you can hear both ways; **Encrypted · Direct** or **Relayed** shows |
| 1.4 | Hang up | The call ends on both sides, and appears in **Calls** within a second or two |

## 2. Ringing — the whole point of a native app

Each of these is a call **to your extension** from another phone (the UCM
landline, a desk phone, or the web app in a browser).

| | What to do | What should happen |
|---|---|---|
| 2.1 | App open and in front | It rings, with Answer and Decline |
| 2.2 | App open, phone **locked** | Rings on the lock screen like a normal call; answer without unlocking |
| 2.3 | App in the background (home screen) | Same as 2.2 |
| 2.4 | **App force-quit** (swipe it away in the app switcher) | Still rings. This is the one that proves the push works |
| 2.5 | Phone in **Low Power Mode**, screen off | Still rings |
| 2.6 | Phone untouched **overnight**, then call it in the morning | Still rings, first try |
| 2.7 | On **mobile data only** (Wi-Fi off) | Still rings, and the call has sound both ways |
| 2.8 | Caller hangs up while it is still ringing | The ringing **stops** within a second or two, and it shows as a missed call |
| 2.9 | Answer, then look at the Phone app's **Recents** | The call is there (unless you turned that off in Settings) |
| 2.10 | Call your extension while already on a call | The second caller goes to voicemail; your phone doesn't ring twice |

**What to measure in 2.4:** roughly how long from the other phone starting to
ring to your iPhone ringing. It should be about **two seconds**. Much longer
and something is wrong — tell me and the server's own numbers will say where.

## 3. Video

| | What to do | What should happen |
|---|---|---|
| 3.1 | In a call, press **Video** | Your camera comes on; the other side is asked whether to turn theirs on |
| 3.2 | Other side says **Not now** | They see you, you hear them, everything else behaves normally |
| 3.3 | Other side turns theirs on | You see each other |
| 3.4 | **Flip camera** | Front/back swaps |
| 3.5 | Turn the phone on its side | The buttons move to the right-hand edge, out of the picture |
| 3.6 | Press **Stop video** | The picture goes, the conversation carries on untouched |
| 3.7 | Walk somewhere with poor signal while video is on | After about a quarter of a minute the camera turns itself off and says so; the call stays up |

## 4. The rest of the app

| | What to do | What should happen |
|---|---|---|
| 4.1 | **Calls** tab | Your real history, missed in red, number on the tab |
| 4.2 | Tap the phone beside a row | It rings them back |
| 4.3 | Search by a number you have called | Finds it, even if it's old enough to be off the screen |
| 4.4 | **Team** tab | Everyone with an extension; call someone and watch their row change to "On a call" while you talk |
| 4.5 | Swipe someone right → **Favourite** | They move to a Favourites section at the top; still there after closing and reopening the app |
| 4.6 | Set yourself **Do not disturb**, then call your extension | It doesn't ring; the caller gets your voicemail |
| 4.7 | **More → Voicemail**, press play | It plays, the blue dot goes, and the number on **More** drops |
| 4.8 | Swipe a voicemail → Delete | Gone, and gone in the web app too |
| 4.9 | **More → Settings → Appearance → Dark** | The whole app goes dark and stays that way after a restart |
| 4.10 | Keypad, press the green button with nothing typed | The last number you rang comes back, ready to ring |

## 5. Losing a phone, and the six-month rule

| | What to do | What should happen |
|---|---|---|
| 5.1 | Web app → My account → My phones → **I've lost it** | The app says to set the phone up again, within seconds; a call on it drops |
| 5.2 | Set it up again with a new QR code | Works, and nothing else about the phone changed |
| 5.3 | Change that person's password in the web app | Their phones ask to be set up again |

## 6. Where it gets hard

Worth trying if you can, not blockers:

- **A network that only allows web traffic** (a hotel, an office guest
  network): calls should still work, over TLS on 443, and the pill should say
  **Relayed**.
- **In a car** (CarPlay or Bluetooth): answer and hang up from the car's
  controls; the app's own screen must agree with the car.
- **An IPv6-only network.** Apple tests on one, so a failure here is a
  rejection later.
- **Mainland China**, if you travel: no lock-screen call there by design — it
  arrives as a notification you tap, and the app rings its own screen
  (`ADR-078`).

---

## What to tell me when something fails

The row number, what the phone showed, and roughly when. If it is about
ringing, the server keeps its own count of every push sent and how long each
phone took to arrive — System → Settings → **Calls to the app** shows it in
words, and I can read the exact numbers.
