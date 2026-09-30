---
title: A phone line is down
audience: admin
section: running
keywords: [line down, trunk down, can't call out, no outside calls, incoming not working, registration failed, provider down, not signed in]
screens: []
---
# A phone line is down

When a line goes down, Linx sends an alert and Phone lines shows it. Calls then use the next line, if you have one ([What phones can call](calling-permissions#which-line-first)).

## Check it

Open the line in Phone lines and press **Test again**. Linx checks, in order: that it can find the company's server, that it answers, that it signs in, and that audio is encrypted. The first step that fails says what to fix.

## Common causes

- *Wrong password*, or the company changed it: **Edit** the line and give the new one.
- *The company's service is down*: check their status page, or call them.
- *Your internet is down*: nothing outside works either.
- *A phone system or gateway* that signs in to Linx is off or lost its settings: check it's on and its trunk settings ([A phone system or gateway](phone-system-or-gateway)).
- *A private connection* that stopped answering: see its **Last heard from** in [Private connections](private-connections).
- *Your address changed*, and the company only accepts calls from your old one: tell them the new address.

When the line comes back, its alert clears by itself.
