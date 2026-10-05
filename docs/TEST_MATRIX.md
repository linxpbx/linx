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
| 1.2a | In that call, press **Speaker**, then press it again | The sound moves to the loudspeaker and back to the earpiece. Plug in a headset mid-call and the button follows the sound |
| 1.2b | In a call, **connect AirPods** (open the case, or put them in) | The sound moves to them within a second or two — even if you were on the loudspeaker — and the sound button becomes the system's **picker** wearing their name. Tap it: AirPods, iPhone and Speaker are all in the list, with no need to make another call |
| 1.3 | Dial your own landline from the keypad | It rings; you can hear both ways; **Encrypted · Direct** or **Relayed** shows |
| 1.4 | Hang up | The call ends on both sides, and appears in **Calls** within a second or two |
| 1.5 | In any call, tap the **Encrypted · …** line at the top | **Call details** opens: whether the sound is going straight there or through Linx's relay, how much is coming in and going out, what routes the phone found, and what Linx's relay said. **Copy these details** puts the lot on the clipboard |
| 1.6 | A call where you hear nothing | After about seven seconds a red line says **No sound is coming through**. Tap it, then **Copy these details** and send them to me — that is the one thing that says where the sound stops |

## 2. Ringing — the whole point of a native app

Each of these is a call **to your extension** from another phone (the UCM
landline, a desk phone, or the web app in a browser).

| | What to do | What should happen |
|---|---|---|
| 2.1 | App open and in front | A **banner at the top of the screen** with Answer and Decline — the iPhone's own, the same as any other call. The app's call screen appears once you answer. (Two ringing screens at once was the bug fixed on 2026-10-04.) |
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
| 3.5 | Turn the phone on its side **during a video call** | The buttons move to the right-hand edge, out of the picture. Turn it on its side anywhere else — keypad, Calls, Team, Settings, or a call with no picture — and **nothing turns** (owner, 2026-10-04) |
| 3.6 | Press **Stop video** | The picture goes, the conversation carries on untouched |
| 3.7 | Walk somewhere with poor signal while video is on | After about a quarter of a minute the camera turns itself off and says so; the call stays up |
| 3.8 | Press **Video** with AirPods or a headset connected | The sound stays with them. The camera must never take the sound back to the phone (owner, 2026-10-05) |
| 3.9 | **Stop video**, then press **Video** again straight away | The button shows it is working ("Starting…") and the picture comes back. On mobile data it takes a second or two: the picture needs a way through the relay of its own |
| 3.8a | On a good Wi-Fi video call, wait half a minute, then open **Call details** | **Your picture** climbs to 540p or 720p. On mobile data through the relay it stops at 540p — that is the relay's own limit, not a fault |
| 3.8b | While video is on, walk somewhere with poor signal | The picture gets smaller rather than freezing, and the sound stays. Come back and it climbs again after a few seconds |
| 3.8c | On a video call, put the phone down and leave it for a minute | The screen **stays on** the whole time. Press the power button and it goes off as usual; a call with no picture in it dims and locks as iOS normally would |
| 3.9a | In a call with **both** cameras on, tap your own small picture | It takes the big screen and theirs moves to the corner; tap either again to swap back. With only one camera on there is nothing to swap and a tap does nothing |
| 3.10 | **Video** in the **Test my sound** call | You see your own camera **on the big screen** (the echo test sends it back, and a call where only one camera is on is an ordinary call). **Stop video** returns to the call screen |

## 4. The rest of the app

| | What to do | What should happen |
|---|---|---|
| 4.1 | **Calls** tab | Your real history, missed in red, number on the tab |
| 4.2 | Tap the phone beside a row | It rings them back |
| 4.3 | Search by a number you have called | Finds it, even if it's old enough to be off the screen |
| 4.4 | **Team** tab | Everyone with an extension; call someone and watch their row change to "On a call" while you talk |
| 4.4a | Close the app on a second phone, wait a minute, then look at **Team** | That person still shows **Available**, not Offline: their phone can be woken. A browser signed out still shows Offline |
| 4.5 | Swipe someone right → **Favourite** | They move to a Favourites section at the top; still there after closing and reopening the app |
| 4.6 | Set yourself **Do not disturb**, then call your extension | It doesn't ring; the caller gets your voicemail |
| 4.7 | **More → Voicemail**, press play | It plays, the blue dot goes, and the number on **More** drops |
| 4.8 | Swipe a voicemail → Delete | Gone, and gone in the web app too |
| 4.9 | **More → Settings → Appearance → Dark** | The whole app goes dark and stays that way after a restart |
| 4.9a | Settings → turn **Show Linx calls in the Phone app** off, make a call, then open the Phone app's **Recents** | The new call is **not** there (calls made before you turned it off stay) |
| 4.10 | Keypad, press the green button with nothing typed | The last number you rang comes back, ready to ring |

## 4b. On the iPad (step 8, 2026-10-05)

These are new, and they are what ADR-076's condition is about: nothing in
the app may look like a phone screen stretched to fill a 13-inch one.

| | What to do | What should happen |
|---|---|---|
| 4b.1 | Open the app on the iPad | The four tabs are along the top (tap the button at the left to turn them into a sidebar), and each tab shows a **list on the left with what you picked open on the right** |
| 4b.2 | **Calls** → tap a row | That call's own page opens beside the list: what happened, when, how long, which group rang, who answered, and the button to ring them back. The list stays where it was |
| 4b.3 | **Team** → tap somebody | Their card opens beside the list, with **Call**, **Video call** and **Add to favourites**. It is live: ring them from another phone and the card changes while you watch |
| 4b.4 | **Keypad** | The people you have starred stand beside the keypad under **Favourites**, with the last number you rang above them. Tapping one *puts their extension on the keypad* (it doesn't ring); the phone beside it rings |
| 4b.5 | **More** | Voicemail and Settings open beside the list, not over it |
| 4b.6 | Take or make a call on the iPad | The call stands in its **own column beside the app** — the list you were looking at stays on screen. Hang up and it goes away again |
| 4b.7 | Turn the iPad on its side during a video call | The picture takes one panel and the buttons the other, both ways round, and nothing jumps |
| 4b.8 | Put the app in a narrow Split View beside another app, then take a call | It becomes a phone again: one column, and the call covers it |
| 4b.8a | **While a call is up**, drag the Split View divider slowly from narrow to full width and back | This is the nearest thing to opening a folding phone out, and it is what to watch: the call moves between covering the screen and standing in its own column **without dropping**, the sound never breaks, the list behind it stays where it was, and the in-call keypad stays open if you had it open |
| 4b.9 | Same on the iPhone | Everything is exactly as it was: one column, and a call covers the screen |

## 5. Losing a phone, and the six-month rule

| | What to do | What should happen |
|---|---|---|
| 5.1 | Web app → My account → My phones → **I've lost it** | The app says to set the phone up again, within seconds; a call on it drops |
| 5.2 | Set it up again with a new QR code | Works, and nothing else about the phone changed |
| 5.3 | Change that person's password in the web app | Their phones ask to be set up again |
| 5.4 | While a call is up, stop that phone from the web app (**I've lost it**) | The call drops mid-sentence, not when it ends: Linx closes the phone's line at once |
| 5.5 | Disable the person (People → the person → **Disable**), then enable them again | The app can't call while they're disabled and says so; enabling them brings the phone back with no setup code and no new QR |
| 5.6 | Stop a phone, then ring that person from another extension | Only their other phones ring, and the stopped one never rings even in a pocket: Linx forgets Apple's way to it (step 9) |
| 5.7 | Settings → This phone's line | **Person**, **Linx server**, **Extension** and **Set up again by** all name the right thing (step 9) |
| 5.8 | Sign out in the app (Settings → **Sign out of this phone**), then look at My phones in the web app | The phone is still listed there: signing out takes Linx off the phone, stopping it in Linx is **I've lost it**. Both are meant to exist |

## 5b. `*97` and the message light (step 9b, on a desk phone)

These need a **desk phone** — the UCM's extensions, or any SIP phone on
the office network. The app and the web client have their own badge and
don't use either of these.

| | What to do | What should happen |
|---|---|---|
| 5b.1 | Leave yourself a voicemail, then look at the desk phone | Its message lamp (or envelope) comes on within a second or two. *If it doesn't:* the phone may not be asking Linx for it — look for **voicemail subscribe** or **MWI** in the phone's own settings. Linx proves the asking path in its tests; a phone that never asks is the one case nothing here can settle |
| 5b.2 | Dial `*97` from that phone | Linx answers, says "Here are your new messages…", reads the caller's number out digit by digit, then plays the message |
| 5b.3 | Let it play to the end and hang up | The light goes out; the message has moved to **Heard** on the website too |
| 5b.4 | Leave two messages, dial `*97`, press `2` in the middle of the first | It skips to the second — and the first is **still new** afterwards (and the light stays on) |
| 5b.5 | Dial `*97`, press `3` while a message plays | It says "Deleted", moves on, and the message is gone from the website |
| 5b.6 | Dial `*97`, press `1` while a message plays | The same message starts again |
| 5b.7 | Hear a message on the website instead, and watch the desk phone | The light goes out there too, within a second or two |
| 5b.8 | Dial `*97` with no new messages | "You have no new messages. Goodbye." |
| 5b.9 | Turn that person's voicemail off (People → Voicemail → Turn off), dial `*97` | "There's no voicemail set up for this phone." — and no messages of anyone's are played |
| 5b.10 | Restart the phone engine (System → Status → Restart on the phone system), then look at the lights | They come back on by themselves within a few seconds, without anyone dialling anything |

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

The row number, what the phone showed, and roughly when. **If it is about
sound** — a call you can't hear — tap the **Encrypted · …** line while the
call is still up, press **Copy these details** and paste them to me: that
names the exact place the sound stopped. If it is about
ringing, the server keeps its own count of every push sent and how long each
phone took to arrive — System → Settings → **Calls to the app** shows it in
words, and I can read the exact numbers.
