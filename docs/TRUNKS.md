# Linx — Phone lines to the outside world (Phase 1D)

Status: **draft for the owner's approval** (ADR-043 to ADR-048, written 2026-09-26 while the owner was away). Nothing in this document is built. The "Needs you" list (§11) holds the decisions only the owner can make; everything else uses the recommended default.
Fourth slice of Phase 1 (`docs/ROADMAP.md`). Until now Linx only calls itself: extensions, desk phones and browsers. This slice connects it to real phone numbers, so people can call mobiles and landlines, and outside callers can reach them. The admin portal screens for all of this come in 1E; this slice gives the engine, the API, a guided `linx trunk add` command and `linx doctor` checks.

## 1. In plain words
- **A trunk is a phone line from a provider** (a company that sells phone numbers and calls over the internet), or a link to **another phone system** you already own, like the Grandstream UCM6304 with its GXW4104 analogue-line gateway. One trunk can carry many calls at once.
- **Encrypted first, always tried first.** Linx connects to the provider over TLS with encrypted audio (SRTP). Only if the provider can't do that does Linx offer an unencrypted connection, and only after you confirm a plain warning ("Calls to and from this provider can be listened to on the way"), ADR-023. Trunks through a WireGuard tunnel are encrypted by the tunnel and get no warning (ADR-024).
- **Nothing new is opened to the internet** for the usual kind of trunk (Linx signs in to the provider, like a phone does). A provider that instead sends calls from fixed addresses gets a door opened for exactly those addresses, nobody else.
- **Your phone numbers ("DIDs") ring someone.** Each number points to an extension (ring groups, menus and office hours come with the routing wizards later in Phase 1).
- **Outgoing calls are dialled the way you'd dial on a mobile.** Choose your country once. Linx understands local, mobile, national, international, toll-free and premium numbers from Google's open numbering database, and decides who may call what. International calls are **off** until you turn them on; premium numbers are blocked; **emergency numbers always work** (if your provider carries them).
- **Protection against phone fraud** (criminals who break into phone systems to call expensive numbers): calls from a trunk can never go back out through a trunk, each extension has a limit on outside calls at once, and you get an alert on unusual international calling and on every emergency call.
- **A test before you save.** `sudo linx trunk add` asks plain questions (or starts from a provider template), checks the connection (name, certificate, encryption, sign-in), and only then saves. `linx route test 0501234567 --from 101` shows exactly what would happen to a call, without making it.

## 2. What runs
| Piece | What it does |
|---|---|
| Asterisk (existing) | Gains the trunks: a TLS client transport, a plain one used only for ADR-023 trunks, trunk contexts, and outbound decisions read from the database (§5). Transcodes Opus (browsers) ↔ G.711 (providers) with the codec built in 1C. |
| Control plane (existing) | Stores trunks (passwords sealed, ADR-030), renders them for Asterisk (ADR-043), probes them, watches their state over ARI, fills the numbering tables (ADR-044), raises alerts and events. |
| `linx-wireguard` (new, small) | Brings up WireGuard tunnels for trunks that use one (ADR-046). The only container with `NET_ADMIN`. |
| `linx-firewall-sync` (new, host systemd timer) | Keeps the host firewall's per-trunk address sets in step with the trunks (ADR-047). Only needed for trunks that call in from fixed addresses. |

## 3. Kinds of trunk
| Kind (plain name) | How calls come in | Firewall | Default |
|---|---|---|---|
| **"Linx signs in to the provider"** (registration) | Over the connection Linx opened | nothing opened | ✅ recommended; works with a changing home address |
| **"The provider sends calls to Linx"** (IP-authenticated) | Provider connects to `sip.<domain>:5061` (TLS) from its published addresses | 5061/tcp (and the audio ports) opened **only to those addresses**; the router forwards 5061. 5060 only for ADR-023 trunks, same addresses only | Warned: needs a fixed home address or DDNS the provider accepts |
| **"Another phone system on my network"** (LAN peer: UCM6304, GXW4104, another Linx) | Either side calls the other | LAN only, nothing on the internet | For a parallel run: send `2xx` to the UCM while people move over |
| Any of the above **"through a WireGuard tunnel"** | Inside the tunnel | nothing on the internet | For providers that offer a VPN; no ADR-023 warning |

Each trunk has: name, kind, provider address (host, port; SRV used when the provider publishes it), login (for registration), transport (TLS or, with the ADR-023 confirmation, TCP/UDP), media encryption (SRTP or, confirmed, none), certificate trust (§6), codecs (default `alaw,ulaw` for the country; G.722 if offered), how numbers are sent (`+971…`, `00971…` or `04…`), caller ID rules (§5), its DIDs, maximum calls at once, **Connection** (Internet or a WireGuard profile, ADR-024), and whether it's the primary or backup line for outgoing calls.

**Provider templates** (a small, versioned YAML catalogue in the image, easy to add to): Telnyx, VoIP.ms, Twilio Elastic SIP Trunking, Grandstream UCM, Grandstream GXW410x, "Another Linx", Generic. A template fills in addresses, transport, number format and codecs; the probe still checks everything.

## 4. How trunks reach Asterisk (ADR-043)
- Devices stay in realtime views (ADR-032). **Trunks don't**: a provider password must be usable by Asterisk (it signs in to the provider with it), so it can't be a one-way hash like device logins, and putting it in a view would leave it readable in the database. Instead the control plane opens the sealed secret and **renders `pjsip_trunks.conf`** (endpoints, auths, registrations, `identify` rules, the SIP ACL) into a memory-only volume shared only with Asterisk (like the 1C `sipws-certs` volume: tmpfs, group-readable by Asterisk only), then asks Asterisk over ARI to reload `res_pjsip` (`PUT /asterisk/modules/res_pjsip.so`). 1B showed that reload keeps registrations and live calls. Outbound registrations can't be added through realtime without a reload anyway.
- At start, Asterisk's entrypoint waits for the file (as it waits for the websocket certificate); if the control plane is down, the last file stays in place and calls keep working.
- **The SIP ACL moves into that file**: PJSIP applies every ACL object separately (a request must pass all), so LAN networks, the browser network and trunk addresses must be one list. `internal/asteriskconf` keeps rendering the device-only ACL until the first trunk file arrives.
- Each trunk's inbound calls land in **`linx-from-trunk`**, which can reach only that trunk's DIDs (then an extension), never `linx-outbound`. No trunk-to-trunk, no dialling out from an inbound call: the classic open-relay fraud can't happen by construction (tested).

## 5. Numbers and routing (ADR-044)
- **Country** (default United Arab Emirates, +971) is chosen once. The control plane uses **`github.com/nyaruka/phonenumbers`** (MIT, a Go port of Google's libphonenumber, whose data is updated monthly) to write the country's numbering rules into tables: national and international prefixes, and for each call type (local/fixed, mobile, toll-free, premium, shared-cost, international) the pattern libphonenumber uses. Emergency numbers come from libphonenumber's short-number data, checked against a small built-in list per country (UAE: 999 police, 998 ambulance, 997 fire, 112).
- **The decision happens in the database**, not in the control plane: a SQL function `asterisk.linx_route_outbound(caller, dialled)` normalises the number to E.164, classifies it with those patterns (PostgreSQL's regular expressions should accept libphonenumber's; step 1 proves it or translates them), checks the caller's permission, and returns the trunk(s) to try and the number in each trunk's format. The dialplan calls it through `func_odbc`, exactly like ring targets today. So outgoing calls, like internal ones, **keep working while the control plane restarts** (ADR-034's rule). A Docker test compares the SQL function with the Go library on a few thousand numbers per supported country.
- **What people dial:** a number as they'd dial it on a mobile (`050 123 4567`, `04 123 4567`, `+44 20…`, `0044 20…`). No "9 for an outside line". Extension numbers (2–6 digits) can't start with the national or international prefix (`0` in the UAE) or equal an emergency or short service number; creating one is refused, and choosing a country flags any existing clash.
- **Who can call what:** permission levels, default "Staff: local, mobile, national, toll-free" and "Managers: + international", assigned per extension (per person and group when 1E brings them). Premium is off for everyone unless turned on per level; emergency is always on and can't be turned off.
- **Which line:** primary trunk, then backup if the primary is down, busy (its call limit reached) or answers with a failure that means "try elsewhere" (e.g. 503), never after the called person answers or rejects.
- **Caller ID** (what people you call see): per trunk a main number; per extension its own DID if it has one; "withheld" per level if the provider supports it. Sent in `From` and `P-Asserted-Identity` as the trunk template says. Inbound caller names are untrusted text: stripped of control characters and quotes and length-limited before they reach the dialplan, the API, webhooks or the web page.
- **Inbound:** each DID → an extension. A call to a DID nobody has set up hears "not in use". A DID on no trunk can't be claimed twice.
- **`linx route test NUMBER --from EXT`** prints the decision in plain words ("Mobile number, allowed for Staff. Goes out on Line A as +971501234567; on Line B if Line A is down."). It runs the same SQL function, so it can't disagree with a real call. It's the engine of 1E's call simulator.

## 6. Encryption and certificates (ADR-023, ADR-045)
- The probe tries, in order: TLS with a publicly trusted certificate whose name matches the provider's address; TLS with **a certificate or CA the admin pins** (many LAN devices like the UCM6304 and some providers use their own); only then, with the ADR-023 confirmation, TCP or UDP without TLS. Media: SRTP (SDES over TLS) first; RTP without encryption only with the same confirmation. A trunk with TLS but unencrypted audio counts as unencrypted.
- **Pinning is verification, not an exception**: Linx checks the provider's certificate against exactly what the admin approved (the fingerprint is shown in plain words at `linx trunk add`, to compare with the provider's). Certificate checking is never switched off (security rules).
- Asterisk checks certificates per transport, not per trunk, so trunks use one TLS client transport whose CA list is the system's public CAs plus every pinned CA/certificate; the name is always checked (step 3 proves it with a wrong-name certificate).
- `linx doctor` and the trunk list show every unencrypted trunk (ADR-023). No plaintext trunk over WireGuard gets the warning (ADR-024).
- Trunk calls are decrypted in Asterisk to transcode and bridge (as 1C's browser calls already are). Documented; end-to-end encryption is for app-to-app and meetings.

## 7. WireGuard (ADR-024, ADR-046)
- Profiles: import the provider's `.conf` or fill in the fields; the private key is sealed (ADR-030) and never shown again. Several profiles; each trunk's **Connection** is "Internet" or one profile.
- **Split tunnel only:** a profile carries only its trunks' addresses. `AllowedIPs = 0.0.0.0/0` (or `::/0`) is narrowed to those addresses, with a note.
- `linx-wireguard` (Go, `golang.zx2c4.com/wireguard/wgctrl` MIT + `github.com/vishvananda/netlink` Apache-2.0, kernel WireGuard from Ubuntu 24.04) **joins Asterisk's network namespace** (`network_mode: service:asterisk`) and holds `NET_ADMIN` there only: Asterisk itself gets no capability, and no other container's routes change. It reads rendered profiles from its own memory-only volume, applies them (interface, keys, peers, routes to trunk addresses only), writes each tunnel's last handshake back for the control plane, and re-applies within seconds if Asterisk restarts (the namespace is new then; step 5 proves the recovery).
- A tunnel with no handshake for 3 minutes marks its trunks down (alert "WireGuard tunnel <name> is down"). Needs the `wireguard` kernel module (setup checks and loads it).

## 8. Home networks and NAT
- Registration trunks over TLS: one outbound connection kept open; calls come in on it. Nothing to forward.
- **Audio from internet providers** goes straight between Asterisk and the provider over UDP (it's server-to-provider, so ADR-042's "443 only" is about people's devices, not this). Linx sends audio first and most providers answer to where it came from, so most homes need no port forward. When a test call shows one-way audio, setup's generated steps say which router forward to add (UDP 10000–10199 → the Linx server), and the host firewall admits it **only from trunk addresses**.
- IP-authenticated trunks need a stable address (warned at `linx trunk add`); the DNS follower (1C) keeps `sip.<domain>` current for providers that accept a name.
- The phones' 5061 and audio ports stay closed to everyone else on the internet, as since 1B.

## 9. Fraud, alerts, events
- **Built-in limits:** no trunk-to-trunk; outside calls only from signed-in devices and browser lines; at most 2 outside calls at once per extension and the trunk's own limit (Asterisk `GROUP_COUNT`); international and premium off by default; emergency always allowed and never limited.
- **Alerts** (existing engine): trunk down / back up (registration failing or unreachable for 2 minutes), WireGuard tunnel down, **emergency call placed** (who, when; so someone knows help was called), **unusual international calling** (more than 30 minutes or 10 calls to international numbers in an hour, both adjustable), and "a country called for the first time".
- **Webhooks:** `trunk.created/updated/deleted`, `trunk.status_changed` (registered, unreachable, …); call events gain `direction` (`internal`/`inbound`/`outbound`), `trunk_id`, and the outside number in E.164.
- **Audit:** every trunk, route, permission and country change; every unencrypted-trunk confirmation records who confirmed the warning.

## 10. API and commands
- `/trunks` (CRUD, merge patch + `If-Match` like extensions), `POST /trunks/{id}/test` (the probe: DNS, TLS and certificate, OPTIONS, registration, SRTP acceptance; results in plain words), `/trunks/{id}/dids`, `/wireguard-profiles` (CRUD, `.conf` import), `/inbound-routes`, `/outbound-routing` (country, primary/backup, caller ID), `/call-permission-levels`, `POST /route-test`. Provider secrets are write-only (never returned).
- New scopes: `trunks:read`, `trunks:write` and `routing:write` (**sensitive**, never in "all": they can make calls that cost money), `routing:read`. Admin/system_admin only.
- CLI (root, `docker exec` like `linx user`): `linx trunk add|list|test|remove`, `linx route test`. `linx trunk add` is the guided form: template or generic → address → login → probe → ADR-023 warning if needed → DIDs → primary/backup.
- `linx doctor` "Phone lines" section: each trunk registered/reachable, its encryption (unencrypted ones listed), certificate expiry for pinned ones, WireGuard handshakes, firewall sets match IP-authenticated trunks, the country is set and emergency numbers route somewhere, no extension clashes with an emergency number.

## 11. Needs you (decisions for the owner)
1. **Approve this design** (ADR-043 to ADR-048). Recommended as written.
2. **Which line for the demo?** Recommended: first your **Grandstream UCM6304 as a LAN trunk** (free, and its GXW4104 already has real phone lines), then a paid provider if you want calls without the UCM. Paying for a provider is your decision; VoIP to the public network is regulated in the UAE (only licensed operators), so a provider outside the UAE may not be allowed for UAE numbers. Tell me which provider (if any) and I'll add or check its template.
3. **Country:** United Arab Emirates (+971) as the default country. Recommended.
4. **Pinned certificates count as verification** (§6), for the UCM6304 and providers with their own certificates. Recommended yes; it isn't an exception to "never disable certificate checking".
5. **Trunk setup through `linx trunk add` and the API now; the admin screens in 1E.** Recommended (the owner-facing wizard gets designed once, with ring groups and office hours).
6. **Audio port forward only when needed** (§8): UDP 10000–10199 forwarded by the router, admitted by Linx's firewall only from trunk addresses. Recommended.
7. **Fraud defaults** (§9): 2 outside calls at once per extension; alert over 30 international minutes or 10 international calls an hour; international off and premium blocked until you turn them on. Recommended.
8. **Emergency list for the UAE:** 999, 998, 997, 112 (plus whatever libphonenumber adds). Please confirm; nobody should test these by calling them (the demo checks them with `linx route test` only).

## 12. Security in this slice
- New outside surfaces: outbound connections to providers (TLS verified or pinned), and for IP-authenticated trunks 5061 (plus 5060 for ADR-023 trunks) open **only to the provider's addresses**, enforced twice (host firewall set and Asterisk's ACL). Registration trunks open nothing.
- Provider passwords and WireGuard private keys: sealed in the database (ADR-030), rendered only into memory-only volumes readable by the one container that needs them, never in views, API responses, events, audit details or logs.
- Toll fraud: §9 by construction (contexts), by limits and by alerts. `trunks:write`/`routing:write` sensitive.
- `linx-wireguard`: the only container with `NET_ADMIN`, only in Asterisk's namespace; non-root where the capability allows, read-only filesystem, no Docker socket.
- `linx-firewall-sync` runs as root on the host (it changes nftables), reads only address lists from the control plane through `docker exec` (the same trust as `linx user`), and can only add or remove addresses in Linx's own sets, never rules.
- Untrusted input from providers (caller names, numbers, SIP headers) sanitised before the dialplan, API, webhooks and web.
- THREAT_MODEL gets rows for trunks, the rendered-config volume, outbound routing, WireGuard, the firewall sync and toll fraud at the review step.

## 13. Build order (one session each)
1. **Numbering:** `nyaruka/phonenumbers` (licence check), country setting, generated rule tables, `asterisk.linx_route_outbound` + tests against the Go library, emergency lists, extension-clash checks, `linx route test`. *(Opus: correctness matters most here)*
2. **Trunks data and API:** migration (trunks, DIDs, WireGuard profiles, inbound routes, outbound routing, permission levels), sealed secrets, endpoints, scopes, events, audit, provider template catalogue. *(Sonnet)*
3. **Asterisk trunks:** rendered `pjsip_trunks.conf` + ARI reload, TLS client transport with pinned CAs and name checks, the ACL move, `linx-from-trunk`/`linx-outbound` contexts, codecs and transcoding, caller ID, call limits. Call suite: SIPp as a provider (TLS+SRTP registration, IP-authenticated, ADR-023 plaintext), inbound DID → extension, outbound mobile, international refused, emergency always, trunk-to-trunk refused, wrong-name certificate refused, reload during a call. *(Opus: hardest integration)*
4. **Probe, CLI, doctor, alerts:** `POST /trunks/{id}/test`, `linx trunk add|list|test|remove`, trunk state over ARI, `trunk.status_changed`, trunk/emergency/international alerts, doctor "Phone lines", call events with direction. *(Sonnet)*
5. **WireGuard:** `linx-wireguard` (image, compose, rendered profiles, handshake status, Asterisk-restart recovery), `.conf` import and split-tunnel narrowing; Docker test with a WireGuard "provider" container and a plaintext SIPp trunk inside it. *(Opus: privileged networking)*
6. **Firewall and NAT:** `linx-firewall-sync` (systemd timer, nft sets for IP-authenticated trunks and the audio range), generated router steps, doctor checks; browser suite: **a browser with UDP blocked calls out through a SIPp provider trunk and back** (the Phase 1 exit test). *(Sonnet)*
7. **Security review + docs:** THREAT_MODEL "Phase 1D review", `docs/DEMO_PHASE1D.md`. *(Opus)*

## 14. Demo exit (Phase 1D)
On the home server, through Pangolin: `linx doctor` all green including "Phone lines"; `linx trunk add` for the UCM6304 over TLS with its pinned certificate (and the owner's provider if chosen); a browser on mobile data **with UDP blocked** calls the owner's mobile through the trunk and both hear each other (the Phase 1 exit test); calling the DID from the mobile rings the browser; a "Staff" extension is refused an international number and hears why; `linx route test 999 --from 101` shows emergency allowed (never dialled for real); the emergency and trunk-down alerts arrive (trunk down by unplugging the UCM); an unencrypted test trunk shows the warning and appears in doctor's list. Automated: call suite (trunk scenarios) and browser suite (browser → trunk with UDP blocked) green in CI.

## 15. Not in this slice
The admin portal screens and the inbound/outbound/ring-group wizards with the full call simulator (1E and later Phase 1); ring groups, IVR, office hours, voicemail, CDR and call recording (later Phase 1 / Phase 4); spend tracking with per-minute rates; STIR/SHAKEN (not used in the UAE; revisit per provider); fax; importing trunks from a UCM backup (migration tools, later); WhatsApp as a trunk (later).
