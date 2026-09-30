---
title: Phone lines
audience: admin
section: admin
keywords: [phone line, trunk, sip trunk, provider, phone company, telnyx, twilio, voip provider, landline, outside calls, test, caller id, calls at once]
screens: [/admin/lines]
---
# Phone lines

A phone line connects Linx to the outside phone network: a phone company over the internet, or your own phone system with landlines.

![Phone lines](screen:lines)

## Adding a line from a phone company

**+ Add** → **Guide me**, then **What is it?** Choose your company, or *Any company that gives you a SIP server, username and password*.

- **Or paste what the company sent you**: paste their email, and Linx picks out the server, username and password. Check what **Linx understood**.
- Or type the details: the company's SIP server, your username and password.

**Add and test**. Linx checks it answers, signs in and has encrypted audio. When the test passes, add the line's **Numbers** and say which extension each rings.

![Adding a line](screen:lines-add-login)

## Your own phone system or a gateway

For a Grandstream UCM, another phone system, or a box that connects landlines: [A phone system or gateway](phone-system-or-gateway).

## Private connections

Some companies offer a private (WireGuard) connection: [Private connections](private-connections).

## On a line's page

- Its status: **Signed in**, **Connected**, or **Something needs fixing**. **Test again** checks it now.
- **Numbers** and where each rings.
- **Outgoing**: the order of lines for outside calls ([What phones can call](calling-permissions)).
- **Turn off** or **Turn on**, and **Remove** under **Danger zone**.

If a line goes down, see [A phone line is down](line-down).
