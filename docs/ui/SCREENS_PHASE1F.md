# Phase 1F — low-fidelity screen specs (step 1)

*Drafted 2026-09-30; **approved by the owner 2026-09-30**, §16 items 2–6 as recommended. Item 1 changed: ring groups and office hours get **their own sidebar rows for now** (not tabs inside Incoming); the owner may revisit where they live later.*

Layout only: no colour, type or icon decisions (those come from `linx-tokens.json` / `DESIGN_TOKENS.md` when each screen is built). This is for the owner to sign off on **what's on each screen and where** before any code is written. The design these screens follow is `docs/PHASE1F.md` (ADR-066 to 071) and `docs/SIMPLER.md` §2–3 and §2.5 (ADR-062 to 064).

Boxes are wireframes (element placement), not pixel layouts. Examples: `example.com`, a home server at `192.168.1.212`, a proxy at `192.168.1.20`, a rented server at `203.0.113.5`, the number `04 200 0100`.

Part A (§1–4) is getting in from outside; Part B (§5–14) is email and calls. §15 lists the screenshots, §16 the questions for the owner.

## 0. Rules

Everything in `ADMIN_SCREENS_PHASE1E.md` §0 (Guide me / Quick add, wizard sheets, show once, greyed with a reason, confirm it's you, plain words, phone width) and `INSTALL_SCREENS.md` §0 (install page frame) still applies. New in 1F:

- **One "where does the call go" picker**, the same drop-down everywhere a call is sent somewhere (Incoming, ring groups, "if nobody answers", after hours, "Calls for any other number"):
  ```
  [ Sales (ring group)                    ▾ ]
  ┌──────────────────────────────────────────┐
  │ 🔍 Search                                │
  │ PEOPLE                                   │
  │   100 Mohammed                           │
  │   101 Sara Haddad                        │
  │ RING GROUPS                              │
  │   600 Sales · 3 people, all at once      │
  │   + New ring group…                      │
  │ VOICEMAIL                                │
  │   Voicemail for Sales                    │
  │   Voicemail for Sara Haddad              │
  │ OTHER                                    │
  │   Play "We're closed" and hang up        │
  │   Play a message and hang up…            │
  │   Nobody (callers hear "not available")  │
  │ ░ Menus and queues: coming later         │
  └──────────────────────────────────────────┘
  ```
  "+ New ring group…" opens ring-group quick add (§8.2) on top and comes back with it chosen. A choice that would make a loop is shown greyed with the reason ("Sales already sends its unanswered calls here"). Menus and queues (Phase 4) appear greyed so the list's shape never changes.
- **A sentence under every routing choice**, rebuilt as you pick, saying in plain words what a caller will get (§10). The same sentence appears on the list, in the simulator and in Routing changes, so there is one way of describing a call's path.
- **"Saved. Undo"** after every routing change (§14): a toast that stays 10 seconds; Undo puts the previous version back at once. Needs routing rights, like the change itself.
- **Email-dependent things are greyed, not hidden**, until email is set up: "Send by email" buttons, voicemail by email, the Email alert channel. Reason: "Set up email first (System → Settings)." with a link. The one exception is **"Forgot your password?"** on the sign-in page, which is hidden (nobody signed in can act on the reason).
- **Times** are the server's time zone (from setup), shown on every schedule: "Times are Dubai time (UTC+4)." When the browser's zone differs, lists show both on hover.
- **New everyday tabs are lazy-loaded** like the admin pages: people who never open Voicemail or Call history don't download them.

---

# Part A — getting in from outside

## 1. The front-door card (ADR-062)

One card, the same in three places: the install page's "What's in front of this server" step (replacing `INSTALL_SCREENS.md` §2.3's list), System → Server settings → **In front → Change**, and the help guide. Choosing a product only changes the instructions under it; Linx's side is the same for every proxy.

### 1.1 Step one: what's in front

```
  What's in front of Linx on the internet?

  (•) Nothing else uses port 443 — Linx takes it     (Recommended on a
      Your router or server provider sends port 443   rented server)
      straight to this server.
  ( ) Another program passes Linx through
      Pangolin, nginx, HAProxy, Caddy, Nginx Proxy Manager or
      similar already uses port 443 and sends Linx's names here.
  ( ) Nothing: only at home
      Linx works on this network only. No calls from outside.

  ▸ Advanced
      ( ) Nothing here can pass Linx through on 443:
          use another public port (§4)
      ( ) My proxy must unlock the traffic itself (§1.4)
```

- Rented server: the first choice is pre-picked and the others sit behind "Something else already uses port 443 here" (as approved for the install page). At home nothing is pre-picked.
- "Another program" adds one field: **Address of the machine it runs on** `[ 192.168.1.20 ]` ("192.168.1.212 if it's this one").

### 1.2 The card: three facts

Shown for "Another program passes Linx through". For "Linx takes 443" the card is one line ("Nothing to set up here. Your router sends TCP 443 and UDP 443 to 192.168.1.212." with the router rule, Copy) and **Check it**.

```
┌ Your front door needs to do three things ────────────────────────────┐
│                                                                      │
│ 1  Pass these names through without unlocking them                   │
│      example.com            [ Copy ]                                 │
│      turn.example.com       [ Copy ]                                 │
│    Linx's own certificate has to reach the browser.                  │
│                                                                      │
│ 2  Send them to this server                                          │
│      example.com       →  192.168.1.212 port 8443    [ Copy ]        │
│      turn.example.com  →  192.168.1.212 port 5349    [ Copy ]        │
│                                                                      │
│ 3  Tell Linx who's visiting                                          │
│      example.com: turn on "PROXY protocol, version 2"                │
│      turn.example.com: leave it off                                  │
│    Linx only accepts it from 192.168.1.20, your front door.          │
│                                                                      │
│ How to do this in                                                    │
│ [ Pangolin ][ nginx ][ HAProxy ][ Caddy ][ Nginx Proxy Manager ]     │
│ [ A router or firewall ]                                             │
│ ┌──────────────────────────────────────────────────────────────────┐ │
│ │ Pangolin                                                         │ │
│ │ 1. Open Pangolin → Resources → Add resource → Raw TCP …          │ │
│ │ 2. …                                                             │ │
│ │ ▸ Or paste this into Pangolin's dynamic_config.yml     [ Copy ]  │ │
│ └──────────────────────────────────────────────────────────────────┘ │
│                                                                      │
│ [ ] I've done these steps                          [ Check it ]      │
└──────────────────────────────────────────────────────────────────────┘
```

- **How to do this in…**: a row of tabs (a drop-down at phone width). Each tab: numbered steps with the facts filled in, and where a config file is the only way, the exact block with **Copy**, named after the domain (so several Linx servers never clash). Pangolin shows its web-page steps if the owner's 1.23 EE allows it (checked in step 2), otherwise the config file.
- **Nginx Proxy Manager** says first: "Nginx Proxy Manager can only pass traffic through by port, not by name, so this works only if nothing else needs port 443." Same for "A router or firewall" (it's the "Linx takes 443" rule in the router, shown there too).
- **Caddy** needs its layer-4 add-on; the tab says so first with the link.
- On the install page, "I've done these steps" must be ticked before **Next**; in Server settings it's part of "Before you apply" (as built).

### 1.3 Only at home

One line: "People can use Linx only on this network. Calls from outside, meetings with guests and phones on mobile data won't work." plus the local address people type. Still needs a DNS token (unchanged).

### 1.4 Advanced: my proxy must unlock the traffic

Only at home, never on a rented server (decision 2026-09-30). A warning box first:

```
┌ ⚠ Not recommended ───────────────────────────────────────────────────┐
│ Your proxy will see everything that passes through it, and Linx      │
│ needs your DNS company's token on this page before it's encrypted.   │
│ Calls need their own port for audio. Caddy and Nginx Proxy Manager   │
│ can both pass Linx through instead: [ Show me how ]                  │
│                                                   [ Use it anyway ]  │
└──────────────────────────────────────────────────────────────────────┘
```

Then today's decrypting-proxy fields (proxy address, audio port) and the token fallback page, unchanged.

## 2. "Check it" (SIMPLER §2.3)

A button on the front-door card, on the install page's waiting step, in Server settings next to **In front**, and on System → Status (a new row "Reachable from outside" with **Check it**). One panel, two halves:

```
┌ Check it ────────────────────────────────────────────────────────────┐
│ From this server                                                     │
│  ✓ example.com points to 94.200.1.10 (your home's address)           │
│  ✓ turn.example.com points to 94.200.1.10                            │
│  ✓ example.com answers with Linx's certificate                       │
│  ✗ turn.example.com doesn't answer                                   │
│    Your front door isn't sending turn.example.com to port 5349.      │
│    Calls from outside will have no audio.   [ Show the steps ]       │
│  ⓘ Your router can't reach its own address from inside, so this     │
│    server can't test the rest. Use your phone below.                 │
│                                                                      │
│ From outside                                                         │
│  Open this on your phone with Wi-Fi turned off:                      │
│   ┌──────┐   https://example.com/reach/7K2Q                          │
│   │ QR   │   [ Copy ]                                                │
│   └──────┘   Waiting for your phone…  (works for 10 minutes)         │
│                                                                      │
│                                               [ Check again ]        │
└──────────────────────────────────────────────────────────────────────┘
```

- Each line is one of doctor's checks in plain words; a ✗ always has one sentence of what it means for people and a button to the fix.
- **"Waiting for your phone…"** turns into "✓ Your phone reached Linx from 5.194.33.12 (mobile network). Calls from outside work." as soon as the phone opens the link (an event, no polling). If the address Linx saw is the proxy's own ("192.168.1.20"), it says: "⚠ Linx saw your proxy's address, not your phone's. Step 3 (PROXY protocol) isn't on."
- **The phone's page** (`/reach/<code>`, no sign-in, the sign-in page's frame):
  ```
       [logo]
    ✓ You reached Linx at example.com
    Linx saw you coming from 5.194.33.12.
    You can close this page.
  ```
  Also tests the relay from the phone (a quick TURN connection) and adds "✓ Calls from here will have audio" or the failure line. The code works once, for 10 minutes, and says nothing else about the server.

## 3. DNS: any DNS company (ADR-063)

In the install page's "Your domain" step (after the domain), in Server settings (**DNS** row, replacing "DNS company Cloudflare ✓ [Replace token]"), and Check it links here when a record is wrong.

### 3.1 The records, by hand

```
┌ Records for example.com ─────────────────────────────────────────────┐
│ Add these at the company where example.com is registered.            │
│ Its name servers: ns1.porkbun.com, ns2.porkbun.com (so: Porkbun)     │
│                                                                      │
│  NAME                TYPE   VALUE             NOW                    │
│  example.com  (@)    A      94.200.1.10 [📋]  ✓ Right                │
│  turn.example.com    A      94.200.1.10 [📋]  ✗ Shows 94.200.1.9     │
│  sip.example.com     A      192.168.1.212[📋] … Not found yet        │
│                                                                      │
│  Changes can take a few minutes to show.         [ Check again ]     │
│                                                                      │
│  Your address changes from time to time? Let Linx keep these right:  │
│  [ Set it up automatically ]                                         │
└──────────────────────────────────────────────────────────────────────┘
```

- Values are checked at the domain's own name servers (so no waiting for caches). Each ✗ says what it shows instead.
- `sip.` is listed only at home ("desk phones and phone systems on your network use it").
- The DNS company is detected from the name servers and shown as a fact, never asked. If it's one of the ten, the "automatically" button names it: "Let Linx keep these right at Porkbun".
- The "@" note: "Some companies call this the root or apex, or want just @ in the name field."

### 3.2 Automatic

```
┌ Keep the records right automatically ────────────────────────────────┐
│ DNS company   [ Porkbun                      ▾ ]  (from name servers)│
│                                                                      │
│ API key       [                          ]                           │
│ Secret key    [                          ]                           │
│ Where to find them: Porkbun → Account → API Access → Create API key, │
│ then turn on "API access" for example.com.                           │
│                                                                      │
│ Linx can only change records under example.com, and it keeps the     │
│ key sealed. It never touches a record it didn't make.                │
│                                              [ Check the key ]       │
└──────────────────────────────────────────────────────────────────────┘
```

- The list: Cloudflare, DuckDNS, Route 53, GoDaddy, Namecheap, Porkbun, DigitalOcean, Hetzner, deSEC, OVH, then "Not in the list: do it by hand (§3.1)". Each shows only the fields it needs (Cloudflare: token; Route 53: key ID, secret, region; Namecheap also asks for this server's public address to be allowed there: said in its help line).
- **Check the key** is read-only (lists the zone, changes nothing): "✓ This key can change example.com's records." or the company's reason in plain words.
- Afterwards the DNS row reads: "Porkbun, kept right automatically · last change 3 days ago (94.200.1.9 → 94.200.1.10) · [ Replace key ] [ Stop ]". Stop keeps the records, just stops updating them.
- Certificates never need this key (they use port 443); the card says so only when the public port isn't 443 (§4), where the key is required.

## 4. Another public port (advanced, ADR-064)

Reached only from §1.1 "Advanced: use another public port", on the install page and in Server settings (**Public port 443 [ Change ]**, a new row). The same three pieces in both.

### 4.1 First, the warning (the owner's words, 2026-09-29)

```
┌ Port 443 is the recommended choice ──────────────────────────────────┐
│ Linx is designed and tuned to work best on port 443, the standard    │
│ port for secure websites. It's always the recommended choice: almost │
│ every network lets it through, so sign-in, calls and meetings work   │
│ wherever people are. Another port can work, but some networks block │
│ it, so some features may not work everywhere, and calls from those   │
│ places may fail or sound worse.                                      │
│                                                                      │
│ Before you choose this: a proxy on 443 (Pangolin, nginx, Caddy, Nginx│
│ Proxy Manager passing Linx through) or a small rented server as your │
│ front door keeps 443.   [ Show me the front-door options ]           │
│                                            [ Use another port ]      │
└──────────────────────────────────────────────────────────────────────┘
```

### 4.2 The ports and the router rules

```
  Public web port        [ 8443 ]   (1024–65535)
  Public port for call audio (UDP)
    (•) 443  (Recommended: often free even when TCP 443 isn't)
    ( ) 3478 (if your router can't forward UDP 443)
    ( ) Another: [      ]

  On your router, forward:
    TCP 8443  →  192.168.1.212 port 8443     [ Copy ]
    UDP 443   →  192.168.1.212 port 443      [ Copy ]

  People will open  https://example.com:8443

  ⚠ Browser calls from networks that only allow standard web traffic,
    like some hotels, workplaces, public Wi-Fi and mobile networks,
    may fail or have no audio, because this setup can't use port 443
    for them. Calls from normal home and mobile networks work.
  ⚠ Some company, school and public networks block any address with
    a port like :8443. There, even this page and signing in can fail.
```

- Refused ports say why: "5061 is used by Linx itself", "6666 is blocked by browsers".
- On a rented server the router lines become "Linx will open TCP 8443 and UDP 443 on this server's firewall." and there's nothing to forward.
- **A DNS token is required** here (certificates can't be checked on port 443): if none is set, the §3.2 form appears next, and Next/Apply stay off until the key checks out. "Without port 443, Let's Encrypt can only check your domain through your DNS company."
- Inside the network: "People at the office use the same address. If it doesn't open there, turn on NAT loopback (hairpin) on your router, or add example.com to your local DNS pointing at 192.168.1.212."

### 4.3 Server settings: "Before you apply" for a port change

The existing panel (as built for domain changes) gains, for a port change:
- "Passkeys keep working" (a ✓ line, since they belong to the name).
- **Company sign-in**: each provider with its new return address and Copy: "Google: add https://example.com:8443/api/v1/sso/callback in Google Cloud → Credentials. Until you do, Continue with Google shows 'redirect URI mismatch'."
- "Invite links already sent still work" (the old address redirects while 443 is still forwarded) or "stop working" when it isn't: whichever is true after Check.
- "I've done these steps" and Apply, as today.

---

# Part B — email and calls

## 5. Email (ADR-066)

### 5.1 System → Settings: the Email card

A new card on System → Settings, above Company sign-in. System admins change it; admins see it read-only with the reason.

```
┌ Email ───────────────────────────────────────────────────────────────┐
│ Linx sends invites, "Forgot your password?", voicemail and alerts    │
│ through a mail account you already have.                             │
│                                                                      │
│ Not set up.                                        [ Set up email ]  │
└──────────────────────────────────────────────────────────────────────┘

 once set up:
│ Sending as   Linx at Example Co <pbx@example.com>                   │
│ Through      Google Workspace (smtp.gmail.com, encrypted) [ Change ] │
│ Last sent    today 09:14 · 12 this hour (limit 60)                   │
│ Queue        ✓ Nothing waiting   or  ⚠ 3 waiting: "Login refused"    │
│              [ Send a test email ]  [ Turn off ]                     │
```

**Set up email** (guided sheet; quick add = preset + fields on one page):

1. **Who sends it?** Cards: Gmail or Google Workspace (Recommended if you use it) · Microsoft 365 · iCloud · Fastmail · Amazon SES · Postmark · Brevo · Mailgun · Something else. Microsoft 365 shows: "Microsoft is turning off password sign-in for sending mail. It may stop working; a sending service (Postmark, Brevo, Amazon SES) is safer."
2. **Sign in**: address to send from, password (**app password** for Google/Microsoft/iCloud, with "How to make an app password" steps and link), "From" name (pre-filled "Linx at <company name>"). "Something else" also asks server and port, with **Encryption**: "From the start (port 465)" / "After connecting, STARTTLS (587)". There is no "none".
3. **Test**: sends to your own address. Checklist: "✓ Connected to smtp.gmail.com, encrypted · ✓ Certificate checked · ✓ Signed in · ✓ Sent. Did it arrive?" [ Yes ] [ No, show me what to check ]. A mail server on your own network gets the private-address offer ("Allow 192.168.1.30 and continue", confirm it's you), as with alert channels.
4. **Done**: "Email is on. Linx will now offer to send invites by email." Confirm it's you before saving (it holds a password).

- **Hourly limit** under "▸ More": "At most [ 60 ] emails an hour" with why ("If an account here is taken over, Linx can't be used to send spam.").
- Admin home checklist gains **Set up email** (after "Add a second way to sign in"); open until a test email has arrived.

### 5.2 Where email appears once it's on

- **People → Add** (both ways) and **New invite link**: the show-once box gains **Send by email** (ticked by default: "Email this link to sara@example.com"), with Copy and QR as today. The person's row shows "Invite emailed 09:14" or "Email failed: [reason] · Copy the link instead".
- **Setup wizard's People step**: "Email everyone their invite" (one button, after the extensions are made).
- **System → Alerts → Add**: an **Email** card (to one or more addresses). The "Email isn't sending" alert is always sent through the other channels, never by email, and says so in its text.
- **My account**: nothing new except "Email me when I get a voicemail" (§12.3).

### 5.3 The emails themselves

Plain text and a simple HTML copy, Linx's logo only, no tracking, no remote images. Subjects:

| Email | Subject | Body (first lines) |
|---|---|---|
| Invite | Your Linx account at Example Co | "Mohammed added you to Linx, Example Co's phone system. Set up your account (the link works once, for 24 hours): …" |
| Password reset | Choose a new Linx password | "Someone (hopefully you) asked to reset your password. The link works once, for 30 minutes: … If this wasn't you, ignore this email: nothing changes." |
| Password changed | Your Linx password was changed | "Your password was changed at 10:42 from Chrome on Mac (5.194.33.12). If this wasn't you, tell your admin now." |
| Voicemail | Voicemail from 050 123 4567 (0:42) | "050 123 4567 called Sales at 10:42 and left a 42-second message. It's attached. Listen in Linx: …" |
| Alert | [Linx] Line "UCM" is down | the alert's sentence and a link |
| Test | Linx can send email | "This is a test from System → Settings. Everything works." |

## 6. "Forgot your password?" (ADR-067)

### 6.1 Sign-in page

A link under the Password field, **only when email is set up**: `Forgot your password?`. Also on the "wrong password" message: "Wrong email or password. Forgot your password?"

### 6.2 Asking for the link

The sign-in frame:
```
       [logo]
  Forgot your password?
  Type your email. If it belongs to an account here,
  we'll email you a link to choose a new one.

  Email  [ sara@example.com        ]
         [ Send the link ]
  ← Back to sign-in
```
Then, always the same (whether or not the account exists, same wait):
```
  Check your email
  If sara@example.com is an account here, an email is on its way.
  The link works once, for 30 minutes.
  Nothing arrived? Check spam, or ask your admin.
  ← Back to sign-in
```
Too many requests: the same page (nothing new said). "Company sign-in only" accounts get no link and no different answer.

### 6.3 Choosing the new password

The emailed link opens:
```
       [logo]
  Choose a new password for sara@example.com
  New password     [                ]  (strength hint, 1C rules)
  Type it again    [                ]
                   [ Continue ]
```
Then **the second step, always** (the 1C code step or "Use my passkey"), with: "To finish, confirm with your authenticator app or passkey." Lost that too: "Ask your admin to reset it, or they can run sudo linx user reset-2fa on the server." Then:
```
  ✓ Your password is changed.
  You've been signed out everywhere else.   [ Continue to Linx ]
```
- A used or expired link: "This link has already been used or is older than 30 minutes. [ Send a new one ]".
- A person with only a passkey (no password yet) can use this to add one; same second step.

## 7. Admin shell: what's new in the sidebar

```
│ Dialer       │
│ Team         │
│ Call history │  ← now live (§13), badge: missed since last look
│ Voicemail ●3 │  ← now live (§12), badge: new messages
│ Meetings   ░ │
│ ───────────  │
│ ADMIN        │
│ Home         │
│ People       │
│ Extensions   │
│ Phone lines  │
│ Incoming     │  ← the numbers and their wizard (§10)
│ Ring groups  │  ← new (§8)
│ Office hours │  ← new (§9)
│ Outgoing     │
│ Simulator    │
│ Calls        │  ← new: everyone's call history (§13.2)
│ System       │  ← new tab: Routing changes (§14)
```

Ring groups and office hours have their own rows, right under Incoming (owner, 2026-09-30, §16 item 1: for now; where they live may be decided again later, so keep them easy to move: each is its own screen and route, `/admin/ring-groups` and `/admin/office-hours`).

## 8. Ring groups (ADR-068)

### 8.1 List (Admin → Ring groups)

```
┌──────────────────────────────────────────────────────────────┐
│  Ring groups        several phones ring for one call         │
│                                          [ + Add ]           │
│  NAME        NUMBER  RINGS                 IF NOBODY ANSWERS │
│  Sales       600     3 people, all at once  Voicemail (Sales)│
│  Support     601     4 people, in turn      Sales            │
│  Reception   —       2 people, all at once  Voicemail (Sara) │
└──────────────────────────────────────────────────────────────┘
```

Empty state: "A ring group rings several people for one call: all at once, or one after another. Use it for Sales, Support or Reception." + Guide me / Quick add.

### 8.2 Add a ring group

- **Guide me**: 1) Name ("Sales") → 2) Who's in it? (people list with ticks, search) → 3) How should it ring? **All at once** (Recommended: "whoever's free answers first") / **One after another** ("in this order, [ 15 ] seconds each"; the chosen people become a list with up/down) → 4) If nobody answers after [ 25 ] seconds → the destination picker (default "Voicemail for Sales": "A voicemail box for this group; everyone in it sees the messages.") → 5) Its own number? "Give it 600?" (next free in the groups range, Recommended: "people can dial it or transfer a call to it") / pick / "No number" → 6) Done: the sentence, and "Send a phone number here? [ Go to Numbers ]".
- **Quick add**: name, people (ticks). Defaults: all at once, 25 s, its own voicemail box, next free number.

### 8.3 Detail (sheet)

```
┌────────────────────────────────────────┐
│ Sales                              ✕   │
│ Number 600 · 3 people · all at once    │
│ "Calls ring Mohammed, Sara and Omar    │
│ together. If nobody answers in 25      │
│ seconds, the caller can leave a        │
│ voicemail for Sales."                  │
│────────────────────────────────────────│
│ People             [ Edit ]            │
│ How it rings       [ Edit ]            │
│ If nobody answers  [ Edit ]            │
│ Number             [ Edit ]            │
│ Voicemail box      [ Edit ]            │
│  On · 4 messages, 1 new · greeting:    │
│  Linx's own                            │
│ Used by                                │
│  04 200 0100 (office hours)            │
│  Support (if nobody answers)           │
│────────────────────────────────────────│
│ Danger zone                            │
│  [ Remove Sales ]                      │
└────────────────────────────────────────┘
```

- **Used by** lists every place that sends calls here. **Remove** while used: "Calls to 04 200 0100 and Support's unanswered calls go here. Choose where they go instead: [ picker ]" before it can be removed.
- A person who is in no ring group and has no phone connected shows ⚠ in People: "In Sales, but no phone or browser is set up."
- A group with nobody who can ring right now shows "Nobody can ring (all offline)" in its status; calls go straight to "if nobody answers".

## 9. Office hours and holidays (Admin → Office hours)

```
┌──────────────────────────────────────────────────────────────┐
│  Office hours                  Now: open (closes at 17:00)   │
│  Times are Dubai time (UTC+4).                               │
│                                                              │
│   Sun  [on ]  08:00 – 17:00   [ + ]                          │
│   Mon  [on ]  08:00 – 17:00                                  │
│   Tue  [on ]  08:00 – 17:00                                  │
│   Wed  [on ]  08:00 – 17:00                                  │
│   Thu  [on ]  08:00 – 17:00                                  │
│   Fri  [off]  Closed                                         │
│   Sat  [off]  Closed                                         │
│   [ Copy Sunday to every open day ]                          │
│                                                              │
│  Holidays                                    [ + Add ]       │
│   Eid al-Fitr        20 – 22 Mar 2027                        │
│   National Day       2 – 3 Dec 2026                          │
│   New Year's Day     1 Jan 2027                              │
│   Past holidays (3) ▸                                        │
│                                                              │
│  ▸ More schedules (e.g. Support hours)                       │
└──────────────────────────────────────────────────────────────┘
```

- Made at setup with the country's usual week (Sun–Thu in the UAE, Mon–Fri elsewhere), 08:00–17:00. Home setups get none and the page says: "At home there are no office hours: every number rings the same all the time. [ Add office hours ]".
- **+** on a day adds a second span (lunch break: 08:00–13:00, 14:00–17:00).
- **Add a holiday**: name, one date or a range, "Every year on the same date" (for fixed ones like National Day; Eid moves, so off by default with a note).
- **Now: open / closed** is the same database answer calls use.
- **More schedules**: collapsed by default; a second schedule has the same layout and appears in the wizard's picker only once it exists.

## 10. "When someone calls" (Admin → Incoming)

### 10.1 The Numbers list

```
┌──────────────────────────────────────────────────────────────┐
│  Incoming calls                                              │
│                                                              │
│  04 200 0100 · UCM                          [ Change ] [Try] │
│   Rings Sales (all at once) Sun–Thu 08:00–17:00. If nobody   │
│   answers in 25 s: voicemail for Sales. Other times and      │
│   holidays: "We're closed", then voicemail for Sales.        │
│                                                              │
│  04 200 0101 · Telnyx                       [ Change ] [Try] │
│   Rings 101 Sara Haddad, all the time. If nobody answers in  │
│   25 s: voicemail for Sara.                                  │
│                                                              │
│  04 200 0102 · Telnyx               ⚠       [ Change ] [Try] │
│   Rings nobody: callers hear the number isn't available.     │
│                                                              │
│  Calls for any other number (UCM)            [ Change ]      │
│   Rings 100 Mohammed.                                        │
└──────────────────────────────────────────────────────────────┘
```

Each number is a card with its sentence (the Phase 1E drop-down becomes the wizard). **Try** opens the simulator filled in (§11).

### 10.2 Change → the wizard (side sheet)

Steps: **1 Office hours · 2 No answer · 3 After hours · 4 Check**. The sentence at the bottom updates as each step changes.

```
┌ When someone calls 04 200 0100 ─────────────────────────── ✕ ┐
│  ● Office hours ─ ○ No answer ─ ○ After hours ─ ○ Check      │
│                                                              │
│  During office hours, ring                                   │
│  [ Sales (ring group)                        ▾ ]             │
│  Office hours: Sun–Thu 08:00–17:00  [ Change hours ]         │
│                                                              │
│  ( ) The same all the time (no office hours)                 │
│                                                              │
│ ──────────────────────────────────────────────────────────── │
│  Calls to 04 200 0100 ring Sales (all at once)               │
│  Sun–Thu 08:00–17:00. …                                      │
│                                          [ Back ] [ Next ]   │
└──────────────────────────────────────────────────────────────┘
```

2. **If nobody answers after [ 25 ] seconds** → picker (Recommended: voicemail of whoever step 1 rings). Greyed when step 1 is a ring group: "Sales decides this itself (voicemail for Sales). [ Change it in Sales ]".
3. **Outside office hours and on holidays** → (•) "Play 'We're closed', then voicemail" (Recommended, with a picker for whose box) · ( ) "The same as during office hours" · ( ) Another destination. **▸ Holidays differently** (collapsed) lets holidays have their own destination.
4. **Check**: the full sentence, then "Try it: what happens at 20:00 on Friday?" (a mini simulator, §11) and **Save** (Saved. Undo).

- **Quick change** stays: a small "Just ring one person" link at the top of the sheet gives the Phase 1E single picker.
- "Calls for any other number" uses the same wizard.
- The "we're closed" greeting is chosen in step 3: "Linx's own" (play button) or "Record your own" (§12.4, for this number's box).

## 11. Call simulator additions

"Someone calls in" gains **When** and follows every step:

```
  (•) Someone calls in   ( ) Someone here calls out
  Your number  [ 04 200 0100 ▾ ]
  When         ( ) Now   (•) [ Fri 2 Oct ] [ 20:00 ]   [ Check ]

  ┌──────────────────────────────────────────────────────────┐
  │ Friday 20:00 (Dubai time): outside office hours          │
  │ 1  Plays "We're closed" (Linx's own greeting)       ▶    │
  │ 2  Voicemail for Sales: greeting, then records up to     │
  │    3 minutes. Sara, Omar and Mohammed see it.            │
  │ This is exactly what a real call does.                   │
  └──────────────────────────────────────────────────────────┘
```
- In office hours with a ring group: "1 Rings Mohammed, Sara, Omar together (Sara: browser; Omar: offline, skipped) → 2 After 25 s nobody answered: …".
- On a holiday: "Eid al-Fitr (holiday): …". Loops can't be saved, but if the step cap is ever hit: "Stopped after 10 steps".
- Dialling a ring group's number from inside ("Someone here calls out", number 600) shows the same steps.

## 12. Voicemail (ADR-069)

### 12.1 The Voicemail tab (everyone)

```
┌──────────────────────────────────────────────────────────────┐
│  Voicemail                       [ Mine ▾ ]   [ Settings ]   │
│                                  (Mine / Sales / All boxes)  │
│  NEW                                                         │
│  ● 050 123 4567       for Sales     10:42    0:42            │
│    [ ▶ ─────────────── 0:00 ]  [ Call back ] [ ⋯ ]           │
│  ● +44 20 7946 0000   for me        yesterday 16:05   1:10   │
│                                                              │
│  HEARD                                                       │
│    Aisha (103)        for me        Mon 09:12        0:15    │
│                                                              │
│  Kept 60 days · this box uses 4 MB                           │
└──────────────────────────────────────────────────────────────┘
```

- Newest first, new ones on top with a dot; playing to the end marks it heard. **⋯**: Mark as new / heard, Download (WAV), Delete (asks). Box filter appears only for people in a group with a box; "All boxes" only for admins.
- **Call back** starts a call from the browser phone. Caller names from outside are shown as text only.
- A group's message heard by Sara shows "Heard by Sara, 10:50" to the others.
- Empty: "No voicemail. When someone leaves you a message, it appears here and (if you like) in your email."
- Phone width: each message is a card; the player takes the full width.

### 12.2 Voicemail badge

Sidebar badge = new messages across my boxes; the browser tab title gets "(3)"; a desktop notification if the person allowed them for calls.

### 12.3 Voicemail → Settings (sheet, for my own box)

```
  Voicemail              [ on ]  Callers who don't reach you
                                 can leave a message.
  Greeting
    (•) Linx's own  ▶  "The person you called isn't available…"
    ( ) My own      ▶  recorded 3 Sep   [ Record again ]
  Email me new messages  [ on ]  to sara@example.com (with the audio)
                                 ░ greyed if email isn't set up
```

### 12.4 Recording a greeting (in the browser)

```
┌ Record your greeting ──────────────────────────────────── ✕ ┐
│  Say something like: "Hi, this is Sara. Leave a message and │
│  I'll call you back."                                       │
│                                                             │
│      [ ● Record ]        0:00 / 0:30                        │
│  ▁▂▅▃▆▂▁  (level meter)                                     │
│                                                             │
│  after recording:  [ ▶ Play ]  [ Try again ]  [ Use this ]  │
└─────────────────────────────────────────────────────────────┘
```
- At most 30 seconds. Uses the microphone chosen in Settings (1C). "Use this" uploads; Linx converts it for phones; shows "Saved. Callers hear it from now on."
- The same dialog records a group's greeting (group detail, members and admins) and a number's "We're closed" greeting (the wizard's step 3).

### 12.5 Admin side

- **People → person detail** gains a **Voicemail** line: "On · 3 messages · email on" with [ Turn off ].
- **Ring group detail**: Voicemail box section (§8.3).
- **System → Settings**: "Keep voicemail [ 60 ] days (7–365)" and "Keep call history [ 1 year ▾ ]", with the space both use now ("Voicemail 120 MB · Call history 9 MB").

## 13. Call history (ADR-070)

### 13.1 The Call history tab (everyone, own calls)

```
┌──────────────────────────────────────────────────────────────┐
│  Call history          [ All ] [ Missed ] [ Search number ]  │
│                                                              │
│  TODAY                                                       │
│  ↙ ✗ Missed · 050 123 4567 · rang Sales    10:42  [ Call ]   │
│       Left a voicemail  [ Listen ]                           │
│  ↗   Sara → +971 4 555 1234 · 3 min        09:15  [ Call ]   │
│  ↙   Aisha (103) · 1 min                   08:50  [ Call ]   │
│  YESTERDAY                                                   │
│  ↙ ✗ Missed · Omar (102)                   17:31  [ Call ]   │
│                                   [ Show more ]              │
└──────────────────────────────────────────────────────────────┘
```
- ↙ incoming, ↗ outgoing, ✗ missed. A row opens a small detail: the steps ("Rang Sales: Mohammed, Sara, Omar · nobody answered in 25 s · voicemail 0:42").
- Missed calls I was part of through a group show "rang Sales". Badge: missed since I last opened the tab.
- The Dialer's "Recent" list (today: this browser only) becomes the last 5 from here, and its note "Call history comes in a later update" goes.

### 13.2 Admin → Calls (everyone's)

```
┌──────────────────────────────────────────────────────────────┐
│  Calls                                     [ Download (CSV) ]│
│  [ Anyone ▾ ] [ Any number ] [ Missed only ] [ Last 7 days ▾]│
│                                                              │
│  WHEN      FROM             TO                RESULT   LENGTH│
│  10:42     050 123 4567     04 200 0100→Sales Missed · VM   — │
│  09:15     Sara (101)       +971 4 555 1234   Answered  3:02 │
│  08:50     Aisha (103)      Sara (101)        Answered  1:10 │
│  08:31     +44 20 7946…     04 200 0101       Answered  0:47 │
│                                          [ Show more ]       │
│  Kept 1 year. Line: UCM / Telnyx shown in the detail.        │
└──────────────────────────────────────────────────────────────┘
```
- Reporters see it too (read-only by nature). Phone width drops the Length and Result columns into the row's second line.
- **Download (CSV)**: the current filter, up to one year; one line per call; the file name says the dates.
- A row opens the detail sheet: every step, which line, who answered, the voicemail if any (admins can play it).

## 14. Undo and Routing changes (ADR-071)

### 14.1 The toast

After any routing change (numbers, ring groups, office hours, holidays, outgoing rules): **"Saved. Undo"** for 10 seconds. Undo: "Put back. [ Redo ]".

### 14.2 System → Routing changes (new tab)

```
┌──────────────────────────────────────────────────────────────┐
│  System                                                      │
│  Status · Alerts · Activity · Routing changes · Settings · … │
│                                                              │
│  Every change to where calls go is kept (last 50).           │
│                                                              │
│  Today 10:12  Mohammed  Sales now rings one after another    │
│                          [ See the change ] [ Put this back ]│
│  Today 09:40  Sara      04 200 0102 now rings Support        │
│  Mon 16:00    Mohammed  Added holiday "Eid al-Fitr"          │
│  Mon 15:58    Mohammed  Put back the version from Mon 15:30  │
└──────────────────────────────────────────────────────────────┘
```
- **See the change**: before and after, as sentences ("Before: Calls to 04 200 0100 ring Sales (all at once)… After: … one after another").
- **Put this back** puts the routing back to just before that change, shows what it will change first, and is itself a new line (so it can be undone). If it would turn on calls abroad or premium numbers: confirm it's you, with the reason.
- Reporters see the list; buttons greyed with the reason.

## 15. Screenshots to capture when built (`make screens`)

Light and dark, desktop and phone width (phone sweep includes every signed-in one):
`front-door-{what,card,card-npm,advanced-decrypt}`, `check-it{,-proxy-address,-phone}`, `dns-{records,automatic,automatic-kept}`, `public-port-{warning,rules,before-apply}`, `system-settings-email{,-setup-who,-setup-test}`, `people-invite-email`, `sign-in-forgot{,-sent,-new-password,-second-step,-done,-link-used}`, `incoming-{numbers,wizard-hours,wizard-after,wizard-check}`, `ring-groups{,-add-how,-detail}`, `office-hours{,-home}`, `destination-picker`, `simulator-after-hours`, `voicemail{,-empty,-settings,-record}`, `call-history{,-detail}`, `admin-calls{,-detail}`, `system-routing-changes{,-see}`, `undo-toast`.

## 16. Owner decisions (2026-09-30)

1. **Where ring groups and office hours live.** Recommended: tabs inside **Incoming**. **Owner: two new sidebar rows for now** (Ring groups, Office hours, under Incoming); to be decided again later.
2. **Password reset for a password-only account** (an admin who chose no second step). Email alone would then be enough to take the account. *Recommend:* allow it, but for an admin also send an alert to the other admins ("Aisha reset her password by email") and put a line on the admin home; normal people all have a second step anyway. **Approved.**
3. **Greeting length.** *Recommend* 30 seconds (short greetings keep callers on the line; a message stays 3 minutes). **Approved.**
4. **Missed-call badge on Call history.** *Recommend yes*, cleared when the tab is opened (Voicemail's badge clears only when messages are heard). **Approved.**
5. **Admin "Calls" page for reporters.** *Recommend yes* (that's what reporters are for); they can't play voicemail from it. **Approved.**
6. **"Check it" from the phone also tests call audio** (a quick relay connection from the phone). *Recommend yes*: it's the one thing people can't test from inside the network, and it's what fails most often. **Approved.**
