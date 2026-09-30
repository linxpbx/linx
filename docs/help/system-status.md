---
title: System status and Restart
audience: admin
section: running
keywords: [status, health, services, restart, logs, certificate, running, stopped, not answering, phone system, asterisk]
screens: [/admin/system, /admin/system/status]
---
# System status and Restart

System → **Status** shows whether each part of Linx works: **Linx services**, the **Certificate**, **Phone lines** and anything that **Needs attention**.

![System status](screen:system-status)

## Linx services

Each service shows **Running**, **Starting…**, **Running, not answering** or **Stopped**. Open one to see its **Recent log**.

## Restart

**Restart** a service that's stuck. Linx first says what you'd notice. For example, restarting the phone system ends calls in progress (phones and browsers reconnect by themselves within a minute), and restarting the front door cuts off everyone reaching Linx from outside for a few seconds.

If **The server helper isn't running**, Restart can't work: check the server with [linx doctor](linx-doctor).

## Certificate

When it runs out. It renews by itself 30 days before.

The same checks, and more, run on the server with [linx doctor](linx-doctor).
