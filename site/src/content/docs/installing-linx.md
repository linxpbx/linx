---
title: Installing Linx
description: Install Linx on a Linux server with one command, then finish in your browser.
section: Start here
order: 2
---

You start the install on the server with one command. Everything after that happens in your browser. It takes about ten minutes, plus any time your DNS company needs.

## 1. Run one command on the server

On a fresh Linux server, run:

```sh
curl -fsSL https://raw.githubusercontent.com/linxpbx/linx/master/install.sh | sudo sh
```

This downloads the right build for your server — it detects an Intel/AMD (`amd64`) or an ARM (`arm64`) machine by itself — checks the download against the release's checksums, installs the `linx` program, and starts the setup.

Setup checks the server, installs what Linx needs, and prints **one link**, like `https://203.0.113.5:6464/install/…`, along with a **fingerprint** (a long code).

On a rented server, your provider's firewall must allow in TCP port 443, UDP port 443, and — during setup only — TCP port 6464.

## 2. Open the link

Open the link in your browser. The first page uses a temporary certificate, so the browser shows a warning. Open the certificate's details and check its fingerprint matches the one in the terminal. If it does, you're talking to your own server — continue past the warning.

The link works once, in one browser, for four hours. Need a new one? Run `sudo linx setup --new-link`; your answers so far are kept.

## 3. Answer a few questions

- **Where is this server?** At home or the office, or a rented server.
- **Your domain** — the name people will use, like `pbx.example.com`.
- **Who's setting this up** — your name and the email you'll sign in with. You become the system admin.

Then **Check and get a certificate**. When it's ready, the page moves itself to your secure address with no warning, and everything from there is encrypted.

## Doing it by hand

Every release has the signed `linx` binaries and a `SHA256SUMS` file attached. To install manually:

```sh
# pick amd64 or arm64 for your server
ARCH=amd64
curl -LO https://github.com/linxpbx/linx/releases/latest/download/linx-linux-$ARCH
curl -LO https://github.com/linxpbx/linx/releases/latest/download/SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS      # must say OK
sudo install -m 0755 linx-linux-$ARCH /usr/local/bin/linx
sudo linx setup
```

If you have `cosign` installed, you can also verify the release signature — the checksums are signed by the project's release workflow.

## Updating later

Run the same one-line command again, or install a newer release's `linx` and run `sudo linx setup` — it notices when the program is newer than what's running and updates in place. See [System requirements](/docs/system-requirements) for what Linx needs to run.
