# Linx — Web-first install

*Status: design drafted 2026-09-28 from the owner's direction of 2026-09-27 (CLAUDE.md "Pre-launch"), **approved by the owner 2026-09-28, all §10 questions as recommended**. ADR-057 and ADR-058 record the decisions.*

Today `sudo linx setup` asks about ten questions in the terminal: resource profile, Docker, front door, Portainer, domain, DNS provider and token, test certificates, contact email, owner email and name. With this design the terminal asks nothing. It checks the server, installs Docker and Linx, and prints **one link**. Everything else happens in a browser.

## 1. In plain words

1. `sudo linx setup` on the new server. It checks the hardware and disk, installs Docker and Linx, and prints one link, such as `http://203.0.113.5:6464/install/k7Q…` (no QR code). The link works once, for one hour.
2. Open the link. It's a plain `http://` page, so there's no certificate warning. That page asks only for things that aren't secret: where the server is, what sits in front of it, your domain, and your name and email. It shows the two DNS records to add (`meet.example.com` and `turn.example.com → 203.0.113.5`).
3. Linx gets its first real certificate through port 443 by itself, with no DNS token. Then the page **moves itself to `https://meet.example.com`**.
4. The secure page asks for the rest: your DNS provider token, your password and passkey or authenticator, then the existing setup wizard (Place, Country, Numbers, People, and so on, or "Restore from a backup").
5. Port 6464 is then closed and blocked in the firewall **for good**. Only running `sudo linx setup` again can reopen it, and only if the secure address doesn't work (§7).

## 2. What moves where

| Question today (terminal) | Where it goes | Why there |
|---|---|---|
| Host checks, Docker install | terminal, no question | Running setup *is* the yes. Replacing a distro Docker that has running containers still stops and asks for `--replace-docker` (§10 item 3). |
| Resource profile | automatic; "More options" on the secure page | The suggested profile is right almost every time. |
| Where the server is, front door (Pangolin/nginx/Caddy·NPM/Linx takes 443/home only/none) | HTTP page | Decides how port 443 reaches Linx, which the certificate needs. Not secret. |
| Domain | HTTP page | Needed for the certificate. Not secret. |
| Your name and email | HTTP page | Personal but not secret. It becomes the first admin (§6) and the certificate contact. |
| DNS provider + token | **secure page** | Secret. Asked on the HTTP page only when the secure page can't exist yet (§4.3), with a warning. |
| Test certificates (staging) | gone | Every install tries staging first, then the real certificate automatically (§4.2). |
| Portainer | secure page, "Extras" (LAN only, off by default) | Optional. Still a host step, done by the same helper. |
| Password, passkey, authenticator | secure page | Secret. Uses the existing first-run flow. |

`sudo linx setup --config setup.yaml` stays exactly as it is for automation and tests: no browser, no port 6464.

## 3. Who does what (the browser asks, the server does it)

Same boundary as restore (ADR-055) and Restart (ADR-056). The control plane shows the pages but never changes the host. A root helper on the host carries out a fixed plan.

```
browser ──HTTP :6464 / HTTPS :443──▶ control plane (install mode, non-root container)
                                          ▲  JSON lines: answers ▶  ◀ progress
                                          │  docker exec -i linx-control-plane service install-bridge
                                     linx setup (host, root, running as systemd unit linx-setup.service)
                                          └─ the existing internal/installer plans: firewall, front door files,
                                             .env, secrets, compose up, CLI, agents
```

- **Terminal part.** `sudo linx setup` runs the host checks, installs Docker, pulls the images and starts a minimal stack: Postgres plus the control plane in *install mode*. Then it re-runs itself as a transient systemd unit (`linx-setup.service`), prints the link and follows the unit's progress. If SSH drops or you press Ctrl-C, only the terminal view stops. The install carries on, and `sudo linx setup` shows it again.
- **Answers are a `Config`, not commands.** The browser's answers become the same `installer.Config` that `setup.yaml` holds. The host re-checks them with the same validation code (domain syntax, front-door kind, proxy address, …) before running any plan. A hijacked control plane can suggest a different domain. It can never run a command on the host.
- **The bridge** reuses the ops-agent pattern (ADR-056): the host opens `docker exec -i … service install-bridge`, so nothing on the host listens. It carries JSON lines both ways: answers and "apply" from the browser, step-by-step progress and plain-words errors back to the page.
- **Install mode.** The control plane serves only `/install/*` and the few API calls those pages need. Nothing else is served on 6464: no admin pages, no `/sip`, no API keys. Install mode ends when the first admin finishes signing in (§6).

## 4. The certificate before the secure page (ADR-058)

### 4.1 TLS-ALPN-01 on port 443
The first certificate uses ACME **TLS-ALPN-01**: Let's Encrypt connects to `meet.<domain>:443` and expects a special certificate for the `acme-tls/1` protocol. No DNS token, and no port 80, ever.

- **Where the challenge is answered.** In the control plane's 8443 listener. Every front door already sends `meet.<domain>` there (passing TLS through by SNI, ADR-040), so this works the same behind Pangolin, nginx and linx-sni. certd (lego's TLS-ALPN-01 solver with a custom "presenter") writes the challenge certificate to a shared tmpfs. The control plane's `GetCertificate` hands it out only when the browser's hello asks for `acme-tls/1` and names that exact domain.
- **Names.** The bootstrap certificate names `meet.` and `turn.<domain>` (as built; the first draft also had `api.`, see §13). `api.` and `sip.<domain>` arrive with the wildcard in §5; `sip.` points at the LAN address, where Let's Encrypt can't reach it.
- **The page shows** the two records to add, `meet.` and `turn.example.com → 203.0.113.5` (public address found the way `certs/records.go` does it). It checks them every few seconds at the domain's own name servers and says in plain words what each shows ("still points at 198.51.100.7", "not found yet").

### 4.2 Staging first, then the real one
Before 443 is known to reach Linx, a failed check would use up Let's Encrypt's limit of 5 failed attempts per name per hour. So Linx asks **staging** first. A staging success proves DNS and port 443 work, and only then does it ask for the real certificate. This is also the reachability test itself: nothing connects anywhere with verification turned off. The "test certificates" question goes away.

### 4.3 When 443 can't reach Linx yet
- **Home only** (DNS at the LAN address): Let's Encrypt can never reach it.
- **Caddy / Nginx Proxy Manager** (`http-proxy`): the proxy decrypts TLS itself, so `acme-tls/1` never gets to Linx.
- **Pangolin/nginx not configured yet**: the page shows the generated block to paste in first, as setup prints it today, and waits.

For the first two, the HTTP page asks for the DNS token instead and issues the wildcard straight away (DNS-01, as today). It shows a plain warning first: "This page isn't encrypted yet. Anyone on the network between you and this server could see the token. Continue only on a network you trust, or put Linx behind a front door that passes 443 through." Then it moves to HTTPS in the same way.

## 5. On the secure page

1. **Handoff.** The HTTP page redirects to `https://meet.<domain>/install/continue#<handoff>`. The handoff is a one-time, 2-minute value that moves the claimed install session to the new address (browsers don't share cookies between the two). It's in the fragment, so it's never logged.
2. **DNS provider + token** (Cloudflare or DuckDNS today). Saved as the `linx_dns_token` Docker secret by the host. Then certd, in the background:
   - issues the **wildcard** by DNS-01 and replaces the bootstrap certificate (consumers already reload, `docs/ops/CERT_RELOAD.md`);
   - creates `meet`, `api` and `turn` at the public address **and `sip.<domain>` at the LAN address** (the owner's decision of 2026-09-27: setup no longer prints it for you to add). The IP follower keeps each record at its own address, and a LAN change still needs setup re-run.
   - *Skip* is allowed only on a rented server (§10 item 1). The certificate then keeps renewing through 443 by TLS-ALPN-01, with no phones on the LAN and no automatic DNS.
3. **You.** The account from the HTTP page's name and email becomes the first `system_admin` and goes straight into the existing first-run flow: password (or passkey-only), then a passkey or authenticator (docs/ADMIN.md §4–5).
4. **Extras**: resource profile, Portainer (LAN only). Then **Apply**. The host brings up the full stack (Asterisk, coturn, WireGuard agent, phone ports and firewall, backup/ops/firewall-sync helpers) and the page shows each step.
5. The existing **setup wizard** follows ("Set up fresh" or "Restore from a backup", then Place …). Its "Place" step starts from the answer given in §2, so you aren't asked twice.

## 6. Closing port 6464 for good

- Install mode ends once the first admin has a finished sign-in (password + second step). The host then removes 6464 from compose and adds a **drop rule** for it in the `inet linx` firewall table. The rule stays after the install, so a later mistake can't expose it again.
- `setup.yaml` records `install.finished_at`. `linx doctor` fails if anything listens on 6464 or the drop rule is missing.
- Before it closes, 6464 answers only with the secret link. Any other path gets the same 404 as a wrong secret. The link needs 256 random bits. It's claimed by the first browser (cookie bound; the secret is removed from the address bar by a redirect) and expires after 1 hour. After the hour, `sudo linx setup` prints a new one. Requests are rate-limited per address.

## 7. Running setup again

- **Setup already finished and the secure address works:** nothing reopens. The link printed is `https://meet.<domain>/install/…`. It needs a sign-in as a **system admin** and shows the host settings (front door, domain, extras). **No new first admin is created, ever.** Someone who has lost every system admin uses `sudo linx user create --role system_admin` or `sudo linx user reset-2fa`, as today.
- **The secure address is broken** (domain lost, certificate gone): the HTTP page on 6464 opens again for one hour, again with a new link. Once a system admin exists, it also needs that admin's sign-in, so a stolen link alone can't take over an installed server. Passkeys can't work on 6464, so a passkey-only system admin uses `sudo linx setup --new-link --no-sign-in` (root only) for a link that skips the sign-in.

## 8. After a restore: "Moved to a new place?"

A restore already follows the new server for everything setup owns: domain, certificate, front door, firewall and public DNS aren't in a backup. What *is* in the backup can still point at the old place. To detect that, every start writes a small **place record** into the database (`install_place`: LAN network, LAN and public address, domain, front-door kind), so it travels in each backup. After a restore, the admin home shows a checklist when the backup's place differs from this server's:

- Phone lines tied to the old network or address: LAN peers (e.g. the UCM), IP-authenticated providers (tell them the new address), WireGuard tunnels.
- Desk phones set up on the old LAN.
- "Admins only from my home network" still lists the old network.
- A **different domain**: passkeys stop working (they belong to the old domain, so sign in with password + authenticator and add new ones), and desk phones need the new `sip.` address.
- Backup places to add again (their keys stay on the old host, `docs/BACKUP.md` §7).
- **Turn the old server off.** Both would point the same DNS names at themselves.

Each item links to its page and can be ticked off. The list goes away when every item is done.

## 9. Build order (one step per session)

1. **Screen specs** `docs/ui/INSTALL_SCREENS.md` (low fidelity): claim, where/front door, domain + DNS record, name/email, waiting for the certificate, token fallback + warning, secure-page steps, progress, moved-server checklist. **Approved by the owner 2026-09-28**, with three additions (its §8): a system admin who signs in with a passkey only gets back in through `sudo linx setup --new-link --no-sign-in` (root on the server, so no extra risk); the "You" step asks for the Let's Encrypt Subscriber Agreement tick; a rented server shows only "Linx takes 443", other front doors behind "Something else already uses port 443 here".
2. **Install mode + bridge.** `linx setup` terminal flow (checks, Docker, minimal stack, `linx-setup.service`, one link), control plane install mode on 6464, `install-bridge`, answers → `installer.Config` validated on the host, HTTP pages.
   - **As built (2026-09-28).** `internal/install`: the bridge protocol (JSON lines), the link (256-bit secret, 43 characters; only its SHA-256 leaves the host), `Server` (the control plane's side) and `Host` (linx setup's side, state in `/etc/linx/install-state.json`, root only). **Install mode is its own small mode of the control-plane binary, `service install-server`**, with no database, domain, certificate or secret: `deploy/compose/install.yaml` runs it as `linx-control-plane` (same name, so the full stack's control plane replaces it later), publishing 6464 on the server's default-route address, and `service install-bridge` joins `docker exec -i` to its socket (`/run/linx/install.sock`, tmpfs, same code as `ops-bridge`). Postgres isn't started yet: nothing needs it until the first admin (step 4). Without the claimed cookie, port 6464 answers only the link: every other path, a wrong, used or expired link, and the page's own scripts all get the same self-contained "This link can't be used" page (own CSP, style by hash, design-token colours; 20 requests then one per 3 s per address). **The host checks the secret itself** on every claim, not just the control plane's word. Cookie `linx_install` (HttpOnly, SameSite=Strict; plain HTTP, so not `__Host-`/Secure); changes also need a JSON body and `Origin: http://<Host>`. No HSTS over HTTP (`webapp.PlainHeaders`). Answers → `installer.WebConfig` (same validation as setup.yaml, plus: not an address, not a public suffix via `golang.org/x/net/publicsuffix`, DuckDNS form, Let's Encrypt tick) → saved to `setup.yaml` with a new `install:` section (`where`, `terms_agreed_at`, `finished_at`); `Config.Installed()` = a domain and not a web install still under way. `linx setup` without `--config` is now the web install (`cmd/linx/websetup.go`): checks, 6464 must be free, Docker (a distro Docker with running containers needs `--replace-docker`), `linx` to `/usr/local/bin`, install stack, then `systemd-run --unit linx-setup.service … linx install-service` (hidden command) and follows its progress from the state file; Ctrl-C only stops the view; `--new-link` stops the unit and starts over. The link is printed at the default-route address, plus the public address when that differs (home, or a provider's NAT). After the hour the unit ends the page and stops the install stack. Web: `web/src/screens/Install.tsx` + `web/src/lib/install.ts` (§2.1–2.5, the draft kept on the server so a reload returns to the same step, host refusals sent back to their step). Tests: `internal/install` (unit + `TestInstallModeDocker`: the real image, install.yaml and docker exec bridge), `internal/installer/web_test.go`, `cmd/linx/websetup_test.go`, `make screens` (install pages light/dark/phone).
   - **Differences from the design, for the owner:** port 443 already in use is a note in the terminal, not a stop (nginx or Caddy on this server is a valid answer in the browser); 6464 in use still stops. The terminal's questions are gone, so until step 4 builds §7, re-running `sudo linx setup` on an installed server says so and points at `sudo linx setup --config /etc/linx/setup.yaml` (the lab server keeps working that way). After "Check and get a certificate" the page shows the DNS record to add; the waiting page that ticks itself (§2.7) is step 3. Fixed on the way: `setup.yaml` never saved `front_door.turn_udp_port`, so a re-run with the saved file lost it.
3. **TLS-ALPN-01.** certd presenter + challenge tmpfs, the control plane's `acme-tls/1` answer, staging-then-real, DNS polling, handoff to HTTPS, token fallback. Browser suite: a fresh stack behind each front door with Pebble (Let's Encrypt's test ACME server) doing TLS-ALPN-01.
   - **As built (2026-09-28).** *certd:* `-bootstrap staging|real` (`internal/certs/bootstrap.go`): lego's TLS-ALPN-01 with `ChallengeWriter`, which leaves each name's challenge certificate in the `acme-challenge` tmpfs volume; staging only proves, real deploys to the `certs` volume the full stack reads (same project and volume names). Prints one result line (`linx_certd_result`: ok, or a kind — connection, dns, wrong_answer, rate_limited, other — and Let's Encrypt's own words), logs to stderr. An ACME account the CA no longer knows is registered again, once. `LINX_ACME_TEST_DIRECTORY` (tests only: Pebble). *Install stack* (`deploy/compose/install.yaml`): the install-mode control plane also listens on 8443 (TLS, PROXY v2 from the front door as in the full stack: `install.TLSConfig` hands the challenge certificate only to a hello offering exactly `acme-tls/1` for a name certd has one for, else the deployed certificate) and 5349 (challenge only: where `turn.` goes through Pangolin and nginx; through linx-sni the control plane takes coturn's alias). `certd` (never started by `up`; run by setup) and `sni` (profile `linx-443`, the full stack's generated haproxy.cfg). *Host* (`install.Host` + `install.Certifier`, cmd/linx `webCert`): once the answers are saved, `installer.CertView` plans the page (mode, records, the front door's block and router steps), then a loop: start the web port (`InstallCertPlan`: front door files, `install.env` with the front door's settings, `up`), look up every record at the domain's own name servers every 5 s (`internal/dnscheck`, miekg/dns; public resolvers are never asked, so they can't cache "no such name"), wait for "I've done this" on the front door's steps, staging, real. A failure stops everything until the page's **Try again**. Token mode (Caddy/NPM, home only): the token is checked (`ValidateDNSToken`), saved as `linx_dns_token` (mounted into that certd run only), then `certd -records` and `certd -once` (DNS-01 wildcard, real straight away: a wrong token fails at the DNS company, before Let's Encrypt). *Handoff:* the plain page first checks the browser can open `https://meet.<domain>` (`/install/api/ping`, no-cors), asks the host for a handoff (256 bits, 2 minutes, only its hash kept), goes to `/install/continue#<handoff>`; the secure page redeems it for a `__Host-linx_install` cookie (Secure, HttpOnly, Strict), the plain page's session ends, and the secure one gets a new hour. The secure side serves only `meet.<domain>`. *Web:* `screens/InstallCertificate.tsx` (§2.6, §2.7, §3.1; the frame moved to `components/InstallFrame.tsx`). *Tests:* unit (certs bootstrap, dnscheck, install host loop and handoff, installer plans, cmd/linx result parsing), `make screens` (7 new states × light/dark/phone), and **`make test-install`** (`TestWebCertificateInstall`: the real install.yaml, control-plane and certd images, linx-sni, Pebble and its DNS test server; `meet.` alone isn't enough, then both records, staging, real, handoff, secure page verified against Pebble's root; in CI's amd64 image job).
   - **Not built / differences, for the owner:** only the "Linx takes 443" front door runs against Pebble; Pangolin and nginx pass the check through by name exactly as they already pass HTTPS (their generated blocks are the Phase 1C ones, proven in the browser suite), but that isn't tested with Pebble yet, and nothing is run in a real browser against the real stack (screens use a stand-in). After **Continue** on the secure page, a notice says the rest arrives with the next update (step 4). The secure page has no countdown (the new-link hint doesn't apply there). A second **Check** after the answers are saved is refused (changing them later is §7's page).
4. **Secure page + close.** Token → wildcard + records including `sip.` (per-record addresses in the follower), first admin via the existing first-run flow, extras, full apply, 6464 dropped for good, doctor checks, re-run behaviour (§7).
5. **Moved-server checklist** (§8).
6. **Security review** (`docs/THREAT_MODEL.md` "Install review") + `docs/DEMO_INSTALL.md`: a fresh VPS (Linx takes 443) and a fresh home VM behind Pangolin.

This replaces most of `linx setup`'s questions and the Phase 1F "Domain & DNS" page: changing the domain or token later is §7's secure page.

## 10. Owner decisions (2026-09-28, all as recommended)

1. **DNS token on a rented server: required or skippable?** *Recommend skippable there, required at home.* On a VPS, phones don't use the LAN, and TLS-ALPN-01 renews by itself, so a domain at any DNS company works. At home, `sip.` needs the token.
2. **An extra code from the terminal on the secure page, against someone tampering with the unencrypted page on the way?** *Recommend no.* You asked for one link. The HTTP page holds nothing secret, and taking over also needs the one-time handoff to be used first. The remaining risk (someone actively tampering between your browser and the server during those minutes) is written into the threat model.
3. **An existing Ubuntu-packaged Docker with running containers.** *Recommend: stop and tell you to re-run with `--replace-docker`* (the only flag besides `--config`). Replacing it restarts those containers, so it shouldn't happen without asking.
4. **Portainer:** *recommend keeping it as an off-by-default extra on the secure page, LAN only*, rather than dropping it.

## 11. Owner additions after step 2 (2026-09-28, built)

1. **Browser or terminal.** In a terminal, `sudo linx setup` now asks first: *How do you want to finish setting up Linx?* **browser** (Enter; recommended) or **terminal** (the question-by-question setup from before step 2, restored: the DNS token is typed over SSH, never over an unencrypted page). Without a terminal (a script), a browser setup already under way, or `--new-link`/`--replace-docker`: the browser, unasked. On a server that's already installed: the terminal's questions with the saved answers as defaults, until §7 gives that a browser page. A terminal setup clears setup.yaml's `install:` section. This softens ADR-057's "the terminal asks nothing" to "the terminal asks one question".
2. **Countdown on the plain page.** Under the card: "This link closes in 47:12. Need more time? Run `sudo linx setup --new-link` on the server for a new link. Your answers so far are kept." A warning for the last 5 minutes; at 0 the page turns into "This link can't be used". It counts from `expires_in` (seconds, by the server's clock), so the visitor's clock doesn't matter. A new link (after the hour, or `--new-link`, which now marks the old one cancelled instead of deleting the state) carries the draft and saved answers over.
3. **Time zone.** Linx's schedules (backups now, office hours in 1F) use setup.yaml's new `time_zone` (→ `LINX_TZ`), falling back to the server's own. Setup never changes the server's clock settings. Terminal: shows the server's zone and asks to keep or change it. Browser: a drop-down on the "You" step, pre-set to the browser's zone, naming the server's own when it differs (a rented server is usually UTC).

## 12. Open question (owner, 2026-09-28): a port other than 443?

Asked during step 2: could someone choose another port for Linx's web address when they don't need to get through strict firewalls? *Recommendation: not now.* It's possible, but (1) the token-free first certificate (TLS-ALPN-01) only works on 443, so such an install would need the DNS token from the start, like the Caddy/home-only path in §4.3; (2) every address, passkey and app would carry the port (`https://meet.example.com:8443`); (3) networks that allow only 443 out (hotels, offices, China) would block the web app and calls from outside. "Something else already uses 443" is covered by Pangolin, nginx and Caddy in front. If wanted later: a separate "custom port" choice that requires the token and warns about blocked networks. Not built.

## 13. Owner question (2026-09-28, during step 3): one name instead of several?

Asked: couldn't everything live under one name instead of `meet.`, `api.`, `turn.` and `sip.`? *Answer, as built:* partly. **`api.`** is the same server as `meet.`, so the first certificate leaves it out and the owner adds only two records by hand (`meet.` and `turn.`); `api.` comes with the wildcard once the token is given, and Linx makes its record itself. **`turn.`** needs its own name: Pangolin, nginx and Linx's own port 443 router tell call audio (TURN over TLS) from the web app by the name asked for, on the same port 443; one name would mean re-designing how calls get through 443-only networks. **`sip.`** must point at the home address while `meet.` points at the public one, so it can't be the same name; Linx creates it with the token. (Found on the way: the first draft of this step asked for `meet.` only while the certificate named `api.` and `turn.` too, which Let's Encrypt would have refused.)
