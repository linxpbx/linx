---
title: Server settings and running setup again
audience: system_admin
section: install
keywords: [server settings, domain, change domain, front door, public port, 8443, dns, dns records, name servers, dns token, dns key, automatic dns, dynamic dns, ddns, address changes, cloudflare, duckdns, route 53, godaddy, namecheap, porkbun, digitalocean, hetzner, desec, ovh, size, performance, portainer, setup again, rerun]
screens: [/admin/system/server]
---
# Server settings and running setup again

Server settings change how the server itself is set up: the domain, what's in front of it, the DNS company's key, the size and the extras. Only a system admin sees them.

## Opening Server settings

- In the browser: System → **Server settings**.
- From the server: `sudo linx setup`, then choose the browser. It checks that Linx's address works and prints the link to Server settings. Nothing else reopens.

## DNS records

The **DNS** row lists the records your domain needs, and checks each one at your domain's own name servers (so there's no waiting for other servers to catch up):

- your domain itself, for the web app and signing in;
- `turn.` in front of it, for call audio through firewalls;
- at home, `sip.` in front of it, pointing at this server on your network, for desk phones and phone systems.

It also names your DNS company, told from the name servers, so you know where to add them. Each record says **Right**, what it shows instead, or **Not found yet**; a record Linx keeps right itself says so. After changing a record, press **Check again**: changes can take a few minutes to show. At Cloudflare, keep Proxy off (grey cloud, "DNS only").

In the name field, many DNS companies want only the first part (`turn`, `sip`), and `@` for the domain itself.

![DNS records](screen:system-server-dns)

## Keeping the records right automatically

A home connection's address changes from time to time. Linx can follow it and change the records itself, at any of these DNS companies: Cloudflare, DuckDNS, Route 53, GoDaddy, Namecheap, Porkbun, DigitalOcean, Hetzner, deSEC and OVH.

1. In the **DNS** row, press **Set it up automatically** (or, under the records, **Let Linx keep these right at** your company).
2. Check the **DNS company**: it starts on the one your name servers show. Fill in what it asks for; the page says where to find it at that company.
3. **Check the key**: Linx reads your domain's records with it and changes nothing. It says "This key can change … records", or the company's own reason.
4. **Apply**, then confirm it's you.

![Records kept right automatically](screen:system-server-dns-automatic)

Linx then looks at your address every five minutes and changes your domain, `turn.` and (at home) `sip.` when it moves. It only changes a record it made, or one that already pointed at the right address; anything else stays as it is. The records card says at which company Linx keeps them, and when the address last changed.

- **Replace key**: a new key, or another company.
- **Stop**: Linx stops changing the records; they stay as they are. The key still renews the certificate. **Let Linx keep them right** starts again.
- Not in the list? Choose **Not in the list** and add the records by hand.

Some companies have limits worth knowing: GoDaddy and Namecheap only let some accounts use their API, Namecheap only accepts the addresses on its own list, and deSEC keeps records for at least an hour, so a new address takes up to an hour to reach everyone.

Give Linx the smallest key your company allows; the page says how for each. A GoDaddy or Namecheap key can do anything your account can, and a Hetzner token anything in its project (so keep your domain's DNS in a Hetzner project of its own, with no servers). Linx keeps the key in one file on the server, readable only by its certificate service and the server's root user.

## Changing something

1. Change what you need: **What's in front of this server**, a **New domain**, **Replace key**, or the **Size of this server** (**Standard** or **Performance**).
2. **Apply**, then confirm it's you.
3. The page shows each step. Linx restarts, and the page comes back by itself.

A new domain needs new DNS records (the page lists them under **DNS records to add**) and has consequences: passkeys belong to the old address, so everyone signs in with their password and authenticator app and adds a new passkey, and desk phones need the new address.

Another public port (under **Advanced** in what's in front, see [Front doors](front-doors)) changes the address to one ending in the port, like `https://pbx.example.com:8443`. **Before you apply** shows the router rules and says what follows: passkeys keep working, company sign-in needs its new return address added at Google or Microsoft, and links already sent for the old address stop working. It needs the DNS company's key; without one, the key form opens.

## If the address doesn't work any more

If Linx's own address is broken (the domain lapsed, the certificate is gone), use the [repair page](repair-page) instead.

Every change shows in System → [Activity](activity).
