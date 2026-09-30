---
title: Private connections (WireGuard)
audience: admin
section: admin
keywords: [wireguard, vpn, private connection, tunnel, connections, provider vpn, configuration file]
screens: [/admin/connections]
---
# Private connections (WireGuard)

Some phone companies connect to you over a private WireGuard connection instead of the internet. Only calls to that company go through it.

**Connections** is an expert page: turn **Simple mode** off to see it ([Admin home and Simple mode](admin-home#simple-mode)).

![A connection added](screen:connections-added)

## Adding one

1. **Connections** → **+ Add**.
2. Give it a **Name** and the **Configuration file** your phone company gave you, or paste it.
3. **Add**. Its private key stays sealed on this server and is never shown again. If the company asks for it, give them **Linx's public key**.

Then choose it as the phone line's connection when you add the line ([Phone lines](phone-lines)).

## Status

**Last heard from** shows when the company's side last answered. **Lines using it** lists its lines.

Removing a connection deletes its keys. To use it again, add the file again.
