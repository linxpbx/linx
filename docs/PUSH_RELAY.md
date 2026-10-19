# Push for everyone's Linx — the blind relay

*Design, 2026-10-19. ADR-094. For the owner: §7 is the handful of decisions
only you can make, and nothing below step 2 can be built until §7.1 is
answered.*

## 1. Why this exists

A VoIP push to `com.linxpbx.app` can only be signed by a key issued to the
Apple team that owns that bundle id. Apple will not issue such a key to
anybody else, and a credential cannot ship inside an open-source server —
anyone could then wake any Linx app anywhere.

So today Linx works beautifully for **one** kind of operator: the one who
also publishes the app. Everybody else installs Linx, installs Linx UC from
the App Store, sets a phone up — and **the phone never rings unless somebody
is looking at it**. There is no setting that fixes this and no key they can
obtain. iOS gives an app no way to hold a connection open in the background;
that is the entire reason PushKit exists.

The answer is a small service the publisher runs, holding the one Apple key,
which each Linx asks to send the push.

## 2. The promise this has to keep

A relay in the middle of everyone's calls is exactly the thing Linx exists to
avoid. So the relay is built to learn as little as the design allows, and the
sentence it has to be able to stand behind is:

> **The relay knows that a phone was woken. It does not know who called, who
> was called, or what was said — and it could not tell you if it wanted to.**

Everything in §3 follows from that sentence.

## 3. How it works

```
  Linx (anyone's)                  the relay                 Apple        the app
  ---------------                  ---------                 -----        -------
  seals {call, caller, time}
  to the phone's own key   ──▶   device token + blob   ──▶   APNs   ──▶   opens the blob,
                                 (cannot be opened)                       reports the call
```

**The sealed envelope.** Today a wake push carries the call's id, the
caller's number, the time and the device id in the clear, and Apple sees
them (`docs/PHASE2.md` §5). With a relay there is one more party, so the
details are sealed **end to end, from the Linx server to the app**:

- At enrolment the app makes a **second key**, for encryption only, and gives
  Linx the public half. It is separate from the key that signs its identity
  (ADR-073) because one key should do one job.
- Linx seals the payload to that public key: an ephemeral P-256 key, ECDH,
  HKDF, AES-GCM. The envelope carries the ephemeral public key, the nonce,
  the ciphertext and a **key hint** (four bytes) so a phone holding several
  accounts knows which key to try without unwrapping all of them.
- The app opens it inside its PushKit handler with its Secure Enclave key
  (`SecKeyCopyKeyExchangeResult`), which needs no tap and no face.
- About 200 bytes before base64 — nowhere near the 5 KB a VoIP push allows.

**What the relay therefore holds:** an Apple device token, an opaque blob, a
push kind (`call`, `missed`, `voicemail`), and which server asked. That last
one is a pseudonym (§4), not a domain.

**What it still learns, honestly stated:** that a particular phone was woken,
and when. That is irreducible — Apple learns the same, and so does anyone
watching the network. It is written in §6 and must be on the privacy page.

## 4. Who may push to whom

The weak point of any relay is becoming a way to wake strangers' phones. Two
rules close it:

1. **A server is a pseudonym.** It registers once and gets an opaque id and a
   token. The relay stores no domain, no address, no email — nothing that
   says whose Linx it is.
2. **A phone says who may wake it.** When the app enrols with a Linx server
   it tells the relay "this server may push to me", signed by its device key.
   The relay refuses a push to a token the phone has not authorised for that
   server.

Rule 2 is what makes rule 1 safe: an attacker who registers a server still
cannot push to a phone that never enrolled with them. It also means the relay
never has to decide whether a server is trustworthy — the phones decide, and
a phone that is set up again withdraws the old permission.

Per-server rate limits sit on top, sized so that a small office is never
troubled by them.

## 5. When the relay is not there

A Linx with its own Apple key never uses the relay (ADR-074 stays true, as
the advanced path). For everyone else:

- A push that cannot be delivered is **retried briefly**, then given up on;
  the call still rings every phone that is already awake, and the caller
  still hears ringing, because the wake step already holds a call only for a
  few seconds (`docs/PBX.md` §4).
- `linx doctor` checks the relay the way it checks everything else, so
  "pushes aren't getting out" is a problem an admin is told about rather than
  one they discover from a missed call. `linx watch` then reports it by
  itself.
- **The honest consequence:** while the relay is down, sleeping phones on
  every installation that depends on it do not ring. That is the price of the
  design and it belongs in the admin's face, not in a footnote.

## 6. What changes elsewhere

- `docs/THREAT_MODEL.md` gains a section: a new party, what it holds, what it
  cannot hold, and the two rules of §4.
- The privacy page and `docs/ops/STORE_SUBMISSION.md` both say today that the
  app sends nothing to us and that Apple sees the call's id, the caller's
  number and the time. **Both become wrong** the moment a relay exists and
  must be rewritten in the same release — one sentence for the relay, and the
  sealed envelope actually makes the Apple sentence *stronger*.
- `docs/RESOURCES.md`: the relay's own cost, and the few hundred bytes a
  sealed push adds.

## 7. Decisions only the owner can make

**7.1 Where does it run, and under what name?** It needs a public address and
a certificate. `push.linxpbx.com` is the obvious name; the test VPS could
host it to begin with, but the moment other people rely on it, it wants a
machine whose job is only this. **Nothing past step 2 can be built until this
is answered.**

**7.2 Is the relay the default?** Suggested: a server with no Apple key of
its own uses the relay, and says so plainly in Calls to the app, with one
switch to turn it off. The alternative — making every admin opt in — means
most installations quietly never ring.

**7.3 Who may register a server?** Suggested: anybody, because §4's rule 2
already stops abuse and a gate would only punish honest self-hosters.

**7.4 What is promised about it?** Uptime, and for how long. This is the part
that is a commitment rather than code: people will build a phone system on
the assumption that the relay answers.

## 8. Build order

1. **The design** — this document. *Done.*
2. **The sealed envelope**, end to end, with no relay in sight: the app's
   encryption key at enrolment, Linx sealing the payload, the app opening it.
   Useful on its own (it takes the call's details out of Apple's view even
   for an operator with their own key) and **does not depend on §7**.
3. **The relay service**: register, authorise, send; the Apple key; rate
   limits. Needs §7.1.
4. **Linx chooses**: own key, relay, or neither, in Calls to the app, with
   `linx doctor` checking whichever is in use.
5. **The phone's permission** (§4 rule 2) and withdrawing it when a phone is
   set up again or lost.
6. **The words**: threat model, privacy page, store submission, help guide.

Step 2 starts now; the rest waits on §7.
