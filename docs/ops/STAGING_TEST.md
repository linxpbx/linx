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
3. Setup should end with "Linx is running" and "Test certificate issued for *.lab.linxpbx.com". It also shows the CA backup passphrase: for a throwaway VM you can ignore it.

## Check
```
sudo docker compose --file /etc/linx/compose.yaml ps          # linx-step-ca "healthy", linx-certd "running"
sudo docker compose --file /etc/linx/compose.yaml logs certd | grep -E '"level":"(INFO|ERROR)"' | tail -5
sudo docker run --rm -v linx_certs:/c:ro alpine cat /c/current/meta.json
```
`meta.json` should show `"staging": true`, `"issuer": "letsencrypt-staging"` and names `*.lab.linxpbx.com`. Also run setup a second time: it should offer to keep the saved token and finish without a new certificate.

## If it fails
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
