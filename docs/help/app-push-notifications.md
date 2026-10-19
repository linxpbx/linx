---
title: Making the app ring when nobody is holding the phone
audience: admin
section: running
keywords: [push, notifications, apns, apple, ios, iphone, ipad, wake, sleeping phone, voip push, calls to the app, p8, team id, bundle id, key, own app, publish]
screens: [/admin/system/settings]
---
# Making the app ring when nobody is holding the phone

An iPhone that is locked, or whose screen is off, or that has had the app
closed, is not running Linx at all. iOS does not let any app sit waiting for a
call in the background — not Linx, not WhatsApp, not Apple's own Phone app.

So when a call arrives, Linx has to *wake the phone*, and the only thing
that can wake an iPhone is Apple's push service. For that, your server needs a
key from Apple.

*Without it, everything still works except the one thing that matters:* the
app rings while somebody is looking at it, and stays silent the rest of the
time. Calls are not rejected and nothing reports an error — they simply go
unanswered. `linx doctor` tells you if this is how your server is set up.

## Why you can't just be given a key

Apple only lets an app be woken by a key belonging to *whoever published that
app*. There is no way to delegate it, no setting to share, and no key you can
obtain for somebody else's app. This is Apple's rule and Linx cannot work
around it.

That has one consequence, and it is the whole of this guide:

*To have push notifications, the app your people install has to be yours.*

## What that means in practice

You publish your own build of the Linx app. The app is part of Linx and the
source is in the `ios/` folder, so this is packaging rather than programming.

*What it costs.* An Apple Developer account, currently 99 USD a year. If you
only need your own staff to have the app, Apple Business Manager can
distribute it privately and no public App Store listing is involved.

*What you do, once:*

1. *Join the Apple Developer Program* and note your *Team ID* (ten
   characters, in Membership details).
2. *Register a bundle id* of your own — something like
   `com.yourcompany.phone`. This is what makes the app yours; it cannot be the
   one Linx itself publishes.
3. *Create a push key*: Certificates, Identifiers & Profiles → *Keys* →
   *+* → tick *Apple Push Notifications service (APNs)* → Register, then
   *Download*. Apple gives you this file once and never again, so put it
   straight into your password manager.
4. *Build and distribute the app* with your Team ID and your bundle id,
   either to the App Store or through Apple Business Manager.
5. *Tell Linx*: System → Settings → **Calls to the app**. Paste the key,
   your Team ID and your bundle id. The key is sealed as soon as you save it
   and is never shown again.

Then set a phone up and check: lock it, close the app, and call that
extension. It should ring like any other call.

## Keep the key safe, and know what revoking it does

The key wakes every phone on your system. Treat it as you would a password:

- Apple lets you download it *once*. There is no way to get the file again.
- You may have *two* keys active at a time, which is how you move to a new
  one without an interruption: add the new one, change Linx over, then revoke
  the old.
- *Revoking a key stops every phone being woken* on every server using it.
  If you run more than one Linx — a live one and a staging one, say — giving
  each its own key means you can revoke one without silencing the other.

## Other kinds of phone

Android works the same way with Google's service rather than Apple's, and the
same rule applies: the app has to be yours. Linx does not have an Android app
yet; when it does, this page will say what to set up for it.

## If you would rather not do any of this

Then do not. Linx is a complete phone system without it:

- *Desk phones* ring whether or not anyone is holding a phone.
- *The web app* rings in any open browser tab.
- *The mobile app* still works for making calls and for calls that arrive
  while somebody is using it.

What you lose is a call reaching a phone in somebody's pocket, which for most
offices is the reason to have the app at all. It is worth being deliberate
about rather than discovering later.
