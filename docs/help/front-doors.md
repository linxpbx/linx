---
title: Front doors: how people reach Linx
audience: system_admin
section: install
keywords: [front door, proxy, pangolin, nginx, haproxy, caddy, caddy-l4, layer4, nginx proxy manager, port 443, router, port forwarding, traefik, proxy protocol, sni, passthrough]
screens: []
---
# Front doors: how people reach Linx

People reach Linx from the internet on port 443, the usual port for secure web pages. What sits in front of your server decides how that port gets to Linx. The install asks **What's in front of Linx on the internet?**, and you can change it later in [Server settings](server-settings).

## Nothing else uses port 443 — Linx takes it

The simplest. On a rented server, your provider sends port 443 to the server and nothing else on it uses that port. At home, your router sends TCP and UDP port 443 straight to this server.

## Another program passes Linx through

Pangolin, nginx, HAProxy, Caddy or similar already uses port 443 for your other sites. It can pass Linx's names on to Linx without unlocking them, next to those sites. Give **Address of the machine it runs on**, and later the page shows a card: **Your front door needs to do three things**.

1. **Pass these names through without unlocking them**: your domain and `turn.` in front of it. Linx's own certificate has to reach the browser.
2. **Send them to this server**: your domain to this server's port 8443, `turn.` to its port 5349.
3. **Tell Linx who's visiting**: turn on "PROXY protocol, version 2" for your domain, and leave it off for `turn.`. Linx only accepts it from your front door's address.

Linx's side is the same whichever program it is. Under **How to do this in** there is a tab for each one, with the steps and the block to paste, already filled in:

- *Pangolin*: a block for Traefik's settings file (`config/traefik/dynamic_config.yml`). Pangolin's own Resources page can't do this: it passes traffic on by port, not by name.
- *nginx*: a `stream` block. Your own sites move to a local port behind it and keep their visitors' addresses.
- *HAProxy*: two lines for your port 443 frontend and two backends.
- *Caddy*: needs Caddy's layer-4 add-on (caddy-l4). The block goes at the top of your Caddyfile; your own sites keep working.
- *Nginx Proxy Manager* and *A router or firewall*: these pass traffic on by port only, so they can't share port 443. If nothing else needs it, send port 443 straight to Linx and choose the first answer instead.

Your router keeps sending TCP port 443 to the other program, and sends a UDP port (443, or 3478 on routers such as UniFi that can't split port 443) straight to Linx for call audio. When it's all done, tick **I've done these steps**.

Setup also writes everything to `/etc/linx/front-door/` on the server (`FRONT-DOOR.txt` and each block), for anyone working in a terminal.

## Nothing: only at home

Linx works on your home or office network only: no calls from outside. It still needs your DNS company's token for its certificate.

## Advanced: my proxy must unlock the traffic itself

Only for a proxy that can't pass Linx through, and only at home. The proxy opens the encrypted connection itself before passing it on, so it sees everything, Linx needs your DNS company's token on the first, unencrypted page, and calls need their own port (TCP 5349) for audio. The page warns first: **Show me how** goes back to passing Linx through, **Use it anyway** keeps this.

## Which to choose

- A rented server with nothing else on it: **Nothing else uses port 443 — Linx takes it**.
- At home with Pangolin, nginx, HAProxy or Caddy already on port 443: **Another program passes Linx through**.
- Nothing from outside needed: **Nothing: only at home**.

If people outside can't open Linx afterwards, see [Can't open Linx's address](cant-open-the-address).
