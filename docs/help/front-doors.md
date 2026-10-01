---
title: Front doors: how people reach Linx
audience: system_admin
section: install
keywords: [front door, proxy, pangolin, nginx, haproxy, caddy, caddy-l4, layer4, nginx proxy manager, port 443, router, port forwarding, traefik, proxy protocol, sni, passthrough, public port, 8443, another port, nat loopback, hairpin]
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

## Advanced: use another public port

For when something else owns port 443 and can't pass Linx through by name, and there's no proxy to put in front. Your router (or, on a rented server, the server itself) brings another port, like 8443, to Linx, and people open Linx at an address that ends in that port: `https://pbx.example.com:8443`.

The page warns first, because port 443 is always the recommended choice: almost every network lets it through. **Show me the front-door options** goes back to the choices that keep port 443; **Use another port** carries on. Then:

- **Public web port**: from 1024 to 65535 (8443 is suggested). Ports Linx uses itself, and ones browsers refuse, are turned down with the reason.
- **Public port for call audio (UDP)**: 443 is recommended, since UDP 443 is often free even when TCP 443 isn't; 3478 if your router can't forward UDP 443.
- At home, the page shows the two router rules to add, each with a Copy button, for example TCP 8443 and UDP 443 to this server, on the same ports. On a rented server there's nothing to forward: Linx opens both ports itself.
- Linx needs your DNS company's key: without port 443, Let's Encrypt can only check your domain through your DNS company.

What it trades away:

- Browser calls from networks that only allow standard web traffic (some hotels, workplaces, public Wi-Fi and mobile networks) may fail or have no audio. Calls from normal home and mobile networks work.
- Some company, school and public networks block any address with a port in it. There, even the page and signing in can fail. A front door on port 443, or a small rented server as your front door, keeps port 443.
- Browsers don't tell sites apart by port, so whatever answers for your Linx address itself on port 443 (`https://pbx.example.com`, with no port) is treated as part of Linx. Keep it that way round: what holds port 443 serves its own names, never Linx's. A wildcard certificate there that also covers Linx's name makes this matter more.

Passkeys keep working if you change the port later: they belong to your domain, not the port. Company sign-in needs its new return address (`https://pbx.example.com:8443/api/v1/sso/callback`) added at Google or Microsoft; Server settings shows it before you apply. Links already sent for the old address stop working, so send new invites.

People at the office use the same address. If it doesn't open there, turn on NAT loopback (sometimes called hairpin) on your router, or add your domain to your local DNS pointing at this server.

To check it works, use **Check it** on [System → Status](system-status), then your phone on mobile data. Check it and [linx doctor](linx-doctor) test the port you chose, and both always add one warning that Linx isn't on port 443. That warning is expected and doesn't mean anything is broken.

## Advanced: my proxy must unlock the traffic itself

Only for a proxy that can't pass Linx through, and only at home. The proxy opens the encrypted connection itself before passing it on, so it sees everything, Linx needs your DNS company's token on the first, unencrypted page, and calls need their own port (TCP 5349) for audio. The page warns first: **Show me how** goes back to passing Linx through, **Use it anyway** keeps this.

## Which to choose

- A rented server with nothing else on it: **Nothing else uses port 443 — Linx takes it**.
- At home with Pangolin, nginx, HAProxy or Caddy already on port 443: **Another program passes Linx through**.
- Nothing from outside needed: **Nothing: only at home**.
- Port 443 taken by something that can't pass Linx through, and no proxy: *Advanced: use another public port*, knowing some networks block it.

If people outside can't open Linx afterwards, see [Can't open Linx's address](cant-open-the-address).
