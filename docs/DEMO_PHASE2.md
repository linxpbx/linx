# Phase 2 demo checklist — the iPhone and iPad app

A hand-run check that Phase 2 does what it promised (`docs/PHASE2.md` §13). Tick each box. **Allow about two and a half hours**, and do it in one sitting: nothing in this document has been asked of you piecemeal, on purpose (owner, 2026-10-05), so this is the first and only time you work through it.

**Phase 2 exit** (`docs/PHASE2.md` §13): a QR code sets up an iPhone with nothing typed; the **locked** phone rings and answers from the lock screen; the app **force-quit** still rings; a call works on **mobile data** and on a network that blocks UDP; 1:1 **video** works and steps down on a poor link; voicemail and call history match the web app; **revoking the phone drops its call at once**; a phone whose clock is past six months of silence asks to be set up again; and `*97` and the **message light** work on a desk phone. Data per minute and the app's size are recorded in `docs/RESOURCES.md`.

The examples use the home server `home.mym.ae` at `192.168.1.213`, the UCM6304 at `192.168.1.60`, your **iPhone 18 Pro Max**, your **iPad Pro 13-inch (M5)**, and a **desk phone** on one of the UCM's extensions. Use your own.

**Never dial an emergency number (999, 998, 997, 112, 901) for real.** The simulator shows them without dialling.

---

## You need

- **The home server** on the newest Linx, including the **new Asterisk image** (step 1 below). Back up first.
- **Your iPhone** with **TestFlight build 14 or later** — the build with step 8's iPad and fold screens and step 9's Settings lines. Build 13 is not enough.
- **Your iPad**, set up as its own phone (a second setup code, not a copy).
- **A desk phone** signed in to an extension of yours, for `*97` and the message light. Any Grandstream or Yealink handset on the office network.
- **A second person to call**: the web client signed in in a browser is fine.
- **Mobile data** on the iPhone, and somewhere the Wi-Fi blocks UDP if you can find one (a hotel, or turn UDP 443 off at the router for ten minutes).
- About **100 MB of mobile data** spare for the video steps.

---

## 1. Before the demo: update the server

`*97` and the message-waiting light need the new Asterisk image; nothing else in this demo does.

- [ ] Back up first (`docs/ops/UPDATING.md`), and write down where the backup is.
- [ ] Update `home.mym.ae` to the commit being demoed.
- [ ] `sudo linx doctor` — green apart from the two standing warnings (the CA root-key backup on the server, and no API key).
- [ ] The UCM landline signs itself back in: System → Status, or Phone lines.

## 2. On your computer

```
cd ~/Projects/linx
git pull
make lint          # ends with "lint: ok"
make test          # failures only; nothing listed means all passed
make security      # govulncheck, npm audit and licences all "ok"
make ios-lint      # ends with "ios lint: ok"
make ios-test      # ends with "ios tests: ok"
make ios-build-device   # ends with "ios device build: ok"
```

- [ ] All of the above pass.
- [ ] CI is green on that commit (`gh run list --limit 1`).

## 3. Setting a phone up (steps 2–4a)

- [ ] On the website: My account → **My phones** → **Add phone** → iPhone → **Show a QR code**.
- [ ] Scan it with the app. **Nothing is typed.** The app signs in by itself.
- [ ] The phone appears in My phones, online, with the name you gave it.
- [ ] Set the **iPad** up the same way, with its own code.
- [ ] Settings → **This phone's line** names the right **Person**, **Linx server**, **Extension**, and a date six months out (step 9).

## 4. Ringing — the part only a real phone can prove

This is the heart of Phase 2. Each one: have the web client call your extension.

- [ ] **4.1 App open.** It rings, you answer, you can both hear each other.
- [ ] **4.2 Phone locked.** It rings on the lock screen like any call, and answers from there.
- [ ] **4.3 App in the background.** Rings.
- [ ] **4.4 App force-quit** (swipe it away in the app switcher). **It still rings.** *This is the one that failed at build 13's predecessor and was fixed in the Asterisk image — if it fails now, stop and say so.*
- [ ] **4.5 Low Power Mode on.** Rings.
- [ ] **4.6 On mobile data**, Wi-Fi off. Rings, and the call has sound both ways.
- [ ] **4.7 Overnight.** Leave the phone untouched for a few hours, then call it. Rings. (Do this last — start it and check in the morning.)
- [ ] **4.8 The caller gives up** before you answer: the phone stops ringing and the call shows as missed in **Calls**, with a badge.

**How fast did it ring?** System → Settings → **Calls to the app** on the website counts every push and how long each phone took.

- [ ] Push to ringing is **under about 2 seconds** on mobile data. Write the number down — `docs/RESOURCES.md` wants it.

## 5. Calls that sound right

- [ ] **5.1** A call from outside your network (mobile data) has sound **both ways**.
- [ ] **5.2** On a network that blocks UDP, a call still works. The **Encrypted · …** line says **Relayed**.
- [ ] **5.3** Tap that line → **Call details**: route, round trip, sound in and out all look sensible.
- [ ] **5.4** The **loudspeaker** button works, and AirPods connected mid-call take the sound.
- [ ] **5.5** `*43` on the keypad — the sound test — plays your own voice back.

## 6. Video (ADR-079, ADR-081)

- [ ] **6.1** On a call, press the video button. Your camera goes on; the other side is **asked once** whether to turn theirs on.
- [ ] **6.2** Say **Not now** on the other side: the one-way call carries on normally.
- [ ] **6.3** Turn theirs on too: both pictures, and tapping either one swaps which is big.
- [ ] **6.4** **Call details** shows the picture's step. On a relayed call it settles at **540p**; direct it may reach **720p**.
- [ ] **6.5** Walk somewhere with a poor signal: the picture steps **down** rather than the call breaking up, and the sound keeps going.
- [ ] **6.6** **Stop video** on both sides: the call goes back to the voice screen, not a blank video one.
- [ ] **6.7** A video call to an **outside number** (the landline): video on, then off — the screen goes back to voice properly.
- [ ] **6.8** The screen **stays awake** while a video call has a picture in it, and sleeps normally on a voice call.
- [ ] **6.9 Read the data off.** With mobile data on and Wi-Fi off, note iOS's data figure for Linx (Settings → Mobile Data), make a **two-minute video call**, and note it again. Write the difference down — this is the measurement `docs/RESOURCES.md` §3b asks for.

## 7. The rest of the app (step 7)

- [ ] **Calls**: history matches the website, missed ones first, **ring back** works, and opening it clears the badge everywhere.
- [ ] **Team**: everyone's there, their status is live, search works, you can call or video-call from a person's card.
- [ ] A person whose app is closed still shows as **reachable** — not offline (owner's rule, "a phone is not a browser").
- [ ] **Keypad**: redial, and the starred people beside it on the iPad.
- [ ] **More → Voicemail**: play, mark heard, delete, ring back — and the website agrees.
- [ ] **More → Settings**: status, appearance, the camera, how calls arrive here, **Show Linx calls in the Phone app**.

## 8. The iPad and the fold (step 8)

- [ ] On the **iPad**, every tab is a **list beside a detail** — Mail-shaped, not a phone screen stretched across 13 inches. *This is the owner's own condition on ADR-076; say plainly whether it is met.*
- [ ] A call **stands beside the app** rather than covering it, and the list behind it doesn't jump.
- [ ] Turn the iPad: the call screen rearranges and nothing lands under your thumb.
- [ ] Drag the Split View divider **during a call**: the call doesn't drop, and the in-call keypad stays open if it was (`docs/TEST_MATRIX.md` 4b.8a — the nearest thing anyone can watch to a phone being opened out).
- [ ] Look at `ios/screenshots/iPhone-18-Pro-Max/`, `iPad-Pro-13-inch-M5/` and `iPhone-Duo/` and say whether they look right.

## 9. Losing a phone, and six months (step 9)

- [ ] **9.1** While a call is up, **My phones → I've lost it**. The call **drops mid-sentence**, not when it ends.
- [ ] **9.2** The app says to set the phone up again, within seconds.
- [ ] **9.3** Set it up again with a new code: everything is back, nothing else changed.
- [ ] **9.4** Change that person's password on the website: their phones ask to be set up again.
- [ ] **9.5** Disable the person, then enable them: the phone stops and comes back **without** a new code.
- [ ] **9.6** Stop a phone, then ring that person: the stopped one never rings, even in a pocket.
- [ ] **9.7 Six months.** On a spare phone, set the clock past the date Settings shows under **Set up again by** — it asks to be set up again. (Skip if you'd rather not move a phone's clock; the rule is covered by tests.)

## 10. `*97` and the message light, on a desk phone (step 9b)

Needs step 1's server update, and a desk phone.

- [ ] **10.1** Leave yourself a voicemail. The desk phone's **light** (or envelope) comes on within a second or two.
- [ ] **10.2** Dial **`*97`**. Linx answers, says "Here are your new messages", reads the caller's number out digit by digit, and plays the message.
- [ ] **10.3** Let it finish and hang up: the light goes **out**, and the message is in **Heard** on the website.
- [ ] **10.4** Two messages; press `2` in the middle of the first: it skips, and the first is **still new** afterwards.
- [ ] **10.5** Press `3` while one plays: "Deleted", and it's gone from the website.
- [ ] **10.6** Press `1` while one plays: it starts again.
- [ ] **10.7** Hear a message **on the website** instead: the desk phone's light goes out too.
- [ ] **10.8** Dial `*97` with nothing new: "You have no new messages."
- [ ] **10.9** Turn that person's voicemail off and dial `*97`: it says there's none, and plays nobody else's.
- [ ] **10.10** Restart the phone system (System → Status → Restart): the lights come back on by themselves.

## 11. The small server

Phase 2 ends on the 1-core, 2 GB VPS like every phase, to prove the low-resource rule held.

- [ ] Linx is running on `vps.mym.ae` at this commit, `linx doctor` green.
- [ ] Set a phone up against it and make a call — it works, and the phone rings when the app is closed (its own Apple key, or borrow the home server's).
- [ ] `docker stats` with a call up: write the numbers into `docs/RESOURCES.md` if they differ from §1's.
- [ ] Nothing was swapping, and the call sounded normal.

## 12. Things that are known, so nobody is surprised

Not failures; written down in `docs/PHASE2.md` and `docs/ops/STORE_SUBMISSION.md`.

- The **iPhone Duo's inner screen** has never been photographed — no real device exists and the simulator's fold can't be driven from a script. Its layout is held to its rules by tests.
- **Opening a folding phone out mid-call** is designed for and tested, but nobody has watched it happen. Dragging the iPad's divider (step 8 above) is the nearest thing.
- **IPv6-only networks** haven't been tested, and Apple reviews on one. Before any App Store submission, not before TestFlight.
- A desk phone that never **asks** for its message light may not get one; Linx proves the asking path, which is what real handsets do.
- Desk phones work **on your network only** — from outside is recorded as a requirement (`docs/ROADMAP.md`), not built.

---

## Result

- [ ] **Phase 2 approved**, date: ____________
- Anything that failed, with what the phone showed: write it here and it becomes the next session's first job.
