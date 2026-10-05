---
title: A phone system or gateway
audience: admin
section: admin
keywords: [ucm, grandstream ucm, ucm6304, pbx, gateway, fxo, landline, analog, register trunk, signs in to linx]
screens: []
---
# A phone system or gateway

Your own phone system (like a Grandstream UCM) or a gateway for landlines can sign in to Linx, the same way a desk phone does. Linx then sends calls out through its landlines, and its calls ring in Linx.

## In Linx

1. Phone lines → **+ Add** → **Guide me** → **A phone system or gateway (signs in to Linx)**.
2. Name it, like `UCM landlines`.
3. Add the landline numbers and which extension each rings. **Calls for any other number ring** one extension: a landline often sends no number, and its calls ring there.
4. Choose whether outside calls go out through it (**Yes, as the main line**).
5. **Create** and confirm it's you. Linx shows the server, port 5061, TLS, a username and a password, once, and **How to enter this on** your system.

## On the phone system

Add a trunk that *registers* to Linx, with exactly those details: the server with port 5061, transport TLS, the username (also as its *Auth ID*) and the password. Set encrypted audio (SRTP) to required.

On a Grandstream UCM: Extension/Trunk → VoIP Trunks → Add SIP Trunk, type *Register SIP Trunk*. Linx's page shows each setting.

## Signed in

Linx's page turns from **Waiting for it to sign in…** to **Signed in** within a minute, by itself. Then:

- on the phone system, send the trunk's calls to the landlines, and the landlines' calls to the trunk;
- call the landline from your mobile: your browser rings;
- call your mobile from the browser.

## Some extensions can call out and others can't

A phone system or gateway usually decides what a call may do from *the caller ID it arrives with*, and Linx's caller ID is not the same for everybody:

- a person whose own number is one of this line's numbers calls out *as that number*;
- everybody else calls out as the line's **Caller ID shown to others** (System → Phone lines → the line → Edit), and when that is empty, as their *extension number* (201, 202…).

So a gateway set to accept only one of those lets some people out and tells the others they aren't allowed — in its own words, down the line, which sounds exactly like Linx refusing the call. It isn't: *Linx's own check is the call permission level*, and when that refuses a call you hear *"This phone isn't allowed to call that number"* and the call appears in Calls as *not permitted*. A refusal from the gateway shows up as *no answer* instead.

Two ways to fix it, and the first is simpler:

1. Set the line's **Caller ID shown to others** to one of the line's own numbers, written *exactly as the gateway expects it* — `+97142340100` and `042340100` are the same number to a person and two different patterns to a gateway. Then everybody calls out as that number.
2. Or widen the gateway's own rule (on a Grandstream UCM: the outbound route's **Source Caller ID Pattern**) to accept your extension numbers as well, and give the route from Linx the privilege that route needs.

## When a caller hangs up on a landline

A landline never tells the gateway that the other person hung up: their exchange plays a busy tone instead. Linx listens for that tone on a phone system's or gateway's line and hangs up within a few seconds, so the landline is free again, and a voicemail ends there without the tone. This needs the line's audio to be G.711 (PCMA or PCMU, what landlines use).

