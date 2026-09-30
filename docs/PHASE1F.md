# Linx — Phase 1F: front doors, DNS, email, call routing, voicemail, call history

*Drafted 2026-09-30; **approved by the owner 2026-09-30, all §10 decisions as recommended**. Front doors, DNS and the public port are already designed and approved in `docs/SIMPLER.md` §2–3 and §2.5 (ADR-062 to 064); this document adds the rest of Phase 1F (ADR-066 to 071) and one build order for all of it.*

## 1. In plain words
When Phase 1F is done:
- **Getting to Linx from outside** is one card with three facts and instructions for your proxy (Pangolin, nginx, Caddy, Nginx Proxy Manager, a router), a **Check it** button, and a link to open on your phone with Wi-Fi off. Any DNS company works: by hand with a checklist, or kept right automatically. **Another public port** (8443) works for homes where 443 is taken.
- **Linx can send email** through your own mail account or a sending service: invites, **"Forgot your password?"**, voicemail, and admin alerts.
- **Calls go where you want**: **ring groups** (several people's phones ring together or one after another), **office hours and holidays**, and one **"When someone calls"** wizard per number: who rings, what happens if nobody answers, and what happens after hours.
- **Voicemail**: callers leave a message; people see it in the web app's **Voicemail** tab, listen there, and get it by email.
- **Call history**: every person sees their own calls (missed ones first); admins see everyone's.
- **Undo** for call routing: every change is kept, and one click puts the previous one back.

Everything keeps to the low-resource rule: **no new container, no new background loop**. Calls keep ringing while the control plane restarts (ADR-034): the routing, office hours, voicemail recording and call history all happen inside Asterisk, reading and writing the database directly.

## 2. What's in this slice, and what waits
**In:** SIMPLER §2 (front-door card, Check it, decrypting proxies as advanced), §3 (any DNS company), §2.5 (public port); email sending; password reset and invites by email; email as an alert channel; ring groups; office hours and holidays; the full inbound wizard; voicemail (take, store, email, web inbox, greetings); call history; routing undo; the call simulator learning all of it; help guides for every new screen; Phase 1's exit test.

**Waits:**
- **Listening to voicemail from a desk phone** (dial `*97`) and the **message-waiting light**: both need Linx to control calls through ARI, which Phase 2 adds anyway for iPhone push (ADR-034). Until then: web app and email. *(Decision §10 item 4.)*
- Forwarding calls to a mobile number outside (after hours or "if nobody answers"): it's an outside call paid for by the company and a toll-fraud path; needs its own design with limits. *(Decision §10 item 5.)*
- Menus ("press 1 for sales"), queues, recording: Phase 4, as planned. The destinations list below is built so they slot in.
- Email sign-in with Google/Microsoft accounts (OAuth) for sending: SMTP with an app password first.

## 3. Front doors, DNS, public port
As designed in `docs/SIMPLER.md` §2, §3 and §2.5; nothing new here except the order (§11). The first task is checking what Pangolin's own web page can do on the owner's Pangolin 1.23 EE (SIMPLER §4). Two decisions from SIMPLER §5 are still open and repeated in §10 below (items 1 and 2).

## 4. Email sending (ADR-066)
- **How:** Linx sends through an SMTP server you already have: your mail provider (Gmail or Google Workspace with an app password, Microsoft 365, Fastmail, iCloud…) or a sending service (Amazon SES, Postmark, Brevo, Mailgun). Presets fill in the server and port; "Other" asks for them. **No mail server runs inside Linx** (a home connection's mail is usually blocked or marked as spam anyway).
- **Security:** always encrypted: TLS from the start (port 465) or STARTTLS (587), certificate checked, never plain. The password is sealed in the database like the AI key (Help step 3). The server's address goes through the private-address guard like webhooks: a mail relay on your own network has to be allowed on purpose.
- **Code:** Go's own `net/smtp` and `mime` packages: **no new dependency**, nothing running between emails.
- **Where:** System → Settings gets an **Email** card: preset, address, password, "From" name, **Send a test email**. The admin home checklist gets "Set up email".
- **Uses:** invites and setup links (instead of only copy/QR), password reset (§5), voicemail (§8), admin alerts (a new channel next to ntfy, Slack…), and the **"email sending is broken"** alert (`docs/API.md` §5's list) after a failure, sent through the other channels.
- **Limits:** a queue in the database (so a restart loses nothing), 3 tries with growing gaps, at most 60 emails an hour by default (a hijacked account can't turn Linx into a spam cannon), all audited.

## 5. "Forgot your password?" (ADR-067)
The owner's 2026-09-27 decision, built:
- The link appears on sign-in **only when email is set up**.
- Typing an email always shows the same answer ("If that's an account here, an email is on its way"), taking the same time, so nobody can find out which emails have accounts.
- The emailed link works **once, for 30 minutes** (only its hash is stored). It opens "Choose a new password" (typed twice), and then **still asks for the second step** (passkey or authenticator code). A lost authenticator is not reset by email (owner, 2026-09-27: that would defeat it); that stays `sudo linx user reset-2fa`.
- Afterwards every other session of that person is signed out, and they get a "your password was changed" email.
- Limits: 3 requests per email per hour, 10 per address per hour; audited; an alert if someone tries many accounts.

## 6. Where a call can go: destinations and ring groups (ADR-068)
- **One list of places a call can go**, used everywhere a choice is made: a person (extension), a **ring group**, a **voicemail box**, **a short message and hang up** ("We're closed…"), and in Phase 4 menus and queues. Every choice in the wizards is this one drop-down.
- **Ring group:** a name, an optional number of its own (from the numbering plan's groups range, so people can dial it or transfer to it), members (people), and how it rings: **all at once** (recommended) or **one after another** (each for N seconds, in the order shown). Then **"If nobody answers after N seconds"** → another destination (default: voicemail of the group's first member, or a group voicemail box).
- **How it runs:** the dialplan asks the database (a function like today's `LINX_RING_TARGETS`) where the call goes next, one step at a time. Each step is capped (at most 10 steps per call, so a loop between two groups can't ring forever; the screens refuse a loop anyway). Calls keep working through a control-plane restart.
- Asterisk stays **read-only** on routing: it only reads views and calls functions the control plane owns.

## 7. Office hours, holidays and "When someone calls" (ADR-068)
- **Office hours:** days and times (e.g. Sun–Thu 08:00–17:00, the UAE week preselected from the country), in the server's time zone from setup. **Holidays:** a list of dates with names ("Eid al-Fitr"); a date range for several days. One schedule, **"Office hours"**, is made at setup; more can be added (e.g. "Support hours") but the wizard never asks for a second one.
- **"When someone calls" (per phone number, or for the whole line):**
  1. During office hours: ring → a destination (person, group, everyone).
  2. If nobody answers after N seconds (default 25) → a destination (voicemail recommended).
  3. Outside office hours and on holidays → a destination (voicemail with the "we're closed" greeting recommended), or "the same as during office hours" (a home, where there are no office hours: the default for Home in setup).
- The wizard shows one sentence under each choice, rebuilt as you pick: *"Calls to 04 123 4567 ring Sales (all at once) Sun–Thu 08:00–17:00. If nobody answers in 25 seconds, the caller can leave a voicemail for Sales. At other times and on holidays, callers hear 'We're closed' and can leave a voicemail."*
- **Quick create** stays: a number → a person, one field. The full wizard adds the rest.
- The **call simulator** (Phase 1E) learns all of it: "What happens if someone calls 04 123 4567 on Friday at 20:00?" answers step by step, with a date and time picker.
- The database decides "open or closed right now" (in the time zone), so the dialplan and the simulator use the same function and can't disagree.

## 8. Voicemail (ADR-069)
- **Boxes:** each person has one (on by default, turn off per person); a ring group can have one (its members all see it). Messages up to **3 minutes**; kept **60 days** by default, then deleted (setting, 7–365 days); the web shows the space used.
- **Leaving a message:** the dialplan answers, plays the greeting, then records after the tone. The **default greeting** is in Linx's own voice (ADR-065): *"The person you called isn't available. Please leave a message after the tone."* (and a "we're closed" one). A person can **record their own greeting** in the web app: the browser records it and uploads plain audio; Linx converts it for phones.
- **How the message travels:** Asterisk records to a small shared folder (a new volume, `linx-voicemail`), then tells the control plane over the existing ARI connection (an event, no polling). The control plane checks it (size, length, format), moves it into the database, and deletes the file. If the control plane is restarting, the message waits in the folder and is picked up on start. **Only the control plane and Asterisk see that folder.**
- **Stored in the database**, so **backups already include voicemail** and nothing new needs backing up. Stored compactly (G.711, about 0.5 MB a minute; measured in `docs/RESOURCES.md`); the web app and email get a normal WAV file made on the fly (every browser and phone plays it).
- **Listening:** the web app's **Voicemail** tab (now live): newest first, caller, time, length, play, download, delete, "mark as heard"; a badge with the new count; the `voicemail.created` webhook (already in the API list).
- **By email** (if email is set up and the person turned it on, default on): the message attached, caller and time in the subject.
- **Asterisk changes:** three small modules (`app_record`, `app_userevent`, `func_env`) added to the image's list; no `app_voicemail` (it wants its own mail program, config files and message folders: a second system to secure next to Linx's).

## 9. Call history (ADR-070) and undo (ADR-071)
**Call history**
- Asterisk writes one record per call leg straight into one database table (its standard call-record module, `cdr_adaptive_odbc`). Asterisk's database user may **only add rows to that one table**: it can't read, change or delete them, or touch anything else. The records are written even while the control plane restarts.
- The control plane turns legs into one line per call in plain words: *"Missed · from 050 123 4567 · rang Sales · 10:42"*, *"Answered by Aisha · 3 min"*, *"Left a voicemail"*.
- **Who sees what:** each person their own calls in the **Call history** tab (now live), missed first-class with "Call back"; admins everyone's under Calls, with filters (person, number, missed, date) and **Download (CSV)**. The `call.missed` webhook ships with it.
- Kept **1 year** by default (setting); about half a kilobyte a call, measured.

**Undo for call routing**
- Every change to routing (what numbers ring, ring groups, office hours, holidays, outgoing rules) saves a snapshot of the routing settings before it (small, in the database; the last 50 kept).
- A banner after each change: *"Saved. Undo"*. System → **Routing changes**: who changed what and when, and **Put this version back**, which itself can be undone. Needs "confirm it's you" when it would change outgoing permissions (as today).

## 10. Owner decisions (2026-09-30, all approved as recommended)
1. *(SIMPLER §5 item 2)* Keep the "proxy decrypts" path (Caddy/NPM decrypting) at all? **Recommend: keep, as advanced only**, with the plain warning; not offered on a rented server.
2. *(SIMPLER §5 item 3)* First DNS companies for automatic updates: **recommend the ten**: Cloudflare, DuckDNS, Route 53, GoDaddy, Namecheap, Porkbun, DigitalOcean, Hetzner, deSEC, OVH (more on request, each measured).
3. **Email:** SMTP with an app password now, Google/Microsoft sign-in (OAuth) later. **Recommend yes.** Note: Microsoft is retiring password sign-in for SMTP in Microsoft 365; the Email card says so and suggests a sending service there.
4. **Voicemail from a desk phone (`*97`) and the message-waiting light:** in Phase 2 with the ARI call control it needs. **Recommend Phase 2**; web and email now.
5. **Forwarding to a mobile outside** after hours: its own small design later (limits, confirm-it's-you, fraud alerts). **Recommend later**, not in 1F.
6. **Voicemail kept** 60 days, 3-minute messages; **call history** 1 year. **Recommend these defaults** (all changeable).
7. **Two demos instead of one:** Part A (front doors, DNS, public port) after step 8, Part B (email, routing, voicemail, history, undo, Phase 1 exit) at the end. **Recommend yes**: problems found early, and the VPS/home setup is fresh for Part A.
8. **Screen specs first** (low-fidelity, reviewed as an artifact, like 1C/1E/install). **Recommend yes.**

## 11. Build order (one session each)
Part A: getting in
1. Screen specs for every new or changed screen (front-door card, Check it, DNS, public port, Email card, forgot password, ring groups, office hours, the wizard, Voicemail, Call history, Routing changes). **Done, approved by the owner 2026-09-30**: `docs/ui/SCREENS_PHASE1F.md` (reviewed as an artifact); §16 items 2–6 as recommended; ring groups and office hours get their own sidebar rows for now (item 1, may be revisited).
2. Pangolin's web page checked on the owner's Pangolin; the **front-door card** everywhere (setup, install page, Server settings) with "How to do this in…" for each proxy; decrypting under Advanced. **Done 2026-09-30** (ADR-062 "As built"): Pangolin's web page can't pass by name (checked on 1.23 EE), so its tab is Traefik's file. New kind `proxy` (older `pangolin`/`nginx` still work); `installer.DoorCard` builds the three facts and six guides (Pangolin, nginx, HAProxy, Caddy with caddy-l4, Nginx Proxy Manager, a router) once, shown by `web/src/components/FrontDoorCard.tsx` on the install page's certificate step and Server settings' "Before you apply", and written to `/etc/linx/front-door/FRONT-DOOR.txt` (+ `Caddyfile-layer4`) by terminal setup. Install page and Server settings: three choices + Advanced (decrypting, home only, "Not recommended" box with **Show me how** / **Use it anyway**). `web/e2e/door-setup.json` is setup's real output, kept current by `TestDoorSetupFixture`.
3. **Check it**: from the server (doctor's checks, in words) and from outside (the phone link, showing the address Linx saw). **Done 2026-09-30**: `internal/reach` runs in the control plane, only when asked (no host helper): the domain and `turn.` at the domain's own name servers, Linx's certificate through the front door (checked against Linx's own chain), the relay over TLS through it, then this network's public address (an `info` line when the router can't reach itself). Phone links: 10 characters, once, 10 minutes, in memory; the phone's page (`/reach/<code>`, no sign-in) shows the address Linx saw and tests the relay (relay-only `RTCPeerConnection`, 3-minute credential); the admin's page waits with one held-open request (`GET /system/reach-links/{id}?after=`, 25 s). `system:write`; 10 tries a minute per address. On System → Status (**Reachable from outside**) and Server settings (**In front**). The browser suite runs both halves behind every real front door (the relay with UDP blocked). Not on the install page: before the full app runs there is no page to serve the phone link; the certificate step's own checks cover that part.
4. **DNS by hand**: the records list and checks at the domain's own name servers. **Done 2026-09-30**: `reach.Checker.Records` (next to Check it, same expected addresses: the public address, or this server at home-only) lists the domain and `turn.`, and `sip.` at this server's home address when it's at home, checks each at the domain's own name servers, and tells the DNS company from them (`dnscheck.Company`, the ten from §10 item 2). `GET /system/dns-records` (`system:write`); a record linx-certd keeps right (`LINX_DNS_RECORDS`, now also given to the control plane) says so. `web/src/components/DnsRecords.tsx` is the card: Server settings' **DNS** row (the token line stays under it until step 5), and **Show the records** on a Check it line whose name points elsewhere. The install page's records step also names the name servers and company (found once); it keeps listing only the two names the certificate needs, since `sip.` isn't needed to install and Server settings lists it afterwards. The "Set it up automatically" button comes with step 5.
5. **DNS automatic**: libdns, the chosen companies, measured (certd image size and memory), licences checked.
6. **Public port**, SIMPLER §2.5 build items 1–2: the setting and the one address helper everywhere; setup, install page and Server settings.
7. **Public port**, items 3–5: doctor and Check it on the port; tests (passkey across a port change, browser suite behind 8443, install with Pebble on DNS-01); help guide.
8. Part A review (THREAT_MODEL) and `docs/DEMO_PHASE1F.md` Part A. **Demo A.**

Part B: calls and email

9. **Email**: sender, queue, Email card, test, alert channel, "email broken" alert, invites by email.
10. **Forgot your password?**
11. **Destinations and ring groups**: tables, dialplan, screens, call suite (all at once, one after another, loops refused).
12. **Office hours, holidays, "When someone calls"** wizard, simulator, call suite at fixed times.
13. **Voicemail part 1**: prompts, recording, the shared folder, import, storage, webhook, email.
14. **Voicemail part 2**: Voicemail tab, greetings recorded in the browser, retention.
15. **Call history**: CDR table and grant, grouping, the two views, CSV, `call.missed`.
16. **Routing undo**.
17. Part B review (THREAT_MODEL), Phase 1 exit test (web-to-web and web-to-line calls with UDP blocked, SIPp suite green), demo checklist. **Demo B.**

Each step: help guides updated in the same commit, `make screens` for every screen, resources measured where anything grows.

## 12. Security in this slice (added to THREAT_MODEL)
- Email: TLS only, checked certificate; sealed password; private-address guard; hourly cap; header injection refused (no line breaks in names or subjects).
- Password reset: no account enumeration (same answer and time), one-use hashed links, second step still required, sessions ended, rate limits, alert.
- Routing: step cap and loop refusal; Asterisk read-only except adding call records to one table.
- Voicemail: shared folder only between Asterisk and the control plane; files checked before import; per-person access (a group's box: its members and admins); downloads need a signed-in session; emailed audio only to the box owner's own address.
- Call history: people see only their own calls; caller names from outside are already cleaned (Phase 1D) and shown as text only.
- Undo: audited; restoring outgoing permissions needs confirm-it's-you.

## 13. Resources (measured as each step lands, `docs/RESOURCES.md`)
Expected: no new container or loop; certd grows by the DNS modules (measured, step 5); Asterisk image grows by three small modules; the database grows by voicemail (~0.5 MB per message minute, with retention) and call history (~0.5 KB per call). The web app's new tabs are lazy-loaded like the admin pages.
