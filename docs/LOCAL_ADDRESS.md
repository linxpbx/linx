# Linx — the app knows a local address as well as a public one

*The roadmap's "Right after Phase 2", step 1 (owner, 2026-10-18): "on Wi-Fi the app tries Linx's local address first and falls back to the public one; on mobile data it goes straight to the public one. Calls at the office stop depending on the office's internet." This file is the design and the build order. **Not built yet — §10 is what the owner decides before it starts.***

## 1. In plain words
- At the office, the app talks to Linx **straight across the office network**, instead of going out to the internet and back in through the front door.
- Away from the office — mobile data, a hotel, anywhere else — nothing changes: the app uses the public address, exactly as today.
- The app works it out by itself, in about a second, each time the phone joins a network. Nobody chooses an address, and there is no setting to get wrong.
- **The security doesn't change.** Linx still has to prove it is Linx, with its own certificate, on the local address exactly as on the public one. If it can't, the app uses the public address — it never settles for less.
- What it buys: internal calls that keep working when the office's internet line is down or slow, calls that start faster at the office, and nothing passing through the front door that doesn't have to.
- What it does **not** buy: an iPhone that is asleep still needs Apple's push to ring, and that needs the internet (§7). With the line down, the web app and desk phones ring; a sleeping app does not.

## 2. What happens today
The app holds one address — the public name, `https://<domain>` — and sends everything there: signing in, the phone line, the `/sip` line that carries calls, the call link, the Team list, history, voicemail.

At the office that means a call to the desk opposite goes out to the router, is turned round at the public address (or at the front door machine), and comes back — when the router does that at all. If the internet line is down, DNS usually stops answering too, so the app can't even find Linx, three metres away.

Desk phones don't have this problem: setup already keeps a name, **`sip.<domain>`**, pointing at the server's own address on the office network (`192.168.1.213` at home), and the phones use it. The app has never been told about it.

The sound and the picture are already fine — WebRTC finds the short route by itself on the same network. This is only the **call-setup line**.

## 3. The local door (server)
**`sip.<domain>` is the local address.** It exists already, setup keeps it up to date, and the wildcard certificate covers it, so there is no new name, no new record and no new certificate to get.

What has to be true is that Linx **answers HTTPS on the office network**. Today that depends on the front door:

| Front door | On the office network today |
|---|---|
| Linx takes 443 | Linx's own port-443 router (`linx-sni`) already answers on the server's office address. **Nothing to do.** |
| Home only | Same. **Nothing to do.** |
| Another public port (8443…) | Same, on that port. **Nothing to do.** |
| Another program passes Linx through (Pangolin, nginx, HAProxy) — *the owner's setup* | **Nothing of Linx's answers.** Its web port is published on the office address but the firewall lets only the front-door machine reach it, and it insists on a front-door header no phone sends. |
| A proxy that decrypts (Caddy, NPM) | Same: nothing. |
| None | Same: nothing. |

So the one new thing on the server: **`linx-sni` runs in every setup**, bound to the server's office address, with the configuration it already generates. Where it is already the public door it is the same container doing the same job; where it isn't, it is started for the local door alone.

- **Port:** the one it already uses (443, or the chosen public port). If the front-door program runs on the Linx server itself, 443 is taken there, so setup uses **5443** instead and writes it down.
- **Firewall:** nothing to change. The firewall only ever *drops*, and it drops the web port and the relay's TLS port, not this one. Where the local door is a port of its own (5443), setup guards it so only the office network reaches it.
- **Who Linx thinks is calling:** `linx-sni` tells the control plane the phone's real office address, the same way it does for the public door, so sign-in limits, audit entries and "Check it" keep telling the truth.
- **Where there is no certificate for `sip.<domain>`** — a server installed without a DNS key, which has a certificate for the public name only — there is **no local door** and the app carries on as today (§10, D2).

**The server tells the app** its local address, in the same three answers the app already reads (setting up, every sign-in, every phone line):

```json
"local": { "url": "https://sip.pbx.example.com", "address": "192.168.1.213" }
```

Both, because of §6. A server that doesn't send it, or sends nothing, leaves the app exactly as it is now — so an old app with a new server, and a new app with an old server, both keep working.

**`linx doctor`** gains one line under "Phone system": the local door answers at the office address, with certd's certificate, for `sip.<domain>`.

## 4. How the app chooses
1. The app keeps the local address beside the public one, and refreshes it every time it signs in.
2. iOS says what kind of network the phone is on. **Mobile data, or no local address known: the public address, and no probe** — that is every call away from the office, and it costs nothing.
3. **On Wi-Fi (or a wired iPad), the app probes the local door once:** a small request (`GET /healthz`) with a **1.5-second deadline** and the certificate checked in full. It answers → the local address is used for everything until the network changes. Anything else — refused, silent, wrong certificate, the wrong machine on a hotel network — → the public address. The cost of being somewhere else is one and a half seconds, once per network, in the background.
4. **The probe runs** when the app starts, when the phone changes network, and when the line has to reconnect. **Never during a call**: a call keeps the address it started on.
5. **Falling back is immediate.** If a request to the local door fails to connect, the app marks it gone, sends that same request to the public address, and stays there until the next network change. Walking out of the office mid-use is an ordinary reconnect, which is what the app already does.
6. The choice covers **everything** the app sends to Linx, not just calls: the Team list, history and voicemail then work on a dead internet line too.

## 5. Settings and Call details
- **Settings → This phone's line** gains one line: *"Connected to Linx directly on this network"* or *"Connected over the internet"*, with the address under it.
- **Call details** records which one the call was set up on, next to what it already records. The owner reads these when something is slow, so the address has to be in there.

## 6. When the internet is down, DNS is down too
This is the part that decides whether §1's promise is real. `sip.<domain>` is a public name: looking it up normally needs a DNS server, and with the line down the router can't answer either. Two more cases fail the same way — a hotel or office network whose router refuses to answer a public name with a private address (rebinding protection), and a phone on a network with its own blocked-name list.

So the server sends the **address** as well as the name, and the app does what it already does for the call relay (`RelayAddresses`: by address, with the name kept for TLS): when the name won't resolve, it connects to the remembered address and **still requires a certificate valid for `sip.<domain>`**. Nothing is trusted that wasn't trusted before — the name is checked either way; only the lookup is skipped.

## 7. What this does not fix
**A sleeping iPhone still needs the internet to ring.** The wake-up goes through Apple's push servers, which are outside. With the office line down: the web app rings, desk phones ring, an app that is awake and on the local door rings — a closed or suspended app does not, and no design on our side changes that (iOS gives no way to wake an app from the local network). Worth saying plainly in the help guide, because "the office works without the internet" will otherwise be read as covering it.

## 8. Security
- **Nothing is relaxed.** The same certificate, the same name, the same checks. No exception list, no pinning changes, no plaintext, no "if it fails, try it anyway".
- The local door is the **same** control plane behind the **same** router process — one more way in to the same place, not a second, weaker one. Tokens, scopes, limits and the relay's rules are unchanged.
- A phone on someone else's network that reaches *something* at the remembered address gets a certificate it can't accept, and moves on. The worst it costs is a second and a half.
- **New in the threat model:** an attacker already on the office network can reach the local door. They could reach the front door from there anyway, so what changes is only that the sign-in limits now count office addresses one by one instead of all as the front door — an improvement. STRIDE rows go in with the build.

## 9. Resources
- Setups that already run `linx-sni`: **nothing new**.
- Setups that don't (the owner's): one more container, about **13–19 MB** by the figures in `docs/RESOURCES.md` — to be measured again with the build and recorded.
- One small request per network change on the phone. No timer, no polling, no battery cost worth naming.

## 10. To decide (owner)
- **D1 — Build it as above?** *Recommended: yes.* It is the roadmap item, and it is small: one container started in setups that lack it, one field in three API answers, and the choosing in the app.
- **D2 — Servers with no DNS key** (certificate for the public name only, so none for `sip.<domain>`). *Recommended: leave them on the public address for now.* The alternative is to have Linx's own certificate authority issue the local one and the app trust it for that single name — the app already keeps that authority from setup for exactly this. It is a sound design and perhaps half a day, but it is the first place the app would trust Linx's own authority, so it deserves its own ADR and its own session. The owner's own servers have a DNS key, so nothing of theirs waits on it.
- **D3 — The web app too?** *Recommended: not now.* A browser at `https://sip.<domain>` is a different address as far as the browser is concerned, so it is a second sign-in, a second set of saved passkeys and a second bookmark — a worse experience for a gain that the app, which actually travels, needs more. Revisit with "desk phones from outside the office".
- **D4 — Which name?** *Recommended: `sip.<domain>`, the one that exists.* Setup keeps it pointing at the server, the wildcard covers it, desk phones already use it, and a second name would be one more thing to go stale.

## 11. Build order (one server update, one app build)
1. **The local door and the field** — `linx-sni` in every setup, the port rule, the firewall guard for a port of its own, `local` in the three API answers, `linx doctor`'s line, the browser suite proving a front-door setup answers on its office address. *Server.*
2. **The app chooses** — the probe, the network watch, the fallback, the by-address path of §6, and the tests for each rule. *App.*
3. **Settings and Call details** show which address is in use.
4. **The bench**, then the owner's try: a call at home with the internet unplugged at the router (two apps, and an app to the web client), and a call on mobile data to prove nothing changed away from the office.
5. Help guide and `docs/TEST_MATRIX.md` rows; `docs/RESOURCES.md` measured; THREAT_MODEL rows; ADR.

## 12. Left open
- **Desk phones from outside the office** (`docs/ROADMAP.md`) is the mirror image of this and shares the port thinking; design it next, with the owner.
- **Linx Connector** (Phase 4) is the same idea for a *remote* office, and should reuse whatever this step builds for choosing an address.
