# Web-first install — low-fidelity screen specs (install step 1)

*Approved by the owner 2026-09-28, with §8's three decisions as recommended. Since then (`docs/INSTALL.md` §14): links last four hours, not one; the first page is HTTPS on 6464 with a self-signed certificate and offers the DNS token first; Sign-in comes after Install. §3.2, §3.4 and §3.5 built in install step 4 part 2 (`web/src/screens/InstallFinish.tsx`); the DNS company is shown as a fact (the domain decides it), not a choice.*

Layout only: no colour, type or icon decisions (those come from `linx-tokens.json` / `DESIGN_TOKENS.md` when the screens are built in install steps 2–5). This is for the owner to sign off on **what's on each screen and where** before any code is written. The design these screens follow is `docs/INSTALL.md` (ADR-057, ADR-058).

Boxes are wireframes (element placement), not pixel layouts. Addresses and names are examples: `203.0.113.5` (a rented server's public address), `192.168.1.212` (a home server), `example.com`.

## 0. Rules every install screen follows

- **One card in the middle of the page**, no sidebar, logo on top, the same frame as the 1C sign-in page. At phone width the card fills the screen (16px sides). No page ever scrolls sideways.
- **One progress line across the whole install**, on both the plain and the secure page, so moving to `https://` feels like the next step, not a new site:
  ```
  ● Server ─ ● Domain ─ ◉ You ─ ○ Certificate ─ ○ DNS ─ ○ Sign-in ─ ○ Install
  ```
  At phone width it becomes "Step 3 of 7 · You". Finished steps can be clicked to go back while still on the plain page; once on the secure page, the first four are fixed (changing them later is §7's page).
- **Plain-page strip.** Every page on port 6464 has a slim strip at the top of the card: "🔓 This page isn't encrypted yet. Nothing secret is asked here." The only exception is §2.6 (token fallback), which replaces it with its own warning box. Secure pages have no strip.
- **One question per step**, pre-filled with our pick marked "(Recommended)" and one sentence of why under it; **Back / Next** at the bottom. Detected values (addresses, hardware) are shown as facts ("This server's public address is 203.0.113.5"), not asked.
- **Checks in plain words, next to the field**, blocking Next ("That isn't a domain. It should look like example.com.").
- **Nothing is changed on the server until you press a button that says so** (Check, Install). Until then, closing the tab loses nothing; opening the same browser again returns to the same step (the claimed session lasts the link's hour).
- **Words**: "front door" is shown as "What's in front of this server"; "certificate" is kept (people see it in their browser) but always with a short explanation the first time; no ACME, TLS-ALPN, SNI, DNS-01, staging in the page text — only under "Details" disclosures.

## 1. The terminal

Everything the terminal shows. One question (owner addition, `docs/INSTALL.md` §11): first, *How do you want to finish setting up Linx? 1) browser (recommended) 2) terminal*; Enter picks the browser, and the terminal choice asks the questions setup always asked (plus the time zone). Below is the browser path.

```
$ sudo linx setup

Checking this server
  ✓ Ubuntu 24.04, 4 processor cores, 8 GB memory, 62 GB free
  ✓ Ports 443 and 6464 are free
Installing Docker 28.4 ................................ done
Downloading Linx 0.9.0 ................................ done
Starting the installer ................................ done

Open this link in a browser to finish setting up Linx:

  http://203.0.113.5:6464/install/k7Qm3v…(43 characters)

It works once, for one hour, in the first browser that opens it.
If it doesn't open, allow TCP port 6464 in your server provider's firewall
(this server's own firewall is already open for it).

Waiting for you in the browser. You can close this window; setup carries on.
Run  sudo linx setup  again to see where it's up to.

  ✓ Link opened (Chrome, 5.36.12.4)                         12:04
  ✓ Domain: example.com, front door: Linx takes port 443     12:05
  … Waiting for meet.example.com to point at 203.0.113.5
```

- **Which address.** On a rented server, the public address; at home, the LAN address (`http://192.168.1.212:6464/…`) with "Open it on a computer on the same network". Where the server has both a public address and a home network, both lines are printed.
- **Stops and asks for nothing, except:** hardware or disk below the minimum (stops with the reason, as today); a distro Docker with running containers (stops: "Re-run with --replace-docker. This restarts those containers."); port 443 or 6464 already taken by something else (names the program, stops).
- **After the hour:** the waiting line turns into "The link expired. Run sudo linx setup again for a new one." A new link cancels the old one.
- **Re-run while installing:** shows the progress lines so far and the same link if it's still unclaimed; if it's claimed: "Being set up in another browser (opened 12:04). To start again there: sudo linx setup --new-link" — prints a new link and cancels the old session.
- **Re-run after the install:** §7.

## 2. The plain page (port 6464)

### 2.1 Opening the link

The secret is swapped for a cookie and removed from the address bar at once (the address becomes `http://203.0.113.5:6464/install`).

```
┌──────────────────────────────────────────┐
│               [logo]                     │
│ 🔓 This page isn't encrypted yet.        │
│    Nothing secret is asked here.         │
│                                          │
│  Let's set up Linx                       │
│  About ten minutes. You'll need:         │
│   • a domain you own (like example.com)  │
│   • access to its DNS settings           │
│   • your phone, for a passkey or an      │
│     authenticator app                    │
│                                          │
│                     [ Start ]            │
└──────────────────────────────────────────┘
```

**Link can't be used** (one page for every case: wrong, used, expired, cancelled — so a guess learns nothing):

```
│  This link can't be used                 │
│  Setup links work once, for one hour.    │
│  For a new one, run on the server:       │
│    sudo linx setup                  [⧉]  │
```

**Already open in another browser** (the claimed cookie is missing): same page. The terminal says which browser holds it (§1).

### 2.2 Where is this server?

```
  Where is this server?

  ┌───────────────────────┐ ┌───────────────────────┐
  │ Rented server (VPS)   │ │ At home or at the     │
  │ In a data centre,     │ │ office                │
  │ with its own public   │ │ Behind your router,   │
  │ address.              │ │ next to your desk     │
  │                       │ │ phones.               │
  └───────────────────────┘ └───────────────────────┘

  We found: public address 203.0.113.5, no home network.
```

- **Pre-selected from what setup detected** (a private default-route network → home; a public address on the server itself → rented), marked "(What we found)" rather than "(Recommended)" since it's a fact, not advice. Picking the other one is allowed, with a one-line note ("We didn't find a home network on this server. Desk phones won't be able to reach it.").
- This answer also pre-fills the setup wizard's Place step later (Home / Business stays a separate question: a business can rent a server).

### 2.3 What's in front of this server?

The front-door choices of today's `linx setup`, filtered by §2.2. One card per choice; the one we recommend first.

**Rented server** — one card and a link:

```
  How do people reach this server from the internet?

  (•) Directly — Linx answers on port 443 itself (Recommended)
      Your server provider sends port 443 here. Nothing else on
      this server uses it.
  ▸ Something else already uses port 443 here
```

"Something else…" opens the nginx/HAProxy and Caddy/Nginx Proxy Manager cards from the home list, with their address field.

**At home or at the office:**

```
  What's in front of Linx on the internet?

  ( ) Pangolin                                    (Recommended
      Your router sends port 443 to Pangolin.      if you use it)
  ( ) nginx or HAProxy, already using port 443
  ( ) Caddy or Nginx Proxy Manager
      Needs your DNS company's token on this unencrypted page (§2.6).
  ( ) Nothing — Linx takes port 443 itself
      Your router sends port 443 straight to this server.
  ( ) Nothing — only at home
      Linx works on this network only. No calls from outside.
      Needs your DNS company's token on this unencrypted page (§2.6).

  ┌ For Pangolin / nginx / Caddy ────────────────────────────┐
  │ Address of the machine it runs on                        │
  │ [ 192.168.1.20        ]  (192.168.1.212 if it's this one) │
  │ ▸ Call audio port: 443 (change to 3478 for UniFi routers) │
  └──────────────────────────────────────────────────────────┘
```

- No choice is pre-selected at home: it depends on what they already have, and setup doesn't scan the network. Pangolin carries "(Recommended if you use it)" (ADR-040).
- Today's `none` choice ("not now: no web address") isn't offered: a web-first install needs a web address. Its note under the list: "Want no web address at all? Use sudo linx setup --config instead."
- The proxy-address and UDP-port questions are today's terminal questions, same checks, same words.

### 2.4 Your domain

```
  What's your domain?

  [ example.com                ]

  Linx will use these names under it:
    meet.example.com   the web app and calls from outside
    api.example.com    for other apps
    turn.example.com   call audio through firewalls
    sip.example.com    desk phones at home       ← home only
```

- A domain you own; subdomains allowed (`pbx.example.com` → `meet.pbx.example.com`). Checks: syntax, not an address, not a public-suffix-only name ("co.uk is shared by everyone. Use your own domain.").
- One sentence: "You need access to this domain's DNS settings, where you'll add one record in a minute."

### 2.5 You

```
  Who's setting this up?

  Your name    [ Mohammed AlMudharreb ]
  Your email   [ mohammed@example.com ]

  You'll be the first admin, with full control.
  Let's Encrypt also uses this email for certificate notices.
  ☐ I agree to Let's Encrypt's Subscriber Agreement ↗

                         [ Back ]  [ Check and get a certificate ]
```

- **Your time zone** (owner addition, `docs/INSTALL.md` §11): a drop-down between email and the note, pre-set to the browser's zone, "For schedules, like backups at 03:00 your time." plus "This server's own clock is set to Etc/UTC; Linx doesn't change it." when they differ.
- **Every plain page** also has a countdown under the card: "This link closes in 47:12. Need more time? Run sudo linx setup --new-link on the server for a new link. Your answers so far are kept." (a warning for the last 5 minutes).
- The Let's Encrypt agreement tick is required (today's terminal setup accepts it silently; the web page asks, as Let's Encrypt expects).
- **Check and get a certificate** is the first button that changes anything: the host validates the answers (the same code as `setup.yaml`) and starts §2.7. A refusal comes back to the step it belongs to, in plain words.

### 2.6 Token fallback (home only, Caddy / Nginx Proxy Manager)

Only for the two choices where Let's Encrypt can't reach Linx on port 443 (`docs/INSTALL.md` §4.3). Comes after §2.5, instead of §2.7.

```
  ┌ ⚠ This page isn't encrypted ─────────────────────────────┐
  │ With your choice, Linx can't get its certificate through │
  │ port 443, so it needs your DNS company's token here,     │
  │ before there's a secure page.                            │
  │ Anyone on the network between you and this server could  │
  │ see the token. Continue only on a network you trust, or  │
  │ go back and choose a front door that passes 443 through  │
  │ (Pangolin, nginx, or Linx takes 443).                    │
  │ ☐ I understand, this network is one I trust               │
  └──────────────────────────────────────────────────────────┘

  DNS company   (•) Cloudflare   ( ) DuckDNS
  Token         [ ••••••••••••••••••••••        ] [show]
                ▸ How to make a Cloudflare token (Zone → DNS → Edit,
                  only example.com)

                     [ Back ]  [ Get the certificate ]
```

- Fields disabled until the box is ticked. The token field is a password field (not autocompleted, not remembered by the browser).
- Then the waiting page (§2.7) without the DNS-record part (Linx creates the records itself with the token) and without the port-443 part.
- The token isn't asked again on the secure page (§3.2 shows "Already added" with **Replace**).

### 2.7 Waiting for the certificate

*As built (install step 3): row 2 shows two records, the domain itself and `turn.`, each with its own status line (`docs/INSTALL.md` §13; the web address is the domain itself since ADR-059, so `meet.example.com` below reads `example.com`).*

One page, a checklist that ticks itself (checked every 5 s). What's shown depends on the front door.

```
  Getting a certificate for meet.example.com
  A certificate is what makes the padlock appear in your browser.

  ✓ 1  Set up Pangolin                              (Pangolin only)
       ▸ Show the block to paste into Pangolin  [⧉ Copy]
         ☐ I've added it
  ◌ 2  Add this record at your DNS company
       ┌───────────────────────────────────────────────┐
       │ Type  A                                       │
       │ Name  meet.example.com                  [⧉]   │
       │ Value 203.0.113.5                       [⧉]   │
       │ Proxy off (grey cloud, on Cloudflare)         │
       └───────────────────────────────────────────────┘
       Still points at 198.51.100.7. Changes can take a few
       minutes to show.                          checked 12:07:15
  ○ 3  Let's Encrypt reaches this server on port 443
  ○ 4  Certificate ready

  ▸ Details
```

- **Row 1** appears only for Pangolin and nginx: the generated block (today's `/etc/linx/front-door/…` files) with Copy, and "I've added it". The router steps (TCP/UDP 443 forward) are a second disclosure. For "Linx takes 443" at home: "Your router sends TCP and UDP port 443 to 192.168.1.212" with ☐ Done.
- **Row 2** states what DNS shows right now, in plain words: "Not found yet", "Still points at 198.51.100.7", "Points here ✓". On a home install the value is the home's public address (what `certs/records.go` finds), with a note: "If your home address changes, Linx will keep this record up to date once you've added your token on the next page."
- **Row 3** starts by itself once row 2 is ✓ (and row 1 is ticked). It's the test certificate (`docs/INSTALL.md` §4.2); the page doesn't say so outside Details. Failure, in words tied to the front door:
  ```
  ✕ 3  Let's Encrypt couldn't reach this server on port 443
       It got: connection timed out.
       Check that your router sends TCP port 443 to 192.168.1.212
       (or to Pangolin, 192.168.1.20).          [ Try again ]
  ```
  **Try again** only asks again while it can't use up Let's Encrypt's real limit (it's still the test certificate); the page never loops by itself on failure.
- **Row 4** (the real certificate). Then: "Moving you to https://meet.example.com…" and the redirect (§3.1). If the browser can't open it (a home DNS server that hasn't caught up), the page stays with: "Your browser can't open https://meet.example.com yet. [ Open it ] — or wait a minute and try again."
- **Details**: the Let's Encrypt service used (test, then real), the exact error, and "Check it yourself: `dig meet.example.com`".

## 3. The secure page (`https://meet.example.com`)

### 3.1 Arriving

`https://meet.example.com/install/continue#…` — the one-time, 2-minute handoff (`docs/INSTALL.md` §5.1). It swaps itself for a cookie at once; the address bar shows `/install`.

```
  🔒 You're on the secure page now
  From here on, everything you type is encrypted.
                                   [ Continue ]
```

**Handoff used or expired** (opened twice, or after 2 minutes): "This link can't be used. Go back to the first page and press Open it again." The plain page keeps a **Open the secure page** button until the handoff is used, and makes a fresh handoff each time it's pressed.

### 3.2 DNS company token

```
  Let Linx look after your DNS

  With a token from your DNS company, Linx:
   • keeps meet, api and turn pointing at this server,
     even when the address changes
   • adds sip.example.com for your desk phones  ← home only
   • gets a certificate that covers every name

  DNS company   (•) Cloudflare   ( ) DuckDNS
  Token         [ ••••••••••••••••••••••        ] [show]
                ▸ How to make a Cloudflare token

  [ Skip ]                                   [ Check and save ]
```

- **Check and save**: the host tries the token read-only first ("That token can't change example.com. It needs Zone → DNS → Edit on that zone."), then saves it as the `linx_dns_token` secret. The wildcard certificate and the records happen in the background and show on §3.5's progress list.
- **Skip** only on a rented server (§10 item 1), with its note: "The certificate still renews by itself. You'll add and change DNS records yourself." At home, Skip isn't shown; the page says why under the button row: "At home your desk phones need sip.example.com, which only Linx can keep up to date."
- **Already added** (after §2.6): shows "Cloudflare token added ✓" with **Replace**, and Next.

### 3.3 Sign-in

The existing first-admin page (`ADMIN_SCREENS_PHASE1E.md` §3.1) inside the install frame: name and email read-only from §2.5, then "How do you want to sign in?" (passkey recommended / password + authenticator / password only with its warning), recovery codes, "Also add a password?". Nothing new here.

- Once this finishes, the first admin exists and port 6464 closes (§4). The progress line shows "Sign-in ✓" and a small note: "The first page (port 6464) is now closed for good."
- If the browser is closed after this point, `https://meet.example.com` asks to sign in and then continues the install where it was.

### 3.4 Extras

```
  A few extras (you can change these later)

  Size of this server       (•) Standard (Recommended)
                                4 cores and 8 GB: all features,
                                medium-sized meetings
                            ( ) Lite   ( ) Performance
  Portainer                 [off]  A web page to look at Docker
                                   containers, on your home network only.
                                   ← home only

                                              [ Install ]
```

- Size: today's three profiles with today's descriptions; our pick from the hardware, with its reason.
- Portainer: off by default, home only (`docs/INSTALL.md` §10 item 4); hidden on a rented server.

### 3.5 Installing

```
  Installing Linx

  ✓ Saved your settings
  ✓ Firewall
  ◌ Phone system (Asterisk)                 about a minute
  ○ Call audio through firewalls (coturn)
  ○ Phone ports on 192.168.1.212            ← home only
  ○ Helpers: backups, status, firewall sync
  ○ Certificate for every name (*.example.com)
  ○ DNS: meet, api, turn → 203.0.113.5; sip → 192.168.1.212
  ○ Health check

  You can close this page. The install carries on; sign in
  at https://meet.example.com to see it.
```

- Rows are the host plan's own steps, reported through the bridge; each ✓ appears as it finishes.
- **A step fails**: that row turns ✕ with its plain-words reason and what to do; rows after it stay ○. Buttons **Try again** (re-runs from that step) and **Details** (the last log lines). Nothing that finished is undone.
- The certificate and DNS rows can still be running when the rest is done; the page then offers **Continue to setup** and keeps ticking them in the background (the admin home's System card shows them if they fail).
- **Done** → the setup wizard (`ADMIN_SCREENS_PHASE1E.md` §3.2): "How do you want to start?" (Set up fresh / Restore from a backup), then Place… The Place step starts from §2.2's answer (rented server → no pre-pick between Home and Business; home → Home pre-picked, still one click to change).

## 4. When port 6464 closes

No screen of its own. After §3.3, a plain page left open on 6464 shows on its next check: "This page is closed. Continue at https://meet.example.com." Any other visit to port 6464 gets no answer (firewalled).

## 5. Running setup again (`docs/INSTALL.md` §7)

### 5.1 Terminal

```
$ sudo linx setup

Linx is installed at https://meet.example.com (working ✓).
To change its domain, front door or extras, sign in as a system admin at:

  https://meet.example.com/install

Nothing was reopened.
```

If `https://meet.example.com` doesn't answer with this server's certificate:

```
Linx is installed, but https://meet.example.com isn't working:
  the certificate expired on 2027-01-03.

Open this link to fix it (works once, for one hour):

  http://203.0.113.5:6464/install/Hq2…

You'll need to sign in as a system admin there.
```

### 5.2 Host settings (secure, system admin)

*As built (install step 4 parts 4a/4b): System → Server settings at `/admin/system/server` (system admins only). Size, Portainer and the DNS token change in place with one Apply; front door and domain have **Change** (inline) with a **Check** right next to each field (the DNS token too; off until the field changes), which shows "Before you apply" under that row (warnings, DNS records to add, the front door's block with "I've done these steps", the steps) before Apply; "where" is shown only (it follows the server's network). Screenshots `system-server-closed`, `system-server-settings`, `system-server-changing`, `system-server-move`, `system-server-move-records`.*

`https://meet.example.com/install`, full page, same frame, no progress line. Needs a system admin sign-in (and "Confirm it's you" before saving):

```
  Server settings

  Where            Rented server                    [ Change ]
  In front         Linx takes port 443 itself       [ Change ]
  Domain           example.com                      [ Change ]
  DNS company      Cloudflare ✓                     [ Replace token ]
  Size             Standard                         [ Change ]
  Portainer        Off                              [ Change ]   ← home only

  Changing any of these restarts Linx for about a minute.
  Calls in progress will drop.
```

- Each **Change** opens the matching install step (§2.2–§2.4, §3.2, §3.4) as a sheet, then a confirm dialog naming the effect ("Move to pbx.example.org? Passkeys stop working and desk phones need the new sip. address.") and the §3.5 progress list.
- A domain change reuses §2.7's certificate checklist, on the secure page.
- Not shown to admins or reporters; the System page gets a link here for system admins ("Server settings").

### 5.3 Broken address (plain page again)

*As built (install step 4 part 4b): `https://<address>:6464/repair/<secret>` → `/repair`, served by the running Linx. The sign-in card (password, then authenticator or recovery code; no passkey or company buttons) with setup's check result, the link's countdown and the passkey-only hint under it; then "Fix this server's address" with the §5.2 panel. With `--no-sign-in`, straight to the panel. Every page a one-time link opens (install and repair) says under the card that the link works only once and now belongs to this browser (owner request, 2026-09-28). Screenshots `repair-sign-in`, `repair-settings`, `repair-no-sign-in`, `phone-repair`.*

The §2 frame, but §2.1's welcome is replaced by the system-admin sign-in (email, password or passkey, second step) before anything else. A passkey can't be used on port 6464 (passkeys belong to `meet.example.com`); the sign-in says so: "Passkeys only work on the secure page. Use your password and authenticator app, or a recovery code." Then the progress line starts at the step that's broken (usually Certificate).

- A system admin with **passkey only** can't sign in here. The page says: "Passkey-only? Run this on the server instead: sudo linx setup --new-link --no-sign-in". That link skips the sign-in; it needs root on the server, and the threat model records it. *(Owner decision, §8 item 1.)*

## 6. "Moved to a new place?" (admin home, after a restore)

*As built (install step 5): `components/MovedChecklist.tsx`; rows linking to pages not built yet (Phone lines, Settings) have no button. Screenshots `admin-home-moved`, `sign-in-moved`.*

A card at the top of Admin home (above "Getting started"), shown to admins and system admins only when the backup's place differs from this server's.

```
┌ Moved to a new place? ──────────────────────────────── 2 of 6 done ┐
│ This server was restored from a backup made somewhere else:        │
│   before: home 192.168.1.0/24, pbx.old.com                         │
│   now:    rented server 203.0.113.5, example.com                   │
│                                                                    │
│ ☐ Turn the old server off                                          │
│   Both servers would point the same names at themselves.           │
│ ☐ Phone line "UCM" is tied to the old network    [ Phone lines → ] │
│ ☐ Tell Telnyx your new address: 203.0.113.5      [ Phone lines → ] │
│ ☑ Desk phones: 3 were set up on the old network  [ Extensions → ]  │
│ ☐ Admins only from 192.168.1.0/24 (old network)  [ Settings → ]    │
│ ☑ New domain: add new passkeys                   [ My account → ]  │
│ ☐ Add your backup places again                   [ Backups → ]     │
│                                                  [ Hide for now ]  │
└────────────────────────────────────────────────────────────────────┘
```

- Only rows that apply are listed; each has one sentence of why and a link to its page. Ticks are by hand (saved on the server, shared by every admin), except "Add your backup places again", which ticks itself once one exists.
- "Turn the old server off" is always first and always shown.
- **Hide for now** collapses it to one line ("Moved to a new place? 4 left") for 7 days; the card goes away for good when every row is ticked.
- A different domain also puts a line on the sign-in page after a restore: "Passkeys from the old address don't work here. Sign in with your password and authenticator."

## 7. Screenshots to capture when built (`make screens`)

`install-terminal` (text), `install-claim`, `install-link-unusable`, `install-where`, `install-front-door-{rented,home}`, `install-domain`, `install-you`, `install-token-fallback`, `install-waiting-{dns,pangolin,failed,ready}`, `install-secure-arrive`, `install-dns-token`, `install-extras`, `install-progress{,-failed}`, `install-server-settings`, `admin-home-moved` — light and dark, desktop and phone width (the phone sweep includes every one).

## 8. Owner decisions (approved 2026-09-28, all as recommended)

1. **Passkey-only system admin with a broken secure address (§5.3).** Passkeys can't work on port 6464, so that admin can't sign in to fix it. *Recommend* `sudo linx setup --new-link --no-sign-in`: root on the server already owns everything, so skipping the sign-in adds no real risk, and it's the only way back.
2. **Let's Encrypt agreement tick (§2.5).** *Recommend asking*, as above: it's their rule, and today's terminal skips it.
3. **Rented server's front-door list (§2.3).** *Recommend* showing "Linx takes 443" alone, with the proxies behind "Something else already uses port 443 here", since on a VPS that's almost always the answer.
