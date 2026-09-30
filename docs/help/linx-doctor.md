---
title: linx doctor
audience: system_admin
section: running
keywords: [doctor, check, diagnose, health check, terminal, command line, ssh]
screens: []
---
# linx doctor

`linx doctor` checks the whole server from the inside: services, certificates, DNS, the firewall, phone lines, backups and more. Run it on the server:

```
sudo linx doctor
```

Each line says ok, a warning, or a failure, with what to do in plain words. It ends with *Everything checked is working* when all is well.

## When to run it

- After installing or moving Linx.
- When something seems wrong, before anything else.
- When System → [Status](system-status) can't be opened.

## Common warnings

- *The root key backup is still on the server*: copy `/etc/linx/ca-backup` somewhere safe, then remove it from the server.
- *No API key*: nothing to do unless your own software needs one.
