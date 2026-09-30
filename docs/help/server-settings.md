---
title: Server settings and running setup again
audience: system_admin
section: install
keywords: [server settings, domain, change domain, front door, dns token, size, performance, portainer, setup again, rerun]
screens: [/admin/system/server]
---
# Server settings and running setup again

Server settings change how the server itself is set up: the domain, what's in front of it, the DNS token, the size and the extras. Only a system admin sees them.

## Opening Server settings

- In the browser: System → **Server settings**.
- From the server: `sudo linx setup`, then choose the browser. It checks that Linx's address works and prints the link to Server settings. Nothing else reopens.

## Changing something

1. Change what you need: **What's in front of this server**, a **New domain**, **Replace token**, or the **Size of this server** (**Standard** or **Performance**).
2. **Apply**, then confirm it's you.
3. The page shows each step. Linx restarts, and the page comes back by itself.

A new domain needs new DNS records (the page lists them under **DNS records to add**) and has consequences: passkeys belong to the old address, so everyone signs in with their password and authenticator app and adds a new passkey, and desk phones need the new address.

## If the address doesn't work any more

If Linx's own address is broken (the domain lapsed, the certificate is gone), use the [repair page](repair-page) instead.

Every change shows in System → [Activity](activity).
