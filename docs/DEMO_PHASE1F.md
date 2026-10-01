# Phase 1F demo checklist

Phase 1F has two demos (`docs/PHASE1F.md` §10 item 7). **Part A** (this one, after build step 8): getting to Linx from outside. Part B (email, call routing, voicemail, call history, undo, Phase 1 exit) is added here after step 17.

## Part A: front doors, DNS, public port

A hand-run check that Part A (`docs/PHASE1F.md` §11 steps 2–7, `docs/SIMPLER.md` §2, §2.5 and §3) does what it promises, on the home server behind Pangolin and on the small VPS. Tick each box. Allow about two hours.

**Part A exit:** on the home server behind Pangolin, Server settings shows one front-door card with the steps for Pangolin (and the other proxies); **Check it** passes from the server, and the phone link on the iPhone with Wi-Fi off shows the mobile network's address and call audio working; the **DNS** row lists the records, checked at the domain's own name servers, names Cloudflare, and Linx keeps them right with the key (Check the key, Stop, start again). On the 1-core VPS, Linx moves to **another public port** (8443): the passkey made at `https://vps.mym.ae` signs in at `https://vps.mym.ae:8443`, Check it and doctor pass with the one ⚠ about the port, a browser call from mobile data has audio, and moving back to 443 works the same way. Automated: `make test`, `make test-docker`, `make screens`, the browser suite (every front door, `public-port` included) and `make test-install` (with the DNS-01 install) are green in CI.

The examples use the home server `home.mym.ae` at `192.168.1.213` behind Pangolin at `192.168.1.211`, and the VPS `vps.mym.ae` (64.177.45.141, Debian 13, 1 core, 2 GB, Linx takes 443, no token). Use your own.

### You need
- The home server and Pangolin, as after the Help demo, with an admin account and a passkey on the iPhone.
- The VPS with Linx running, an admin account, and a passkey made at `https://vps.mym.ae` (step 7 makes one if you don't have it).
- A **Cloudflare token** for `mym.ae`: My Profile → API Tokens → Create Token → "Edit zone DNS", Zone Resources: only `mym.ae`. The VPS needs it for the public port; the home server already has one.
- The **iPhone** on mobile data, and a laptop on your home network.
- In the VPS provider's firewall: allow **TCP 8443** in (UDP 443 and TCP 443 are already allowed).
- The images published by CI for the commit you build.

### 1. Docs
- [ ] `docs/SIMPLER.md` §2, §2.5 and §3, ADR-062 to 064 "As built" in `docs/DECISIONS.md`, and the "Phase 1F Part A review" at the end of `docs/THREAT_MODEL.md` read sensibly to you.

### 2. On your computer
```
cd ~/Projects/linx
git pull
make lint          # ends with "lint: ok"
make test          # nothing listed means all passed
make screens       # screenshots in web/e2e/screenshots/
GOOS=linux GOARCH=amd64 make build
```
- [ ] All finish without errors.
- [ ] These screenshots look right to you: `install-front-door-home-light.png`, `install-front-door-unlock-light.png`, `install-public-port-warning-light.png`, `install-public-port-light.png`, `install-dns-token-light.png`, `light-system-status-check-it.png`, `light-system-server-dns.png`, `light-system-server-dns-automatic.png`, `reach-phone.png`.
- [ ] The latest **CI** run on master is green in every job (one browser job per front door, `public-port` among them).

### 3. Update both servers
For each server, as for the Help demo:
```
scp bin/linx bin/linx-firewall-sync bin/linx-backup-agent bin/linx-ops-agent linx@home.mym.ae:
ssh linx@home.mym.ae 'sudo ./linx setup'      # pick the terminal, keep every answer
```
Then the same with `vps.mym.ae`.
- [ ] Both end with "Linx is running".
- [ ] `sudo linx doctor` on each: nothing failing (the known warnings only: the CA backup still on the server, no API key).

### 4. Home: the front-door card
Laptop, signed in as the admin at `https://home.mym.ae`: System → **Server settings** → **In front** → **Change**.
- [ ] The choices: Linx takes 443, **Another program passes Linx through**, only at home; **Advanced** adds the decrypting proxy and **another public port (advanced)**. Choosing the decrypting proxy shows its "Not recommended" line.
- [ ] Choose **Another program passes Linx through**, at `192.168.1.211` (your Pangolin), and **Apply**: **Before you apply** shows the card, with the three facts (your domain to this server's port 8443, `turn.` to its port 5349, PROXY protocol on, from Pangolin's address) and **How to do this in**: Pangolin, nginx, HAProxy, Caddy, Nginx Proxy Manager, a router.
- [ ] Pangolin's steps match what's in your Pangolin today (Traefik's file; Pangolin's own web page can't pass Linx by name).
- [ ] Cancel: nothing changes.

### 5. Home: Check it
System → **Status** → **Reachable from outside** → **Check it**.
- [ ] From this server: `home.mym.ae` and `turn.home.mym.ae` point at your address, `home.mym.ae` answers with Linx's certificate through Pangolin, `turn.home.mym.ae` relays call audio. Your router may not reach its own address from inside: then an ℹ line sends you to the phone, which is fine.
- [ ] iPhone with **Wi-Fi off**: scan the picture. The phone's page shows the mobile network's address and tests call audio. Within a few seconds the laptop says "Your phone reached Linx from …" (that address) and "Calls from outside will have audio".
- [ ] Open the same link again on the iPhone: "This link can't be used".
- [ ] **Check again**, then scan the new picture with **Wi-Fi on**: the laptop says Wi-Fi was still on (your home address).

### 6. Home: DNS records and the key
Server settings → **DNS**.
- [ ] The records: `home.mym.ae` and `turn.home.mym.ae` at your public address, `sip.home.mym.ae` at 192.168.1.213, each **Right**, "checked at your domain's name servers", company **Cloudflare**, and "Linx keeps these right at Cloudflare" with the last change.
- [ ] **Replace key** → **DNS company** starts on Cloudflare, and the page says where to find the key. **Check the key** with your current token: "This key can change … records". Cancel without applying.
- [ ] **Stop** → Apply → confirm it's you. The records card still lists the records, all **Right**, and no longer says Linx keeps them. In Cloudflare, nothing changed.
- [ ] **Let Linx keep them right** → Apply → confirm it's you: the card says it again.
- [ ] System → **Activity**: the two `system.server_settings` entries; none shows the token.
- [ ] Optional, if you have a domain at another of the ten companies: the page names that company for it (the install page's token step is the quickest place to see this).

### 7. VPS: a passkey at 443
Laptop at `https://vps.mym.ae`, signed in as the VPS's admin.
- [ ] If you have no passkey there: My account → add one (iPhone or the laptop).
- [ ] Sign out, sign in again with the passkey: it works.

### 8. VPS: move to port 8443
System → **Server settings** → **In front** → **Change** → **Advanced** → **another public port (advanced)**.
- [ ] The warning comes first (port 443 is always recommended), with **Show me the front-door options** and **Use another port**. Choose **Use another port**.
- [ ] **Public web port** 8443, **call audio (UDP)** 443. Try 5061 first: turned down with the reason. Then 8443.
- [ ] It asks for the DNS company's key (no port 443 for Let's Encrypt's check): Cloudflare, paste the token, **Check the key** ✓.
- [ ] **Before you apply** says: on a rented server there's nothing to forward (Linx opens both ports); Linx moves to `https://vps.mym.ae:8443`; passkeys keep working; company sign-in's new return address; links already sent stop working.
- [ ] **Apply** → confirm it's you → the steps tick → the page comes back at `https://vps.mym.ae:8443`.

### 9. VPS: on port 8443
- [ ] `https://vps.mym.ae` (no port) doesn't open any more.
- [ ] Sign out, then sign in at `https://vps.mym.ae:8443` **with the passkey made at 443**: it works.
- [ ] System → Status → **Check it**: every line ✓, and the last one ⚠ *People open Linx at port 8443, not the standard 443* with what it trades away.
- [ ] Phone link with Wi-Fi off: its address ends in `:8443`; the phone's page shows the mobile address and audio works.
- [ ] On the VPS, `sudo linx doctor`: the front door passes, one warning that the port isn't 443, nothing failing.
- [ ] Server settings → **DNS**: the records are **Right**, and Linx keeps them right at Cloudflare now.
- [ ] iPhone on mobile data at `https://vps.mym.ae:8443`: sign in, **echo test** (or a call to another signed-in browser): clear audio both ways.
- [ ] On the VPS, `docker stats --no-stream`: within `docs/RESOURCES.md` §1's numbers at rest; note them in the Results.

### 10. VPS: back to 443
Server settings → **In front** → **Change** → **Nothing else uses port 443 — Linx takes it** → Apply → confirm it's you.
- [ ] The page comes back at `https://vps.mym.ae`; `https://vps.mym.ae:8443` doesn't open any more.
- [ ] Sign in with the same passkey: it works.
- [ ] Check it: every line ✓, no ⚠ about the port.

### 11. Clean up
- [ ] VPS: keep the Cloudflare token there (Linx now keeps `vps.mym.ae` and `turn.vps.mym.ae` right), or **Stop** and delete the token in Cloudflare. Remove TCP 8443 from the provider's firewall.

### Results
- Not run yet.
