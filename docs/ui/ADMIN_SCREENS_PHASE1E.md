# Admin portal — low-fidelity screen specs (Phase 1E, step 1)

Layout only: no colour, type or icon decisions (those are fixed in `linx-tokens.json` / `DESIGN_TOKENS.md` and applied when the screens are built in steps 5–8). This is for the owner to sign off on **what's on each screen and where** before any UI code is written.

Scope is `docs/ADMIN.md` §2 "In 1E". Things that wait for 1F (ring groups, office hours, voicemail, call history, email, undo, changing the domain) appear only as a plain note where someone would look for them.

Boxes are wireframes (element placement), not pixel layouts.

## 0. Rules every admin screen follows

These apply everywhere below, so they aren't repeated per screen.

- **Two ways to add anything** (owner direction, 2026-09-26). Every list has one **Add** button that opens a small chooser:
  ```
  ┌──────────────────────────────────────────┐
  │  Add a person                        ✕   │
  │                                          │
  │  ┌────────────────────┐ ┌──────────────┐ │
  │  │ Guide me           │ │ Quick add    │ │
  │  │ A few short steps, │ │ Just a name  │ │
  │  │ each explained,    │ │ and email.   │ │
  │  │ with our pick.     │ │ Change the   │ │
  │  │  (Recommended)     │ │ rest later.  │ │
  │  └────────────────────┘ └──────────────┘ │
  │  [ ] Always use quick add                │
  └──────────────────────────────────────────┘
  ```
  "Always use quick add" is remembered per browser; once ticked, Add goes straight to quick add, with "Guide me instead" as a link at the top.
- **Guided wizards** are a side sheet (right side, full screen at phone width) with a step list at the top ("1 Name · 2 Number · 3 Phone · 4 Done"), one question per step, a pre-filled **recommended** answer marked "(Recommended)", one sentence of why under it, **Back / Next**, and a last "Done" step that says what was made and what to do next. Closing half-way asks "Discard this?".
- **Quick add** is a small dialog with only the required fields and a note: "Everything else uses safe defaults. You can change it any time."
- **Lists**: search box, at most two filter chips, a status column, rows open a detail sheet. Sorting on column headers. Twenty rows, then "Show more".
- **Empty state** (first visit, nothing yet): one sentence on what this thing is, in plain words, and both buttons (Guide me / Quick add). No empty table.
- **Detail sheets** (right side): heading, status line, sections with **Edit** per section (inline, Save / Cancel), dangerous actions at the bottom in their own "Danger zone" box, each with a confirm dialog that names the thing ("Remove the line "UCM"? Calls to its 2 numbers will stop.").
- **Greyed, not hidden**: if the role can't do something, the button is shown disabled with the reason on hover/tap ("Only a system admin can change this"). Reporters see every admin page read-only.
- **"Confirm it's you"** (ADR-053): when the server answers `confirm_required`, a small dialog appears over the current screen, the action retries by itself afterwards (§12).
- **Show once** (device logins, API keys, invite links): a box with the secret, **Copy**, a QR where useful, and "You won't see this again" plus an "I've saved it" button to close. Cleared from memory when closed.
- **Saving**: each save shows a short "Saved" toast; a clash with someone else's edit (412) shows "Someone else changed this. Reload to see their version."
- **Words**: phone line (not trunk), phone number (not DID), desk phone or phone app (not SIP endpoint), what phones can call (not permission level/class of service), connection (not VPN/WireGuard) in Simple mode. Exact terms appear only under an "Advanced details" disclosure.
- **Phone width**: sidebar collapses as in 1C; lists become stacked cards (name + status + one line); sheets go full screen.
- **Refresh**: lines, alerts and status refresh every 15 s while their page is open; "on a call" comes from the Team websocket.

## 1. Admin shell

The 1C shell, with an **Admin** group added below the everyday items. Only shown to `admin`, `system_admin`, `reporter`.

```
┌──────────────┬────────────────────────────────────────────┐
│ logo         │  [ Search people or dial a number ]        │
│              ├────────────────────────────────────────────┤
│ Dialer       │                                            │
│ Team         │                                            │
│ ───────────  │                                            │
│ Call hist. ░ │             main content                   │
│ Voicemail  ░ │                                            │
│ Meetings   ░ │                                            │
│ ───────────  │                                            │
│ ADMIN        │                                            │
│ Home      ●2 │   ← open-alert count badge                 │
│ People       │                                            │
│ Extensions   │                                            │
│ Phone lines ●│   ← dot when a line is down                │
│ Incoming     │                                            │
│ Outgoing     │                                            │
│ Simulator    │                                            │
│ System       │                                            │
│  (expert:)   │                                            │
│ Connections  │   ← only when Simple mode is off           │
│ Webhooks     │                                            │
│ API keys     │                                            │
│ ───────────  │                                            │
│ ⚙ Settings   │                                            │
│ [me ▾]       │                                            │
└──────────────┴────────────────────────────────────────────┘
```

- ░ = greyed "Coming soon" items from 1C, unchanged.
- **Admin group order** follows the checklist's order (people → numbers → lines → calls → system).
- **Expert pages** (Connections = WireGuard, Webhooks, API keys) appear under a small "Expert" label only when Simple mode is off. The call-permission editor's raw category list is inside Outgoing, also only with Simple mode off.
- **Simple mode switch**: in System → Settings (§10.5) and as a link at the bottom of the admin group: "Show expert pages".
- **Admin area hidden by "home network only"** (ADR-049): an admin signed in from outside sees only the everyday items and one greyed "Admin" row; hovering/tapping it says "Admin pages are only available from your home or office network. A system admin can change this in System → Settings."
- **Account menu** (`[me ▾]`) gains **My account** (§11) above "Sign out".
- Phone width: the admin group is a second section in the collapsed icon rail; icons with tooltips.

## 2. Admin home

```
┌──────────────────────────────────────────────────────────────┐
│  Good morning, Mohammed                                      │
│                                                              │
│  ┌── Getting started ─────────────────── 4 of 7 done ──────┐ │
│  │ ✓ Choose how extension numbers look                     │ │
│  │ ✓ Add the people who'll use Linx                        │ │
│  │ ✓ Test a call from your browser                         │ │
│  │ ✓ Add a second way to sign in (passkey)                 │ │
│  │ ○ Connect a phone line               [ Connect ]        │ │
│  │ ○ Send your phone number to someone  [ Choose ]         │ │
│  │ ○ Decide what your phones can call   [ Review ]         │ │
│  │                                        Hide this list   │ │
│  └─────────────────────────────────────────────────────────┘ │
│                                                              │
│  ┌ Needs attention ──────────┐  ┌ Phone lines ────────────┐  │
│  │ ⚠ Line "UCM" is down      │  │ ● UCM        Working    │  │
│  │   since 09:12  [ Look ]   │  │ ● Telnyx     Down 4 min │  │
│  │ ⚠ Certificate renews in   │  │                         │  │
│  │   9 days (normal)         │  │ [ All lines ]           │  │
│  │ [ All alerts ]            │  └─────────────────────────┘  │
│  └───────────────────────────┘                               │
│  ┌ On a call now ────────────┐  ┌ System ─────────────────┐  │
│  │ Sara (101) ↔ +971 50…     │  │ ● All services running  │  │
│  │   04:12 · outgoing        │  │ ● Certificate OK (61 d) │  │
│  │ Omar (102) ↔ Aisha (103)  │  │ ● Reachable from outside│  │
│  │ [ Team ]                  │  │ [ Status ]              │  │
│  └───────────────────────────┘  └─────────────────────────┘  │
└──────────────────────────────────────────────────────────────┘
```

- **Checklist**: ticks come from real state (an extension exists, a line exists and is working, a phone number routes to an extension, what phones can call has been reviewed (saved once, in the wizard or on Outgoing), the admin has a passkey or authenticator, a `*43` call was made). Each open item's button jumps to the right screen or wizard. "Hide this list" collapses it to one line ("Getting started: 4 of 7") and can be reopened; it disappears by itself once everything's done.
- Items not relevant yet are not shown (e.g. "Send your phone number" appears only after a line exists).
- **Needs attention**: open alerts, most serious first, each with one plain sentence and a button to the page that fixes it. Empty: "Nothing needs you right now."
- **Phone lines**: every line with its status dot and text. No lines: "No phone line yet — Linx can call between extensions only." + Connect.
- **On a call now**: live calls (from `/calls/active` + Team websocket); outside numbers shown in full. Empty: "Nobody's on a call."
- **System**: three lines summarising §10.1. Any red line links there.
- Reporter: same page, buttons that change things are disabled.
- Phone width: cards stack in the order checklist → attention → lines → calls → system.

## 3. First-run setup

### 3.1 First-admin link (from `linx setup`)

The existing 1C setup-link page, with passkeys added:

```
┌──────────────────────────────────┐
│            [logo]                │
│  Welcome to Linx                 │
│  You're the first admin of       │
│  pbx.example.com.                │
│                                  │
│  Email   mohammed@example.com    │  ← read-only
│                                  │
│  How do you want to sign in?     │
│  (•) Passkey — Face ID, Touch ID │
│      or your phone (Recommended) │
│  ( ) Password + authenticator app│
│  ( ) Password only               │
│      ⚠ Not recommended           │
│                                  │
│          [ Continue ]            │
└──────────────────────────────────┘
```

- **Passkey path**: browser passkey prompt → "Name this passkey" (pre-filled, e.g. "Mac — Safari") → recovery codes screen (as in 1C: copy, download, "I've saved these") → a gentle "Also add a password? You can do this later in My account." (Skip / Add) → setup wizard.
- **Password path**: 1C's choose password → authenticator QR → recovery codes → setup wizard.
- **Password only** (owner decision 2026-09-27, offered to every role, admins included): picking it opens a warning box, amber-bordered, before Continue works: "Anyone who learns or guesses your password can control your whole phone system: add people, make expensive calls abroad, listen to settings. A passkey or authenticator app stops that. You can add one later in My account." Checkbox "I understand, use a password only", then choose password (1C rules) → setup wizard. No recovery codes (there's no second step to recover).
- An admin or system admin with password only then sees, on every admin page, a slim amber banner: "You sign in with a password only. [ Add a passkey ]" (dismissable for 7 days per browser, comes back), and the checklist item "Add a second way to sign in" stays open.
- The same page, for an ordinary invited person, offers the same three choices, passkey recommended; for a `user`, "Password only" shows the note without the warning box (their account can't change settings).
- Used/expired link: 1C's "This link can't be used" page, unchanged.

### 3.2 Setup wizard

A full-page wizard (no sidebar), resumable: leaving and coming back returns to the last unfinished step.

**Before step 1, for a system admin on an untouched install** (docs/BACKUP.md §4, added with backup step 4): "How do you want to start?" — two cards, **Set up fresh** (Recommended) and **Restore from a backup**. The restore screen (same header, no progress bar): where is the backup (a folder on this server, default `/var/backups/linx`, Recommended; or a backup place set up on this server, by name), which backup (the newest, Recommended; or an older one by ID), the backup's password, and a "This replaces everything on this server" warning box whose "I understand, replace everything" checkbox enables **Restore** (confirm it's you first). Then a progress page (waiting for the server → restoring → Linx restarting) and "Restored — Sign in". A failed attempt comes back to the form with the reason at the top and "Nothing on this server was changed". Screenshots: `setup-wizard-start`, `setup-wizard-restore*`.

```
┌──────────────────────────────────────────────────────────────┐
│ [logo]   Set up Linx                          Finish later → │
│                                                              │
│  ● Place ─ ● Country ─ ◉ Numbers ─ ○ People ─ ○ Line ─       │
│  ○ Calls ─ ○ Test                                            │
│                                                              │
│  (step content, one question per step)                       │
│                                                              │
│                                                              │
│  [ Back ]                       [ Skip for now ]  [ Next ]   │
└──────────────────────────────────────────────────────────────┘
```

- **Finish later** goes to Admin home; the checklist shows the remaining steps. "Skip for now" is on every step except Numbers.
- Each step saves on Next.

**Step 1 — Place.** "Where will you use Linx?"
Two big cards: **Home** ("family and a few phones") · **Business** ("staff, a reception, office hours"). No recommendation (it's their answer). Under it: "This only changes examples and suggestions. Everything works the same."

**Step 2 — Country.** "Which country are your phone lines in?" Dropdown, UAE pre-selected (only country available now; the list says "More countries coming"). One sentence: "Linx uses this to recognise mobile, local and emergency numbers." Shows the always-allowed numbers: "Emergency numbers 999, 998, 997, 112 and 901 always work, from every phone."

**Step 3 — Numbers.** "How should extension numbers look?"

```
  Digits     ( ) 2   (•) 3 (Recommended)   ( ) 4   ( ) 5  ( ) 6

  ┌──────────────────────────────────────────────────────────┐
  │ 100 ──────────── 599 │ 600 ─ 699 │ 700 ─── 899 │ 900–999 │
  │      People          │  Groups   │ Kept free   │ Avoided │
  │  (500 numbers)       │ (later)   │ for later   │ (UAE    │
  │                      │           │             │ short   │
  │                      │           │             │ codes)  │
  └──────────────────────────────────────────────────────────┘
   Example: Sara → 100, Omar → 101

  ▾ Change the ranges
     People      from [ 100 ]  to [ 599 ]
     Groups      from [ 600 ]  to [ 699 ]
     Kept free   from [ 700 ]  to [ 899 ]
                            [ Use the recommended ranges ]
```

- **Ranges are customizable** (owner decision 2026-09-27). Collapsed by default ("Change the ranges"); the recommended ranges for the chosen digit count are pre-filled. The bar redraws as they change. Checks, shown in plain words next to the field and blocking Next: every number has the chosen digit count, a range's "from" is below its "to", ranges don't overlap, and no range contains a number that can't be an extension in the country ("901 and 999 are emergency numbers in the UAE, so People can't include 900–999."). Gaps are allowed and shown as "Not used". Groups and Kept free can be left empty (clear both boxes: "No groups"). The "Avoided" block isn't editable: it's the country's own short codes, drawn from the numbering rules.
- The next free number suggested everywhere comes from the People range; new extensions outside it are allowed but warned ("110 is outside the People range (100–599)").
- Changing ranges later (System → Settings → Numbers) never renumbers anyone: extensions left outside the new range are listed with a note, not blocked.
- The bar is a picture that redraws as digits/ranges change. Home shows "Mum's phone → 100, Kitchen → 101", Business "Reception → 100, Sara → 101".
- Changing digits when extensions already exist with another length: the step lists them ("101, 102 have 3 digits. Renumber them first in Extensions.") and disables Next for that choice.
- Recommendation reason: "3 digits gives room for 500 people and is quick to dial."

**Step 4 — People.** "Who will use Linx?"

```
  NAME             EMAIL                ROLE           EXT
  Mohammed (you)   mohammed@…           System admin   100
  [ Sara Haddad ]  [ sara@…         ]   [ Person ▾ ]   101  ✕
  [             ]  [                ]   [ Person ▾ ]   102
  + Add another row
                                           [ Create and get invite links ]
```

- The extension column fills itself from the plan; click to change. Role options with one line each: Person ("makes and takes calls"), Reporter ("can see the admin pages, can't change them"), Admin ("can change everything except other system admins").
- Making someone an Admin shows "Confirm it's you" (§12) when pressed.
- After Create: each row shows **Invite link** (Copy) and **QR** (opens a big QR to scan with a phone). Note: "Invite links work once, for 24 hours. Email invites come later."
- Home: example placeholders "Mum", "Kitchen".

**Step 5 — Phone line.** "Connect a phone line now?" Template cards (same as §6.2 step 1) + **Later** card ("Linx works between extensions without one"). Picking a template runs the phone-line wizard (§6.2) inside this step and returns here.

**Step 6 — Calls.** "What can your phones call?" One set of switches for every extension (owner decision 2026-09-27: flat, no per-person levels in 1E):

```
  [on ] Local numbers
  [on ] Mobiles
  [on ] Other cities in the UAE
  [on ] Free numbers (800)
  [off] Abroad            (Recommended off: most phone fraud is calls abroad)
  [off] Premium-rate      (costs a lot per minute)
  [on 🔒] Emergency numbers — always on
```

Note: "This applies to every phone. Letting some people call abroad and not others comes later."

**Step 7 — Test.** "Make a test call." One big **Call the echo test** button (`*43` from this browser, using the 1C call panel). "Speak — you should hear yourself back." Then **I heard myself** (→ Done) / **I didn't** (→ opens Settings' microphone/speaker choice and a link to System status).

**Done.** "Linx is ready." Summary of what was set, with **Go to admin home**.

## 4. People

### 4.1 List

```
┌──────────────────────────────────────────────────────────────┐
│  People        12 people              [ Search ] [ + Add ]   │
│  [ All ▾ roles ] [ Active ▾ ]                                │
│                                                              │
│  NAME            EXT   ROLE          SIGN-IN      STATUS     │
│  Mohammed        100   System admin  Passkey      Active     │
│  Sara Haddad     101   Person        Password+app Active     │
│  Omar Nasser     102   Person        Google       Active     │
│  Aisha Rahman    103   Admin         —            Invited    │
│  Yusuf Ali       104   Person        Password     Locked 🔒  │
│  Chen Wei        —     Reporter      Password     Disabled   │
└──────────────────────────────────────────────────────────────┘
```

- **Sign-in** column: what they've set up (Passkey, Password, +app, Google/Microsoft/…). An admin with a password only shows "Password only ⚠" (hover: "Not recommended for an admin"). **Status**: Invited (link not used yet), Active, Locked (too many wrong passwords), Disabled.
- Filters: role; Active / Invited / Locked / Disabled / All.

### 4.2 Add a person

- **Guide me**: 1) Name and email → 2) What can they do? (Person recommended; Reporter/Admin explained; Admin needs confirm) → 3) Extension: "Give them extension 104?" (next free, Recommended) / pick another / "No phone" → 4) Done: invite link + QR (show once), "Send this to Sara. It works once, for 24 hours."
- **Quick add**: Name, Email. Defaults: Person, next free extension. Result: the invite link box.

### 4.3 Person detail (sheet)

```
┌────────────────────────────────────────┐
│ Sara Haddad                        ✕   │
│ Person · Ext 101 · Active              │
│ Last signed in today 08:41 (Chrome)    │
│────────────────────────────────────────│
│ Details              [ Edit ]          │
│  Name, Email, Role                     │
│ Phone                [ Edit ]          │
│  Extension 101                         │
│ Sign-in                                │
│  Passkeys: 2 · Authenticator: on       │
│  Company account: Google (sara@…)      │
│                     [ Unlink Google ]  │
│  Signed-in browsers: 3  [ Sign out all]│
│────────────────────────────────────────│
│ Danger zone                            │
│  [ New invite link ]   (resets password│
│                         set-up)        │
│  [ Reset authenticator ]  (confirm)    │
│  [ Unlock ]            (when locked)   │
│  [ Disable ]                           │
└────────────────────────────────────────┘
```

- **Reset authenticator** (confirm it's you): "Sara will set up a new authenticator the next time she signs in. All her browsers are signed out." Audited.
- **Unlock**: only shown when locked; "Unlocks now instead of waiting."
- **Disable**: signs them out everywhere and stops their browser phone; their extension keeps ringing desk phones unless removed too (the dialog says so and offers "Also remove extension 101").
- **Role change to/from Admin** needs confirm; admins can't edit system admins (greyed, reason shown).
- Invited person: the top shows "Hasn't used their invite yet" + **Copy new invite link** (the old link stops working).

## 5. Extensions and devices

### 5.1 List

```
┌──────────────────────────────────────────────────────────────┐
│  Extensions    9 extensions           [ Search ] [ + Add ]   │
│  Numbers are 3 digits (people 100–599). [ Change ]            │
│                                                              │
│  EXT   NAME         PERSON         PHONES                    │
│  100   Mohammed     Mohammed       Browser, iPhone           │
│  101   Sara         Sara Haddad    Desk phone ●              │
│  110   Reception    —              Desk phone ○              │
│  111   Kitchen      —              —                         │
└──────────────────────────────────────────────────────────────┘
```

- **Phones**: kinds of device, with a dot (● online, ○ offline). "Browser" appears while the person is signed in on the web.
- "Change" goes to System → Settings → Numbers.
- An extension without a person is normal (a reception desk phone); the note on the empty state says so.

### 5.2 Add an extension

- **Guide me**: 1) Number (next free, Recommended; the numbering picture small) → 2) Name ("Reception", "Kitchen") → 3) Belongs to a person? (pick / nobody) → 4) Add a desk phone or phone app now? (yes → §5.4 / later) → Done.
- **Quick add**: Name. Number = next free, no person.

### 5.3 Extension detail (sheet)

- Details (number, name, person) with Edit. Changing the number warns if a phone number routes to it (it keeps routing: same extension).
- **Phones** section: each device as a row — name, kind, online/offline + "last seen", **Show settings again** is not offered (logins are shown once); **New password** and **Remove** per row; **+ Add a desk phone or phone app**.
- Danger zone: **Remove extension** ("Its 2 phones stop working and number 101 becomes free.").

### 5.4 Add a desk phone or phone app

- Guided only (it's short): 1) What is it? — Desk phone / Phone app on a mobile or computer (Grandstream Wave, Zoiper, …). 2) Name ("Sara's desk"). 3) **Its settings** — shown once, after confirm it's you:

```
┌──────────────────────────────────────────┐
│ Settings for "Sara's desk"               │
│                                          │
│ Server     sip.example.com               │
│ Port       5061   Transport  TLS         │
│ Username   u7k2m9x4          [ Copy ]    │
│ Password   ••••••••  [ Show ] [ Copy ]   │
│ Audio encryption   SRTP (required)       │
│                                          │
│ ▸ How to enter this on common phones     │
│                                          │
│ You won't see the password again.        │
│ Lost it? Use "New password".             │
│                         [ I've saved it ]│
└──────────────────────────────────────────┘
```

- "How to enter this" expands the plain-language settings block the API already returns.
- **New password** repeats the same box (confirm it's you) and says "The phone stops working until you enter the new password."
- Note under the Phones section: "Desk phones work on your home/office network. Using one from outside comes later."

## 6. Phone lines

### 6.1 List

```
┌──────────────────────────────────────────────────────────────┐
│  Phone lines   2 lines                          [ + Add ]    │
│                                                              │
│  NAME     STATUS              NUMBERS   ORDER    SECURITY    │
│  UCM      ● Working           1         1st      Encrypted   │
│  Telnyx   ● Down since 09:12  3         2nd      Encrypted   │
│  Old SIP  ● Working           1         —        ⚠ Not enc.  │
└──────────────────────────────────────────────────────────────┘
```

- **Order**: position in "which line to try first" (set in Outgoing, §8). "—" = not used for outgoing calls.
- **Security**: Encrypted / Encrypted, pinned certificate / ⚠ Not encrypted (ADR-023) / Through a connection (WireGuard; Simple mode: "Private connection").
- Empty state: "A phone line connects Linx to the phone network, so you can call mobiles and landlines and be called on your number. Linx works between extensions without one."

### 6.2 Add a phone line — guided (mirrors `linx trunk add`)

1. **Who provides it?** Template cards: Grandstream UCM (on your network) · Telnyx · VoIP.ms · Twilio · Another Linx server · Something else. Each card one line ("A phone system in your office, like a UCM6304").
2. **Where is it?** Address (host or IP) with the template's hint ("The UCM's address on your network, e.g. 192.168.1.20"); for providers pre-filled. **Advanced details**: port, transport, connection (WireGuard, Simple mode off).
3. **How does it know it's you?** The template picks the way; asks only what that way needs: username + password, or "your provider lists Linx's address" (shows Linx's public address to give them), or "on your network" (nothing).
4. **Test** — runs the connection test automatically, shown as a checklist that fills in:
   ```
   ✓ Found 192.168.1.20
   ✓ Connected
   ✗ Its certificate isn't trusted
       This phone system uses its own certificate.
       Fingerprint  3A:9F:…:C2
       Check it matches the one on the UCM's screen
       (Settings → Security → TLS).
       [ Trust this certificate ]   [ Upload the certificate file ]
       ▸ The UCM doesn't have a certificate for its address?
         [ Make one for it ]  (same as linx trunk cert)
   ○ Signed in
   ```
   After a fix, the test re-runs. A failure never lets you continue without an explicit choice.
5. **Encryption fallback (ADR-023)** — only if TLS couldn't work at all and the provider can't encrypt: a warning box, red-bordered: "Calls on this line can be listened to by anyone between you and the provider. Only continue if your provider can't encrypt." Checkbox "I understand", then **Continue without encryption** (confirm it's you). Not shown for lines through a private connection.
6. **Numbers** — "Which phone numbers come with this line?" Rows: number + "rings" (extension picker, default the first admin's extension). "Add later" allowed.
7. **Use it for outgoing calls?** First line: "Yes, as the main line" (Recommended). Otherwise: "As the backup" / "Main, move the others down" / "Only for incoming".
8. **Done** — status as it settles ("Connecting… → Working"), with **Make a test call** (dials a number you type, from this browser) and **Back to lines**.

### 6.3 Quick add

Template, name, address, login (if the template needs one). Then the same **Test** panel (step 4), because a line that doesn't work isn't saved silently: "Save anyway (fix later)" is allowed and saves it disabled.

### 6.4 Line detail (sheet)

- Status with how long, and the last problem in plain words ("The provider refused the password at 09:12").
- **Test again** button (same checklist).
- Sections with Edit: Connection (address, login, advanced), Numbers (list, add, where each rings), Limits (calls at once), Caller ID shown to others.
- Changing address/transport/encryption re-asks the ADR-023 confirmation where the API requires it.
- Danger zone: **Disable**, **Remove** ("Its 3 numbers stop ringing.").

### 6.5 Connections (WireGuard) — expert page

Shown only with Simple mode off (also reachable from a line's Advanced details).

- List: name, status (Connected / Connecting / Not connected, last handshake), lines using it.
- **Add**: Guided = 1) Paste or upload the config file your provider gave you (Recommended) or 2) fill in the fields; the public key is shown, never asked. Note when imported "AllowedIPs" are ignored and why (from the API's `notes`).
- Detail: fields, lines using it, **Remove** (disabled with the reason while a line uses it).

## 7. Incoming calls

```
┌──────────────────────────────────────────────────────────────┐
│  Incoming calls                                              │
│  When someone calls one of your numbers, it rings here.      │
│                                                              │
│  YOUR NUMBER          LINE     RINGS                         │
│  +971 4 200 0100      UCM      [ 100 Mohammed        ▾ ]     │
│  +971 4 200 0101      Telnyx   [ 110 Reception       ▾ ]     │
│  +971 4 200 0102      Telnyx   [ Nobody (not answered)▾]  ⚠  │
│                                                              │
│  Office hours, ring groups and "if nobody answers"           │
│  are coming in the next update.                              │
└──────────────────────────────────────────────────────────────┘
```

- One editable dropdown per number (saves on change, toast). "Nobody" shows a ⚠ with "Callers hear that the number isn't available."
- **Try it** icon per row opens the simulator (§9) pre-filled with that number.
- Adding a number happens on its line (§6.4); the empty state says so and links to Phone lines.

## 8. Outgoing calls

Three sections on one page.

```
┌──────────────────────────────────────────────────────────────┐
│  Outgoing calls                                              │
│                                                              │
│  Country   United Arab Emirates          [ Change ]          │
│                                                              │
│  Which line first                                            │
│   ≡ 1  UCM       ● Working                                   │
│   ≡ 2  Telnyx    ● Down   (used if the one above can't)      │
│   Not used for outgoing: Old SIP   [ Use ]                   │
│                                                              │
│  What your phones can call               (every extension)   │
│   [on ] Local   [on ] Mobiles   [on ] Other UAE cities       │
│   [on ] Free (800)   [off] Abroad   [off] Premium-rate       │
│   [on 🔒] Emergency — always                                  │
│                                                              │
│  Alerts for calls abroad                                     │
│   Tell me when someone calls a new country      [ on ]       │
│   Tell me after [ 10 ] calls or [ 60 ] minutes abroad a day  │
└──────────────────────────────────────────────────────────────┘
```

- **Which line first**: drag handles (and up/down buttons for keyboard/phone). One sentence: "Linx tries the next line only if one is down or full, never after someone answers."
- **What your phones can call**: one set of switches for every extension (flat, owner decision 2026-09-27). Each switch saves on change. Turning on Abroad or Premium-rate asks "Confirm it's you" and says why ("Calls abroad are where phone fraud costs money"). Emergency is always on, shown locked. Simple mode off adds the raw categories (shared cost, service numbers) and "Hide our number on outgoing calls". A note: "Different rules for different people come later."
- Needs routing rights; without them the switches are read-only with the reason.
- **Country Change**: shows clashing extensions if any (1D's `ExtensionClashes`).

## 9. Call simulator

```
┌──────────────────────────────────────────────────────────────┐
│  Call simulator                                              │
│  See where a call would go, without making it.               │
│                                                              │
│  (•) Someone here calls out   ( ) Someone calls in           │
│                                                              │
│  From extension  [ 101 Sara ▾ ]                              │
│  Number          [ 050 123 4567          ]   [ Check ]       │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐    │
│  │ ✓ Allowed                                            │    │
│  │ Mobile number in the UAE. Your phones can call      │    │
│  │ mobiles.                                             │    │
│  │ Goes out on "UCM" as 0501234567, showing             │    │
│  │ +971 4 200 0100.                                     │    │
│  │ If UCM is down: "Telnyx" as +971501234567.           │    │
│  └──────────────────────────────────────────────────────┘    │
│                                                              │
│  Recent checks: 050… ✓ · 0044 20… ✗ · 999 ✓                  │
└──────────────────────────────────────────────────────────────┘
```

- **Refused** result: "✗ Not allowed. International number (United Kingdom). Your phones can't call abroad. [ Change what phones can call ]".
- **Emergency**: "✓ Always allowed — emergency (Police). Goes out on …". No line: "Would fail: no phone line can take it" + Connect a line.
- **Someone calls in**: "Your number" dropdown (every phone number) → "Rings extension 100 (Mohammed): Browser, iPhone." / "Rings nobody: callers hear the number isn't available." A note: "Office hours and groups will show here when they arrive."
- Same database function real calls use, stated under the result in small text: "This is exactly what a real call does."
- Recent checks: this browser only, last 5.

## 10. System

One page with tabs: **Status · Alerts · Activity · Settings · Backups**; expert pages Webhooks and API keys are their own sidebar rows (Simple mode off).

*As built (backup step 5, 2026-09-27):* only **Backups** is live so far; the other four tabs are greyed "Coming soon" and the sidebar's System opens Backups (`/admin/system/backups`). *(2026-09-28: Status is live too and is what System opens; Alerts, Activity and Settings are still "Coming soon".)* Four cards: **Backups** (what a backup holds; "Back up automatically" Off / Every day / Every week / Every month with day and time, Save schedule; **Back up now** top right, "Starts within a minute" while queued), **Download a copy** (Make a backup file → "Waiting for the server…" / "Backing up now and making the file…" → "Your backup file is ready: 340 MB, newest backup from …, until 16:08" with **Download file** and **Show its password** (confirm it's you; amber box, Copy, "keep it apart from the file"); failed shows the reason and Try again; another admin's file is noted, not offered), **Where backups go** (each destination's last result; adding one is `sudo linx backup destination add` on the server, shown with Copy), **History** (When · How · Result with failed destinations' reasons · Backup ID). Reporters: everything visible, buttons greyed "Only an admin can change backups". The setup wizard's restore screen gained a third place, **A backup file on my computer** (file picker, size, 2 GB check, upload progress bar). Screenshots: `{light,dark}-system-backups{,-password}`, `system-backups-empty`, `system-backups-waiting`, `system-backups-download-failed`, `setup-wizard-restore-file`.

### 10.1 Status

```
┌──────────────────────────────────────────────────────────────┐
│  System   [Status] Alerts  Activity  Settings                │
│                                                              │
│  Checked 10 s ago                               [ Check now ]│
│                                                              │
│  Linx                                                        │
│   ● Web and API              running                         │
│   ● Phone system             running                         │
│   ● Database                 running, up to date (v20)       │
│   ● Calls from outside relay running                         │
│  Certificate and address                                     │
│   ● Certificate for *.example.com   valid, renews in 31 days │
│   ● pbx.example.com reachable through Pangolin               │
│   ● DNS for meet / api / turn → 203.0.113.7                  │
│  Phone lines                                                 │
│   ● UCM    working       ● Telnyx   down since 09:12         │
│  Connections (WireGuard)                                     │
│   ● office-vpn   connected, last contact 40 s ago            │
│                                                              │
│  Some checks can only run on the server itself (firewall,    │
│  start-up services). Run  sudo linx doctor  there.   [Copy]  │
└──────────────────────────────────────────────────────────────┘
```

- Each red/amber row expands into the plain-language "what to do" text doctor prints.
- Domain, certificate and front door are read-only here: "Change them by running sudo linx setup on the server." (Domain & DNS page is 1F.)

*As built (2026-09-28, ADR-056):* `/admin/system/status`, now what the sidebar's System and `/admin/system` open. "Checked N s ago" + **Check now**; an amber box when the server helper isn't running ("…run this on the server: sudo linx setup [Copy]"). **Linx services**: one row per service (optional ones not installed are left out): dot, plain name, "Running since …" / "Starting…" / "Running, not answering" / "Stopped", and "restarted by itself N times" when Docker has. A row opens: **Recent log** (last 200 lines, newest at the bottom, time then text, wraps rather than scrolling sideways; Refresh, Copy) and **Restart** with a dialog saying what it does ("Calls in progress end. Phones and browsers reconnect by themselves within a minute." for the phone system; the web service: "This page and everyone's browser phones disconnect for a few seconds"). Restart is greyed with its reason for the database and internal certificates ("…run sudo linx doctor on the server"), for reporters ("Only an admin can restart services") and without the helper; logs likewise for reporters. **Certificate** (valid until, days left; amber under 21 days, red under 7), **Phone lines** (+ WireGuard connections when there are any), **Needs attention** (open alerts), and the `sudo linx doctor` line with Copy. Front-door reachability and DNS stay in doctor. The home page's System card reads "All N Linx services are running" or lists the ones that aren't, and its Status button opens this page. Screenshots: `{light,dark}-system-status{,-log}`, `system-status-restart`, `system-status-no-helper`, `phone-system-status`.

### 10.2 Alerts

- Two parts: **Open and recent alerts** (list: severity, sentence, since, resolved-at) and **Where alerts go** (channels).
- Channels list: name, kind (ntfy, Gotify, Slack, Teams, Telegram, Webhook), severities, quiet hours, **Send a test**.
- **Add a channel** — Guided: 1) Where? (kind cards, each one line, ntfy Recommended for phones) → 2) its fields with a "where do I find this?" link text → 3) Which alerts? (Critical and warnings, Recommended) → 4) Quiet hours (off / e.g. 22:00–07:00, critical always sent) → 5) Test sent, "Did it arrive?". Quick: kind + its fields.
- Private addresses (a Gotify on the LAN) → the outbound allowlist offer with the reason (ADR-028), confirm it's you.

### 10.3 Activity

```
│  [ Search ] [ Anyone ▾ ] [ Any change ▾ ] [ Last 7 days ▾ ]   │
│                                                              │
│  WHEN        WHO               WHAT                          │
│  09:41       Mohammed          Added person Sara Haddad      │
│  09:40       Mohammed          Signed in (passkey)           │
│  09:12       Linx              Line "Telnyx" went down       │
│  Yesterday   API key "CRM"     Changed extension 110         │
│  Yesterday   (unknown)         Sign-in refused: wrong code   │
```

- Plain sentence per audit entry; row opens the details (exact event, fields changed, address). Never passwords or codes.
- Filters map to `/audit-log` (who, what, when). Cursor paging ("Show more").

### 10.4 Webhooks and API keys (expert)

- **Webhooks**: list (address, events, last delivery ✓/✗, enabled). Detail: events picker (grouped: People, Extensions & phones, Calls, Lines), secret rotation (show once), recent deliveries with **Resend**, disabled-because-failing banner + **Turn back on**. Add: Guided (address → events → HTTPS check with a test delivery) / Quick (address + "all events").
- **API keys**: list (name, scopes summary, created by, last used, expires). Create: name → access (presets: "Read only", "Manage people and extensions", "Everything", custom scopes under Advanced) → expiry (90 days Recommended) → show once. Sensitive scopes → confirm it's you. **Revoke** with confirm. OAuth clients under the same page as a second tab.

### 10.5 Settings

```
│  Numbers         3 digits, from 100     [ Change ]            │
│  Place           Business               [ Change ]            │
│  Country         UAE                    (in Outgoing calls)   │
│  Simple mode     [ on ]  Hides expert pages and settings.     │
│  Admins can sign in from                                      │
│    (•) Anywhere (Recommended)                                 │
│    ( ) Only my home or office network                         │
│        Networks: 192.168.1.0/24 (from setup)  [ + Add ]       │
│  Company sign-in                                              │
│    Google       ● on, shown on the sign-in page  [ Edit ]     │
│    [ + Add a provider ]                                       │
│    [ ] People must use company sign-in                        │
│        (system admins can always use a password or passkey)   │
│  Setup wizard    [ Run it again ]                             │
```

- **Numbers → Change**: the numbering step (§3.2 step 3) as a sheet, including the "renumber first" list.
- **Admins can sign in from**: confirm it's you. "Anywhere" is the default for every admin, password-only ones included; "Only my home or office network" is an option the admin turns on, never forced by how someone signs in (owner, 2026-09-27). Switching to "Only my network" while you are outside it warns: "You're signed in from outside those networks. You'll lose admin pages until you're back." with **Change anyway**.
- **Add a provider** (company sign-in), guided: 1) Which? Google (Recommended for Gmail/Workspace), Microsoft, Authentik, Keycloak, Other → 2) "Create an app at Google" steps with the exact **redirect address** to paste (Copy) → 3) Paste client ID and secret (and the issuer, pre-filled for Google/Microsoft) → 4) Test: "Sign in with Google in a new window" to check it works (links your own account if the email matches) → 5) Show on the sign-in page? (yes). Confirm it's you before saving. LAN providers get the allowlist offer. Quick add: provider + ID + secret (+ issuer).
- **People must use company sign-in**: confirm it's you; lists people who have no linked company account yet ("They can't sign in until they link it: 3 people").

## 11. My account (every person)

From the account menu. Its own page (not admin), tabs on phone width become stacked sections.

```
┌──────────────────────────────────────────────────────────────┐
│  My account                                                  │
│  Sara Haddad · sara@example.com · Ext 101                    │
│                                                              │
│  Passkeys                                        [ + Add ]   │
│   Sara's iPhone     last used today       [ Rename ][ Remove]│
│   MacBook Safari    last used 3 days ago  [ Rename ][ Remove]│
│                                                              │
│  Password               set 2 months ago        [ Change ]   │
│  Authenticator app      on                      [ Replace ]  │
│  Recovery codes         7 left                  [ New codes ]│
│  Company account        Google: sara@…          [ Unlink ]   │
│                          or [ Link Google ]                  │
│                                                              │
│  Signed-in browsers                                          │
│   ● This browser — Chrome on Mac, Dubai    now               │
│   ○ Safari on iPhone                        yesterday [Sign out]│
│   [ Sign out everywhere else ]                               │
└──────────────────────────────────────────────────────────────┘
```

- **Add passkey**: browser prompt → name it (pre-filled from the device). At most 10 (Add greyed with the reason at 10).
- **Remove passkey / Replace authenticator / Remove password**: confirm it's you. Removing an admin's last passkey-or-authenticator is allowed but shows the same "Not recommended" warning box as the first-admin page (§3.1) and needs its checkbox.
- **Change password**: current + new (1C rules, strength hint). **Add a password** if they only have a passkey.
- **New recovery codes**: confirm it's you, shows the 10 new codes once (1C screen); old ones stop working.
- **Link company account**: only for enabled providers; goes through the provider and back with "Linked" or the plain reason (email doesn't match, email not verified).
- Signed-in browsers: from the person's sessions (device + rough place from the address where known, last active).

## 12. Sign-in additions and "Confirm it's you"

### 12.1 Sign-in page

```
┌───────────────────────────────┐
│             [logo]            │
│                               │
│   [ Sign in with a passkey ]  │  ← primary when supported
│                               │
│   [ G  Continue with Google ] │  ← one per enabled provider
│                               │
│   ─────────── or ───────────  │
│   Email    [               ]  │  ← passkey autofill offered here
│   Password [               ]  │
│            [ Sign in ]        │
└───────────────────────────────┘
```

- "Sign in with a passkey" asks for no email (discoverable credentials); the email field also offers passkeys in the browser's autofill where supported.
- Company buttons in the order set; hidden when none.
- "People must use company sign-in" on: the email/password part collapses to a small link "Sign in as a system admin".
- After company sign-in: the 1C authenticator-code step appears when the person has an authenticator or passkey, admins included; a password-only admin isn't asked (their choice, §3.1); a passkey also satisfies that step ("Use a passkey instead").
- Company refusals, all on the sign-in page, plain: "No Linx account uses this Google account. Ask your admin to add you." / "Google hasn't confirmed this email address." / "Your Linx account is disabled." Never more detail.
- Passkey refusals: "That passkey isn't registered here" (or the browser's own cancel → back to the page, no error).

### 12.2 Confirm it's you

```
┌─────────────────────────────────────┐
│  Confirm it's you               ✕   │
│  This change needs you to sign in   │
│  again. It lasts 10 minutes.        │
│                                     │
│  [ Use my passkey ]  (if they have) │
│  ──────────── or ─────────────      │
│  Password   [                  ]    │
│  Code       [      ]  (if they have │
│                        an app)      │
│  [ Continue with Google ] (if linked│
│                  + code if needed)  │
│                       [ Confirm ]   │
└─────────────────────────────────────┘
```

- Opened automatically on a `confirm_required` answer; on success the original action runs again by itself. Cancel leaves everything unchanged ("Nothing was changed.").
- Wrong password/code counts toward the same lockout as sign-in and shows the same generic message.

## Not covered by this spec

Everything under 1F in `docs/ADMIN.md` §2 (ring groups, office hours, the full inbound wizard, voicemail, email invites, call history, undo, the Domain & DNS page) and §14 (SAML, SCIM, custom roles, iOS admin). Their only trace here is the plain "coming" notes on Incoming calls, the setup wizard's People step and System status.
