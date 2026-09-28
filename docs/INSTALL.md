# Linx — Web-first install

*Status: design drafted 2026-09-28 from the owner's direction of 2026-09-27 (CLAUDE.md "Pre-launch"), **approved by the owner 2026-09-28, all §10 questions as recommended**. ADR-057 and ADR-058 record the decisions.*

Today `sudo linx setup` asks about ten questions in the terminal: resource profile, Docker, front door, Portainer, domain, DNS provider and token, test certificates, contact email, owner email and name. With this design the terminal asks nothing. It checks the server, installs Docker and Linx, and prints **one link**. Everything else happens in a browser.

## 1. In plain words

1. `sudo linx setup` on the new server. It checks the hardware and disk, installs Docker and Linx, and prints one link, such as `http://203.0.113.5:6464/install/k7Q…` (no QR code). The link works once, for one hour.
2. Open the link. It's a plain `http://` page, so there's no certificate warning. That page asks only for things that aren't secret: where the server is, what sits in front of it, your domain, and your name and email. It shows the one DNS record to add (e.g. `meet.example.com → 203.0.113.5`).
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
- **Names.** The bootstrap certificate names `meet.`, `api.` and `turn.<domain>`. `sip.<domain>` points at the LAN address, where Let's Encrypt can't reach it. It arrives with the wildcard in §5.
- **The page shows** "Add this record: `meet.example.com → 203.0.113.5`" (public address found the way `certs/records.go` does it). It checks DNS every few seconds and says in plain words what it sees ("still points at 198.51.100.7", "not found yet").

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
- **The secure address is broken** (domain lost, certificate gone): the HTTP page on 6464 opens again for one hour, again with a new link. Once a system admin exists, it also needs that admin's sign-in, so a stolen link alone can't take over an installed server.

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

1. **Screen specs** `docs/ui/INSTALL_SCREENS.md` (low fidelity): claim, where/front door, domain + DNS record, name/email, waiting for the certificate, token fallback + warning, secure-page steps, progress, moved-server checklist. Owner approval.
2. **Install mode + bridge.** `linx setup` terminal flow (checks, Docker, minimal stack, `linx-setup.service`, one link), control plane install mode on 6464, `install-bridge`, answers → `installer.Config` validated on the host, HTTP pages.
3. **TLS-ALPN-01.** certd presenter + challenge tmpfs, the control plane's `acme-tls/1` answer, staging-then-real, DNS polling, handoff to HTTPS, token fallback. Browser suite: a fresh stack behind each front door with Pebble (Let's Encrypt's test ACME server) doing TLS-ALPN-01.
4. **Secure page + close.** Token → wildcard + records including `sip.` (per-record addresses in the follower), first admin via the existing first-run flow, extras, full apply, 6464 dropped for good, doctor checks, re-run behaviour (§7).
5. **Moved-server checklist** (§8).
6. **Security review** (`docs/THREAT_MODEL.md` "Install review") + `docs/DEMO_INSTALL.md`: a fresh VPS (Linx takes 443) and a fresh home VM behind Pangolin.

This replaces most of `linx setup`'s questions and the Phase 1F "Domain & DNS" page: changing the domain or token later is §7's secure page.

## 10. Owner decisions (2026-09-28, all as recommended)

1. **DNS token on a rented server: required or skippable?** *Recommend skippable there, required at home.* On a VPS, phones don't use the LAN, and TLS-ALPN-01 renews by itself, so a domain at any DNS company works. At home, `sip.` needs the token.
2. **An extra code from the terminal on the secure page, against someone tampering with the unencrypted page on the way?** *Recommend no.* You asked for one link. The HTTP page holds nothing secret, and taking over also needs the one-time handoff to be used first. The remaining risk (someone actively tampering between your browser and the server during those minutes) is written into the threat model.
3. **An existing Ubuntu-packaged Docker with running containers.** *Recommend: stop and tell you to re-run with `--replace-docker`* (the only flag besides `--config`). Replacing it restarts those containers, so it shouldn't happen without asking.
4. **Portainer:** *recommend keeping it as an off-by-default extra on the secure page, LAN only*, rather than dropping it.
