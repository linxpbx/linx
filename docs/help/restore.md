---
title: Restoring from a backup
audience: system_admin
section: running
keywords: [restore, recover, disaster recovery, bring back, backup file, backup password, rebuild]
screens: []
---
# Restoring from a backup

A restore replaces everything in Linx with what the backup holds.

## On a new install

In the setup wizard, choose **Restore from a backup**.

1. **Where is the backup?**
   - **A backup file on my computer**: the file System → Backups → **Download file** gave you.
   - **A folder on this server**.
   - **A backup place set up on this server**, like a NAS.
2. **Which backup?** **The newest**, or **An older one** by its **Backup ID**.
3. **The backup's password**, then tick **I understand, replace everything** and confirm it's you.
4. Linx restores it and restarts. Sign in with the accounts from the backup.

If the backup came from another place, Admin home shows **Moved to a new place?** ([Moving Linx to a new server](moving-to-a-new-server#moved-to-a-new-place)).

## On a server that's already set up

From the server:

```
sudo linx restore
```

It checks everything before changing anything, and keeps what it replaced until the next restore.
