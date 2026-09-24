# Web client — low-fidelity screen specs (Phase 1C, step 1)

Layout only — no colour, type or icon decisions here (those are fixed already, in `linx-tokens.json` / `DESIGN_TOKENS.md`, and applied when the real screens are built in step 5). This is for the owner to sign off on **what's on each screen and where**, before any UI code is written (`docs/ROADMAP.md` UI rule).

Scope is exactly `docs/WEB.md` §6: sign-in, the app shell, dialer, incoming call, active call panel, Team list, settings. Everything else visible in `Web · Console & presence@1x.png` (call history, voicemail, meetings, inbox, reports, parked calls, support queue) is a later phase and appears here only as greyed-out nav items, so the shell doesn't have to be rebuilt when they arrive.

Boxes below are wireframes (element placement), not pixel layouts.

## 1. Sign-in

Three steps in one screen, one shown at a time. Small bundle — this is the page a phone on the road loads first.

```
┌───────────────────────────────┐
│                                │
│             [logo]             │
│                                │
│   Email                        │
│   ┌───────────────────────┐   │
│   └───────────────────────┘   │
│   Password                     │
│   ┌───────────────────────┐   │
│   └───────────────────────┘   │
│                                │
│            [ Sign in ]         │
│                                │
└───────────────────────────────┘
```

- **Step A — email + password.** On failure: one generic message ("Wrong email or password"), never "no such account". While an account is in its lockout wait, the same generic message — no mention of lockout (WEB.md §4).
- **Step B — authenticator code** (admin/system_admin only; skipped for `user` accounts without MFA). Replaces the form with a single 6-digit code field + "Use a recovery code instead" link.
- **Step C — first sign-in only:** "Choose a password" (12+ chars, rejected if in the common-password list, live strength hint) → for admin/system_admin, "Set up your authenticator app": QR code + manual key, one 6-digit field to confirm, then a screen listing the 10 recovery codes with a "Copy" and "Download" action and a confirm checkbox ("I've saved these") before continuing.
- No "forgot password" link in this slice — accounts are provisioned by an admin (`docs/WEB.md` §4); self-service reset is later.
- States: default, submitting (button spinner, form disabled), field error, lockout wait (form disabled, no timer shown to the user).

## 2. App shell

Same skeleton as `Web · Console & presence@1x.png` — sidebar, top bar, main content — so later phases only add rows and panels, not restructure it.

```
┌───────┬──────────────────────────────────────────────┬──────────┐
│ logo  │  [ Search people or dial a number ]           │          │
│       ├──────────────────────────────────────────────┤          │
│ Dialer│                                                │  (right  │
│ Team  │              main content                     │  panel,  │
│ ───── │              (Dialer or Team screen)           │  active  │
│ Call  │                                                │  call    │
│  hist.│                                                │  only —  │
│ Voice.│                                                │  see §5) │
│ Meet. │                                                │          │
│ Inbox │                                                │          │
│ Rprts │                                                │          │
│       │                                                │          │
│ ───── │                                                │          │
│⚙ Sett.│                                                │          │
│ [me]  │                                                │          │
└───────┴──────────────────────────────────────────────┴──────────┘
```

- Nav items **built and clickable this phase:** Dialer, Team.
- Nav items **shown but disabled** (greyed, no click, no badge counts): Call history, Voicemail, Meetings, Inbox, Reports. Tooltip on hover: "Coming soon".
- **Settings** (gear, above the account row) is built this phase — see §7.
- Account row at the bottom: name, extension, own presence dot + status text; click opens a small menu with "Set status" (Available/Away/Do not disturb) and "Sign out". Signing out ends the session and the browser phone line at once (WEB.md §4).
- Top search bar: free text filters the Team list by name/extension; typing digits offers "Call <number>" and jumps straight to an outgoing call (no separate numeric dial pad needed at the top level — the full keypad lives in the dialer screen and the active-call panel).
- Right panel is empty/hidden when there's no active call; becomes the active-call panel (§5) the moment one starts, on top of whichever screen (Dialer or Team) is open underneath.
- No "New meeting" button and no low-data toggle in this slice — both are later phases (meetings: Phase 3).
- Responsive: below a phone-width viewport, the sidebar collapses to icons only (labels in a tooltip), and the right panel becomes a full-screen overlay when a call is active instead of a side column.

## 3. Dialer

```
┌──────────────────────────────────────────────┐
│                                                │
│   ┌────────────────────────────────────┐     │
│   │  Enter a name, extension or number  │     │
│   └────────────────────────────────────┘     │
│                                                │
│              1        2        3              │
│              4        5        6              │
│              7        8        9              │
│              *        0        #              │
│                                                │
│                 (  Call  )                     │
│                                                │
│   Recent                                       │
│   • Sara Haddad · Ext 1024 · missed, 09:14     │
│   • +971 50 000 4417 · outgoing, yesterday     │
└──────────────────────────────────────────────┘
```

- Typing a name filters teammates by name/extension and shows matches above the keypad (click to call); typing digits dials a raw number.
- The keypad also sends DTMF during an active call (same component, reused inside §5 when its keypad is opened).
- "Recent" list is calls made/received/missed from this session onward — call history storage is a later phase, so this list does not need to survive a sign-out (plain-language note under the list: nothing older is kept yet).
- Pressing Call moves the whole app into the active-call state (§5); the dialer screen stays underneath, dimmed, until the call ends.

## 4. Incoming call

Shown as an overlay while the page is open (no push — WEB.md §10), over whatever screen the person was on.

```
┌──────────────────────────────────────┐
│                                        │
│              (avatar/initials)         │
│                                        │
│             Sara Haddad                │
│             Ext 1024                   │
│                                        │
│                                        │
│   (  Decline  )      (  Answer  )      │
│                                        │
└──────────────────────────────────────┘
```

- Browser tab title and favicon badge change to signal a ringing call while the page is in a background tab; the ring tone respects the volume set in Settings (§7).
- Unknown caller (outside trunk, later phase) falls back to showing the raw number in place of a name — noted for forward compatibility, not built this phase.
- Answer transitions directly into the active-call panel (§5). Decline dismisses the overlay and the call ends; no voicemail in this slice (WEB.md §10).

## 5. Active call panel

The right-hand column from the shell (§2), built to the detail in WEB.md §6.

```
┌──────────────────┐
│ Direct · 24 ms     │  ← connection chip
│                    │
│  (avatar) Sara     │
│  Haddad             │
│  Ext 1024           │
│      04:12          │  ← call timer
│                    │
│ [mute] [keypad] [end]│
└──────────────────┘
```

- **Connection chip:** "Direct" or "Relayed", plus round-trip time in ms, updated live (WEB.md §6). Relayed = TURN in use.
- **Controls:** mute/unmute (toggle, pressed state), keypad (opens the same numeric pad as §3 as a small popover, sends DTMF), end call (destructive colour, per tokens' `color.end`).
- No hold, transfer, park or add-participant controls — those are the greyed icons in the full mockup, not built until Phase 4 (WEB.md §10).
- Ringing-out state (before the other side answers): same panel, timer replaced with "Calling…", mute/keypad disabled, only End shown.
- Reconnecting state (ICE restart after a network change, WEB.md §6): connection chip shows "Reconnecting…" in place of the mode/RTT; call continues, no interruption to the controls.

## 6. Team list

Same table shape as `Web · Console & presence@1x.png`, trimmed to what step 4/5 of this phase actually wires up.

```
┌──────────────────────────────────────────────┐
│  Team          32 people · 21 reachable        │
│                                                │
│  NAME            EXT    STATUS                │
│  Sara Haddad     1024   On a call · 04:12      │
│  Daniel Reyes    1040   Ringing                │
│  Aisha Rahman    1042   Available               │
│  Yusuf Nasser    1044   Away                    │
│  Chen Wei        1047   Offline · last seen …   │
└──────────────────────────────────────────────┘
```

- One flat, sortable-by-name list — no department grouping, favourites or filter chips yet (those need data this phase doesn't add); a plain-language note can say "Grouping and favourites are coming".
- Status text + dot per `DESIGN_TOKENS.md`'s presence table, sourced from the existing ARI state over the small events websocket (WEB.md §6). No "Devices" column (device-kind detail is an admin-portal concern, 1E).
- Click a row (or the phone icon) to call that person — goes straight to the active-call panel (§5); no per-row video/chat icons this phase (video is a later phase; chat doesn't exist yet).
- Empty/loading states: skeleton rows while the websocket connects; "Nobody else has an account yet" when the list is genuinely empty.

## 7. Settings

Reached from the gear icon in the shell (§2). A single scrollable panel, not a modal, so it can be deep-linked.

```
┌──────────────────────────────────────┐
│  Settings                              │
│                                        │
│  Microphone     [ Built-in mic ▾ ]     │
│  Speaker        [ Built-in output ▾]   │
│  Ringtone volume  [────●────]          │
│                                        │
│  ( Test sound )                        │
└──────────────────────────────────────┘
```

- Microphone/speaker: dropdowns listing the browser's available input/output devices (permission prompt triggered on first open if not already granted).
- Ringtone volume: slider, persisted per browser (local device setting, not synced — nothing here is account data).
- **Test sound** places a call to `*43` (the echo test dialplan from Phase 1B) using the currently selected devices, so the person can hear themselves before their first real call.
- No appearance/theme, notification or account-security settings here — password/MFA management lives under the account-row menu from §2, not this screen, and isn't built until step 3.

## Not covered by this spec

Everything in `docs/WEB.md` §10 ("Not in this slice"): push/PWA ringing when closed, video, hold/transfer/park, voicemail, call history storage, company sign-in/passkeys, the admin portal, and public 5061 for remote desk phones. The greyed nav items above are the only trace of them in this phase's UI.
