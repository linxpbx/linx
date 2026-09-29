# Linx — Simpler phone lines, front doors and DNS

*Status: design drafted 2026-09-29 from the owner's request during the install demo ("a very painful process to add the UCM"; "one unified setup" for reverse proxies; "I don't expect integration with every DNS company"). The directions in §1–§3 were agreed in the session; this write-up is **not yet approved**. ADR-061 to ADR-063. Built in the phases in §4, not before. The low-resource and low-bandwidth rule (CLAUDE.md) applies to all of it.*

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
- **Pangolin**: in its web page where the version allows it (to verify on the owner's Pangolin 1.23 EE first: its config already has PROXY transports, which its own resources use), else the lines for its config file, named after the domain so several Linx servers never clash (built 2026-09-28)
- **nginx or HAProxy**: their stream (TCP) blocks
- **Nginx Proxy Manager**: its "Streams" page (by port, since it can't route TCP by name: 443 to Linx only when nothing else needs 443)
- **Caddy**: its layer-4 module
- **A router or firewall** forwarding TCP 443 straight to Linx (no proxy at all)

Choosing a product only changes the instructions shown, not how Linx is set up: Linx's side is the same for every proxy (PROXY v2 required from the proxy's address, refused from anyone else). The front-door kinds collapse to three: **Linx takes 443**, **another program passes it through** (with its address), **home only**.

### 2.3 "Check it"
- **From the server:** both names resolve to the right address at the domain's own name servers, and the address answers with Linx's certificate on 443 (the doctor's checks, on the page, in plain words).
- **From outside:** a home network often can't reach its own public address from inside (no "hairpin"), so the page offers a short link to open on a phone **with Wi-Fi off**. It shows ✓ and the visitor's address as Linx saw it, which also proves the PROXY header works.

### 2.4 Decrypting proxies become a last resort
"Caddy or Nginx Proxy Manager" today means the proxy decrypts. That's the one path with special cases: a DNS token asked on the first page, forwarded-address headers instead of PROXY, a separate port for call audio. Both products can pass through instead (§2.2), so that's what the card recommends. Decrypting stays under "Something else (advanced)" with a plain warning, and isn't offered on a rented server.

## 3. DNS: any DNS company (ADR-063)

### 3.1 Three layers
1. **Certificates never need a DNS token.** Let's Encrypt checks port 443 (TLS-ALPN-01, built in the web install). This becomes the default everywhere; the wildcard certificate (which needs DNS) is only used when a token is given, and nothing needs it.
2. **Any DNS company, by hand:** the page lists the records to add (the domain, `turn.`, and `sip.` at home), checks them at the domain's own name servers, and says when each is right. For a server whose address doesn't change, that's all there is.
3. **Automatic, for addresses that change** (a home connection): Linx keeps the records right itself, through **libdns** (MIT), the Go library behind Caddy's DNS support: one interface for reading and changing records across about 60 DNS companies. Adding a company is one line in a list, not new code. Certificates keep using lego (already in certd). The owner picks the company from a list and pastes its token; the form shows only the fields that company needs.

### 3.2 Staying small
Each libdns company is its own small module. The low-resource rule applies: a curated set first (Cloudflare, DuckDNS, Route 53, GoDaddy, Namecheap, Porkbun, DigitalOcean, Hetzner, deSEC, OVH), measured for certd's image size and memory; more on request. Licences are checked per module (MIT/BSD/Apache only).

### 3.3 `sip.` stays
Desk phones and gateways keep using `sip.<domain>` at the home address (owner decision, 2026-09-29). With automatic DNS it's kept right; by hand it's one more record on the list.

## 4. When each is built
- **§1 Phone lines:** with Phase 1E step 7 (the Phone lines, WireGuard, incoming and outgoing screens), since that step builds these screens anyway. The UCM demo line moves to `registers_here` in its demo.
- **§2 Front doors** and **§3 DNS:** Phase 1F, replacing its "Domain & DNS" item (the web install already covers the first-run part). §2's first task is checking Pangolin's web page on the owner's Pangolin.

## 5. To decide when each phase starts
1. §1: whether a gateway may sign in "from anywhere" before public SIP has its password-guessing protection. *Recommend no:* phone networks only until then.
2. §2: whether to keep the decrypting-proxy path at all. *Recommend keep, as advanced only,* for people whose proxy can't pass through.
3. §3: the first set of DNS companies. *Recommend the ten in §3.2.*
