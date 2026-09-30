---
title: Admins only from my home or office network
audience: admin
section: admin
keywords: [admin network, home network, office network, restrict admin, lan only, admin access, anywhere, security]
screens: [/admin/system/settings]
---
# Admins only from my home or office network

You can let admins manage Linx only from your home or office network. Outside it, an admin gets the ordinary view: the Admin area is replaced by one greyed row saying why. Calls work as usual everywhere.

## Turning it on

System → Settings → *Admins can sign in from* → **Only my home or office network**. Linx knows your home or office network; you can add other networks too, one per line (like an office's `203.0.113.0/24`).

If you aren't on one of those networks now, you lose the admin pages as soon as you save, until you're back. **Change anyway**, then confirm it's you.

Put it back with **Anywhere**.

## If you moved

After moving Linx to a new place, this may still list the old network ([Moving Linx to a new server](moving-to-a-new-server)). Change it from the new network.
