---
title: Ring groups
audience: admin
section: admin
keywords: [ring group, hunt group, group, several phones, ring all, ring everyone, one after another, in turn, sales, support, reception, if nobody answers]
screens: [/admin/ring-groups]
---
# Ring groups

A ring group rings several people for one call: **all at once** (whoever's free answers first), or **one after another** in the order you choose. Use one for Sales, Support or Reception.

![Ring groups](screen:ring-groups)

## Adding a ring group

Go to **Ring groups** and choose **+ Add**:

- **Guide me** asks, one step at a time: its name, **Who's in it?**, **How it rings**, **If nobody answers**, and its own number.
- **Quick add** asks only the name and the people. It rings everyone at once for 25 seconds, gets the next free number, and then callers can leave a voicemail for the group. Change any of it later.

Under each group, Linx writes in one sentence what a caller gets, for example: *"Calls ring Sara and Omar together. If nobody answers in 25 seconds, the call goes to Reception."*

## How it rings

- **All at once** (recommended): every phone and browser of everyone in the group rings together. The first to answer gets the call; the others stop.
- **One after another**: each person rings for the seconds you set (15 by default), in the order shown. Use the arrows to change the order. Someone who is offline or on **Do not disturb** is skipped straight away.

The person calling never rings themselves, even when they're in the group.

![Choosing one after another](screen:ring-groups-add-how)

## If nobody answers

Choose where unanswered calls go: the group's own voicemail (recommended: callers leave a message, and everyone in the group will see it), someone's voicemail, a person, another ring group, *Play "We're closed" and hang up*, or **Nobody** (callers hear "not available"). Messages left for a group aren't emailed, since nobody owns the group's address. Everyone in the group hears them in [Voicemail](voicemail).

The group's details have its **Voicemail** settings: on or off (admins), and its own greeting and "we're closed" greeting, which anyone in the group can record.

A choice that would send calls round in circles (Sales to Support, and Support back to Sales) is greyed out, with the reason. Even so, Linx stops any call after 10 places.

## Its own number

A ring group can have a number of its own (from the **groups** range of your numbering plan, 600 to 699 unless you changed it). People can dial it, or transfer a call to it. A group without a number is reached only through another group.

A number is either an extension's or a ring group's, never both.

## Changing or removing a group

Choose a group to open it. Each part has its own **Edit**. **Used by** lists what sends calls to it.

**Remove** asks first where those calls should go instead, then removes the group. A group a phone number rings, or whose voicemail a number sends calls to, can't be removed until that number goes somewhere else. Its number stops working at once, and its voicemail goes with it.

When an extension is removed, it leaves its ring groups; a group that sent its unanswered calls to that extension, or to its voicemail, plays "not available" instead.

## Sending a phone number to a ring group

In [Incoming calls](incoming-numbers), choose **Change** next to the number and pick the group under **Office hours**. **Used by** on the group then lists the number.

## Undo

Every change here can be undone: **Undo** shows for 10 seconds after you save, and System → **Routing changes** keeps the last 50. See [Undo and routing changes](routing-changes).
