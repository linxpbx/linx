# Linx — Simpler phone lines, front doors and DNS

*Status: design drafted 2026-09-29 from the owner's request during the install demo ("a very painful process to add the UCM"; "one unified setup" for reverse proxies; "I don't expect integration with every DNS company"). **Approved by the owner 2026-09-29.** ADR-061 to ADR-063. Built in the phases in §4, not before. The low-resource and low-bandwidth rule (CLAUDE.md) applies to all of it.*

## 1. Phone lines: a PBX or gateway signs in to Linx (ADR-061)

### 1.1 Why the UCM was painful
Linx reached **out** to the UCM, as it does to an internet provider. For that to be encrypted, the UCM needed a certificate naming its own IP address, Linx had to pin it (`linx trunk cert`), the trunk needed the special "LAN peer" mode, and the UCM's firmware needed exact settings (docs/DEMO_PHASE1D.md step 7). Every other PBX or gateway (Grandstream GXW/HT, Yeastar, FreePBX, an FXO adapter) would need the same, differently.

### 1.2 The simpler way: reverse the direction
The PBX or gateway **signs in to Linx, exactly like a desk phone does.** Every one of them can do this: it's the ordinary "SIP trunk with registration" every provider uses, with Linx as the provider.

- **In Linx:** Phone lines → **Add** → **"Another phone system or gateway"** (next to "An internet phone company"). Linx asks for a name ("UCM landlines") and shows a settings box once, the same box desk phones get (docs/ADMIN_SCREENS_PHASE1E.md §5):
  - Server: `sip.<domain>` (the owner's decision: phones and gateways use `sip.`), port **5061**, transport **TLS**, audio encryption **SRTP**
  - Username and password (130 random bits, shown once; only the digest hash kept, as for devices)
  - "How to enter this" with steps for the common ones (Grandstream UCM and GXW/HT, Yeastar, FreePBX/Asterisk, a generic one)
- **On the PBX or gateway:** add a **register-type SIP trunk** with those values. Nothing else: no certificate to make on it (it checks Linx's real certificate like any provider, or skips checking where its firmware can't), no pinning, no "LAN peer" mode, no Linx-side address to type.
- **Calls both ways go over that one connection:**
  - Out: Linx sends the call to wherever the gateway is signed in from (as it rings a desk phone).
  - In: the gateway's calls arrive signed in with its username, so Linx knows the line by its login, not its address. Calls land in the same safe place as every trunk's (`linx-from-trunk`: only that line's numbers, never back out).
  - **A line without numbers** (an FXO landline often sends none): one "calls on this line ring…" choice (an extension or, in 1F, a ring group), instead of a list of numbers.
- **Where it may sign in from:** the phone networks only (the same ACL as desk phones), unless the admin picks "from anywhere", which needs TLS and gets the password-guessing protection public SIP needs (docs/PBX.md §6; not built yet, so "anywhere" waits for it). Through the Site Connector later, a remote office's gateway signs in the same way.
- **Old gateways without TLS:** plain SIP (TCP/UDP) on the phone networks only, after the ADR-023 warning, as today.
- **Status for free:** signed in or not, since when, from which address, in the Phone lines list, the "line down" alert and `linx doctor`, like a device's online state.

**As built underneath:** a new trunk kind `registers_here`. Its endpoint is rendered like a device (an `auth` with the digest hash, an `aor` with `max_contacts=1` and `remove_existing`, identified by the login, not by address), in `linx-from-trunk`, on `transport-tls`. The existing kinds stay for internet providers (`registration`, `ip_authenticated`); `lan_peer` stays for a PBX that can't register anywhere, and the UCM moves to `registers_here` (its "LAN peer" setup and `linx trunk cert` become the fallback).

### As built (Phase 1E step 7, part 1, 2026-09-29)
- **Kind `registers_here`** (migration `0028_registers_here.sql`): no host (`''`), always TLS + SRTP, never WireGuard, no stored password; its login name is its endpoint name, `trunk-<id>` (Asterisk tells whose request it is by the endpoint's *name*: `identify_by=auth_username` looks up the endpoint named after the Authorization username), and only the digest hash is kept (`trunk.digest_hash`, as for devices). The database checks all of it (`trunk_registers_here_login`, `trunk_registers_here_encrypted`). Plain-SIP gateways stay `lan_peer` for now (5062 isn't published on the LAN).
- **"Calls on this line ring…"**: `trunk.rings_extension_id` (any kind) → `asterisk.linx_line_rings(endpoint)` (`SECURITY DEFINER`, like `linx_inbound`); the dialplan's `linx-trunk-call` tries it only after no DID matched (`LINX_LINE_RINGS`). Deleting an extension now also clears it from lines and phone numbers.
- **Asterisk** (`internal/trunkconf.writeSignedIn`): aor `max_contacts=1`/`remove_existing`/qualify 60 s, auth `md5_cred` in realm `linxpbx`, endpoint on `transport-tls` in `linx-from-trunk`, `identify_by=auth_username,username` (a gateway's INVITE usually carries the caller's number in From: the first INVITE isn't identified, so the artificial endpoint challenges it; pjsip.conf's `[global] default_realm=linxpbx` makes that challenge's realm the one the digest hash was made with), `from_domain=sip.<domain>`, no identify-by-address, nothing added to the ACL.
- **Status** (`trunkstatus.DecideSignedIn`): its registered contact → `registered` ("It's signed in to Linx."); never signed in → `unknown` ("Waiting for it to sign in"), so no "line down" alert while it's being set up; once signed in, losing it → `unreachable`. `POST /trunks/{id}/test` answers from that (step `signed_in`).
- **API**: `kind: registers_here` (422 `registers_here_fixed` if host/login/connection fields are sent), `rings_extension_id` on create/patch, the one-time `login` block (username, password, server, port, TLS, settings text) in the create answer and in the new `POST /trunks/{id}/reset-password` (409 `no_login_here` for other kinds); both need "confirm it's you" in a session.
- **CLI**: template `phone_system` ("Another phone system or gateway (it signs in to Linx)") in `linx trunk add`: name, numbers, outgoing; prints the login once. The UCM template is now "Grandstream UCM, Linx connects to it (advanced)".
- **Tests**: `internal/trunk` (`registershere_test.go`), `internal/trunkconf`, `internal/trunkstatus`, CLI, `internal/store` docker (database checks, `linx_line_rings`, extension delete), and the call suite's "a phone system that signs in to Linx" on real Asterisk: signs in with the made login, status `registered`, Alice's mobile call goes out through it, its call for its number with the caller's number in From rings Bob, a call for none of its numbers rings its extension and never goes back out, a wrong or replaced password is refused.
- Not yet: the screens (part 2), the "how to enter this" steps per product, signing in "from anywhere".

### 1.3 Internet phone companies: paste, check, done
Most providers' setups are the same few fields, which is why templates work.
- **Paste what the company sent you** (the email or the web page's settings text): Linx reads the usual labels ("SIP server", "registrar", "proxy", "domain", "username", "auth ID", "password", "port", "transport", "DID"/"number") and fills the form, showing what it understood for a yes before saving. Nothing is sent anywhere to read it.
- **Or pick the company** from the list (the templates already built), then only the username, password and number are asked.
- **Test** runs the existing connection test (address, TLS, sign-in) and says what's wrong in plain words ("the password was refused", "the company's address can't be reached from this server").
- Quick add and Guide me as everywhere else (docs/ADMIN.md §2).

## 2. Front doors: one method, every proxy (ADR-062)

### 2.1 What every front door that works does
Pangolin, nginx, HAProxy, Nginx Proxy Manager, Caddy, a router forwarding TCP 443, and Linx's own port 443 router all do the same three things:
1. **Pass `<domain>` and `turn.<domain>` through untouched**: by name (SNI), without decrypting, so Linx's own certificate reaches the browser.
2. **Send them to Linx's address**: `<Linx>:8443` for the domain, `<Linx>:5349` for `turn.`.
3. **Tell Linx the visitor's address** with the PROXY protocol (v2) on the first; the second has none (coturn can't read it).

### 2.2 One "front door" card, the same everywhere
Setup's front-door step and System → Server settings show one card with exactly those three facts, each with Copy, and under it **"How to do this in…"** with instructions generated from the same facts for:
- **Linx takes port 443 itself** (nothing else to do; recommended on a rented server)
- **Pangolin**: the lines for its Traefik config file, named after the domain so several Linx servers never clash (built 2026-09-28). *Checked 2026-09-30 on the owner's Pangolin 1.23 EE:* its web page's raw TCP resources can send PROXY v1/v2 but are routed by port (their own entry point, `HostSNI(*)`), not by name, so they can't share 443 with Pangolin's own sites; the web page isn't offered.
- **nginx or HAProxy**: their stream (TCP) blocks
- **Nginx Proxy Manager**: its "Streams" page (by port, since it can't route TCP by name: 443 to Linx only when nothing else needs 443)
- **Caddy**: its layer-4 module (caddy-l4 as a listener wrapper in front of Caddy's own TLS, so Caddy's sites keep 443)
- **A router or firewall** forwarding TCP 443 straight to Linx (no proxy at all)

Choosing a product only changes the instructions shown, not how Linx is set up: Linx's side is the same for every proxy (PROXY v2 required from the proxy's address, refused from anyone else). The front-door kinds collapse to three: **Linx takes 443**, **another program passes it through** (with its address), **home only**.

### 2.3 "Check it"
- **From the server:** both names resolve to the right address at the domain's own name servers, and the address answers with Linx's certificate on 443 (the doctor's checks, on the page, in plain words).
- **From outside:** a home network often can't reach its own public address from inside (no "hairpin"), so the page offers a short link to open on a phone **with Wi-Fi off**. It shows ✓ and the visitor's address as Linx saw it, which also proves the PROXY header works.

### 2.4 Decrypting proxies become a last resort
"Caddy or Nginx Proxy Manager" today means the proxy decrypts. That's the one path with special cases: a DNS token asked on the first page, forwarded-address headers instead of PROXY, a separate port for call audio. Both products can pass through instead (§2.2), so that's what the card recommends. Decrypting stays under "Something else (advanced)" with a plain warning, and isn't offered on a rented server.

### 2.5 Public port (advanced): behind a router, 443 taken, no proxy (ADR-064)
*Design drafted 2026-09-29 at the owner's request; **owner decisions 2026-09-29** (§5 item 4): the relay's TLS shares the web port, the UDP port is asked separately, 8443 suggested but any allowed port can be typed, and it's offered on rented servers too. Built in Phase 1F. Not built yet.*

**When.** Linx is at home or in an office behind a router, nothing can pass it through on 443 (another program owns public 443 and can't forward by name), so the router forwards another public port, e.g. **8443 → Linx's 443**. Setup offers this **only after** the front-door question (§2.2) finds nothing on 443 that can pass Linx through, marked *Advanced*, with the warning below.

**On a rented server** (owner decision 2026-09-29: offered there too, still marked advanced, after "Linx takes 443" is recommended): there's no router, so the public port *is* the server's port. Docker publishes the chosen TCP port straight to Linx's own 443 router (`linx-sni`: host `8443` → container `443`) and the chosen UDP port to the relay, and the firewall opens exactly those; nothing listens on 443. No new service: only which host ports Compose publishes changes. Everything below (addresses, token, passkeys, company sign-in, doctor, warnings) is the same.

**What the page says first** (owner request 2026-09-29), on the setup page's port question, the install page and Server settings wherever the port can be changed, above the choice and again next to "use another port": *"Linx is designed and tuned to work best on port 443, the standard port for secure websites. It's always the recommended choice: almost every network lets it through, so sign-in, calls and meetings work wherever people are. Another port can work, but some networks block it, so some features may not work everywhere, and calls from those places may fail or sound worse."* The 443 choice stays marked Recommended; the other port stays under Advanced.

**The approach (owner decision).** Linx keeps listening on 443 inside the network, exactly as "Linx takes 443" does today (`linx-sni` splits `<domain>` and `turn.<domain>` by name). One new optional setting, **public port** (default 443, allowed 1024–65535, never a port browsers refuse or Linx uses: 5060–5064, 5349, 6000, 6464, 6665–6669). Linx **never listens on it**: it's used only where addresses are built and checked. The router rule is the owner's job; setup and Server settings show it exactly: *"On your router, forward TCP 8443 to 192.168.1.50 port 443."*

1. **Addresses.** One helper builds Linx's public address (`https://<domain>` or `https://<domain>:<port>`), and everything uses it: setup links, invites and their QR codes, the first-admin and repair links' follow-on address, the sign-in address `sudo linx admin-access` prints, "Moved to a new place?", doctor, help pages, alert links, the company sign-in return address, and the `meet.`/`api.` 308 redirects (which keep the port). What the browser sends already carries it (Origin and Host include `:8443`), so the websocket same-origin checks and cookies need no change (`__Host-` cookies don't depend on the port).
2. **Passkeys.** The relying party stays the domain (no port); the allowed page address becomes `https://<domain>:<port>` (go-webauthn's origin list gets both, so a change of port is not a lockout). **Existing passkeys keep working** when the port changes, since they're tied to the name; tested with Chromium's virtual authenticator (register on 443, sign in on 8443, and back).
3. **Company sign-in.** The return address is `https://<domain>:<port>/api/v1/sso/callback`. When the port changes, Server settings' "Before you apply" lists every provider with the exact new address to register at Google or Microsoft (Copy), the same way a domain change does (docs/INSTALL.md §7). Until it's registered there, company sign-in fails with the provider's own "redirect URI mismatch": the page says so.
4. **Certificates.** Token-free certificates (TLS-ALPN-01) only work on public 443, so a public port other than 443 **needs a DNS token** (any of the ten since Phase 1F step 5; the §3 DNS companies later). Setup says so plainly and won't continue without one; renewals use the same token. The install's first page (6464) already has the "token first" path.
5. **The calls-from-outside relay.** TURN over TLS rides the **same forwarded port**: it arrives at Linx's 443 and `linx-sni` sends `turn.<domain>` to the relay by name, so it needs no second TCP rule (see decision 1). TURN over UDP needs its own rule: setup asks the public UDP port (default 443, since UDP 443 is often free when TCP 443 isn't; else 3478, as `front_door.turn_udp_port` already allows) and shows it: *"Forward UDP 443 to 192.168.1.50 port 443."* Browsers get `turn:turn.<domain>:<udp>?transport=udp` and `turns:turn.<domain>:<port>?transport=tcp`. The trade-off, in these words on the setup page, Server settings, doctor and the help guide: *"Browser calls from networks that only allow standard web traffic, like some hotels, workplaces, public Wi-Fi and mobile networks, may fail or have no audio, because this setup can't use port 443 for them. Calls from normal home and mobile networks work."*
6. **Restrictive networks.** Some networks allow only 443 out, some inspect traffic, and some corporate, school and public networks block any address with a port like `:8443`. There, **even the web page and signing in can fail**, not just calls; the same will apply to meetings. Setup, Server settings and the help guide say so and recommend instead: a front door on 443 (Pangolin, nginx, Caddy or Nginx Proxy Manager passing Linx through), or a small rented server as the front door (Pangolin with Newt) for people who travel or depend on such networks.
7. **Front doors first.** Setup's order: "Does something already use port 443 here?" → "Can it pass Linx through by name?" (the §2.2 kinds) → only if not: *Advanced: use another public port*. Existing installs choose it in Server settings the same way.
8. **Doctor.** Checks the domain and `turn.` through `https://<domain>:<port>` with certd's certificate and TURN allocate over TLS on that port, STUN on the forwarded UDP port, and warns (never fails) that the port isn't 443, with the restrictive-network sentence. Checks from the server itself need the router to loop back to its own public address ("NAT loopback" / "hairpin"); when it can't, doctor says so and points at "Check it" (§2.3), which tests from the owner's phone on mobile data. Inside the network, the same loopback is what lets people at the office use the public address; setup mentions it and the fix (turn on NAT loopback, or a local DNS entry for the domain).
9. **Desk phones and gateways** on the LAN are unaffected: they use `sip.<domain>` at the LAN address, port 5061, never the public port. Browser phones use the web address, so they follow it.
10. **Low resources.** No new service, container, port or background loop: one setting (`.env` `LINX_PUBLIC_PORT`, `LINX_TURN_UDP_PORT` already exists), address building, doctor checks and page text.

**Build order (Phase 1F, after §2.2's front-door card, which it reuses):** (1) the setting and the one address helper everywhere it's needed (passkey origins, SSO return address, redirects, relay URLs, links), with unit tests; (2) setup, the install page and Server settings: the question after the front-door step, the token requirement, router rules, "Before you apply" provider addresses; (3) doctor and "Check it" on the public port; (4) tests: the virtual-authenticator port change, `make test-browser` behind a port-forwarding door (Docker publishing 8443 → 443, a Playwright browser on the "outside" network), `make test-install` with Pebble on DNS-01; (5) help guide page; (6) demo.

**Demo (in the Phase 1F demo).** On a home router, forward TCP 8443 → Linx 443 and UDP 443 → Linx 443. Show: setup with a token; sign-in with a password and with a passkey; company sign-in with Google; a browser call from outside on ordinary mobile data; doctor clean except the expected "not port 443" warning. Then the expected failure from a network that allows only 443 (a laptop behind a firewall allowing only TCP/UDP 443 out), with the plain-language explanation on screen.

## 3. DNS: any DNS company (ADR-063)

### 3.1 Three layers
1. **Certificates never need a DNS token.** Let's Encrypt checks port 443 (TLS-ALPN-01, built in the web install). This becomes the default everywhere; the wildcard certificate (which needs DNS) is only used when a token is given, and nothing needs it.
2. **Any DNS company, by hand:** the page lists the records to add (the domain, `turn.`, and `sip.` at home), checks them at the domain's own name servers, and says when each is right. For a server whose address doesn't change, that's all there is.
3. **Automatic, for addresses that change** (a home connection): Linx keeps the records right itself, through **libdns** (MIT), the Go library behind Caddy's DNS support: one interface for reading and changing records across about 60 DNS companies. Adding a company is one line in a list, not new code. Certificates keep using lego (already in certd). The owner picks the company from a list and pastes its token; the form shows only the fields that company needs.

### 3.2 Staying small
Each libdns company is its own small module. The low-resource rule applies: a curated set first (Cloudflare, DuckDNS, Route 53, GoDaddy, Namecheap, Porkbun, DigitalOcean, Hetzner, deSEC, OVH), measured for certd's image size and memory; more on request. Licences are checked per module (MIT/BSD/Apache only). *As built (2026-10-01): three of the ten have Linx's own short clients instead, to keep certd small and the licences clean (ADR-063).*

### 3.3 `sip.` stays
Desk phones and gateways keep using `sip.<domain>` at the home address (owner decision, 2026-09-29). With automatic DNS it's kept right; by hand it's one more record on the list.

## 4. When each is built
- **§1 Phone lines:** with Phase 1E step 7 (the Phone lines, WireGuard, incoming and outgoing screens), since that step builds these screens anyway. The UCM demo line moves to `registers_here` in its demo.
- **§2 Front doors** and **§3 DNS:** Phase 1F, replacing its "Domain & DNS" item (the web install already covers the first-run part). §2's first task is checking Pangolin's web page on the owner's Pangolin. **§2.5 Public port** comes after §2.2's card, once the owner approves ADR-064.

## 5. To decide when each phase starts
1. §1: whether a gateway may sign in "from anywhere" before public SIP has its password-guessing protection. *Recommend no:* phone networks only until then.
2. §2: whether to keep the decrypting-proxy path at all. *Recommend keep, as advanced only,* for people whose proxy can't pass through.
3. §3: the first set of DNS companies. *Recommend the ten in §3.2.*
4. §2.5 (ADR-064) — **decided by the owner 2026-09-29:** (a) TURN over TLS shares the forwarded web port by name (one TCP rule): **yes**. (b) The public UDP port for calls asked separately, default 443, 3478 if the router can't: **yes**. (c) 8443 suggested when 443 is taken, **customizable** (any 1024–65535 except the ones listed). (d) Offered on a rented server too: **yes** (against the recommendation), advanced, with Docker publishing the chosen ports itself.
