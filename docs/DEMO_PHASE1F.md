# Phase 1F demo checklist

Phase 1F has two demos (`docs/PHASE1F.md` §10 item 7). **Part A** (after build step 8): getting to Linx from outside. **Part B** (after build step 17, below Part A's results): email, call routing, voicemail, call history, undo, and Phase 1's exit test.

## Part A: front doors, DNS, public port

A hand-run check that Part A (`docs/PHASE1F.md` §11 steps 2–7, `docs/SIMPLER.md` §2, §2.5 and §3) does what it promises, on the home server behind Pangolin and on the small VPS. Tick each box. Allow about two hours.

**Part A exit:** on the home server behind Pangolin, Server settings shows one front-door card with the steps for Pangolin (and the other proxies); **Check it** passes from the server, and the phone link on the iPhone with Wi-Fi off shows the mobile network's address and call audio working; the **DNS** row lists the records, checked at the domain's own name servers, names Cloudflare, and Linx keeps them right with the key (Check the key, Stop, start again). On the 1-core VPS, Linx moves to **another public port** (8443): the passkey made at `https://vps.mym.ae` signs in at `https://vps.mym.ae:8443`, Check it and doctor pass with the one ⚠ about the port, a browser call from mobile data has audio, and moving back to 443 works the same way. Automated: `make test`, `make test-docker`, `make screens`, the browser suite (every front door, `public-port` included) and `make test-install` (with the DNS-01 install) are green in CI.

The examples use the home server `home.mym.ae` at `192.168.1.213` behind Pangolin at `192.168.1.211`, and the VPS `vps.mym.ae` (64.177.45.141, Debian 13, 1 core, 2 GB, Linx takes 443, no token). Use your own.

### You need
- The home server and Pangolin, as after the Help demo, with an admin account and a passkey on the iPhone.
- The VPS with Linx running, an admin account, and a passkey made at `https://vps.mym.ae` (step 7 makes one if you don't have it).
- A **Cloudflare token** for `mym.ae`: My Profile → API Tokens → Create Token → "Edit zone DNS", Zone Resources: only `mym.ae`. The VPS needs it for the public port; the home server already has one.
- The **iPhone** on mobile data, and a laptop on your home network.
- In the VPS provider's firewall: allow **TCP 8443** in (UDP 443 and TCP 443 are already allowed).
- The images published by CI for the commit you build.

### 1. Docs
- [ ] `docs/SIMPLER.md` §2, §2.5 and §3, ADR-062 to 064 "As built" in `docs/DECISIONS.md`, and the "Phase 1F Part A review" at the end of `docs/THREAT_MODEL.md` read sensibly to you.

### 2. On your computer
```
cd ~/Projects/linx
git pull
make lint          # ends with "lint: ok"
make test          # nothing listed means all passed
make screens       # screenshots in web/e2e/screenshots/
GOOS=linux GOARCH=amd64 make build
```
- [ ] All finish without errors.
- [ ] These screenshots look right to you: `install-front-door-home-light.png`, `install-front-door-unlock-light.png`, `install-public-port-warning-light.png`, `install-public-port-light.png`, `install-dns-token-light.png`, `light-system-status-check-it.png`, `light-system-server-dns.png`, `light-system-server-dns-automatic.png`, `reach-phone.png`.
- [ ] The latest **CI** run on master is green in every job (one browser job per front door, `public-port` among them).

### 3. Update both servers
For each server, as for the Help demo:
```
scp bin/linx bin/linx-firewall-sync bin/linx-backup-agent bin/linx-ops-agent linx@home.mym.ae:
ssh linx@home.mym.ae 'sudo ./linx setup'      # pick the terminal, keep every answer
```
Then the same with `vps.mym.ae`.
- [ ] Both end with "Linx is running".
- [ ] `sudo linx doctor` on each: nothing failing (the known warnings only: the CA backup still on the server, no API key).

### 4. Home: the front-door card
On the server, `sudo ./linx setup` → **the Server settings page** (changes open only while setup has the page open, four hours). Laptop, signed in as the admin at `https://home.mym.ae`: System → **Server settings** → **In front** → **Change**. Nothing changes while the front door is the one you have: to see the card, change the address (say to `.212`) and press **Check**, then Cancel.
- [ ] The choices: Linx takes 443, **Another program passes Linx through**, only at home; **Advanced** adds the decrypting proxy and **another public port (advanced)**. Choosing the decrypting proxy shows its "Not recommended" line.
- [ ] Choose **Another program passes Linx through**, at `192.168.1.211` (your Pangolin), and **Apply**: **Before you apply** shows the card, with the three facts (your domain to this server's port 8443, `turn.` to its port 5349, PROXY protocol on, from Pangolin's address) and **How to do this in**: Pangolin, nginx, HAProxy, Caddy, Nginx Proxy Manager, a router.
- [ ] Pangolin's steps match what's in your Pangolin today (Traefik's file; Pangolin's own web page can't pass Linx by name).
- [ ] Cancel: nothing changes.

### 5. Home: Check it
System → **Status** → **Reachable from outside** → **Check it**.
- [ ] From this server: `home.mym.ae` and `turn.home.mym.ae` point at your address, `home.mym.ae` answers with Linx's certificate through Pangolin, `turn.home.mym.ae` relays call audio. Your router may not reach its own address from inside: then an ℹ line sends you to the phone, which is fine.
- [ ] iPhone with **Wi-Fi off**: scan the picture. The phone's page shows the mobile network's address and tests call audio. Within a few seconds the laptop says "Your phone reached Linx from …" (that address) and "Calls from outside will have audio".
- [ ] Open the same link again on the iPhone: "This link can't be used".
- [ ] **Check again**, then scan the new picture with **Wi-Fi on**: the laptop says Wi-Fi was still on (your home address).

### 6. Home: DNS records and the key
Server settings → **DNS**.
- [ ] The records: `home.mym.ae` and `turn.home.mym.ae` at your public address, `sip.home.mym.ae` at 192.168.1.213, each **Right**, "checked at your domain's name servers", company **Cloudflare**, and "Linx keeps these right at Cloudflare" with the last change.
- [ ] **Replace key** → **DNS company** starts on Cloudflare, and the page says where to find the key. **Check the key** with your current token: "This key can change … records". Cancel without applying.
- [ ] **Stop** → Apply → confirm it's you. The records card still lists the records, all **Right**, and no longer says Linx keeps them. In Cloudflare, nothing changed.
- [ ] **Let Linx keep them right** → Apply → confirm it's you: the card says it again.
- [ ] System → **Activity**: the two `system.server_settings` entries; none shows the token.
- [ ] Optional, if you have a domain at another of the ten companies: the page names that company for it (the install page's token step is the quickest place to see this).

### 7. VPS: a passkey at 443
Laptop at `https://vps.mym.ae`, signed in as the VPS's admin.
- [ ] If you have no passkey there: My account → add one (iPhone or the laptop).
- [ ] Sign out, sign in again with the passkey: it works.

### 8. VPS: move to port 8443
On the VPS, `sudo ./linx setup` → **the Server settings page** (again for step 10). System → **Server settings** → **In front** → **Change** → **Advanced** → **another public port (advanced)**.
- [ ] The warning comes first (port 443 is always recommended), with **Show me the front-door options** and **Use another port**. Choose **Use another port**.
- [ ] **Public web port** 8443, **call audio (UDP)** 443. Try 5061 first: turned down with the reason. Then 8443.
- [ ] It asks for the DNS company's key (no port 443 for Let's Encrypt's check): Cloudflare, paste the token, **Check the key** ✓.
- [ ] **Before you apply** says: on a rented server there's nothing to forward (Linx opens both ports); Linx moves to `https://vps.mym.ae:8443`; passkeys keep working; company sign-in's new return address; links already sent stop working.
- [ ] **Apply** → confirm it's you → the steps tick → the page comes back at `https://vps.mym.ae:8443`.

### 9. VPS: on port 8443
- [ ] `https://vps.mym.ae` (no port) doesn't open any more.
- [ ] Sign out, then sign in at `https://vps.mym.ae:8443` **with the passkey made at 443**: it works.
- [ ] System → Status → **Check it**: every line ✓, and the last one ⚠ *People open Linx at port 8443, not the standard 443* with what it trades away.
- [ ] Phone link with Wi-Fi off: its address ends in `:8443`; the phone's page shows the mobile address and audio works.
- [ ] On the VPS, `sudo linx doctor`: the front door passes, one warning that the port isn't 443, nothing failing.
- [ ] Server settings → **DNS**: the records are **Right**, and Linx keeps them right at Cloudflare now.
- [ ] iPhone on mobile data at `https://vps.mym.ae:8443`: sign in, **echo test** (or a call to another signed-in browser): clear audio both ways.
- [ ] On the VPS, `docker stats --no-stream`: within `docs/RESOURCES.md` §1's numbers at rest; note them in the Results.

### 10. VPS: back to 443
Server settings → **In front** → **Change** → **Nothing else uses port 443 — Linx takes it** → Apply → confirm it's you.
- [ ] The page comes back at `https://vps.mym.ae`; `https://vps.mym.ae:8443` doesn't open any more.
- [ ] Sign in with the same passkey: it works.
- [ ] Check it: every line ✓, no ⚠ about the port.

### 11. Clean up
- [ ] VPS: keep the Cloudflare token there (Linx now keeps `vps.mym.ae` and `turn.vps.mym.ae` right), or **Stop** and delete the token in Cloudflare. Remove TCP 8443 from the provider's firewall.

### Results
Run 2026-10-01 with the owner, at `344d0ae` (fixes below on top).
- Steps 1–3 ✓: lint, test, screens, Linux build; CI green in all 23 jobs; both servers updated, doctor nothing failing (VPS: three more warnings that are the test VPS's own: no alert channel, no phone lines, public address).
- Step 4: choices ✓. **Found:** Before you apply showed only the router line, without the card: the API dropped it (fixed, `bb2c585`). Owner: "Nothing: only at home" now says "(no internet)" (`a85c625`). Card itself: see below.
- Steps 5–7 ✓ (Check it from the server and the phone with Wi-Fi off and on, the used link refused; DNS records, Replace key, Stop, start again, Activity without the token; passkey at 443).
- Step 8 ✓ (5061 refused with its reason; Before you apply as listed). **Found:** with the move pending, **Check the key** showed no tick, and the move's Check stayed grey until an edit (fixed, `7b9a09b`).
- Step 9 ✓: passkey from 443 at :8443, Check it with the one ⚠, phone link at :8443, doctor (the port warning only), DNS kept right, iPhone on mobile data with audio. At rest: about 1% processor; Docker shows about 226 MB for Linx's services (most is file cache: step-ca 43 MB shown, 17 MB its own), 1.2 GB of 2 GB available.
- Step 10 ✓ (passkey, Check it without the port ⚠). **Found:** the page at :8443 spun for ever on the firewall step: the closed port's packets are dropped, so the page never noticed Linx had gone. Now it gives up after 8 s, and (owner) it opens the new address by itself after a move to another port (`6779005`).
- Step 11 ✓: TCP 8443 removed at the provider; Cloudflare token kept on the VPS.
- CI: `TestSIPRelay` failed twice on amd64: the relay registered a browser's connection just after accepting it, so a sign-out or a disabled person in between waited up to 15 s for the recheck. Now registered first (`internal/siprelay`).
- Owner: re-test moving ports (key tick, the page following the move, no spinner) in Demo B, not now.
- Fixed after the demo: doctor's UDP line on a rented server no longer says "127.0.0.1" and "your router's forward" (`c2a3781`).
- Owner decisions after the demo (as recommended): **Let Linx update them for you** wording, and **Show the steps** for the front door in use, both done. Linx already followed a changing home address (linx-certd, every 5 minutes); Claude had wrongly said it didn't.
- Still to do: `docs/RESOURCES.md` §1 re-measured as own memory, not cache; `make screens` re-saves a few help pictures each run (timing only).

## Part B: email, call routing, voicemail, call history, undo, Phase 1 exit

A hand-run check that Part B (`docs/PHASE1F.md` §11 steps 9–16) does what it promises, on the home server with the UCM landline, plus Phase 1's exit test and the port-move re-test you asked for on the VPS. Tick each box. Allow about three hours.

**Part B exit:** Linx sends email through your own mail account (a test, an invite, an alert, a voicemail with the recording attached), and nobody can find out from **Forgot your password?** who has an account. A call to the landline rings a ring group, follows office hours, and goes to voicemail when nobody answers or when you're closed; the message is in the **Voicemail** tab and in your email. Every call is in **Call history** with its way through Linx in words, and the download opens in a spreadsheet without running anything. Every routing change can be undone, and putting back calls abroad asks "confirm it's you". **Phase 1 exit:** a browser on a network with UDP blocked calls another browser and a mobile through the landline, both relayed, with audio both ways; the call suite (SIPp) and the browser suite are green in CI.

The examples use the home server `home.mym.ae` (192.168.1.213, behind Pangolin), the UCM6304's landline `042340100` (it rings extension 200), and the VPS `vps.mym.ae`. Use your own.

### You need
- The home server and the VPS as after Part A, each with an admin account (yours: extension 200 at home).
- **An email account Linx can send from.** For Gmail: turn on 2-Step Verification, then make an **app password** at myaccount.google.com/apppasswords (16 letters; Linx keeps it sealed). Other providers: the Email card lists them.
- **A second test person** at home with an email you can read: your own Gmail address with `+sara` before the `@` works (`you+sara@gmail.com` arrives in your inbox). Step 5 makes the person.
- The **laptop** (you, 200), a **second browser** on the laptop (Firefox, or a Chrome window with another profile) for the test person, the **iPhone** (mobile calls to the landline), and the **iPad** on the iPhone's hotspot for step 13.
- A spreadsheet app (Numbers or Excel) for the call history download.
- In the VPS provider's firewall: allow **TCP 8443** in again for step 14.

### 1. Docs
- [ ] `docs/PHASE1F.md` §4–9, ADR-066 to 071 "As built" in `docs/DECISIONS.md`, and the "Phase 1F Part B review" at the end of `docs/THREAT_MODEL.md` read sensibly to you.

### 2. On your computer
```
cd ~/Projects/linx
git pull
make lint          # ends with "lint: ok"
make test          # nothing listed means all passed
make screens       # screenshots in web/e2e/screenshots/
GOOS=linux GOARCH=amd64 make build
```
- [ ] All finish without errors.
- [ ] These screenshots look right to you: `light-system-settings-email.png`, `light-sign-in-forgot.png`, `light-sign-in-forgot-second-step.png`, `light-ring-groups.png`, `light-office-hours.png`, `light-incoming.png`, `light-incoming-wizard-after.png`, `light-simulator-after-hours.png`, `light-voicemail.png`, `light-voicemail-record.png`, `light-call-history.png`, `light-admin-calls-detail.png`, `light-system-routing-changes-see.png`, and the phone-width ones `phone-voicemail.png`, `phone-call-history.png`, `phone-system-routing-changes.png`.
- [ ] The latest **CI** run on master is green in every job. The **call suite** (SIPp: ring groups, office hours, voicemail, call records) and the **browser suite** (one job per front door; in each, a browser with UDP blocked calls another browser and a line) are the automated half of the Phase 1 exit.

### 3. Update both servers
As in Part A step 3, for `home.mym.ae` and `vps.mym.ae`.
- [ ] Both end with "Linx is running"; `sudo linx doctor` on each: nothing failing (its phone-system line now also checks that Asterisk may only add call records).

### 4. Home: the front-door card (left from Part A)
Part A step 4 on the home server: Server settings → **In front** → **Change** → address `192.168.1.212` → **Check**.
- [ ] **Before you apply** shows the card with the three facts and **How to do this in**, Pangolin first. Cancel: nothing changes.

### 5. Email
System → **Settings** → **Email** → **Set up email**.
- [ ] The list of providers: choose **Gmail or Google Workspace**; it asks only your address and the app password. **Save and send a test** → confirm it's you → the test arrives in your inbox within a minute → **Did it arrive?** → **Yes**.
- [ ] The card shows **Sending as** your address and **Last sent**. Open it again: the password box is empty (Linx never shows it back).
- [ ] Type a wrong app password and save: the card says what Gmail answered (e.g. "Username and Password not accepted"), in plain words. Put the right one back.
- [ ] System → **Alerts** → add an **Email** channel to your address. Its test arrives.
- [ ] People → **+ Add** → Sara, `you+sara@gmail.com`, extension 201: the invite arrives by email with a set-password link. Open it in the second browser, set Sara's password and her authenticator.
- [ ] System → **Activity**: the email entries; none shows the app password.

### 6. Forgot your password?
In the second browser, signed out.
- [ ] Sign-in page → **Forgot your password?** → type `nobody@example.com` → **Send the link**: "If that email has an account…". Then type Sara's email: the same words, just as fast. Only Sara's email gets a message.
- [ ] Open Sara's link: new password (twice), then her authenticator code. She's signed in. Sign in again in her old tab: it was signed out.
- [ ] Open the same link again: it says it was used, with **Send a new one**.
- [ ] Sign out. Sign in with Sara's password, and on the code step choose **Ask my admin to reset it**: your Email alert channel gets the alert within a minute. (Don't reset it; this only checks the alert.)

### 7. Ring groups
Laptop as you (200), the second browser as Sara (201), both on the **Dialer** and **Available**.
- [ ] Admin → **Ring groups** → **+ Add** → **Quick add** → "Sales", you and Sara, **All at once**. Its sentence reads right; note its number.
- [ ] **Saved. Undo** appears at the bottom: don't press it yet.
- [ ] From Sara's browser, dial Sales's number: your browser rings. Hang up.
- [ ] Edit Sales → **One after another**, 15 seconds each. **Simulator** → call Sales's number: the steps show you first, then Sara.

### 8. Incoming, office hours, "We're closed"
- [ ] Admin → **Incoming** → the landline's card → **Change**: **Somewhere else** → Sales → **Follow office hours** → **After hours**: **Play "We're closed", then voicemail**, whose voicemail: Sales → the sentence reads right → **Save**.
- [ ] iPhone: call `042340100`. Your browser rings, then Sara's after 15 seconds. Sara answers; both hear each other. Hang up.
- [ ] Admin → **Office hours**: it says **Now: open** (if not, it's after hours already: skip to the next box). Change today's closing time to a few minutes ago → **Save hours** → it says **Now: closed**.
- [ ] iPhone: call the landline again. You hear "We're closed", then the tone. Leave a short message ("test one") and hang up.
- [ ] **Simulator** → **When**: tomorrow at 10:00 → the landline's number: steps say open, Sales; at 03:00: "We're closed", then Sales's voicemail.
- [ ] Office hours → **Holidays** → add today as "Test day": it says **Now: closed** even within today's hours. Remove it again.

### 9. Voicemail
- [ ] Sara's browser: the **Voicemail** tab has a badge 1 within a few seconds (no reload). The message is in **Ring groups** → Sales, from your mobile number, with its length. **Play**: you hear "test one". It's now **Heard**.
- [ ] Your browser: you see it too (you're in Sales). Ring-group messages aren't emailed (your decision): no email arrives for it.
- [ ] Your **Voicemail** → **Settings**: **Email me new messages** on. **Greeting** → **Record**: say "You've reached the test line" → **Stop** → **Play** → **Use this**.
- [ ] Office hours back as they were (put today's closing time back) → **Incoming** → the landline → **Just ring one person**: you (200), no answer → your voicemail.
- [ ] iPhone: call the landline, don't answer on the laptop. After the ringing you hear your own greeting, then the tone. Leave "test two".
- [ ] Your **Voicemail**: the message, badge 1. Your inbox: an email with the recording attached (a `.wav`, plays on the laptop).
- [ ] From your browser, dial Sara (201); she doesn't answer; leave "for Sara". Your **Voicemail** → **All boxes** (admins see every box) → Sara's → **Play**. System → **Activity** shows that you listened to a message in Sara's box.
- [ ] **⋯** → **Delete** "test one" in Sales: gone from both browsers.

### 10. Call history
- [ ] Your browser: **Call history** has a badge for the calls you missed; opening it clears it. Each call shows its way through Linx in words ("Rang Sales: … · Sara answered", "Rang you · no answer · left a message (0:05)"); **Listen** plays "test two".
- [ ] **Missed only** and **Search number** (a few digits of your mobile) work. The Dialer's **Recent** shows the last 5.
- [ ] Sara's history shows only her calls (none of yours alone).
- [ ] Admin → **Calls**: everyone's calls; **Anyone** → Sara narrows it. **Download (CSV)** → open it in Numbers or Excel: one row per call, readable times, nothing runs, no warnings about formulas.
- [ ] System → **Settings** → **Keep call history** shows 1 year; **Keep voicemail** 60 days.

### 11. Undo and routing changes
- [ ] Ring groups → Sales → change the ring time → **Save** → **Saved. Undo** → **Undo**: the old time is back.
- [ ] System → **Routing changes**: every change from today, newest first, with who made it. **See the change** on the office-hours one shows before and after in sentences.
- [ ] **Put this back** on the change that pointed the landline at Sales (step 8): the preview lists what will change → put it back → Incoming shows the landline as it was before step 8. Then **Undo** that, and it's back to "Just ring one person".
- [ ] Admin → **Outgoing** → a calling level → turn **abroad** on → Save, then **Undo** (abroad off: no question asked). In Routing changes, **Redo** the undo (abroad on again): it asks you to confirm it's you. Then turn abroad off again.
- [ ] System → **Activity**: `routing.undo` and `routing.put_back` entries.

### 12. A restart doesn't stop calls
The phone system (Asterisk) takes calls by itself; the web and API service (the control plane) isn't needed for them. Browsers' phone lines run through the web and API service, so they can't ring while it restarts; desk phones can.
- [ ] System → **Status** → **Restart** on the **web and API service** (not the phone system: while that restarts, no call can come in). While it restarts (about 10 seconds), call the landline from the iPhone: Linx still answers it as Incoming says (outside office hours: "We're closed", then the tone). Leave "test four".
- [ ] Once the page is back (and the line's tone has passed): "test four" is in your Voicemail (the web and API service picks it up when it starts) and in Call history.

### 13. Phase 1 exit: UDP blocked
The iPad on the iPhone's hotspot, signed in as Sara at `https://home.mym.ae`. Find the hotspot's public address: on the iPad, open `https://1.1.1.1/cdn-cgi/trace` and read `ip=`. On the home server:
```
sudo nft add table inet linxdemo
sudo nft add chain inet linxdemo pre '{ type filter hook prerouting priority -300; }'
sudo nft add rule inet linxdemo pre ip saddr HOTSPOT_IP meta l4proto udp counter drop
```
Reload Linx on the iPad.
- [ ] **Web to web:** the iPad calls you (200) on the laptop. Both hear each other for 20 seconds; the iPad's chip says **Relayed**.
- [ ] **Web to line:** the iPad dials your iPhone's mobile number. The iPhone rings, both hear each other; the chip says **Relayed**.
- [ ] `sudo nft list table inet linxdemo` shows the counter above 0. **This is the Phase 1 exit test.**

Undo it: `sudo nft delete table inet linxdemo`.

### 14. VPS: moving ports again (your Part A request)
Part A steps 8–10 on the VPS, looking especially at the three things fixed then:
- [ ] With the move pending, **Check the key** shows its tick at once, and the move's **Check** isn't grey.
- [ ] After **Apply**, the page opens `https://vps.mym.ae:8443` by itself, signed in again with the passkey.
- [ ] Back to 443: no endless spinner; the page opens `https://vps.mym.ae` by itself.
- [ ] On the VPS during an echo test from the iPhone, `docker stats --no-stream`: note the processor and memory in the Results. Remove TCP 8443 from the provider's firewall afterwards.

### 15. Clean up
- [ ] Home: Incoming as you want it (the landline ringing you, or back to Sales); delete the test messages; keep Sara or remove her (People → Sara → Remove). The Email alert channel can stay.

### Results
Run 2026-10-03 with the owner, at `424cc46`.
- Steps 1–3 ✓: CI green; both servers updated to `424cc46` (schema 38), doctor nothing failing (home: the CA backup and API key warnings; VPS: those plus no alert channel, public address, no phone lines).
- Step 4 ✓: the home front-door card at `.212`, Pangolin first; Cancel changed nothing.
- Step 5: **Found:** on a server where email was never set up, **Save and send a test** stayed grey with Gmail: Gmail was already chosen, so its (hidden) server was never filled in (fixed, `2d7842c`; screens test). With the workaround (another provider, then Gmail) the test email arrived.
- Step 5: **Found:** adding an **Email** alert channel said "Something went wrong": the database still allowed only the first six kinds (fixed, migration 0039; a real-Postgres test saves every kind). Invite by email arrived; Activity doesn't show the app password.
- Step 6: boxes 1–4 ✓ (same answer for an unknown email, reset with the second step, used link refused). Ask my admin: after the update.
- Step 7 ✓ (Sales all at once, Saved. Undo shown, the simulator's steps one after another).
- Update to `e7bec3e` for the two fixes: **Found:** `sudo ./linx setup` didn't update, because the Server settings page opened in step 4 was still open (four hours), so setup went straight to it ("Nothing was reopened"). Updated with `sudo ./linx setup --config /etc/linx/setup.yaml`. **Owner decision 2026-10-03 (as recommended):** when the `linx` program is newer than what's running, setup says so and gives the update command, even while the page is open; build after the demo. Email alert channel then added, its test arrived.
- Step 6 box 5 ✓ (Ask my admin: the alert email arrived).
- Step 8 ✓. **Found:** **Change** on a number showed "request body has an error: … readOnly property \"label\"" until a choice was picked again: the wizard sent back the destinations as the server gave them, with their read-only label; Ring groups' **If nobody answers** had the same fault when saved unchanged. Both now send them without it (`sendable`), and the screens' stand-in server refuses a label like the real one.
- Step 9: **Found:** a ~5 s message through the UCM's analog landline was kept as 1 min 46 s. The line never signals a hang-up (Phase 1D's known limit), so Linx recorded what the exchange plays after the caller hangs up: about 40 s of UAE busy tone (400 Hz, 0.375 s on and off), then about 60 s of a steady 950 Hz tone, then silence, which ended the recording after 10 s (measured from the recording). The landline also stays busy that long. **Owner decision 2026-10-03 (as recommended):** Linx listens for the busy tone and hangs up (Asterisk's tone detection, the country's busy tone; trims the tone off a message; also ends ordinary calls whose far end hung up); build after Demo B, measured, with a call-suite test.
- Step 9: **Found:** dragging the slider to a message's end didn't mark it heard (only playing out did), and the slider's handle had no name for screen readers. Both fixed (screens test). The "label" error seen again on the way was the step 8 fix not yet on the server.
- Step 9: **Found:** **Mark as heard** said "header Content-Type has unexpected value application/merge-patch+json": the API's description said plain JSON for marking voicemail (and for the moved-server checklist's ticks), the web app sends every change as merge-patch. Both corrected; a test holds every change endpoint to the web app's rule.
- Step 9 ✓ after updating to `525d29f`: greeting, the message from the landline (saved about 2 min after hanging up: the tone tail), its email with the WAV (to the account's own address), Mark as heard, leaving and playing Sara's message (in Activity), deleting the Sales message from both browsers. The CI run for `525d29f` failed once on a call-suite timing check (31 s measured for a 30 s ring); re-run green, check loosened (`06a82be`).
- Step 10 ✓ (history in words, Listen, filters, Recent, Sara sees only hers, admin Calls and Anyone, the CSV in a spreadsheet). The checklist said "365 days" where the screen says **1 year** (the same setting, as a choice); checklist corrected.
- Step 11 ✓ (Routing changes, See the change, Put this back and its Undo, abroad: Undo without a question, Redo asks to confirm it's you, Activity), except: **Found:** after saving in Sales's panel, **Saved. Undo** didn't appear: it was drawn behind the panel's backdrop (the change itself was kept). Now drawn over everything (screens test presses Undo there).
- Step 12, first try: the **phone system** was restarted (the checklist said "the control plane", which isn't what Status calls it): the iPhone kept ringing until it was back, as expected; the call then went to "We're closed" and voicemail without ringing (Saturday, office hours Mon–Fri). The checklist also wrongly expected the browser to ring during the restart (its line runs through the web and API service). Checklist rewritten.
- Step 12 ✓ (web and API service restarted: the landline call still answered, "We're closed" and voicemail, the message there once it was back). The first try's call reached Linx with no caller number (the UCM held it while the phone system restarted), so it showed as withheld; every other call had the number.
- Step 13 ✓ **Phase 1 exit:** iPad on the iPhone's hotspot (31.218.150.254), all its UDP dropped at the home server (30 packets, 4,168 bytes counted): web to web (iPad → 200 on the laptop) and web to line (iPad → the iPhone's mobile through the UCM landline) both **Relayed**, audio both ways. Block removed afterwards.
- Step 14 ✓ (your Part A request): on the VPS, with the move pending Check the key ticked at once and Check wasn't grey; after Apply the page opened `https://vps.mym.ae:8443` by itself, passkey sign-in worked; back to 443 with no spinner, the page followed by itself. TCP 8443 closed at the provider again. At rest afterwards (Claude, `docker stats`): under 1.3% processor in all; Linx's services about 249 MB as Docker counts it (Postgres 63, Asterisk 51, control plane 47, step-ca 43), 1.1 GB of 2 GB available.
