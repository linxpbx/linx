---
title: System requirements
description: What Linx needs to run — server, network and domain.
section: Start here
order: 3
---

Linx is built to run well on small servers. These are the minimums; a little more headroom is better once you have several people and calls.

## Server

| | Minimum | Comfortable |
|---|---|---|
| CPU | 1 core | 2 cores |
| Memory | 1 GB | 2 GB |
| Free disk | 5 GB | 20 GB |
| OS | Linux (Ubuntu 24.04 / Debian) | — |
| CPU type | `amd64` or `arm64` | — |

A rented VPS, a mini PC at the office, or a Raspberry Pi 4/5 all work. The installer sets up Docker and runs Linx's services in containers.

## Network

- **A domain you own** (like `example.com`) with access to its DNS settings. Point a name such as `pbx.example.com` at the server.
- **Ports:** TCP 443 and UDP 443 open to the server for calls and the web app; TCP 6464 open **during setup only** (Linx closes it afterwards).
- Linx works **behind home routers (NAT)** and on **networks that only allow 443**. Audio and video are encrypted and scale down to survive slow links.

## Apps

- **iPhone & iPad** — the Linx app (coming to the App Store; TestFlight meanwhile).
- **Web** — any modern browser; nothing to install.

## Good to know

- Linx can't guarantee **emergency calling** — that depends on your phone-line provider.
- **VoIP is regulated in some countries** (for example the United Arab Emirates). Check your local rules before connecting external phone lines or using Linx over the internet.
