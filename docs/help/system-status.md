---
title: System status and Restart
audience: admin
section: running
keywords: [status, health, services, restart, logs, certificate, running, stopped, not answering, phone system, asterisk, check it, reachable, from outside, phone link, hairpin]
screens: [/admin/system, /admin/system/status]
---
# System status and Restart

System → **Status** shows whether each part of Linx works: **Linx services**, the **Certificate**, whether it's **Reachable from outside**, **Phone lines** and anything that **Needs attention**.

![System status](screen:system-status)

## Linx services

Each service shows **Running**, **Starting…**, **Running, not answering** or **Stopped**. Open one to see its **Recent log**.

## Restart

**Restart** a service that's stuck. Linx first says what you'd notice. For example, restarting the phone system ends calls in progress (phones and browsers reconnect by themselves within a minute), and restarting the front door cuts off everyone reaching Linx from outside for a few seconds.

If **The server helper isn't running**, Restart can't work: check the server with [linx doctor](linx-doctor).

## Certificate

When it runs out. It renews by itself 30 days before.

## Reachable from outside

**Check it** answers "can people outside reach Linx, and will their calls have audio?" in two halves:

- **From this server**: your domain and `turn.` point at your address, your domain answers with Linx's own certificate through your front door, and `turn.` carries call audio through it. A ✗ line says what it means for people, with a link to the steps that fix it; for a name that points somewhere else, **Show the records** opens the list of DNS records to add, each checked at your domain's own name servers. Many home routers can't reach their own public address from inside; Linx then says so and leaves the rest to your phone.
- **From outside**: scan the picture, or open the link, on your phone with Wi-Fi turned off. The phone's page shows the address Linx saw and tests call audio from there, and this page updates by itself: "Your phone reached Linx from …" and "Calls from outside will have audio". If Linx saw your own network's address, Wi-Fi was still on. If it saw your front door's address, the front door isn't telling Linx who's visiting (step 3 on the [front-door card](front-doors)).

A link works once, for 10 minutes. **Check again** runs everything again with a new link. Only admins can use Check it. It's also in [Server settings](server-settings), under what's in front of the server.

![Check it](screen:system-status-check-it)

The same checks, and more, run on the server with [linx doctor](linx-doctor).
