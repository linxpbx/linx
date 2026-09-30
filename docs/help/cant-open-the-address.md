---
title: Can't open Linx's address
audience: everyone
section: running
keywords: [can't connect, site can't be reached, not loading, timeout, certificate error, not secure, dns, down, offline]
screens: []
---
# Can't open Linx's address

## Check the basics

- Is the address right, starting with `https://`?
- Does another website open? If not, it's your internet connection.
- Does it open on your phone on mobile data? If yes, the problem is the network you're on.

## For admins

- On the server: `sudo linx doctor` ([linx doctor](linx-doctor)). It checks DNS, the certificate, the front door and every service.
- DNS: does your domain still point at the server (or the front door)? Did the domain lapse?
- The front door: at home, does the router still send port 443 to the server or to Pangolin? On a rented server, does the provider's firewall allow TCP and UDP 443? See [Front doors](front-doors).
- A certificate warning: the certificate ran out or doesn't match. `linx doctor` says which.

If the address can't be fixed from inside, use the [repair page](repair-page).
