# Phase 0 demo checklist

A hand-run check that Phase 0 does what the roadmap promises. Tick each box. It takes about an hour, most of it waiting for the certificate. The server part repeats the live staging test and adds `linx doctor`.

**Phase 0 exit:** on a fresh VM, `linx setup` → staging certificate for a linxpbx.com test subdomain → step-ca healthy → `linx doctor` all green.

## You need
- Your computer with Go, Node.js and Docker, and an up-to-date clean checkout of master.
- A fresh Ubuntu 24.04 server and a Cloudflare token for `linxpbx.com`, as described in [`ops/STAGING_TEST.md`](ops/STAGING_TEST.md) under "You need".

## 1. Docs (Phase 0a)
- [ ] `CLAUDE.md`, `docs/ARCHITECTURE.md`, `DECISIONS.md`, `THREAT_MODEL.md`, `ROADMAP.md` and `ui/DESIGN_TOKENS.md` exist and match what you approved on 2026-09-23.

## 2. On your computer
Open Terminal. Every command here runs inside the project folder:
```
cd ~/Projects/linx
git pull
make setup-dev
make lint          # ends with "lint: ok"
make test          # failures only; nothing listed means all passed
make security      # govulncheck, npm audit and licences all "ok"
make test-docker   # builds a throwaway internal CA and checks it; needs Docker running
```
- [ ] All four finish without errors.

**Design tokens:**
```
make tokens && git status --short    # nothing listed: generated files match design/tokens.json
cd web && npm run dev                 # open the address it prints
```
- [ ] `git status` lists nothing.
- [ ] The placeholder page shows the "linx" wordmark in Cobalt blue with "Your calls. Your server." Press Ctrl+C to stop the page's server.

## 3. CI on GitHub
Open the repository → **Actions** → the latest **CI** run on master.
- [ ] Every job is green: `test (amd64)`, `test (arm64)`, `security`, `images (certd)`, `images (control-plane)`.
- [ ] The `security` job has a `linx-source.spdx.json` artifact (the software bill of materials).
- [ ] Under the repository's **Packages**, `linx-certd` and `linx-control-plane` have an image tagged `sha-<that commit>`.

Optional, if you have `cosign`: check an image's signature proves it came from this CI on master.
```
cosign verify ghcr.io/linxpbx/linx-certd:sha-<commit> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/linxpbx/linx/\.github/workflows/ci\.yml@refs/heads/master$'
```
- [ ] It prints "The cosign claims were validated".

## 4. Install on a fresh server
Follow steps 1–3 of [`ops/STAGING_TEST.md`](ops/STAGING_TEST.md): build `linx` from the commit CI just built, copy it to the server, run `sudo ./linx setup`. Choose **no** container management screen.
- [ ] Setup checks the server, suggests a resource profile, and installs Docker if needed.
- [ ] Setup ends with "Linx is running" and "Test certificate issued for *.lab.linxpbx.com".
- [ ] It shows a backup passphrase in four-letter groups and tells you to copy `/etc/linx/ca-backup` off the server.

## 5. `linx doctor`
```
sudo ./linx doctor
```
- [ ] Every line says `ok` except one `warning`: the root key backup is still on the server. Each `ok` line should be one of:
  - The certificate service is running; the internal certificate authority is running and answering. (Docker gets a line only if it's down.)
  - Test certificate covers admin., api., meet., provision., sip. and turn.lab.linxpbx.com.
  - Valid until a date about 90 days away.
  - Full chain checked up to Let's Encrypt's test authority.
  - Internal root and intermediate certificates valid for about 10 years.

Delete the backup: fine on a throwaway server. On a real one, copy it off first.
```
sudo rm -r /etc/linx/ca-backup
sudo ./linx doctor
```
- [ ] All lines `ok`, ending with "Everything checked is working." **This is the Phase 0 exit.**

**Doctor catches a problem:**
```
sudo docker stop linx-certd
sudo ./linx doctor; echo "exit code $?"
```
- [ ] It reports a `problem`: the certificate service isn't running (it's exited). A `Fix:` line under it gives the command to start it, and the exit code is 1.
```
sudo docker compose --file /etc/linx/compose.yaml up --detach
sudo ./linx doctor
```
- [ ] All green again.

## 6. Security spot checks (on the server)
```
sudo grep -rlF "$(sudo cat /etc/linx/secrets/linx_dns_token)" /etc/linx --exclude-dir=secrets   # prints nothing
sudo ls -l /etc/linx/secrets                                 # every file owned by root, mode -r--r-----
sudo docker ps --format '{{.Names}}  ports: {{.Ports}}'      # linx-certd and linx-step-ca, no ports
ps -eo uid,comm | grep -E ' (service|step-ca)$'            # certd's "service" as 65532, step-ca as 1000; never 0 (root)
```
- [ ] The DNS token appears nowhere in `/etc/linx` except its secret file.
- [ ] Secrets are root-owned and not readable by everyone.
- [ ] No Linx container publishes a port.
- [ ] Neither service runs as root.

**Running setup again is safe:**
```
sudo ./linx setup
```
- [ ] It offers to keep the saved token, keeps the existing internal CA (no new passphrase), and doesn't request a new certificate.

## 7. Clean up
Follow "Clean up" in [`ops/STAGING_TEST.md`](ops/STAGING_TEST.md), then delete the Cloudflare token.

## Results
Add one line per run: date, server, commit, passed or what failed.

- 2026-09-23, Ubuntu 24.04 VM (the staging-test server after its clean-up, so Docker was already installed), `lab.linxpbx.com` via Cloudflare, commit `ff145ad`: **passed.** Every check matched. `linx doctor` was all green once the root key backup was deleted, reported a stopped certificate service as a problem with a fix (exit code 1), and went green again after the restart.
