---
title: Installing Linx
audience: system_admin
section: install
keywords: [setup, install, new server, link, fingerprint, certificate, domain, dns, token, cloudflare, duckdns, 6464]
screens: [/install, /install/continue]
---
# Installing Linx

You start the install on the server with one command. Everything after that happens in your browser. It takes about ten minutes, plus any time your DNS company needs.

You need:

- a server: a rented server (VPS) or a computer at home or at the office, with at least 1 GB of memory and 5 GB of free disk (2 GB and 20 GB are better);
- a domain you own, like `example.com`, and access to its DNS settings;
- your phone, for a passkey or an authenticator app.

## 1. Start it on the server

On the server, run:

```
sudo linx setup
```

Choose the browser when it asks how you want to finish. Setup checks the server, installs what Linx needs and prints *one link*, like `https://203.0.113.5:6464/install/…`, and a **fingerprint** (a long code).

On a rented server, your provider's firewall must let in TCP port 443, UDP port 443 and, for now, TCP port 6464.

If you close the terminal, the install carries on. Run `sudo linx setup` again to see the link.

## 2. Open the link

Open the link in your browser. The browser warns you about the page's certificate: this first page uses a temporary one. Open the certificate's details and check that its fingerprint is the one in the terminal. If it is, you're talking to your own server, and you can continue past the warning.

The link works once, in one browser, for four hours. Anyone else who opens it sees **This link can't be used**. Need more time? Run `sudo linx setup --new-link` for a new link; your answers so far are kept.

## 3. Answer the first questions

- **Where is this server?** At home or at the office, or a rented server.
- **What's in front of this server**: how people reach it from the internet. See [Front doors](front-doors).
- **What's your domain?** The name people will use, like `pbx.example.com`.
- **Who's setting this up?** Your name, the email you'll sign in with, and an email for certificate notices. You become the *system admin*, the highest authority over Linx.

Then **Check and get a certificate**.

## 4. The certificate

A certificate is what makes the padlock appear in your browser. Linx gets one from Let's Encrypt, a free certificate company.

**How should Linx get it?**

- **With my DNS company's token** (Cloudflare or DuckDNS): Linx adds its DNS records itself and gets a certificate that also covers desk phones at home. This is the one to pick at home.
- **Not now: I'll add 2 records**: the page shows two records to add at your DNS company, for your domain and for `turn.` in front of it, both pointing at this server's address. On Cloudflare, set the proxy to *off (grey cloud)*. The page checks them every few seconds and says what each one shows.

When the certificate is ready, the page moves itself to `https://<your domain>`, with no warning this time. From here on, everything you type is encrypted.

## 5. On the secure page

- **Your DNS company's token**, if you didn't give it already. On a rented server you may skip it; the certificate still renews by itself.
- **A few extras**: the size of this server (the suggested one is right almost every time) and, at home, Portainer (on a rented server a note says why it isn't offered).
- **Install**. The page ticks through each step. Before Linx starts, a box shows things to **write down now**, like the certificate authority backup passphrase. Save them in your password manager, then tick **I've written these down**.

## 6. Your first sign-in

Choose a password, then a passkey or an authenticator app (see [Passkeys and authenticator apps](passkeys-and-authenticator)). Then the [setup wizard](setup-wizard) starts.

After the install, port 6464 is closed for good. Check everything with [linx doctor](linx-doctor).
