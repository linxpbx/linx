# Phase 1C demo checklist

A hand-run check that the web client (`docs/WEB.md` §8, steps 1–8) does what it promises. Tick each box. Allow about two hours: most of it is setup, the Pangolin change and a certificate.

**Phase 1C exit** (`docs/WEB.md` §9): through **Pangolin**, `linx doctor` is all green; a person signs in at `meet.<domain>` on a laptop at home and on a phone on mobile data; they call each other and hear both ways; with UDP blocked on the laptop the call still connects ("Relayed", TURN over TLS on 443); a browser calls a Linphone on the home Wi-Fi and back; `*43` echoes in Opus; wrong passwords bring the wait and the alert; signing out drops the phone line at once. Automated: the browser call suite (UDP blocked) and the SIPp suite are green in CI.

The examples use `lab.linxpbx.com`, a server at `192.168.1.50` and Pangolin at `192.168.1.20`. Use your own.

## You need
- Everything under "You need" in [`DEMO_PHASE1B.md`](DEMO_PHASE1B.md): the server on your home network (bridged VM), a Cloudflare token, Linphone on a second device, webhook.site open in a tab.
- **Your Pangolin at home**, with your router sending **TCP 443** to it, and a way to edit its files (SSH to the Pangolin machine).
- **Router access**, to forward **UDP 443** (or 3478) to the Linx server.
- **A laptop** (your Mac) with Chrome or Safari, and **a phone** (your iPhone) with Safari, whose Wi-Fi you can turn off (mobile data).
- The images `linx-certd`, `linx-control-plane`, `linx-asterisk` **and** `linx-coturn` published by CI for the commit you build.
- An authenticator app on your phone (Google Authenticator, 1Password, Authy, …): the laptop person is an admin, and admins must use one.
- A trusted certificate (not a test one), as in Phase 1B: browsers and phones refuse test certificates.

## 1. Docs
- [x] `docs/WEB.md`, ADR-036 to ADR-041 in `docs/DECISIONS.md` and the "Phase 1C review" in `docs/THREAT_MODEL.md` read sensibly to you.

## 2. On your computer
```
cd ~/Projects/linx
git pull
make setup-dev
make lint          # ends with "lint: ok"
make test          # failures only; nothing listed means all passed
make security      # govulncheck, npm audit and licences all "ok"
make test-docker   # real Postgres, coturn: ends with "docker tests: ok"
make image SERVICE=asterisk && make image SERVICE=control-plane && make image SERVICE=coturn
make test-calls    # SIPp phones and the relay: ends with "call suite: ok"
make test-browser  # two Chromiums behind four front doors, one with UDP blocked
```
- [x] All finish without errors. (`make test-browser` takes a while: it runs the whole stack four times.)

## 3. CI on GitHub
Open the repository → **Actions** → the latest **CI** run on master.
- [x] Every job is green, including the call suite and the browser call suite in the amd64 Asterisk job, and `images (coturn)` for amd64 and arm64.
- [x] Under **Packages**, `linx-coturn` and `linx-control-plane` have an image tagged `sha-<that commit>`.

## 4. Install on a fresh server
Follow steps 1–3 of [`ops/STAGING_TEST.md`](ops/STAGING_TEST.md) with the commit CI just built. Answer **n** to "Use test certificates for now?" and give your email. When setup asks **"What sits in front of Linx on the internet?"**, choose **Pangolin**, and give the Pangolin machine's home address (`192.168.1.20`). For **"UDP port for call audio"** answer `443`, or `3478` if your router (UniFi, for one) won't send UDP 443 to Linx while TCP 443 goes to Pangolin.
- [x] Setup ends with "Linx is running", "Trusted certificate issued for *.lab.linxpbx.com", and a **Calls from outside** block naming `/etc/linx/front-door/PANGOLIN.txt`.
- [x] It reports the DNS records: `meet.`, `api.` and `turn.lab.linxpbx.com` created (or already pointing) at your home's public address. In Cloudflare they're grey-cloud (DNS only) with the comment "Linx (linx setup)".

Add the `sip.lab` record for Linphone as in Phase 1B (A record → `192.168.1.50`, DNS only).

## 5. Pangolin and the router
```
sudo cat /etc/linx/front-door/PANGOLIN.txt
sudo cat /etc/linx/front-door/pangolin-dynamic-config.yml
```
Do the steps it lists:
1. On the Pangolin machine, append the second file's contents to `config/traefik/dynamic_config.yml`.
2. In `traefik_config.yml` remove the `http3:` lines (and `advertisedPort: 443`) and `docker restart traefik`.
3. On the router, forward **UDP 443** (or the port you chose, e.g. 3478, same port on both sides) to `192.168.1.50`. TCP 443 stays with Pangolin.

- [x] Your other Pangolin sites still work.

## 6. `linx doctor`
```
sudo linx doctor
```
- [ ] Everything is `ok`, including **Phone system** and **Calls from outside**: each of `meet.`, `api.` and `turn.` reaches Linx through Pangolin with Linx's own certificate; a relay connection works over TLS on 443; the relay answers on UDP 443 (or your port); the names point at your public address; only Pangolin may reach the web port.
- [ ] The database line shows `schema version 14`. The only warnings are no API key and no alert channel.

## 7. Key, alert channel, extensions, people
On your laptop (it reaches Linx through Pangolin like anyone outside):
```
# on the server
sudo linx api-key create --name "Demo" --role admin --scopes all,devices:write

# on the laptop
KEY='linx_...'
API=https://meet.lab.linxpbx.com/api/v1
api() { curl -sS -H "Authorization: Bearer $KEY" "$@"; }
JSON='Content-Type: application/json'
HOOK_URL='https://webhook.site/...'
api -X POST -H "$JSON" -d "{\"name\":\"Demo\",\"kind\":\"webhook\",\"config\":{\"url\":\"$HOOK_URL\"}}" $API/alert-channels | jq .id
api -X POST -H "$JSON" -d '{"number":"101","display_name":"Laptop"}' $API/extensions | jq .number
api -X POST -H "$JSON" -d '{"number":"102","display_name":"Phone"}'  $API/extensions | jq .number
api -X POST -H "$JSON" -d '{"number":"103","display_name":"Linphone"}' $API/extensions | tee /tmp/e103.json | jq .number
api -X POST -H "$JSON" -d '{"name":"Linphone"}' $API/extensions/$(jq -r .id /tmp/e103.json)/devices | tee /tmp/d103.json | jq -r .settings_text
```
Then create the two people on the server:
```
sudo linx user create --email you@example.com  --name "Laptop Person" --role admin --extension 101
sudo linx user create --email you+phone@example.com --name "Phone Person" --role user --extension 102
```
- [ ] The alert channel and three extensions are created. Each `linx user create` prints a link `https://meet.lab.linxpbx.com/setup/…` that works once, for 24 hours.
- [ ] Linphone (on the home Wi-Fi) signs in with the 103 settings exactly as in Phase 1B, SRTP mandatory.

## 8. Sign in on the laptop (admin, with an authenticator)
Open the **Laptop Person** link in Chrome on the laptop.
- [ ] You choose a password (12+ characters; `password123456` is refused), then scan a QR code with your authenticator app, type its code, and see **10 recovery codes** once. Keep them for this demo.
- [ ] You land on the **Dialer**, the sidebar shows Dialer and Team (the rest greyed "coming soon"), and the browser asks for the microphone once.
- [ ] Open the link again: "This link has already been used…".

**The authenticator can't be skipped** (fixed in this phase's review). Sign out, sign in with the password only, stop at the code screen, and in the same tab open the browser console (View → Developer → JavaScript Console) and paste:
```
fetch('/api/v1/me/mfa',{method:'POST',headers:{'X-CSRF-Token':document.cookie.match(/__Host-linx_csrf=([^;]+)/)[1]}}).then(r=>r.json()).then(console.log)
```
- [ ] It answers `sign_in_unfinished` ("Enter the code from your authenticator app first"). Enter your real code to finish signing in.

## 9. Sign in on the phone (mobile data)
Turn the iPhone's **Wi-Fi off**. Send yourself the **Phone Person** link and open it in Safari.
- [ ] You choose a password and land on the Dialer (no authenticator required for a `user`). Allow the microphone.
- [ ] On the laptop, **Team** shows 101 and 102 online and Available, and 103 (Linphone) online. Set yourself **Away** on the phone: the laptop's list changes within a second or two.
- [ ] Set it back to Available.

## 10. Calls
- [ ] **Echo in Opus:** on the laptop, Settings → **Test sound** (or dial `*43`). You hear yourself. On the server, while it's up:
  ```
  for c in $(sudo docker exec linx-asterisk asterisk -rx 'core show channels concise' | cut -d'!' -f1); do
    sudo docker exec linx-asterisk asterisk -rx "core show channel $c" | grep -iE 'NativeFormats|ReadFormat|WriteFormat'; done
  ```
  shows `opus`.
- [ ] **Laptop ↔ phone:** the laptop calls `102`. The phone rings (incoming-call screen with the laptop person's name), answer; both hear each other clearly for 20 seconds; the call panel shows a timer and a connection chip ("Direct" or "Relayed") with a round-trip time; mute works both ways; hang up. Then the phone calls `101` the same way.
- [ ] **Browser ↔ Linphone:** the laptop calls `103`: Linphone rings, both hear each other. Then Linphone calls `101`: the laptop rings, both hear each other.
- [ ] **Do not disturb:** set the laptop person to Do not disturb; the phone calls `101` and hears "nobody is available". Set it back.

## 11. UDP blocked on the laptop (TURN over TLS on 443)
On the laptop, block every UDP packet except DNS:
```
echo 'block drop out quick proto udp from any to any port != 53' | sudo pfctl -ef -
```
Reload the Linx tab (sign in again if asked) and call `102` from the laptop.
- [ ] The call connects, both hear each other, and the chip says **Relayed**. **This is the core of the Phase 1C exit.**

Undo it:
```
sudo pfctl -f /etc/pf.conf && sudo pfctl -d
```

## 12. Wrong passwords: the wait and the alert
On the laptop:
```
for i in $(seq 1 20); do
  curl -sS -H 'Content-Type: application/json' -d '{"email":"you+phone@example.com","password":"a wrong guess entirely"}' \
    https://meet.lab.linxpbx.com/api/v1/session | jq -r .code
  sleep 4
done
```
- [ ] The first 4 answer `sign_in_invalid`, then `account_locked` ("Try again after …").
- [ ] Signing in as Phone Person with the **right** password on a fresh browser tab now also says to wait.
- [ ] About five minutes later webhook.site gets the alert **"Someone is guessing a password"** for `you+phone@example.com`, and `sudo linx doctor` lists it under open alerts.
- [ ] A form post is refused, not signed in: `curl -sS -d 'email=x&password=y' https://meet.lab.linxpbx.com/api/v1/session | jq -r .code` answers `content_type_invalid`.

After the wait (a few minutes; up to the time it names), the phone person signs in again normally, and the alert resolves on the next sign-in.

## 13. Signing out drops the line at once
On the phone, open the menu and **Sign out** (not during a call).
- [ ] On the laptop, Team shows 102 offline within a second or two, and webhook.site gets `device.revoked` for the phone's browser line.
- [ ] The laptop calls `102`: "nobody is available".

The server ends it too, not just the page. Sign the phone person in again, then from the laptop disable them (id from `sudo linx user list`):
```
api -X PATCH -H 'Content-Type: application/merge-patch+json' -d '{"disabled":true}' $API/users/<id> | jq .disabled
```
- [ ] Within a few seconds Team shows 102 offline, and calling `102` gets "nobody is available", though the phone's page is still open.

Re-enable with `{"disabled":false}`; they sign in again with their password. (A call already in progress when a line ends carries on until someone hangs up, like a revoked desk phone's: see the residual risks.)

## 14. Security spot checks
From the laptop:
```
curl -sSI https://meet.lab.linxpbx.com/ | grep -iE 'strict-transport|content-security|x-frame|referrer|permissions'
openssl s_client -connect meet.lab.linxpbx.com:443 -servername meet.lab.linxpbx.com </dev/null 2>/dev/null | grep -E 'subject=|Verify return'
nc -vz 192.168.1.50 8443    # from the laptop, not Pangolin
```
- [ ] All five headers are there; the certificate is Linx's own `*.lab.linxpbx.com` (Pangolin passes it through), `Verify return code: 0 (ok)`.
- [ ] `nc` to 8443 fails: only Pangolin may reach the web port.

On the server:
```
sudo ss -Hlntu | grep -E ':(443|5060|5061|5349|8443) '
sudo ls -l /etc/linx/secrets | grep -E 'turn|ari|sipws|ca_services'
sudo docker exec linx-postgres psql -U linx -d linx -tAc \
  "SELECT action, count(*) FROM audit_log WHERE action LIKE 'sip.relay%' OR action LIKE 'user.%' GROUP BY 1 ORDER BY 1"
```
- [ ] 8443 and 5349 listen on the LAN address, UDP 443 (or your port) is coturn's, 5061 LAN only, nothing on 5060.
- [ ] `linx_turn_secret` is there with the others, root-owned, not world-readable.
- [ ] The audit log shows the people changes (`user.create`, `user.password_set`, `user.mfa_enabled`, …).

## 15. Clean up
```
rm /tmp/e103.json /tmp/d103.json
sudo linx api-key revoke "${KEY:0:17}"
```
Remove the Linx block from Pangolin's `dynamic_config.yml` (put HTTP/3 back if you want it), remove the router's UDP forward, delete the Linphone account, the `meet.`/`api.`/`turn.`/`sip.lab` DNS records, then follow "Clean up" in [`ops/STAGING_TEST.md`](ops/STAGING_TEST.md) and delete the Cloudflare token.

## Results
Add one line per run: date, server, commit, passed or what failed.
