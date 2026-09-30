---
title: No audio on a call
audience: everyone
section: running
keywords: [no audio, can't hear, one-way audio, silence, microphone not working, choppy, robotic, echo, quality]
screens: []
---
# No audio on a call

## First, the echo test

Call `*43`. If you hear yourself, your microphone and speaker work, and the problem is on the other side of the call.

## You hear nothing, or they can't hear you

- Your browser must be allowed to use the microphone. Look for a microphone or camera icon in the address bar.
- Check [Settings](microphone-and-speaker): the right **Microphone** and **Speaker**.
- Are you muted? The **Mute** button shows it.
- Close other apps that use the microphone (another call app, a meeting).

## Choppy or robotic sound

Usually a slow or busy connection. Linx keeps calls working on poor links, but very poor Wi-Fi still hurts: move closer to the router, or try mobile data.

## For admins

- On some hotel and office networks, calls take a slower path through port 443. They still work.
- Nobody outside hears anything? Check [Front doors](front-doors): TCP and UDP port 443 should both reach Linx.
- Calls through a phone line only: see [A phone line is down](line-down).
