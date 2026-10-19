# Linx — more than one account on one iPhone

*Owner's instruction 2026-10-19: "I want linx app to support multiple accounts." Asked the same day whether accounts would all be live or have to be switched between — this design says **all live at once**, and why. ADR-093. **Being built: §9 is the build order.***

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
- **Settings → Accounts** — each row: the name (the person and the Linx server, or a name the person typed), the extension, and whether the line is up. **Add account** opens the setup screen the app already has. Swipe to remove, which is today's "Set this phone up again" for that one account.
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
