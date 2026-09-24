# Phase 1A demo checklist

A hand-run check that the first slice of Phase 1 — the public API, webhooks and admin alerts (`docs/API.md` §8, steps 1–6) — does what it promises. Tick each box. It takes about an hour, much of it waiting for the certificate and the 5-minute alert hold-back.

**Phase 1A exit:** on a fresh server, `linx doctor` is all green with one API key and one alert channel; a signed test webhook arrives and its signature can be seen; an admin alert reaches your phone, and a "resolved" message follows when the problem is fixed.

The API isn't published to the internet yet (the edge arrives with the deployment profiles), so on the server you call it through a throwaway `curl` container attached to Linx's internal network. Nothing listens on a public port.

## You need
- Everything under "You need" in [`DEMO_PHASE0.md`](DEMO_PHASE0.md): your computer with Go, Node.js and Docker; a fresh Ubuntu 24.04 server; a Cloudflare token for `linxpbx.com`.
- Both images, `linx-certd` **and** `linx-control-plane`, published on ghcr.io for the commit you build (CI does this on every push to master) and public, or `docker login ghcr.io` on the server first.
- The free **ntfy** app on your phone (no account needed).
- A browser tab on <https://webhook.site>: it gives you a private test address that shows every request it receives.

## 1. Docs
- [ ] `docs/API.md` §§1–8, ADR-025 to ADR-030 in `docs/DECISIONS.md` and the "Phase 1A review" in `docs/THREAT_MODEL.md` read sensibly to you.

## 2. On your computer
```
cd ~/Projects/linx
git pull
make setup-dev
make lint          # ends with "lint: ok"
make test          # failures only; nothing listed means all passed
make security      # govulncheck, npm audit and licences all "ok"
make test-docker   # real Postgres: migrations, API keys, webhooks, alerts, doctor's query
```
- [ ] All four finish without errors.

## 3. CI on GitHub
Open the repository → **Actions** → the latest **CI** run on master.
- [ ] Every job is green, including `images (control-plane)`.
- [ ] Under **Packages**, `linx-control-plane` has an image tagged `sha-<that commit>`.

## 4. Install on a fresh server
Follow steps 1–3 of [`ops/STAGING_TEST.md`](ops/STAGING_TEST.md) with the commit CI just built.
- [ ] Setup ends with "Linx is running" and "Test certificate issued for *.lab.linxpbx.com".
```
sudo docker compose --file /etc/linx/compose.yaml ps
```
- [ ] `linx-postgres` and `linx-control-plane` are both `healthy`; `linx-step-ca` healthy, `linx-certd` running.

On a throwaway server, delete the root key backup so it doesn't warn (on a real one, copy it off first): `sudo rm -r /etc/linx/ca-backup`.

## 5. `linx doctor` on a fresh install
```
sudo ./linx doctor
```
- [ ] Four headings: **Services**, **Certificates**, **Database, access and alerts**, **Secrets**.
- [ ] Every line is `ok` except two `warning`s, each with a `Fix:` line: no API key yet, and no alert channels turned on. The database line shows `schema version 4`. It ends with "Working, with 2 warnings to look at." and exit code 0.

## 6. The first API key
```
sudo ./linx api-key create --name "Demo" --role admin
```
- [ ] It prints a key starting `linx_`, says it won't be shown again, and lists the scopes (`all` doesn't include `api_keys:write`, `oauth_clients:write`, `outbound_allowlist:write`, recordings, transcripts or call control).

Keep it in the shell for the rest of the demo, and set up two helpers (`jq` reads the answers):
```
sudo apt-get install -y jq
KEY='linx_...'        # paste the key between the quotes
api()  { sudo docker run --rm --network linx-public curlimages/curl:8.11.1 -sS -H "Authorization: Bearer $KEY" "$@"; }
API=http://linx-control-plane:8080/api/v1
```
```
api $API/me | jq
api $API/event-types | jq '.items[].name'
sudo docker run --rm --network linx-public curlimages/curl:8.11.1 -sS $API/me | jq .code
FAKE=linx_notarealkey
sudo docker run --rm --network linx-public curlimages/curl:8.11.1 -sS -H "Authorization: Bearer $FAKE" $API/me | jq .code
```
- [ ] `/me` shows your key's id, type `api_key`, role `admin` and its scopes.
- [ ] The event types include `webhook.test`, `alert.fired` and `alert.resolved`.
- [ ] Without a key: `"auth_required"`. With a made-up key: `"auth_invalid"`.

**Scopes are enforced:**
```
sudo ./linx api-key create --name "Read only" --role reporter    # copy this second key
READ='linx_...'
sudo docker run --rm --network linx-public curlimages/curl:8.11.1 -sS -H "Authorization: Bearer $READ" \
  -X POST -H 'Content-Type: application/json' -d '{"url":"https://example.com/x"}' $API/webhooks | jq .code
sudo ./linx api-key revoke "${READ:0:17}"     # the key's public start, linx_ + 12 letters
```
- [ ] The reporter key gets `scope_missing`. After revoking, `sudo ./linx api-key list` shows it revoked.

## 7. Webhooks
Copy your address from webhook.site (it looks like `https://webhook.site/1234abcd-...`).
```
HOOK_URL='https://webhook.site/...'
api -X POST -H 'Content-Type: application/json' -d "{\"url\":\"$HOOK_URL\",\"description\":\"Demo\"}" $API/webhooks | tee /tmp/hook.json | jq
HOOK=$(jq -r .webhook.id /tmp/hook.json)
api -X POST $API/webhooks/$HOOK/test | jq '{status, log}'
```
- [ ] Creating it shows a `secret` starting `whsec_` once. `GET $API/webhooks/$HOOK` doesn't show it.
- [ ] The test shows `"status": "succeeded"` and a 200 in its log.
- [ ] On webhook.site, the request has `webhook-id`, `webhook-timestamp` and `webhook-signature: v1,...` headers, and a body with `"type":"webhook.test"`.

**Unsafe addresses are refused:**
```
for u in https://169.254.169.254/latest https://10.0.0.1/hook https://127.0.0.1/hook http://example.com/hook; do
  api -X POST -H 'Content-Type: application/json' -d "{\"url\":\"$u\"}" $API/webhooks | jq -r '.code + ": " + .detail'
done
```
- [ ] The first three are `url_blocked` (cloud metadata, a private address, the server itself); the last is `url_invalid` (not https).

## 8. Admin alerts
Pick a hard-to-guess topic name (anyone who knows it can read it), and subscribe to it in the ntfy app on your phone.
```
TOPIC=linx-demo-$(openssl rand -hex 6); echo $TOPIC
api -X POST -H 'Content-Type: application/json' \
  -d "{\"kind\":\"ntfy\",\"name\":\"My phone\",\"config\":{\"topic\":\"$TOPIC\"}}" $API/alert-channels | tee /tmp/ch.json | jq
CH=$(jq -r .alert_channel.id /tmp/ch.json)
api -X POST $API/alert-channels/$CH/test | jq
```
- [ ] The channel is created; its settings (the topic) are **not** in the response.
- [ ] The test answers `"succeeded": true` and your phone shows "Test alert from Linx".
- [ ] `sudo ./linx doctor` is now **all green**: "1 API key can use the API", "1 alert channel will be told about problems", "No open alerts", ending "Everything checked is working." **This is the first half of the Phase 1A exit.**

**A real alert, end to end.** Turn the webhook off: that's a problem Linx reports.
```
api -X PATCH -H 'Content-Type: application/merge-patch+json' -d '{"enabled":false}' $API/webhooks/$HOOK | jq '{enabled, disabled_reason}'
api "$API/alerts?status=open" | jq '.items[] | {title, severity, status}'
sudo ./linx doctor
```
- [ ] The webhook shows `"enabled": false`, `"disabled_reason": "admin"`. The alert "A webhook was turned off" is open straight away, and doctor lists it as a `warning`.
- [ ] Your phone stays quiet for about 5 minutes (a problem must last 5 minutes before anyone is told), then shows "A webhook was turned off".

Turn it back on:
```
api -X PATCH -H 'Content-Type: application/merge-patch+json' -d '{"enabled":true}' $API/webhooks/$HOOK | jq .enabled
api "$API/alerts?status=resolved" | jq '.items[0] | {title, status, resolved_at}'
```
- [ ] Within a few seconds your phone shows the same alert marked resolved (✅), and the alert is `resolved`. `sudo ./linx doctor` is all green again. **This is the Phase 1A exit.**

## 9. Security spot checks (on the server)
```
sudo ls -l /etc/linx/secrets                                  # every file root-owned, -r--r-----
sudo docker ps --format '{{.Names}}  ports: {{.Ports}}'       # no Linx container publishes a port
ps -eo uid,comm | grep -E ' (service|step-ca|postgres)$'     # 65532 (service), 1000 (step-ca), 999 (postgres); never 0
sudo docker exec linx-postgres psql -U linx -d linx -tAc \
  "SELECT count(*) FROM alert_channel WHERE position('$TOPIC' in encode(config_enc, 'escape')) > 0"
sudo docker exec linx-postgres psql -U linx -d linx -tAc \
  "SELECT count(*) FROM webhook_endpoint WHERE position('whsec_' in encode(secret_enc, 'escape')) > 0"
sudo docker exec linx-postgres psql -U linx -d linx -tAc \
  "SELECT count(*) FROM api_key WHERE position('$KEY' in encode(secret_hash, 'escape')) > 0"
sudo docker exec linx-postgres psql -U linx -d linx -c \
  "SELECT at, actor, action, result FROM audit_log ORDER BY at DESC LIMIT 12"
```
- [ ] Four secret files (`linx_db_encryption_key`, `linx_db_password`, `linx_dns_token`, `linx_jwt_signing_key`), all root-owned and not readable by everyone.
- [ ] No Linx container publishes a port, and nothing runs as root.
- [ ] The three counts are `0`: your ntfy topic, the webhook secret and your API key are not stored readably in the database (they're encrypted, encrypted and hashed).
- [ ] The audit log shows your writes (`alert_channel.create`, `webhook.update`, `api.write`, ...), the reporter key's `auth.scope_denied`, and the made-up key's `auth.failed`.

**Doctor catches a problem:**
```
sudo docker stop linx-control-plane
sudo ./linx doctor; echo "exit code $?"
sudo docker compose --file /etc/linx/compose.yaml up --detach
```
- [ ] It reports a `problem`: the API service isn't running (it's exited), with a `Fix:` line, and exit code 1. After `up`, within a minute doctor is all green again.

## 10. Clean up
```
api -X DELETE $API/webhooks/$HOOK -o /dev/null -w '%{http_code}\n'        # 204
api -X DELETE $API/alert-channels/$CH -o /dev/null -w '%{http_code}\n'    # 204
sudo ./linx api-key revoke "${KEY:0:17}"
```
Then follow "Clean up" in [`ops/STAGING_TEST.md`](ops/STAGING_TEST.md), delete the Cloudflare token, and unsubscribe from the topic in ntfy.

## Results
Add one line per run: date, server, commit, passed or what failed.
