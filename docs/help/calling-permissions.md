---
title: What phones can call
audience: admin
section: admin
keywords: [outgoing, outbound, calling permissions, abroad, international, countries, only these countries, country, premium rate, emergency, 999, 911, 112, toll fraud, hide number, caller id, which line first, simulator, test call]
screens: [/admin/outgoing, /admin/simulator]
---
# What phones can call

**Outgoing calls** decides what every extension may call, and which phone line outside calls use.

![Outgoing calls](screen:outgoing)

## Your country

**Country** is where your phone lines are. Linx reads numbers the way people dial them there: local numbers, mobiles, free numbers and the country's emergency numbers. **Change** picks another; Linx can be set up in any country. Extension numbers that look like the new country's outside or emergency numbers (in North America, anything starting with 1) are listed under Extensions to renumber.

## What your phones can call

Each kind of number can be on or off: **Local numbers**, **Mobiles**, *other numbers in your country* (company and internet numbers), **Free numbers**, **Abroad** and **Premium-rate**. **Abroad** and **Premium-rate** start off, because they're what a thief would call with a stolen password. Turning them on asks you to confirm it's you.

With **Abroad** on, choose where those calls can go:

- **Every country**.
- **Only the countries I choose**: type a country's name to add it; anywhere else is refused, premium-rate numbers there included. The safest way to allow calls abroad. Adding a country asks you to confirm it's you, like turning calls abroad on.

A number that shares your country's calling code isn't abroad: from the United States, Canada counts as home.

**Emergency** numbers always work, from every phone: the screen lists your country's (in the UAE 999, 998, 997, 112 and 901; in the United States 911 and 112).

Different rules for different people come later.

## Which line first

Outside calls go out on the first line. The next ones are used only when the lines above are down or full.

## Alerts for calls abroad

**Tell me when an hour has more than** a number of calls or minutes abroad: you get an alert, so a stolen password can't run up a bill unnoticed ([Alerts](alerts)).

## Hide our number on outgoing calls

People you call see no number. Some phone companies ignore this.

## The call simulator

**Simulator** shows where a call would go, without making it. This is exactly what a real call does.

- **From extension** and a **Number**, then **Check**: **Allowed** and the line it goes out on, or **Not allowed** and why, with **Change what phones can call**.
- **Which way**: someone calling in to **Your number** shows what happens, step by step: who rings, and if nobody answers, what next. Under **When**, **Now** or **On** a day and time (the server's time zone), since office hours and holidays change it.
- Dialling another extension or a ring group's number from **From extension** shows the same steps.

![A call the simulator allowed](screen:simulator-allowed)

![A call in on Friday evening, outside office hours](screen:simulator-after-hours)

## Undo

Every change here can be undone: **Undo** shows for 10 seconds after you save, and System → **Routing changes** keeps the last 50. See [Undo and routing changes](routing-changes).
