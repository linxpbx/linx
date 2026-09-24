# Phase 1B demo checklist

A hand-run check that the phone engine (`docs/PBX.md` §8, steps 1–6) does what it promises. Tick each box. It takes about an hour and a half, much of it waiting for the certificate and setting up two phone apps.

**Phase 1B exit** (`docs/PBX.md` §7): on a fresh Ubuntu 24.04 server, `linx doctor` is all green; extensions `101` and `102` get one device each through the API; a free SIP app on your iPhone and on your Mac signs in over TLS on home Wi-Fi; `*43` echoes; `101` calls `102` and both hear each other; `call.started`/`answered`/`ended` arrive at webhook.site; revoking a device stops it at once.

Phones connect from your **home network only** in this slice. Nothing is opened to the internet.

## You need
- Everything under "You need" in [`DEMO_PHASE1A.md`](DEMO_PHASE1A.md): your computer with Go, Node.js and Docker; a Cloudflare token for `linxpbx.com`; a browser tab on <https://webhook.site>.
- **The server on your home network.** A VM must use *bridged* networking so it gets an address from your router (like `192.168.1.50`), not a NAT address only your computer can reach (`10.0.2.15`). Phones connect to that address.
- All three images, `linx-certd`, `linx-control-plane` **and** `linx-asterisk`, published on ghcr.io for the commit you build (CI does this on every push to master) and public, or `docker login ghcr.io` on the server first.
- **Linphone**, free, on your iPhone (App Store) and your Mac (linphone.org). It's only a test phone; Linx never ships it.
- An email address for Let's Encrypt. This demo uses a **trusted** certificate, not a test one: phone apps refuse test certificates.

## 1. Docs
- [ ] `docs/PBX.md`, ADR-031 to ADR-035 in `docs/DECISIONS.md` and the "Phone engine" rows and "Phase 1B review" in `docs/THREAT_MODEL.md` read sensibly to you.

## 2. On your computer
```
cd ~/Projects/linx
git pull
make setup-dev
make lint          # ends with "lint: ok"
make test          # failures only; nothing listed means all passed
make security      # govulncheck, npm audit and licences all "ok"
make test-docker   # real Postgres: migrations, store, webhooks, alerts, phone data
make image SERVICE=asterisk   # about 15 minutes the first time
make test-calls    # real Asterisk + SIPp phones over TLS: ends with "call suite: ok"
```
- [x] All finish without errors.

## 3. CI on GitHub
Open the repository → **Actions** → the latest **CI** run on master.
- [x] Every job is green, including `images (asterisk)` for amd64 and arm64 and the call suite.
- [x] Under **Packages**, `linx-asterisk` has an image tagged `sha-<that commit>`.

## 4. Install on a fresh server
Follow steps 1–3 of [`ops/STAGING_TEST.md`](ops/STAGING_TEST.md) with the commit CI just built, with one difference: when setup asks **"Use test certificates for now?"**, answer **n**, then give your email.
- [ ] Setup says "Phones will be able to connect from your local network (192.168.1.0/24), where this server is 192.168.1.50" (your numbers), and says it's writing the firewall rules and restarting Docker once.
- [ ] It ends with "Linx is running", "Trusted certificate issued for *.lab.linxpbx.com", and a **Phones** block asking for a DNS record.

**Add the DNS record it asks for.** In Cloudflare → `linxpbx.com` → DNS → Add record: type **A**, name `sip.lab`, IPv4 address = the server's address from the Phones block, proxy status **DNS only** (grey cloud).
```
sudo docker compose --file /etc/linx/compose.yaml ps
```
- [ ] `linx-postgres`, `linx-control-plane` and `linx-asterisk` are `healthy`; `linx-step-ca` healthy, `linx-certd` running.

On a throwaway server, delete the root key backup so it doesn't warn: `sudo rm -r /etc/linx/ca-backup`.

## 5. `linx doctor`
```
sudo ./linx doctor
```
- [ ] A **Phone system** heading, all `ok`: running and answering; reads the phone settings from the database and nothing else; connected to the API service; only encrypted phone connections (port 5061); ports open on the local network address only; phones get the current certificate; nothing offers port 5060; the firewall only lets phones connect from your local network; "Phones find this server at sip.lab.linxpbx.com".
- [ ] The database line shows `schema version 9`. The only warnings are no API key and no alert channel, as in Phase 1A. (If the DNS line warns, the record from step 4 hasn't spread yet: wait a few minutes.)

## 6. API key and webhook
A key that can make phone logins needs the sensitive scope `devices:write` by name:
```
sudo ./linx api-key create --name "Demo" --role admin --scopes all,devices:write
sudo apt-get install -y jq
KEY='linx_...'        # paste the key between the quotes
api()  { sudo docker run --rm --network linx-public curlimages/curl:8.11.1 -sS -H "Authorization: Bearer $KEY" "$@"; }
API=http://linx-control-plane:8080/api/v1
JSON='Content-Type: application/json'
HOOK_URL='https://webhook.site/...'    # your address from webhook.site
api -X POST -H "$JSON" -d "{\"url\":\"$HOOK_URL\",\"description\":\"Demo\"}" $API/webhooks | jq .webhook.id
```
- [ ] The key lists `devices:write` among its scopes; the webhook is created.

## 7. Extensions and phone logins
```
api -X POST -H "$JSON" -d '{"number":"101","display_name":"iPhone"}' $API/extensions | tee /tmp/e101.json | jq
api -X POST -H "$JSON" -d '{"number":"102","display_name":"Mac"}' $API/extensions | tee /tmp/e102.json | jq
api -X POST -H "$JSON" -d '{"name":"My iPhone"}' $API/extensions/$(jq -r .id /tmp/e101.json)/devices | tee /tmp/d101.json | jq -r .settings_text
api -X POST -H "$JSON" -d '{"name":"My Mac"}'    $API/extensions/$(jq -r .id /tmp/e102.json)/devices | tee /tmp/d102.json | jq -r .settings_text
```
- [x] Each device prints a settings block: Server `sip.lab.linxpbx.com`, Port `5061`, Transport `TLS`, a username like `d_k2m9x4qa`, a long password, "Encrypted audio: required (SRTP)".
- [x] `api $API/devices/$(jq -r .device.id /tmp/d101.json) | jq` shows the device **without** the password.
- [x] On webhook.site: `extension.created` twice and `device.created` twice, none containing a password.

(The files in `/tmp` hold the passwords: `rm /tmp/d10*.json` at the end.)

## 8. Sign in two phones
Both on your home Wi-Fi. In Linphone (iPhone: the first screen offers "Use SIP account" / "Third-party SIP account"; Mac: "Use a SIP account"):
- Username and password: from the settings block (iPhone gets 101's, Mac gets 102's).
- Domain: `sip.lab.linxpbx.com`. Transport: **TLS**.
- In Linphone's settings: **Media encryption: SRTP**, and turn on **Media encryption is mandatory**.
- If it doesn't connect: in the account's advanced settings set the SIP server / proxy to `sip:sip.lab.linxpbx.com:5061;transport=tls`.

```
api $API/extensions/$(jq -r .id /tmp/e101.json)/devices | jq '.items[] | {name, online, last_registered_from}'
```
- [x] Both apps show the account as connected (green), with no certificate warning.
- [x] The device shows `"online": true` and your phone's address; webhook.site has `device.registered`.

## 9. Calls
- [x] **Echo test:** on the iPhone, dial `*43`. You hear yourself about half a second late. Hang up.
- [ ] **Messages:** dial `555` (no such number): "the number you have dialled is not in service"-style message. Dial `101` from the iPhone itself: "nobody is available" message.
- [ ] **A real call:** from the iPhone dial `102`. The Mac rings, shows "iPhone" / `101` as the caller; answer; both sides hear each other clearly for about 20 seconds; hang up.
- [ ] While it's up: `api $API/calls/active | jq` shows one call, `state` `answered`, from `101` to `102`, and `"phone_engine_connected": true`.
- [ ] On webhook.site: `call.started`, `call.answered` (with `answered_by`) and `call.ended` (`outcome` `answered`, `duration_seconds` about 20), all with the same `id`. **This is the core of the Phase 1B exit.**
- [ ] **Nobody answers:** call `102` again and don't answer. After 30 s the iPhone hears "nobody is available"; webhook.site gets `call.missed`.
- [ ] **Unencrypted audio is refused:** on the Mac, set Media encryption to **None** and call `*43`. The call fails at once (Linphone shows an error, e.g. "Not acceptable"). Set it back to SRTP (mandatory).

## 10. Revoke a device
```
api -X DELETE $API/devices/$(jq -r .device.id /tmp/d101.json) -o /dev/null -w '%{http_code}\n'    # 204
api -X PATCH -H 'Content-Type: application/merge-patch+json' -d '{"enabled":true}' $API/devices/$(jq -r .device.id /tmp/d101.json) | jq .code
```
- [ ] The first answers `204`; webhook.site gets `device.revoked`. Trying to turn it back on answers `"device_revoked"`: a revoked device stays revoked (add a new device instead).
- [ ] From the Mac, call `101`: straight to "nobody is available" (the iPhone doesn't ring).
- [ ] From the iPhone, call `102` or `*43`: the call fails. Within a minute, or when you reopen the app, its account shows an error. **This is the Phase 1B exit.**

## 11. Security spot checks (on the server)
```
sudo ss -Hlntu | grep -E ':(5060|5061) '                   # only <LAN address>:5061
sudo nft list table inet linx | head -20                  # phone_networks = your LAN only
sudo ls -l /etc/linx/secrets                              # root-owned, never world-readable
ps -eo uid,comm | grep -E ' (asterisk|service)$'          # 100 (asterisk), 65532 (service); never 0
sudo docker exec linx-postgres psql -U linx -d linx -tAc \
  "SELECT has_table_privilege('linx_asterisk','api_key','SELECT'), has_table_privilege('linx_asterisk','device','SELECT')"
PW=$(jq -r .password /tmp/d102.json)
sudo docker exec linx-postgres psql -U linx -d linx -tAc \
  "SELECT (SELECT count(*) FROM audit_log WHERE detail::text LIKE '%$PW%') + (SELECT count(*) FROM event_outbox WHERE position('$PW' in encode(body, 'escape')) > 0)"
```
- [ ] 5061 listens on the LAN address only, and nothing on 5060. The firewall table lists your network.
- [ ] The new secrets `linx_asterisk_db_password`, `linx_ari_password` and `linx_ca_services_password` are there with the others.
- [ ] Asterisk and the API service don't run as root.
- [ ] `f|f`: Asterisk's database login can't read API keys, or even the device table (only its views).
- [ ] The last count is `0`: the Mac's password isn't in the audit log or webhook payloads.

From your Mac (on the home Wi-Fi):
```
openssl s_client -connect sip.lab.linxpbx.com:5061 -servername sip.lab.linxpbx.com </dev/null 2>/dev/null | grep -E 'Protocol|Verify return'
```
- [ ] `TLSv1.3` and `Verify return code: 0 (ok)`.

**Doctor catches a problem:**
```
sudo docker stop linx-asterisk
sudo ./linx doctor; echo "exit code $?"
sudo docker compose --file /etc/linx/compose.yaml up -d
```
- [ ] Doctor reports a `problem` under Phone system (not running) with a `Fix:` line, and exit code 1. The Mac's account goes red. After `up`, within a minute doctor is all green again, the Mac reconnects on its own (or when you reopen it), and `*43` works again.

## 12. Clean up
```
rm /tmp/d10*.json /tmp/e10*.json
sudo ./linx api-key revoke "${KEY:0:17}"
```
Remove the Linphone accounts, delete the `sip.lab` DNS record, then follow "Clean up" in [`ops/STAGING_TEST.md`](ops/STAGING_TEST.md) and delete the Cloudflare token.

## Results
Add one line per run: date, server, commit, passed or what failed.
