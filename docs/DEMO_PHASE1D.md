# Phase 1D demo checklist

A hand-run check that phone lines to the outside world (`docs/TRUNKS.md` §13, steps 1–7) do what they promise. Tick each box. Allow about two hours. Most of it is the UCM6304's side and the router.

**Phase 1D exit** (`docs/TRUNKS.md` §14): through **Pangolin**, `linx doctor` is all green including "Phone lines". `linx trunk add` connects the UCM6304 over TLS with its pinned certificate. A browser on mobile data **with UDP blocked** calls your mobile through the UCM's landline, and both hear each other (the Phase 1 exit test). Calling the landline's number from your mobile rings the browser. A "Staff" extension is refused an international number and hears why. `linx route test 999 --from 101` and `901` show "always allowed" (never dialled for real). The emergency and trunk-down alerts arrive. An unencrypted test trunk shows the warning and appears in doctor's list. Automated: the call suite (trunk scenarios) and the browser suite (browser → trunk with UDP blocked) are green in CI.

The examples use `lab.linxpbx.com`, the Linx server at `192.168.1.50`, Pangolin at `192.168.1.20`, the UCM6304 at `192.168.1.60` and the landline number `042345678`. Use your own.

**Never dial an emergency number (999, 998, 997, 112, 901) for real in this demo.** Step 12 shows the emergency alert only with every line switched off first.

## You need
- Everything under "You need" in [`DEMO_PHASE1C.md`](DEMO_PHASE1C.md): the server on your home network (bridged VM), Pangolin at home with the UDP audio port forwarded, a trusted certificate, an authenticator app, a fresh webhook.site address.
- **The UCM6304** on the same home network, with at least one working **landline** plugged into an FXO port, and its admin password.
- **A mobile phone with a normal phone number** (your iPhone) to call and be called on.
- **A browser on mobile data that isn't that phone**: the iPad on the iPhone's hotspot works (the iPhone can't take a normal call while its own browser holds the call).
- The images `linx-certd`, `linx-control-plane`, `linx-asterisk`, `linx-coturn` **and** `linx-wireguard` published by CI for the commit you build.

## 1. Docs
- [ ] `docs/TRUNKS.md`, ADR-043 to ADR-048 in `docs/DECISIONS.md` and the "Phase 1D review" in `docs/THREAT_MODEL.md` read sensibly to you.

## 2. On your computer
```
cd ~/Projects/linx
git pull
make setup-dev
make lint          # ends with "lint: ok"
make test          # failures only; nothing listed means all passed
make security      # govulncheck, npm audit and licences all "ok"
make test-docker   # real Postgres: ends with "docker tests: ok"
make image SERVICE=asterisk && make image SERVICE=wireguard && make image SERVICE=control-plane && make image SERVICE=coturn
make test-calls    # SIPp phones and SIPp "providers": ends with "call suite: ok"
make test-browser  # two Chromiums behind four front doors; one calls out through a SIPp provider with UDP blocked
```
- [ ] All finish without errors.

## 3. CI on GitHub
Open the repository → **Actions** → the latest **CI** run on master.
- [ ] Every job is green, including the call suite and the browser call suite in the amd64 Asterisk job, and `images (wireguard)` for amd64 and arm64.
- [ ] Under **Packages**, `linx-wireguard` has an image tagged `sha-<that commit>`.

## 4. Install or update the server
Follow steps 1–3 of [`ops/STAGING_TEST.md`](ops/STAGING_TEST.md) with the commit CI just built (copy `bin/linx-firewall-sync` too, next to `linx`: setup installs it from there), on a fresh server or the Phase 1C one. Answer as in Phase 1C: trusted certificate, **Pangolin** at `192.168.1.20`, the same UDP audio port.
- [ ] Setup ends with "Linx is running" and the **Calls from outside** block, as in Phase 1C.
- [ ] `systemctl status linx-firewall-sync.timer` is **active (waiting)**, and `ls /etc/linx/TRUNK-AUDIO-FORWARD.txt` exists.
- [ ] `lsmod | grep wireguard` shows the module (setup loads it).

On a fresh server, redo Phase 1C's Pangolin block and the `sip.lab` DNS record.

## 5. `linx doctor`
```
sudo linx doctor
```
- [ ] Everything under **Phone system** and **Calls from outside** is `ok`, as in Phase 1C.
- [ ] A **Phone lines** heading: country "United Arab Emirates". It **warns** that no line can carry emergency calls yet. That's expected until step 8. (The firewall sync timer and the two provider sets are checked only once a line needs them: step 13's test line. A LAN phone system like the UCM doesn't, since the local network is already let in.)
- [ ] The database line shows `schema version 20`.

## 6. Key, alert channel, extensions, call permissions
A key that sets up phone lines and decides who may call what needs `trunks:write` and `routing:write` by name (both can cost money, so "all" leaves them out):
```
# on the server
sudo linx api-key create --name "Demo" --role admin --scopes all,devices:write,users:write,trunks:write,routing:write

# on the laptop
KEY='linx_...'
API=https://meet.lab.linxpbx.com/api/v1
api() { curl -sS -H "Authorization: Bearer $KEY" "$@"; }
JSON='Content-Type: application/json'
PATCH='Content-Type: application/merge-patch+json'
HOOK_URL='https://webhook.site/...'   # a fresh address: a free one stops accepting (429) after about 100 requests, and a demo sends more
api -X POST -H "$JSON" -d "{\"name\":\"Demo\",\"kind\":\"webhook\",\"config\":{\"url\":\"$HOOK_URL\"}}" $API/alert-channels | jq .id
api -X POST -H "$JSON" -d "{\"url\":\"$HOOK_URL\",\"description\":\"Demo\"}" $API/webhooks | jq .webhook.id
api -X POST -H "$JSON" -d '{"number":"101","display_name":"Browser"}' $API/extensions | tee /tmp/e101.json | jq .number
api -X POST -H "$JSON" -d '{"number":"102","display_name":"Staff"}'   $API/extensions | tee /tmp/e102.json | jq .number
api -X POST -H "$JSON" -d '{"name":"Local calls","allowed_categories":["landline","mobile","national","toll_free"]}' \
  $API/call-permission-levels | tee /tmp/local.json | jq .id
api -X PATCH -H "$PATCH" -d "{\"call_permission_level_id\":\"$(jq -r .id /tmp/local.json)\"}" $API/extensions/$(jq -r .id /tmp/e101.json) | jq .call_permission_level_id
api -X PATCH -H "$PATCH" -d "{\"call_permission_level_id\":\"$(jq -r .id /tmp/local.json)\"}" $API/extensions/$(jq -r .id /tmp/e102.json) | jq .call_permission_level_id
```
Then the two people, on the server:
```
sudo linx user create --email you@example.com       --name "Browser Person" --role admin --extension 101
sudo linx user create --email you+staff@example.com --name "Staff Person"   --role user  --extension 102
```
- [ ] The alert channel, the webhook, both extensions and the level are created, and both extensions show the level's id.
- [ ] Open each setup link and set a password (the admin also scans the authenticator code), as in Phase 1C.

**Choosing a level needs `routing:write`** (fixed in this phase's review). Make a key without it and try:
```
sudo linx api-key create --name "No routing" --role admin    # on the server; "all" only
KEY2='linx_...'
curl -sS -H "Authorization: Bearer $KEY2" -X PATCH -H "$PATCH" -d '{"call_permission_level_id":""}' \
  $API/extensions/$(jq -r .id /tmp/e102.json) | jq -r .code
```
- [ ] It answers `scope_missing`. Revoke that key: `sudo linx api-key revoke "${KEY2:0:17}"`.

## 7. Get the UCM6304 ready
Menu names below are as tested on UCM6304 firmware **1.0.33.30** (2026-09-26). The goal is written first, so you can find the place if a name differs. Press **Apply Changes** (top right) after each save.

**a. A certificate that names the UCM's address.** Linx always checks that a certificate names the address it dialled, and the UCM's built-in one usually doesn't (a wildcard one like `*.example.com` is refused too). Linx makes one for it. On the server (no `sudo` needed):
```
linx trunk cert 192.168.1.60
```
- [ ] It writes `linx-line-192.168.1.60.crt` (the certificate) and `linx-line-192.168.1.60.key` (its private key), and prints a SHA-256 fingerprint. Write the fingerprint down.

Copy both files to the laptop (`scp you@192.168.1.50:linx-line-192.168.1.60.* .`). On the UCM: **PBX Settings → SIP Settings → TCP/TLS** (not System Settings → HTTP Server, which is the web page's certificate). TLS on, bind `0.0.0.0:5061`. Click **Reset** next to "Certificate & Private Key" first (a new upload doesn't replace an old certificate otherwise: the UCM names every upload `TLS.crt`/`TLS.key`, so the page can't tell you which one is loaded), then upload the `.key` as **TLS Private Key** and the `.crt` as **TLS Cert**; leave TLS CA Cert and Server Certificate Verification alone. Then delete the `.key` from the laptop and the server: Linx doesn't keep it, and only the UCM needs it.
- [ ] On the server, `echo | openssl s_client -connect 192.168.1.60:5061 2>/dev/null | openssl x509 -noout -fingerprint -sha256` prints the fingerprint from above.

**b. A peer trunk to Linx.** **Extension/Trunk → VoIP Trunks → Add SIP Trunk**: type **Peer SIP Trunk**, provider name `Linx`, host name `192.168.1.50:5061`, transport **TLS**, no caller ID. Advanced Settings: codecs **PCMA** then **PCMU** only; SRTP **Enabled and forced** (crypto suite `AES_CM_128_HMAC_SHA1_80`); DTMF **RFC4733**; DID Mode **Request-line**; **Enable Heartbeat Detection** on. No login: each side knows the other by address.

**c. Linx's calls → the landline.** Linx sends numbers the UAE's local way (`0501234567`) and shows the landline's number (`042345678`, the DID you give it in step 8) as the caller. Two changes:
1. **Inbound Routes**, trunk **SIP Trunks -- Linx** → Add: name `From_Linx`, pattern `_X.`, **Dial Trunk** on (in the Pattern section), privilege **Local**, Default Destination **By DID**.
2. The outbound route that uses the landline (FXO) trunk: if its Privilege Level is "Disable" (only allowlisted extensions), add `042345678` to its **Source Caller ID Pattern**; otherwise its level must be Local or lower. Add `999`, `998`, `997`, `112` and `901` to its patterns if they aren't there, or Linx's emergency calls stop at the UCM.

**d. Landline calls → Linx.** The UCM can't send an inbound call straight to a trunk, so it dials Linx through a small outbound route:
1. **Outbound Routes → Add**: name `To_Linx`, pattern `_*88X.`, Privilege Level **Internal**, source caller ID allowlist off, Main Trunk **SIP Trunks -- Linx**, Strip `3`.
2. **Inbound Routes**, trunk **Analog Trunks -- (the landline)**: note its current Default Destination (to put back in step 15), then set it to **External Number** `*88042345678`. This is the number Linx matches to ring 101. The house phones don't ring for the landline while this is set.

- [ ] The UCM saves all of these without errors.

## 8. Connect the UCM: `linx trunk add`
In the same folder as the certificate from step 7a:
```
sudo linx trunk add --template grandstream_ucm --host 192.168.1.60 --pin linx-line-192.168.1.60.crt
```
Answer: name `UCM landlines`, port `5061`. It tests the connection against the certificate you pinned. Phone number: `042345678`, ringing extension `101`. Outgoing: **primary**.

(All in one go: add `--name "UCM landlines" --port 5061 --did 042345678=101 --outgoing primary --yes`.)
- [ ] If the certificate step fails with "is for …, not 192.168.1.60" or "isn't signed by…", the UCM is still serving its old certificate: check the upload in step 7a (some firmware needs a restart of the SIP service).
- [ ] The test steps all say `ok`: address, connection, certificate ("the one you pinned"), SIP answer. The line is saved.
- [ ] `sudo linx trunk list` shows it **reachable**, "Encrypted (TLS + SRTP)", outgoing 1.
- [ ] `sudo linx doctor`: the **Phone lines** section is all `ok` now, including "emergency calls have a line". Webhook.site got `trunk.created`, with no password in it.

## 9. What Linx would do (nothing is dialled)
```
sudo linx route test 999 --from 101
sudo linx route test 901 --from 102
sudo linx route test "050 123 4567" --from 101
sudo linx route test "+44 20 7946 0958" --from 102
sudo linx route test "+44 20 7946 0958" --from 103
```
- [ ] 999: an emergency number (police), "Always allowed, for everyone, and never limited", going out on "UCM landlines" as `999`.
- [ ] 901: always allowed (police, non-emergency), the same.
- [ ] The mobile: allowed for 101, out on "UCM landlines" as `0501234567`.
- [ ] The UK number from 102 (Staff): "Extension 102 isn't allowed to call this kind of number…".
- [ ] From 103: "There is no extension "103"."

## 10. Calls through the landline
Sign in as Browser Person (101) in the iPad's browser, on **the iPhone's hotspot** (mobile data).

- [ ] **Out:** the iPad dials your iPhone's mobile number (`05…`). The iPhone rings, showing the landline's number (or the UCM's caller ID). Answer, and both hear each other clearly for 20 seconds. Hang up.
An analog landline doesn't tell the UCM when the far end answers or hangs up (UAE lines, 2026-09-26): the call counts as answered, and the timer starts, as soon as the UCM has dialled; and if the mobile hangs up first, the browser hears the busy tone until you hang up there, unless the UCM's busy-tone detection catches it (it didn't on the demo line, with the right tone set: 400 Hz, 375/375 ms). Don't turn on the analog trunk's "Polarity Reversal" unless your line supports it: the UCM then waits for an answer signal that never comes, and calls stay silent. Call records for such a line say "answered" even when nobody picked up.
- [ ] **In:** from the iPhone, call the landline `042345678`. The iPad rings (incoming-call screen with your mobile number). Answer, and both hear each other. Hang up.
- [ ] **Refused:** sign in as Staff Person (102) in another browser and dial `+44 20 7946 0958`. You hear "I'm sorry, that feature is not available on this line" and nothing goes out. `sudo docker logs linx-asterisk 2>&1 | tail -5` shows no call to the UCM.
- [ ] Webhook.site got `call.started` and `call.ended` for each, with `direction` `outbound`/`inbound`, the outside number, and for the refused one the outcome `not_permitted`.

**The Phase 1 exit: UDP blocked.** Find the hotspot's public address: on the iPad, open `https://1.1.1.1/cdn-cgi/trace` and read `ip=`. On the server:
```
sudo nft add table inet linxdemo
sudo nft add chain inet linxdemo pre '{ type filter hook prerouting priority -300; }'
sudo nft add rule inet linxdemo pre ip saddr HOTSPOT_IP meta l4proto udp counter drop
```
Reload the Linx tab on the iPad (sign in again if asked) and dial your iPhone's mobile number again.
- [ ] The call connects, both hear each other, and the iPad's chip says **Relayed**. `sudo nft list table inet linxdemo` shows the counter above 0. **This is the Phase 1 exit test.**

Undo it: `sudo nft delete table inet linxdemo`.

## 11. The line going down
Unplug the UCM's **network** cable (leave the landline in).
- [ ] Within about 4 minutes `sudo linx trunk list` shows it **unreachable**, webhook.site gets `trunk.status_changed`, and the alert channel gets **Phone line "UCM landlines" is down** (critical: it's the only outgoing line).
- [ ] The iPad dials the mobile: "all circuits are busy".

Plug it back in.
- [ ] Within a couple of minutes it's reachable again and the alert's "resolved" notice arrives.

## 12. The emergency alert (optional, with every line off)
Linx raises an alert the moment anyone dials an emergency number. To see it without calling anyone, switch the only line off first:
```
TRUNK=$(api $API/trunks | jq -r '.items[] | select(.name=="UCM landlines") | .id')
api -X PATCH -H "$PATCH" -d '{"enabled":false}' $API/trunks/$TRUNK | jq .enabled
sudo linx route test 999 --from 101
```
**Only if** `route test` says "No outside line is set up for outgoing calls, so it can't go out until one is": dial `999` from the iPad.
- [ ] You hear "all circuits are busy", and the alert channel gets **"Emergency call from extension 101"** at once.

Switch the line back on: the same `PATCH` with `{"enabled":true}`, then `sudo linx trunk list` shows it reachable.
Skip this step if you'd rather not dial it at all: the call suite checks the same alert.

## 13. An unencrypted line shows the warning
A line that goes nowhere (a documentation address), just to see the warning:
```
api -X POST -H "$JSON" -d '{"name":"Test unencrypted","kind":"ip_authenticated","host":"192.0.2.10","port":5060,"transport":"udp","media_encryption":"none"}' $API/trunks | jq -r '.code, .detail'
api -X POST -H "$JSON" -d '{"name":"Test unencrypted","kind":"ip_authenticated","host":"192.0.2.10","port":5060,"transport":"udp","media_encryption":"none","confirm_unencrypted":true}' $API/trunks | tee /tmp/plain.json | jq '.unencrypted_confirmed_by'
```
- [ ] The first is refused with `unencrypted_confirmation_required`: "Calls to and from this trunk can be listened to on the way…". The second is saved, with your key as the one who confirmed it.
- [ ] Pointing it somewhere else asks again (fixed in this phase's review): `api -X PATCH -H "$PATCH" -d '{"host":"192.0.2.11"}' $API/trunks/$(jq -r .id /tmp/plain.json) | jq -r .code` answers `unencrypted_confirmation_required`.
- [ ] `sudo linx doctor` lists it under **Phone lines** as unencrypted, with who confirmed it (and as unreachable, which is expected).
- [ ] Within a minute, `sudo nft list set inet linx trunk_plain_addresses` holds `192.0.2.10`: the firewall lets this "provider" in, and nobody else.
- [ ] `sudo linx doctor` now also shows the firewall sync timer on and the two provider sets matching.

Delete it: `api -X DELETE $API/trunks/$(jq -r .id /tmp/plain.json)`. Within a minute both sets are empty again.

## 14. Security spot checks
On the server:
```
sudo docker exec linx-asterisk asterisk -rx "pjsip show endpoint trunk-$TRUNK" | grep -E 'context|identify_by|allow_transfer|media_encryption '
sudo docker exec linx-asterisk ls -l /var/lib/linx/trunks
sudo ss -Hlntu | grep -E ':(5060|5061|5062) '
api $API/trunks/$TRUNK | grep -ci password
sudo docker exec linx-postgres psql -U linx -d linx -tAc \
  "SELECT action, detail FROM audit_log WHERE action LIKE 'trunk.%' OR action LIKE 'call_permission_level.%' ORDER BY at DESC LIMIT 8"
```
- [ ] The UCM's endpoint: context `linx-from-trunk`, identified by address only, `allow_transfer` `false` (fixed in this phase's review), media encryption `sdes`.
- [ ] The trunks file is readable by Asterisk's group only (`-rw-r-----`).
- [ ] 5061 listens on the LAN address only; nothing on 5060; 5062 isn't published on the host.
- [ ] The trunk as the API returns it has no password field (count `0`).
- [ ] The audit log shows the trunk and level changes, and no password or key values.

## 15. Clean up
```
rm -f /tmp/e10*.json /tmp/local.json /tmp/plain.json linx-line-192.168.1.60.*
sudo linx trunk remove "UCM landlines" --yes
sudo linx api-key revoke "${KEY:0:17}"
```
On the UCM, put back its inbound route for the landline and remove the Linx peer trunk. Then follow Phase 1C's "Clean up" if you're done with the server.

## Results
Add one line per run: date, server, commit, passed or what failed.
- 2026-09-26, Ubuntu 24.04 VM on the home LAN (`pbx.mym.ae`, Pangolin at home), UCM6304 fw 1.0.33.30 with landline FXO1, commit `710caea` (+ fixes in the next commit): **passed.** Line added with the pinned certificate (all probe steps ok), calls out and in through the landline with audio both ways and the right caller ID, 102 refused abroad (`not_permitted`, nothing sent to the UCM), browser on mobile data with UDP blocked called a mobile through the landline (**Relayed**, 18 UDP packets dropped: Phase 1 exit), line down → unreachable and critical alert after 2 min → resolved on reconnect, unencrypted test line warned, re-confirmed on move, listed by doctor, admitted by the firewall and removed again, security spot checks all as expected. Step 12 skipped (optional). Found: the UCM's steps (step 7 rewritten as tested); the UCM had no route for emergency numbers from Linx (added); `route test` added a wrong "no outside line" sentence after a refusal (fixed); doctor named the confirmer `api_key:<id>` (now the key's name); setup only installs `linx-firewall-sync` when it's copied next to `linx` (docs); webhook.site hit its free quota (429) mid-demo, so the down/resolved alerts were sent but refused; an analog landline gives no answer or hang-up signal (see step 10), not a Linx fault.
