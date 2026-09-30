---
title: Moving Linx to a new server
audience: system_admin
section: install
keywords: [move, migrate, new server, new hardware, transfer, restore, moved to a new place]
screens: []
---
# Moving Linx to a new server

Moving Linx is a backup on the old server and a restore during the install on the new one.

## Steps

1. On the old server: System → [Backups](backups) → **Make a backup file**, then **Download file** and **Show its password**. Keep the password apart from the file.
2. Install Linx on the new server ([Installing Linx](install-linx)), with the same domain if you can.
3. In the setup wizard, choose **Restore from a backup** and give it the file and its password. See [Restoring from a backup](restore).
4. Point your DNS at the new server, if Linx doesn't do it for you.
5. *Turn the old server off.* Both would claim the same names.

## Moved to a new place?

After a restore, Admin home shows **Moved to a new place?** when the backup came from somewhere else. It lists what may still point at the old place:

- phone lines tied to the old network or address (a phone system on the old network, a phone company that knows the old address, private connections);
- desk phones set up on the old network;
- "admins only from my home network", if it lists the old network;
- a different domain: passkeys stop working, so sign in with your password and authenticator app and add new ones; desk phones need the new address;
- backup places on other computers or storage, which you add again on the new server.
