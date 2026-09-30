---
title: The repair page
audience: system_admin
section: install
keywords: [repair, broken, domain lost, certificate expired, new link, 6464, no sign in]
screens: [/repair]
---
# The repair page

When Linx's own address stops working (the domain lapsed, the certificate is gone, the front door changed), you can't reach Server settings through it. The repair page opens on the server's own address instead, for one hour.

## Opening it

On the server:

```
sudo linx setup --new-link
```

Choose the browser. It prints a repair link on port 6464, with the same fingerprint check as the install (see [Installing Linx](install-linx#2-open-the-link)).

Open it and sign in as a system admin with your password and authenticator app. Passkeys don't work on this page, because they belong to Linx's normal address.

If your only way to sign in is a passkey, run this on the server for a link that doesn't ask you to sign in:

```
sudo linx setup --new-link --no-sign-in
```

## What you can change

The same as [Server settings](server-settings): the domain, what's in front of this server and the DNS token, so Linx's address works again.

## Closing it

The page closes by itself after an hour, or when you stop setup on the server. Port 6464 is closed again in the firewall.
