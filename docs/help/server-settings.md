---
title: Server settings and running setup again
audience: system_admin
section: install
keywords: [server settings, domain, change domain, front door, dns, dns records, name servers, dns token, size, performance, portainer, setup again, rerun]
screens: [/admin/system/server]
---
# Server settings and running setup again

Server settings change how the server itself is set up: the domain, what's in front of it, the DNS token, the size and the extras. Only a system admin sees them.

## Opening Server settings

- In the browser: System → **Server settings**.
- From the server: `sudo linx setup`, then choose the browser. It checks that Linx's address works and prints the link to Server settings. Nothing else reopens.

## DNS records

The **DNS** row lists the records your domain needs, and checks each one at your domain's own name servers (so there's no waiting for other servers to catch up):

- your domain itself, for the web app and signing in;
- `turn.` in front of it, for call audio through firewalls;
- at home, `sip.` in front of it, pointing at this server on your network, for desk phones and phone systems.

It also names your DNS company, told from the name servers, so you know where to add them. Each record says **Right**, what it shows instead, or **Not found yet**; a record Linx keeps right itself (with your DNS token) says so. After changing a record, press **Check again**: changes can take a few minutes to show. At Cloudflare, keep Proxy off (grey cloud, "DNS only").

In the name field, many DNS companies want only the first part (`turn`, `sip`), and `@` for the domain itself.

![DNS records](screen:system-server-dns)

## Changing something

1. Change what you need: **What's in front of this server**, a **New domain**, **Replace token**, or the **Size of this server** (**Standard** or **Performance**).
2. **Apply**, then confirm it's you.
3. The page shows each step. Linx restarts, and the page comes back by itself.

A new domain needs new DNS records (the page lists them under **DNS records to add**) and has consequences: passkeys belong to the old address, so everyone signs in with their password and authenticator app and adds a new passkey, and desk phones need the new address.

## If the address doesn't work any more

If Linx's own address is broken (the domain lapsed, the certificate is gone), use the [repair page](repair-page) instead.

Every change shows in System → [Activity](activity).
