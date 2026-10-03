# Updating Linx

Until releases exist (Phase 5, `docs/ROADMAP.md` "Releases and updating from the web"), a server is updated by copying a newer `linx` program to it and running setup with the saved answers. This stays the way to update when the web page is broken or GitHub can't be reached.

## Steps
1. On your computer, from an up-to-date, clean checkout of master, once CI is green for that commit (its service images must be on ghcr.io):
   ```
   GOOS=linux GOARCH=amd64 make build    # GOARCH=arm64 for an ARM server
   scp bin/linx bin/linx-firewall-sync <you>@<server>:   # side by side: setup installs both
   ```
2. On the server, a backup first, then the update:
   ```
   sudo linx backup
   sudo ./linx setup --config /etc/linx/setup.yaml
   ```
   It asks nothing and keeps every setting: it installs the new `linx` as `/usr/local/bin/linx`, downloads the new service images, restarts what changed, waits until every service is healthy, and removes the previous version's images.
3. Check: `sudo linx doctor`.

## How setup tells you
Plain `sudo ./linx setup` on an installed server only opens the Server settings page (or finds it already open: "Nothing was reopened"), which doesn't update anything. So when the program you run isn't the version running, it says so first and prints the command from step 2:
```
This linx program (e7bec3e) isn't the version of Linx running here (525d29f).
To update Linx to it, keeping every setting, run:

  sudo /home/linx/linx setup --config /etc/linx/setup.yaml
```
It names the program by its full path because `sudo linx` would still run the installed, older one. "The version running" is `LINX_VERSION` in `/etc/linx/.env`, which setup writes each time it sets up the services. The versions are commits: setup can't tell newer from older, so check the commit before copying an older program by mistake.

## Going back
Copy the older `linx` program and run step 2's setup command with it. If the update changed the database, the older version may not start: restore the backup taken in step 2 (`docs/DEMO_BACKUP.md`).
