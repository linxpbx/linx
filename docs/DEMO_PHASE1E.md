# Phase 1E demo checklist

A hand-run check that the admin portal (`docs/ADMIN.md` §12 steps 2–8, with `docs/SIMPLER.md` §1) does what it promises. Tick each box. Allow about three hours; the UCM's side is the longest part.

**Phase 1E exit** (`docs/ADMIN.md` §13): on a fresh install through **Pangolin**, `linx setup` prints the first-admin link; you set a password and a passkey on the iPhone, run the setup wizard (business, UAE, 3 digits, three people with invite QR codes), connect the UCM6304 **as a phone system that signs in to Linx**, send its number to 101 and leave "Abroad" off; the simulator shows a mobile number allowed and an international one refused, matching real calls; a custom people range (200–499) is saved and the next free number follows it; a second person joins by QR and signs in with Google; an admin signing in from mobile data works, and after "home network only" gets the ordinary view; "confirm it's you" appears before creating an admin; System → Status matches `linx doctor`. Automated: `make screens`, the call suite and the browser suite are green in CI.

The examples use the home server `home.mym.ae` at `192.168.1.213`, Pangolin at `192.168.1.211`, the UCM6304 at `192.168.1.60` with landline `042345678`, and the test VPS `vps.mym.ae`. Use your own.

**Never dial an emergency number (999, 998, 997, 112, 901) for real in this demo.** The simulator shows them without dialling.

## You need
- The home server (Proxmox VM on your home network) and Pangolin, as in [`DEMO_INSTALL.md`](DEMO_INSTALL.md) Part B, with the DNS token at hand.
- **The UCM6304** with a working landline on an FXO port, and its admin password.
- **The iPhone** (passkey, Google, mobile data), **a second person's phone** (or a second browser profile) for the invite and Google, and **a laptop** on your home network.
- **The ntfy app** on the iPhone (for the alerts step).
- A **Google Cloud** account for company sign-in (Step 9 makes the app there).
- The images published by CI for the commit you build.

## 1. Docs
- [ ] `docs/ADMIN.md`, `docs/SIMPLER.md` §1, ADR-049 to 054 and 061, and the "Phase 1E review" in `docs/THREAT_MODEL.md` read sensibly to you.

## 2. On your computer
```
cd ~/Projects/linx
git pull
make setup-dev
make lint          # ends with "lint: ok"
make test          # failures only; nothing listed means all passed
make security      # govulncheck, npm audit and licences all "ok"
make test-docker   # ends with "docker tests: ok"
make screens       # screenshots of every screen, light, dark and phone width
```
- [ ] All finish without errors. Open a few screenshots in `web/e2e/screenshots/` (`light-lines-add-login.png`, `dark-system-activity.png`, `phone-incoming.png`) and check they look right to you.

## 3. CI on GitHub
- [ ] The latest **CI** run on master is green in every job, including the call suite ("a phone system that signs in to Linx") and the browser suite behind all four front doors.

## 4. Fresh install through Pangolin
On the home server, start clean (`docs/DEMO_INSTALL.md` "Clean up"), then `sudo linx setup`, choose the browser, and follow the page with "Pangolin", the token first.
- [ ] The install finishes; the first-admin page opens at `https://home.mym.ae/setup/…`.
- [ ] Choose **Passkey**, make it on the iPhone (Face ID), save the recovery codes, then add a password when asked (or from My account later).

## 5. The setup wizard
- [ ] "How do you want to start?" → **Set up fresh**. Place: **Business**. Country: **UAE**. Numbers: **3 digits**.
- [ ] Change the people range to **200–499** and save. People: add yourself and **three people**; each shows an invite link and **QR code**.
- [ ] Phone line: **Later** (step 8 does it on the Phone lines page). Calls: leave **Abroad** and **Premium-rate** off. Test call: `*43` echoes you. Done.
- [ ] Extensions → **+ Add** → Quick add: the number offered is **the next free one from 200** (not 100).

## 6. Admins, and "confirm it's you"
- [ ] People → + Add → Guide me → a person with role **Admin**: "Confirm it's you" appears before it's created.
- [ ] On the iPhone, **on mobile data**, sign in as yourself: the Admin area is there.
- [ ] System → Settings → Admins can sign in from → **Only my home or office network** → Change anyway (confirm it's you). Reload on mobile data: the Admin area is replaced by one greyed row saying why. On the home Wi-Fi it's back.
- [ ] Put it back to **Anywhere**.

## 7. A second person, by QR and Google
Do step 9 first if company sign-in isn't set up yet.
- [ ] The second person scans their invite QR, sets their sign-in, and later signs in with **Sign in with Google**.
- [ ] Their name appears in Team, and in System → **Activity** as "Signed in".

## 8. The UCM6304 signs in to Linx
In Linx: **Phone lines → + Add → Guide me → Another phone system or gateway**.
- [ ] Name it `UCM landlines`. Numbers: add `042345678`, ringing **101** (or your own extension). "Calls for any other number ring": your extension. Outgoing: **Yes, as the main line**. **Create** asks "confirm it's you", then shows the server, port 5061, TLS, username and password once, and "How to enter this on **Grandstream UCM**".
On the UCM (menu names from firmware 1.0.33):
- [ ] **Extension/Trunk → VoIP Trunks → Add SIP Trunk**: type **Register SIP Trunk**, provider name `Linx`, host name `sip.home.mym.ae:5061`, transport **TLS**, username and Auth ID and password exactly as shown. Advanced: codecs PCMA then PCMU, SRTP **Enabled and forced**, DTMF RFC4733.
- [ ] Linx's page turns from "Waiting for it to sign in…" to **Signed in** within a minute, without reloading. The Phone lines list shows it Signed in, 1st, Encrypted.
- [ ] **Inbound Routes** for the Linx trunk: send its calls to the analog trunk (Dial Trunk). The landline's inbound route: send calls to the Linx trunk (the `*88` outbound-route trick from `docs/DEMO_PHASE1D.md` step 7d, pointing at this trunk).
- [ ] From your mobile, call the landline: **your browser rings** (it arrives with no number of its own, so "calls for any other number" rings you, or with `042345678` if the UCM sends it).
- [ ] From the browser, call your mobile: it rings, both hear each other.
- [ ] Phone lines → the line → **Test again**: "Signed in to Linx". Turn the UCM's trunk off for 3 minutes: the line shows **Down**, the "line down" alert fires (step 10's ntfy gets it); turn it back on: Signed in, the alert resolves.
- [ ] `sudo linx trunk list` shows it "signs in to Linx", registered.

## 9. Company sign-in with Google
- [ ] System → Settings → Company sign-in → **+ Add a provider** → Google: follow the steps, paste the **redirect address** into Google's console, paste the client ID and secret, Add (confirm it's you).
- [ ] **Link my account with Google**: you come back to My account showing it linked.

## 10. Incoming, outgoing, simulator
- [ ] **Incoming calls**: `042345678` rings 101; change it to another extension and back (each says "Saved").
- [ ] **Outgoing calls**: UCM landlines is 1st, Working. Turn on **Abroad**: "confirm it's you" first; then turn it **off** again.
- [ ] **Simulator**: from 101, `050 123 4567` → **Allowed**, goes out on "UCM landlines" as `0501234567`. `0044 20 7946 0958` → **Not allowed** (abroad) with "Change what phones can call". `999` → **Always allowed: emergency** (not dialled). Someone calls in → `042345678` → rings 101.
- [ ] A real call from 101 to an international number is refused with the "not available on this line" message, matching the simulator.

## 11. System
- [ ] **Status** matches `sudo linx doctor` on the server (services, certificate, phone lines).
- [ ] **Alerts** → + Add → Guide me → **ntfy**, a long made-up topic subscribed in the ntfy app → Problems → Not at night → the test message arrives on the iPhone.
- [ ] **Activity** shows the day's changes as sentences; open one to see its details (no passwords anywhere).
- [ ] **Settings**: Simple mode **off** shows the expert pages (Connections, Webhooks, API keys). Turn it back on.
- [ ] **API keys** (Simple mode off) → + Create → Read only, 90 days → shown once → Revoke it.
- [ ] **Webhooks** → + Add → Quick add with a fresh webhook.site address → the test delivery arrives there with the `webhook-signature` header; remove it.

## 12. My account
- [ ] Signed-in browsers shows the laptop and the iPhone. On the laptop, **Sign out** the iPhone: the iPhone's next click goes back to sign-in.
- [ ] Change your password: you're signed out everywhere; sign in again with the new one and your passkey.

## 13. The small VPS
On `vps.mym.ae` (1 core, 2 GB; reinstalled with Debian 13), a fresh install with "Linx takes 443" and no token (`docs/DEMO_INSTALL.md` Part A), then:
- [ ] The admin pages open and feel quick on mobile data (the first visit to each admin page loads it once).
- [ ] `docker stats --no-stream` stays within `docs/RESOURCES.md` §1's numbers at rest; note them in the Results.

## 14. Clean up
- [ ] Remove the test API key and webhook if left; keep the UCM line (it stays as the lab server's line).

## Results
*(Filled in when the demo runs.)*
