---
title: Desk phones and phone apps
audience: admin
section: admin
keywords: [desk phone, handset, ip phone, yealink, grandstream, snom, polycom, softphone, phone app, zoiper, grandstream wave, linphone, sip account, register, credentials, settings box]
screens: []
---
# Desk phones and phone apps

A desk phone, or a phone app on a mobile or computer, signs in to Linx with its own username and password.

## Adding one

Extensions → open the extension → **+ Add a desk phone or phone app**. Choose **Desk phone** or **Phone app**.

![Adding a desk phone](screen:extensions-add-device)

Linx shows the settings box once. Enter each line on the phone:

- **Server**: Linx's phone address, `sip.` in front of your domain.
- **Port**: 5061.
- **Transport**: TLS. Some phones call it *SIP over TLS*.
- **Username** and **Password**: exactly as shown.
- **Audio**: **SRTP (required)**. Some phones call it *encryption* or *secure audio*; set it to required or mandatory.

You won't see the password again, so enter it now, then **I've saved it**. Lost it? Open the phone and choose **New password**: the phone stops working until you enter the new one.

## Where they work

Desk phones work on your home or office network. Using one from outside comes later.

## It won't sign in

See [A phone won't register](phone-wont-register).
