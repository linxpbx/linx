# Web-first install demo checklist

A hand-run check that the web-first install (`docs/INSTALL.md` §9, steps 1–6) does what it promises, on two fresh servers. Tick each box. Allow about three hours: most of it is waiting for DNS and setting up the two servers.

**Exit:** on a **fresh VPS** (Linx takes port 443, no DNS token), `sudo linx setup` asks one question, prints one link, and the rest happens in a browser. The link works once. Let's Encrypt's certificate arrives through port 443 alone. The page moves itself to `https://<domain>`, Install brings up the whole of Linx, and the first sign-in leads to the setup wizard. On a **fresh home VM behind Pangolin**, the same happens with the Cloudflare token given first: Linx adds every DNS record itself, `sip.` included, and gets the wildcard certificate. On both, port 6464 is closed afterwards and `linx doctor` is green. Running `sudo linx setup` again opens Server settings (browser) and, with `--new-link`, the repair page on 6464. Automated: unit tests, `make test-docker`, `make test-install` (Pebble) and `make screens` are green in CI.

The examples use a VPS `vps.mym.ae` at `203.0.113.5` and a home VM `home.mym.ae` at `192.168.1.214`, both in the Cloudflare zone `mym.ae`, and the owner's Pangolin on the home network. Use your own.

## You need
- **A VPS** with Ubuntu 24.04, at least 1 GB memory and 5 GB free disk (setup refuses less; 2 GB and 20 GB or more recommended), its own public IPv4 address, and nothing installed. In the provider's firewall allow **TCP 443, UDP 443 and TCP 6464** in. Plan to delete it at the end.
- **A second home VM** with Ubuntu 24.04 on the home network (bridged, like the lab one), fresh. Give it a fixed address in the router (a DHCP reservation).
- Your **Pangolin** (the one in front of the lab server), and the router sending TCP/UDP 443 to it, as today.
- A **Cloudflare token** for `mym.ae` with Zone · DNS · Edit and Zone · Zone · Read (docs/ops/STAGING_TEST.md "You need").
- Your authenticator app, and a phone or a second computer for "someone else opens the link" (step 5).
- The images published by CI for the commit you build, and from `GOOS=linux GOARCH=amd64 make build`: `bin/linx`, `bin/linx-firewall-sync`, `bin/linx-backup-agent`, `bin/linx-ops-agent` (setup installs the helpers only when they're next to `linx`).

## 1. Docs
- [ ] `docs/INSTALL.md`, ADR-057 to 059 in `docs/DECISIONS.md`, and the "Install review" at the end of `docs/THREAT_MODEL.md` read sensibly to you.

## 2. On your computer
```
cd ~/Projects/linx
git pull
make setup-dev
make lint          # ends with "lint: ok"
make test          # failures only; nothing listed means all passed
make security      # govulncheck, npm audit and licences all "ok"
make test-docker   # ends with "docker tests: ok"
make screens       # screenshots in web/e2e/screenshots/, including install-*
GOOS=linux GOARCH=amd64 make build
```
- [ ] All finish without errors.
- [ ] `web/e2e/screenshots/install-where-light.png`, `install-waiting-*-light.png` and `install-progress-light.png` look right to you.

## 3. CI on GitHub
- [ ] The latest **CI** run on master is green in every job (the amd64 image job ran `make test-install`), and **Packages** has `linx-control-plane` tagged `sha-<that commit>`.

## Part A: the VPS, Linx takes port 443

## 4. Setup in the terminal
```
scp bin/linx bin/linx-firewall-sync bin/linx-backup-agent bin/linx-ops-agent root@203.0.113.5:
ssh root@203.0.113.5
sudo ./linx setup
```
- [ ] It asks one thing: **How do you want to finish setting up Linx?** Press Enter (browser).
- [ ] It checks the server (hardware, disk, ports 443 and 6464 free), installs Docker and starts the installer, then prints **one link**, `https://203.0.113.5:6464/install/…`, the line about allowing TCP 6464 in the provider's firewall, and a **SHA-256 fingerprint**.
- [ ] Press Ctrl-C. `sudo linx setup` again shows the **same link** and "Waiting for you in the browser".

## 5. The link works once
- [ ] Open the link on your computer. The browser warns about the certificate. Open the certificate's details: its SHA-256 fingerprint is **the one in the terminal**. Only then continue past the warning.
- [ ] The address bar shows `…:6464/install` (the secret is gone from it). The terminal shows "Link opened (Chrome, <your address>)".
- [ ] Open the **same link** on your phone: "This link can't be used". So does `https://203.0.113.5:6464/anything`.
- [ ] Under the card, the countdown shows about 4 hours.

## 6. The first page
- [ ] **Where is this server?** is pre-set to a rented server. **What's in front of this server** shows only "Linx takes 443" (the others behind "Something else already uses port 443 here").
- [ ] Domain `vps.mym.ae`. **You**: your name, email, time zone, and tick Let's Encrypt's agreement. **Check and get a certificate**.
- [ ] **How should Linx get it?** Choose **Not now: I'll add 2 records**.
- [ ] The page shows two records: `vps.mym.ae` and `turn.vps.mym.ae`, **A**, `203.0.113.5`. Add them in Cloudflare (DNS only, grey cloud). Within a minute each row reads "Points here ✓".
- [ ] "Let's Encrypt reaches this server on port 443", then **Certificate ready**, then "Moving you to https://vps.mym.ae…". The browser shows the secure page with **no warning** and a padlock.
- [ ] Go back to the `…:6464` tab and reload: "This link can't be used" (the session moved).

## 7. The secure page and Install
- [ ] **Your DNS company's token**: **Skip** is offered (a rented server). Press **Skip**.
- [ ] **A few extras**: size pre-picked, **no Portainer** offered. **Continue**, then **Install**.
- [ ] The progress list ticks through: settings, firewall, internal certificate authority, download, certificate (renews through port 443), **Start Linx**, your account, phone system, helpers, Finish.
- [ ] Before Start Linx, the box **Write these down now** shows the **Certificate authority backup passphrase**. Save it in your password manager. The page won't leave until **I've written these down** is ticked.
- [ ] After Install it moves to the first sign-in: set a password, then a passkey or authenticator, then the setup wizard: **How do you want to start?** → **Set up fresh**. Place has no pre-pick (rented server).
- [ ] Finish the wizard with one person (you) and "Later" for the phone line.

## 8. After the install, on the VPS
```
sudo linx doctor
sudo grep -c "backup passphrase" /etc/linx/install-state.json    # prints 0
curl -sk --max-time 5 https://203.0.113.5:6464/ ; echo "exit $?"   # from your computer: fails (exit 28 or 7)
sudo nft list table inet linx | grep 6464
```
- [ ] Doctor: every line ok, except the warning that the root key backup is still on the server (copy `/etc/linx/ca-backup` off, then `sudo rm -r /etc/linx/ca-backup`; doctor then ends "Everything checked is working"). It includes "The installer's first page (port 6464) is closed for good."
- [ ] The passphrase isn't in the install state (0 matches). 6464 doesn't answer from outside. The firewall shows `6464 … drop`.
- [ ] `https://vps.mym.ae` signs you in. From a phone on mobile data (UDP blocked is fine), the web phone's `*43` echo test works: call audio through `turn.vps.mym.ae` on 443.

## 9. Running setup again (Server settings)
```
sudo linx setup
```
- [ ] It asks **browser or terminal**. Browser: it checks `https://vps.mym.ae` ("working ✓") and prints `https://vps.mym.ae/admin/system/server`. Nothing reopens 6464.
- [ ] System → **Server settings** in the browser is open for changes. **Portainer isn't offered** (rented server). Change **Size**, **Apply** → confirm it's you → the steps tick, "Linx is restarting…", back.
- [ ] Activity shows `system.server_settings`.

## Part B: the home VM behind Pangolin, token first

## 10. Setup in the terminal
Same as step 4 on `192.168.1.214`.
- [ ] The link is `https://192.168.1.214:6464/install/…` ("on a computer on the same network"), plus your home's public address when port 6464 is sent there (it isn't: ignore it).

## 11. The first page
- [ ] **Where** is pre-set to home. **What's in front**: **Pangolin**, its home-network address. Domain `home.mym.ae`. **You** as before.
- [ ] **How should Linx get it?** Choose **With my DNS company's token**. Tick **I checked the fingerprint, or I trust this network**, paste the Cloudflare token.
- [ ] The page shows the **Pangolin block** to add (Traefik TCP routers for `home.mym.ae` and `turn.home.mym.ae`, PROXY v2). Add it to Pangolin's dynamic config next to the lab server's, then **I've done this**.
- [ ] "Linx points your names at this server": in Cloudflare, `home.mym.ae` and `turn.home.mym.ae` appear at your home's public address and **`sip.home.mym.ae` at 192.168.1.214**, all grey cloud.
- [ ] The wildcard certificate arrives. The page moves to `https://home.mym.ae` with no warning. The token step shows it's already saved.

## 12. Install and the first sign-in
- [ ] **A few extras**: **Portainer** is offered here (home). Turn it on. **Install**.
- [ ] **Write these down now** shows the CA passphrase **and Portainer's password**.
- [ ] First sign-in, then the wizard: Place is pre-picked **Home**.
- [ ] `https://192.168.1.214:9443` (Portainer) opens from the home network only.
- [ ] `sudo linx doctor` on the home VM: ok (apart from the CA backup warning), including `sip.home.mym.ae` at 192.168.1.214 and "The installer's first page (port 6464) is closed for good."
- [ ] A desk phone or Linphone on the home network registers with `sip.home.mym.ae:5061` (TLS).

## 13. The repair page
```
sudo linx setup --new-link
```
- [ ] Choose browser. It prints a repair link `https://192.168.1.214:6464/repair/…` with the same fingerprint, "You'll sign in there as a system admin…".
- [ ] Open it: a sign-in card with no passkey button. Sign in with your password and authenticator. The Server settings panel shows, with the countdown.
- [ ] `sudo nft list set inet linx install_page` shows `0.0.0.0/0` while it's open.
- [ ] `sudo systemctl stop linx-setup.service`: within a few seconds the page stops working, `install_page` is empty, and 6464 doesn't answer (`curl -sk --max-time 5 https://192.168.1.214:6464/`).
- [ ] Activity: your sign-in, from your computer's address.

## Clean up
- VPS: delete it, and the `vps.mym.ae` and `turn.vps.mym.ae` records.
- Home VM: remove its block from Pangolin, delete the VM, and the `home`, `turn.home` and `sip.home` records.
- Delete the Cloudflare token if you made one for this.

## Results
