# Linx — more than one account on one iPhone

*Owner's instruction 2026-10-19: "I want linx app to support multiple accounts." Asked the same day whether accounts would all be live or have to be switched between — this design says **all live at once**, and why. ADR-093. **Built 2026-10-19: §11 is what each step came to.***

## 1. In plain words
- One iPhone or iPad can be set up on **several Linx servers at once** — your live system and your staging one, or two companies.
- **They are all live together.** Every account rings. Nothing has to be switched to, and no account misses a call because you were "in" another one.
- Each account is still set up exactly as it is today: its own setup code, its own key on this phone, its own extension. Adding a second one changes nothing about the first.
- **Calls**, **Voicemail** and **Team** show everything in one place, with a small label saying which account a row belongs to. The label only appears when there is more than one account, so a phone with one is exactly the app you have now.
- The **keypad** asks which account to call from, once there is more than one, and remembers the last one used.
- **Settings → Accounts** is the list: add one, rename one ("Work", "Staging"), remove one.

## 2. Why all live, and not a switcher
A phone system's one job is to ring. An account you have to switch into is an account that misses calls, which is the opposite of what a phone is for. Linx is also built for it already: each account is a separate enrolment on a separate server, with its own certificate, its own token, its own phone line and its own push registration. Two of them on one phone need **no server change at all** except naming which account a push belongs to (§5).

The cost is two open connections instead of one per account (the phone line and the Team list), and both are quiet — measured and recorded in `docs/RESOURCES.md` with the build. **Four accounts** is the limit; the plain message at the limit says so. Nobody has four phone systems, and an unbounded list is a battery bill waiting to happen.

## 3. What an account is
Everything today's `AppModel` holds, one per account, in a new `Account`:
- the identity and its Secure Enclave key (`PhoneSession`),
- the phone line and its calls (`PhoneModel`),
- the team, the status and the badges (`HomeModel`),
- what Linx has been told about Apple's tokens.

`AppModel` keeps **the list**, which one is the default for outgoing calls, and the routing in §5. Nothing inside `PhoneModel` changes: it already is one line's worth of state.

## 4. Where they are kept
Today the Keychain has two fixed slots, `identity` and `device-key`. They become one pair per account, named by the account's id — the device id Linx issued, which is already unique across servers — with a small index listing them in order and naming the default. The first run after the update finds the old pair, writes it back as the first account and removes the old slots; a phone set up today carries on without noticing. Each account's key stays what it is: its own Secure Enclave key, never shared, never re-used, and removing one account deletes only its own two slots.

## 5. Which account a call belongs to
Apple gives the app **one** push token, so both servers push to the same token. The wake push therefore has to say which account it is for, and today it says only the call's id, the caller's number and the time.

- **Server:** the wake push, the ringing-call notification and the quiet ones gain **`device`: the id of the phone the push was sent to** — the one Linx already issued and the app already stores. Nothing new is disclosed to Apple: the push token itself is the per-phone identifier, and this id is only meaningful to the phone that holds it.
- **App:** the id picks the account, and that account rings, opens its line and answers. A push with no id (a server older than this release) rings on the default account, which is right for every phone with one account and is the only honest answer for the rest.

## 6. CallKit with more than one line
iOS wants **one** call provider for the app. Today each line makes its own, which was right when there could only ever be one. It moves up to the app: one provider, and every call carries the id iOS already gives it, so an answer, a hang-up or a mute is handed to the account whose call it is. Two accounts ringing at the same moment is two calls in iOS's own list, which is what it is built for.

Two details that follow from one provider, found 2026-10-19 while answering the owner's question "while it rings, will it show which account the call is coming through?":

- **The provider's limits have to be raised.** Today `maximumCallGroups` and `maximumCallsPerCallGroup` are both `1`, which is correct for one line — a second caller on the same account still hears busy or goes to voicemail, and that does not change. But with two accounts iOS would refuse the second account's call outright. One group per account, up to the four-account limit.
- **The lock screen gets one line of text, and the account goes in it.** `CXCallUpdate.localizedCallerName` is all there is; the name under it is the app's own (`CFBundleDisplayName`, "Linx"), taken from the bundle and not settable per call, and with one provider it could not vary per account anyway. So an incoming call on a phone with several accounts reads **"Ahmed Khan — Work"**, and with one account it reads "Ahmed Khan", exactly as now. The same line is what a car and a headset announce.

## 7. The screens
- **Settings → Accounts** — each row: the name (the one the person typed, else the Linx server), who it signs in as, the extension, and whether the line is up. **Add account** opens the setup screen the app already has and then **asks what to call the account** (owner, 2026-10-19: the name belongs to setting it up, not to a rename found later); blank falls back to the server's address. Two accounts on the **same** Linx carry their extension as well, since a host they share names neither of them. Swipe to rename, or to remove — today's "Set this phone up again" for that one account.
- **Calls, Voicemail** — one list, newest first, each row labelled with its account when there are several. Badges add up.
- **Team** — one list, a **section per account**: two companies' people are not one directory.
- **Keypad** — a chooser above the pad when there are several ("Call with: Work"), the default remembered. Dialling an extension only means anything inside one account.
- **The call screen** names the account under the caller, when there are several — in the app, and on the lock screen as part of the one line iOS allows (§6).
- **One account: every one of these is invisible.** No new chooser, no labels, no sections.

## 8. Security
- **Accounts are strangers to each other.** Separate keys, separate certificates, separate tokens, separate stores in memory; no screen can read one account's data through another's. Removing one leaves nothing of it behind.
- **The shared push token is unavoidable on iOS** and worth stating plainly: both servers learn the same Apple token for this phone, so two unrelated operators could in principle tell they are talking to the same device. Nothing else crosses over, and neither can send to it without the owner's own Apple key and this app's bundle id. A STRIDE row goes in with the build.
- A push naming an account this phone doesn't have rings nothing — it reports and ends the call the way an unreadable push already does, so iOS stays happy.

## 9. Build order
1. **Storage** — many identities, the index and the default, the migration from the single pair, removal. Tests. *(No visible change.)*
2. **`Account`** — today's `AppModel` becomes `Account`; `AppModel` becomes the list of them, with exactly one account behaving precisely as now. Tests.
3. **One CallKit provider** at the app level, routed by call id; the group limits raised so two accounts can ring at once, and the account's name in `localizedCallerName` (§6).
4. **Push routing** — `device` in the payload (server, small), the account lookup in the app.
5. **Screens** — Accounts, Add account, labels, sections, the keypad's chooser, the call screen's line.
6. **Finish** — app tests and screenshots, `docs/help/iphone-and-ipad.md`, `docs/RESOURCES.md` measured with two accounts, THREAT_MODEL rows, ADR-093, `docs/TEST_MATRIX.md` rows (two accounts ringing; a call on each at once; remove one while the other is on a call).

Steps 1–2 are the refactor and carry the risk; 3–5 are each small. One TestFlight build at the end, and one server release carrying step 4's field.

## 10. Left open
- **Two calls at once, one per account** — iOS can show both; Linx's own line still takes one call at a time per account, which is unchanged and right for now.
- **A second line on the same account** (call waiting) is a different feature and stays where it is in the roadmap.

## 11. Steps 1 and 2, as built (2026-10-19)

**Step 1 — storage.** `PhoneAccounts` and a rewritten `PhoneStore` in `ios/Linx/Core/PhoneIdentity.swift`: a pair of Keychain slots per account id, an index naming the order and the preferred account, the one-time move of the old single pair, and removal that touches only its own two slots. `PhoneSession` knows which account it is (`account:`), and `forget()` removes that one alone.

**Step 2 — `Account`.** `ios/Linx/App/Account.swift` is the whole of what `AppModel` used to be, lifted out unchanged: the state, the identity, the line, the problem, its own `PhoneModel` and `HomeModel`, its `PhoneSession`, and setting up, signing in and signing out. `AppModel` is now the list and nothing else:

- `accounts` (oldest first, all live) and `current` (the one new calls are made from, remembered in the index through `prefer`);
- `account(id:)`, which is how step 4's push will find the account it belongs to, and `woken(device:from:video:)`, which already routes by it and falls back to `current` for a push that doesn't say (§5);
- Apple's **one** token told to **every** account's server, and iOS's notification question asked once for the whole app — `Account.onSignedIn` hands that back up rather than an account reaching for the app;
- `start()`, which signs every account in at the same time, so one account on a slow or unreachable server doesn't hold the others up;
- forwarding members (`state`, `identity`, `line`, `problem`, `phone`, `home`, `linx`, `setUp`, `signIn`, `signOut`) that read and write the current account, so no screen had to change in this step. Step 5 replaces them where a screen really has to show several.

An account knows its id from birth (`storedAs`), before anything has been read out of the Keychain, because the list has to be put back together before any of it has signed in.

Two things worth writing down, both found while building this step:

- **A locked Keychain on a phone that has already been moved to accounts.** The old two slots are deleted by the move, so they answer "not there" however locked the phone is — and a phone woken by a call before its first unlock would have been told to set itself up again instead of waiting for the unlock. `PhoneStore.status` now asks the **index** first, and only falls back to the old slots when the index is readable. This could only ever have shown up on a real phone, after an update.
- **An account that can't be read is still an account.** One whose certificate is gone shows "set it up again"; it is not a free slot, so **Add account** doesn't reuse it and it counts towards the limit of four. Only an account that has never been set up at all is a free slot. The same rule decides whether labels and sections appear (`hasSeveralAccounts` counts the accounts the phone *holds*, not the ones signed in this moment), so a label doesn't come and go as an account signs itself in.

Tests: `ios/LinxTests/PhoneAccountsTests.swift` — the storage suite from step 1, plus "The list of accounts" (one account is the whole app and signing out keeps it; every stored account comes back with the chosen one current; a push's id picks its account; the choice is remembered; signing out of one leaves the others; four is the most and an empty account is never doubled) and the locked-after-the-move case. `make ios-lint`, the full `make ios-test` (227 tests) and `make ios-build-device` all green.

## 12. Steps 3 to 6, as built (2026-10-19)

**Step 3 — one call provider.** `ios/Linx/Core/Call/SystemCallsRouter.swift`. The app has one `SystemCalls` underneath (CallKit, or the in-app ringing where CallKit may not be used) and hands each account a `SystemCallsLine` of its own, so nothing in `PhoneModel` changed — it still asks the system and is told by the system, and never learns another account exists. Every call is written down against its line **before** the system is told about it, because the system can answer a call before `reportNewIncomingCall` has returned. Three things the tests pinned down:

- **The provider's limits.** `maximumCallGroups` was 1, which would have made iOS refuse the second account's call outright; it is now one group per account (four), with one call per group, so a second caller on the *same* account still goes to voicemail exactly as before.
- **The sound.** CallKit hands over the microphone and the speaker without naming a call, so the router follows the call most recently answered or placed, and falls back to the only line that has a call. Every reference the router holds to a line is weak, and the line holds the router; a call nobody owns is ended rather than guessed at.
- **The lock screen's one line of text.** `localizedCallerName` is all there is, and the name under it is the app's own (`CFBundleDisplayName`), so the account shares that line: "Ahmed Khan — Work", and just "Ahmed Khan" with one account.

**Step 4 — push routing.** The server adds `device` — the id of the phone the push was sent to — to the wake push, the ringing-call notification and the quiet ones (`internal/push`). The payload is therefore written per phone rather than once per call. In the app, `PushService.call(in:)` reads it and `AppModel.woken(device:…)` rings that account; a push with no id rings the account calls are made from. **A push to a closed app launches it before anything is set up**, so `woken` reads the accounts out of the Keychain first — otherwise the call would ring on whichever account happened to be first. `docs/PHASE2.md` §5 and `docs/THREAT_MODEL.md` were corrected, since both promised Apple is told nothing but the call, the caller and the time.

**Step 5 — the screens.** Settings → **Accounts** (`AccountsView`): each account by its Linx server, with who it signs in as, its extension and whether its line is up; **Add account** opens the setup screen the app already has, on an empty account that is dropped again if the screen is closed; swipe to name or remove. Calls and Voicemail are each **one merged list**, newest first, labelled per row, with a per-account page cursor so "show older" asks every account that has more. **Ringing back goes out on the account the call came in on** — each row carries its account and the row is given that account's phone line. Team keeps a **section per account**. The keypad gets **Call with**, the call screen names the account under the caller, and the badges add up. Three things that came out of building it:

- **Favourites were keyed by extension number alone**, so starring 101 at work would have starred a different person on 101 elsewhere. They are per account now, and the stars from before are given to the account they can only have come from (`Favourites.belongTo`).
- **Merged rows need composite ids.** Each Linx numbers its own calls and messages, so a row is named by account *and* record.
- **A view must ask the thing it is showing**, not the whole app: the call screen reads `PhoneModel.accountLabel` and the speed dial reads `HomeModel.account`. The first attempt had both reaching for `AppModel`, which broke every screen rendered without one — the app's own tests caught it.

**Step 6 — the finish.** ADR-093; this document; `docs/THREAT_MODEL.md` "More than one account on one phone" (eight rows, including the one thing that genuinely crosses between accounts); `docs/TEST_MATRIX.md` **section 5d** (13 rows needing a second Linx); the help guide's "More than one Linx on one phone"; `docs/RESOURCES.md` measured against a build of `0221439` — **the app +412 KB, the control plane +4 KB (4096 bytes), no new dependency, container, timer, migration or endpoint**; and the `accounts` screenshot (`ios/screenshots/iPhone-17/accounts-light.png`, `-dark.png`).

**The phone bench proves it** (2026-10-19, `internal/browsertest/phonebench_docker_test.go`, `ios/tools/bench.sh`). Two scenarios, both passing on the real stack:

- **"two accounts on one phone: the second one rings"** — the phone that is rung holds **two** accounts (extensions 202 and 203 of the same Linx), set up one after the other as the app starts, and the caller rings the **second** one. Mid-call, with the second account busy, Asterisk is asked which lines it has: all three are registered at the same moment (`lines registered: map[201:true 202:true 203:true]`), which is the feature's whole claim — every account live at once, nothing to switch into. The call is reported by the account that was rung and **not** by the other one, which is the app's own routing seen from the server's side; and the idle account is still registered after the call ends.
- **"two accounts on one phone, China: the idle account stays live"** — the same with no UDP at all, everything through the relay over TLS on 443, to check the idle account isn't quietly dropped while the other one is busy on the one connection a restricted network allows.

New bench flags: `second=LINK` (the phone that is rung sets up a second account from that link; it belongs to the phone being rung whichever side is working a camera) and `call-after=N` (the caller waits for both accounts to be ready). `Bench.secondSetupLink` in the app, redeemed by `AppModel.setUpTheBenchsSecondAccount` once the first account is signed in.

**Still only on a real phone:** CallKit itself — a simulator's own call service ends an app's outgoing CallKit call about a second in, so **two accounts ringing at the same moment**, the account's name on the lock screen, and a push naming its account are what `docs/TEST_MATRIX.md` §5d is for. That needs a setup code from a second Linx.
