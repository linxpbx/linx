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
On the server, `sudo ./linx setup` → **the Server settings page** (changes open only while setup has the page open, four hours). Laptop, signed in as the admin at `https://home.mym.ae`: System → **Server settings** → **In front** → **Change**. Nothing changes while the front door is the one you have: to see the card, change the address (say to `.212`) and press **Check**, then Cancel.
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
On the VPS, `sudo ./linx setup` → **the Server settings page** (again for step 10). System → **Server settings** → **In front** → **Change** → **Advanced** → **another public port (advanced)**.
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
Run 2026-10-01 with the owner, at `344d0ae` (fixes below on top).
- Steps 1–3 ✓: lint, test, screens, Linux build; CI green in all 23 jobs; both servers updated, doctor nothing failing (VPS: three more warnings that are the test VPS's own: no alert channel, no phone lines, public address).
- Step 4: choices ✓. **Found:** Before you apply showed only the router line, without the card: the API dropped it (fixed, `bb2c585`). Owner: "Nothing: only at home" now says "(no internet)" (`a85c625`). Card itself: see below.
- Steps 5–7 ✓ (Check it from the server and the phone with Wi-Fi off and on, the used link refused; DNS records, Replace key, Stop, start again, Activity without the token; passkey at 443).
- Step 8 ✓ (5061 refused with its reason; Before you apply as listed). **Found:** with the move pending, **Check the key** showed no tick, and the move's Check stayed grey until an edit (fixed, `7b9a09b`).
- Step 9 ✓: passkey from 443 at :8443, Check it with the one ⚠, phone link at :8443, doctor (the port warning only), DNS kept right, iPhone on mobile data with audio. At rest: about 1% processor; Docker shows about 226 MB for Linx's services (most is file cache: step-ca 43 MB shown, 17 MB its own), 1.2 GB of 2 GB available.
- Step 10 ✓ (passkey, Check it without the port ⚠). **Found:** the page at :8443 spun for ever on the firewall step: the closed port's packets are dropped, so the page never noticed Linx had gone. Now it gives up after 8 s, and (owner) it opens the new address by itself after a move to another port (`6779005`).
- Step 11 ✓: TCP 8443 removed at the provider; Cloudflare token kept on the VPS.
- CI: `TestSIPRelay` failed twice on amd64: the relay registered a browser's connection just after accepting it, so a sign-out or a disabled person in between waited up to 15 s for the recheck. Now registered first (`internal/siprelay`).
- Owner: re-test moving ports (key tick, the page following the move, no spinner) in Demo B, not now.
- Fixed after the demo: doctor's UDP line on a rented server no longer says "127.0.0.1" and "your router's forward" (`c2a3781`).
- Owner decisions after the demo (as recommended): **Let Linx update them for you** wording, and **Show the steps** for the front door in use, both done. Linx already followed a changing home address (linx-certd, every 5 minutes); Claude had wrongly said it didn't.
- Still to do: `docs/RESOURCES.md` §1 re-measured as own memory, not cache; `make screens` re-saves a few help pictures each run (timing only).
