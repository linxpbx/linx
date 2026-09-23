# Live staging test (Phase 0b exit)

Checks the whole path on a real server: `linx setup` → Let's Encrypt **test** certificate for a linxpbx.com subdomain via Cloudflare → step-ca healthy.

## You need
- A fresh Ubuntu 24.04 server (VM or VPS), 2 GB RAM or more, with internet access. No ports need to be open for this test (DNS-01 only).
- A Cloudflare API token for the `linxpbx.com` zone: dash.cloudflare.com → My Profile → API Tokens → Create Token → "Edit zone DNS". Permissions **Zone · DNS · Edit** and **Zone · Zone · Read**; Zone Resources **Include · Specific zone · linxpbx.com**. Set a short TTL (e.g. expire in 7 days) for testing.
- The service images for the commit you build from must be on ghcr.io (CI publishes them on every push to master) and the `linx-certd` package must be public, or run `docker login ghcr.io` on the server first.

## Steps
1. On your computer, from an up-to-date, clean checkout of master (the commit CI has already built):
   ```
   GOOS=linux GOARCH=amd64 make build    # use GOARCH=arm64 for an ARM server
   scp bin/linx <you>@<server>:
   ```
2. On the server:
   ```
   sudo ./linx setup
   ```
   Answer: domain `lab.linxpbx.com` (any unused subdomain), paste the token, keep **test certificates** (Enter), skip the email (Enter).
3. Getting the certificate takes about 2 minutes (setup waits until your DNS provider's name servers have the proof record, then 90 seconds more). Setup should end with "Linx is running" and "Test certificate issued for *.lab.linxpbx.com". It also shows the CA backup passphrase: for a throwaway VM you can ignore it.

## Check
```
sudo docker compose --file /etc/linx/compose.yaml ps          # linx-step-ca "healthy", linx-certd "running"
sudo docker compose --file /etc/linx/compose.yaml logs certd | grep -E '"level":"(INFO|ERROR)"' | tail -5
# the step-ca image is already on the server (pinned); 65532 is the certificate service user
sudo docker run --rm --network none --user 65532 -v linx_certs:/c:ro --entrypoint cat smallstep/step-ca:0.30.2@sha256:a2b17872915c193259b75a5474c398326f41bd199f0842093e52cf4182bc8270 /c/current/meta.json
```
`meta.json` should show `"staging": true`, `"issuer": "letsencrypt-staging"` and names `*.lab.linxpbx.com`. Also run setup a second time: it should offer to keep the saved token and finish without a new certificate.

Then run `sudo linx doctor`. Every line should say `ok`, except one warning that the root key backup is still on the server. On a throwaway VM, `sudo rm -r /etc/linx/ca-backup` and run doctor again: it should end with "Everything checked is working."

## If it fails
- "NXDOMAIN looking up TXT for _acme-challenge…": the domain is very new and some DNS resolvers still remember that it didn't exist. Wait 30 minutes and run setup again.
- "propagation: time limit exceeded": the DNS provider's name servers didn't show the record within 10 minutes. Check the provider's status page, and that the server can make DNS queries (UDP port 53) to the internet.
- "no matching manifest" / "denied" on download: the images for this commit aren't published or aren't public. Check the CI run for your commit.
- Cloudflare "Authentication error" / "could not find zone": the token's permissions or zone are wrong.
- Setup is safe to run again after fixing the problem.

## Clean up
```
sudo docker compose --file /etc/linx/compose.yaml down --volumes
sudo docker volume rm linx-step-ca
sudo rm -r /etc/linx
```
Then delete the Cloudflare token.

## Results
- 2026-09-23, Ubuntu 24.04 VM (amd64), `lab.linxpbx.com` via Cloudflare, commit `1f596eb`: **passed.** Test certificate `*.lab.linxpbx.com` from `letsencrypt-staging` (valid to 2026-12-22); step-ca healthy; certd running; a second `linx setup` run kept the saved token and issued nothing new; Portainer reachable from the LAN only. Two earlier attempts failed on DNS timing for the same-day domain, fixed in `b267ccb` and `1f596eb` (see ADR-010 notes).
- 2026-09-23, same VM after clean-up, commit `ff145ad`: **passed**, including `linx doctor` all green (part of [`DEMO_PHASE0.md`](../DEMO_PHASE0.md)).
