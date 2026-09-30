---
title: Backups
audience: admin
section: running
keywords: [backup, back up, schedule, download backup, backup file, backup password, nas, sftp, s3, cloud storage, history]
screens: [/admin/system/backups]
---
# Backups

A backup holds everything Linx knows: people, extensions, phone lines, settings, and the keys to read them. Keep backups, and keep a copy away from the server.

![Backups](screen:system-backups)

## Back up automatically

Choose **Every day** (recommended), **Every week** or **Every month**, and a time. A quiet hour, like 03:00, is best. **Save schedule**.

**Back up now** makes one straight away. It starts within a minute.

## Download a copy

**Make a backup file**. After a minute or two:

- **Download file** saves it on your computer.
- **Show its password**: the file is locked with it. Keep the password apart from the file, in your password manager.

The file stays ready for an hour.

## Where backups go

On the server itself, in `/var/backups/linx`, and anywhere else you add from the server, like a NAS or cloud storage:

```
sudo linx backup destination add
```

Their keys stay on the server, never in a backup, so after a move you add them again.

## History

Each backup, when and how it ran, and whether it worked. If one fails, you get an alert ([Alerts](alerts)).

To bring a backup back: [Restoring from a backup](restore).
