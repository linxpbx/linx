---
title: Front doors: how people reach Linx
audience: system_admin
section: install
keywords: [front door, proxy, pangolin, nginx, haproxy, caddy, nginx proxy manager, port 443, router, port forwarding, traefik]
screens: []
---
# Front doors: how people reach Linx

People reach Linx from the internet on port 443, the usual port for secure web pages. What sits in front of your server decides how that port gets to Linx. The install asks **What's in front of this server**, and you can change it later in [Server settings](server-settings).

## Linx takes port 443 itself

The simplest. On a rented server, your provider sends port 443 to the server and nothing else on it uses that port. At home, your router sends TCP and UDP port 443 straight to this server.

## Pangolin

Your router sends port 443 to Pangolin, and Pangolin passes Linx's names on to Linx without opening them. The install shows a block to add to Pangolin's settings; add it, then press **I've done this**.

## nginx or HAProxy

Something on your network already uses port 443. It passes Linx's names through to Linx without opening them. The install shows the lines to add.

## Caddy or Nginx Proxy Manager

These open the encrypted connection themselves before passing it on, so Linx can't get its certificate through them. Linx needs your DNS company's token on the first, unencrypted page instead. Give it only on a network you trust.

## Nothing, only at home

Linx works on your home or office network only: no calls from outside. It still needs your DNS company's token for its certificate.

## Which to choose

- A rented server with nothing else on it: **Linx takes port 443 itself**.
- At home with Pangolin already running: **Pangolin**.
- Something else already answers on port 443: the one you use.

If people outside can't open Linx afterwards, see [Can't open Linx's address](cant-open-the-address).
