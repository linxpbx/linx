# Backup and restore demo checklist

A hand-run check that backup and restore (`docs/BACKUP.md` §8, steps 1–6) do what they promise. Tick each box. Allow about two hours, most of it setting up the second server for the restore.

**Exit:** on the lab server, System → Backups backs up on a schedule and on request, the history shows it, and a failing destination raises the "backup failure" alert. A backup file downloads from the browser, and its password shows only after "confirm it's you". On a **second, fresh server**, the setup wizard's **Restore from a backup → A backup file on my computer** brings back everyone, their extensions, the phone line and the settings, and people sign in with their accounts from the backup (their authenticator codes still work: the encryption keys came back too). A file that isn't a Linx backup, or a wrong password, is refused with "Nothing on this server was changed". Automated: unit tests, `make test-docker` (real Postgres restores, including the crafted-backup cases from the review) and `make screens` are green in CI.

The examples use the lab server `pbx.mym.ae` at `192.168.1.212` (SSH user `linx`), the UCM6304 at `192.168.1.202`, and a second server `restore.mym.ae` at `192.168.1.213`. Use your own.

## You need
- The lab server from the Phase 1D demo, with its UCM line, people and extensions: a backup worth restoring.
- **A second Ubuntu 24.04 VM** on the home network (bridged, like the lab one), fresh, with its own name in your DNS zone (`restore.mym.ae`). Plan to delete it at the end.
- Your authenticator app with the lab server's accounts in it. (Passkeys are tied to the server's name, so they won't work on `restore.mym.ae`; the authenticator app and passwords will.)
- A password manager to save the backup's password in.
- The images published by CI for the commit you build, and `bin/linx`, `bin/linx-firewall-sync`, `bin/linx-backup-agent` from `make build` (setup installs the two helpers only when they're next to `linx`).

## 1. Docs
- [ ] `docs/BACKUP.md`, ADR-055's "As built" notes in `docs/DECISIONS.md`, and the "Backup review" at the end of `docs/THREAT_MODEL.md` read sensibly to you.

## 2. On your computer
```
cd ~/Projects/linx
git pull
make setup-dev
make lint          # ends with "lint: ok"
make test          # failures only; nothing listed means all passed
make security      # govulncheck, npm audit and licences all "ok"
make test-docker   # real Postgres: ends with "docker tests: ok"
make screens       # screenshots in web/e2e/screenshots/, including system-backups-*
make build
```
- [ ] All finish without errors.
- [ ] `web/e2e/screenshots/light-system-backups.png` and `setup-wizard-restore-file.png` look right to you.

## 3. CI on GitHub
- [ ] The latest **CI** run on master is green in every job, and **Packages** has `linx-control-plane` tagged `sha-<that commit>`.

## 4. Update the lab server
Follow steps 1–3 of [`ops/STAGING_TEST.md`](ops/STAGING_TEST.md) with that commit, copying `bin/linx-backup-agent` and `bin/linx-firewall-sync` next to `linx`. Keep your Phase 1D answers.
- [ ] Setup shows **Install the backup tool (restic)** (the first time only) and ends with "Linx is running".
- [ ] `restic version` prints a version (0.16 or newer).
- [ ] `systemctl status linx-backup-agent.timer` is **active (waiting)**.
- [ ] `sudo linx doctor` shows the database at `schema version 26`.

## 5. Back up from the browser
Sign in at `https://meet.pbx.mym.ae` as the system admin. Sidebar → **System** (opens on **Backups**).
- [ ] The tabs Status, Alerts, Activity and Settings are greyed ("Coming soon"); **Backups** is open.
- [ ] Choose **Every day** at **03:00**, **Save schedule** → "Saved."
- [ ] **Back up now** → "Starts within a minute". Within about a minute, **History** shows a row: *Back up now*, **Worked**, and a backup ID. **Where backups go** shows `local` worked.
- [ ] On the server, `sudo ls /var/backups/linx` shows `config data index keys locks snapshots`.

## 6. Download a backup file
Still on System → Backups, **Download a copy**:
- [ ] **Make a backup file** asks you to confirm it's you (if you signed in more than ten minutes ago), then shows "Waiting for the server…", then "Backing up now and making the file…", then **Your backup file is ready** with its size and the time of the newest backup in it. History gained another *Back up now* row.
- [ ] **Download file** saves `linx-backup-YYYY-MM-DD-HHMM.tar` to your computer. Its size matches.
- [ ] **Show its password** (confirm again if asked) shows it in the amber box. **Copy**, and save it in your password manager as "Linx backup, pbx.mym.ae". Keep it apart from the file.
- [ ] On the server, the password is the same one Linx keeps there: `sudo cat /etc/linx/secrets/linx-backup/local.password`.
- [ ] The activity log recorded it, without the password:
  ```
  sudo docker exec linx-postgres psql -U linx -d linx -tAc \
    "SELECT action, actor FROM audit_log WHERE action LIKE 'backup.%' ORDER BY at DESC LIMIT 6"
  ```
  shows `backup.password_shown`, `backup.downloaded`, `backup.download_requested`, `backup.requested`.
- [ ] Sign in in another browser as a different admin (or a reporter). System → Backups says another admin has a backup file ready and offers them no download. A reporter sees every card with the buttons greyed ("Only an admin can change backups").

## 7. A failing destination raises the alert
Add a place that can't work, back up, then remove it:
```
sudo linx backup destination add --kind sftp --host 192.168.1.250 --user backup --remote-path /srv/linx broken
```
(It prints a public key to add on that server; there's no server there, so skip it.)
- [ ] **Back up now** → History shows **Partly worked**, with `broken:` and the reason under it. **Where backups go** lists `local` worked and `broken` failed.
- [ ] The Admin **Home** shows an open alert "A backup destination failed" (and it reaches your alert channel, if one is set up).
```
sudo linx backup destination remove broken
```
- [ ] **Back up now** again → **Worked**, and the alert resolves.

## 8. The second server
Install Linx on the fresh VM (`ops/STAGING_TEST.md` steps 1–3), as `restore.mym.ae`, front door **home-only**, a trusted certificate, owner email your own.

**Before you restore**, stop the copy from ever reaching the UCM (a restored copy has the lab's phone line, and two servers on one line would steal each other's calls):
```
sudo ip route add prohibit 192.168.1.202/32
```
- [ ] `ping -c1 192.168.1.202` fails with "Packet filtered" or similar.
- [ ] Open the first-admin link setup printed, set a password and an authenticator. The wizard asks **How do you want to start?**

## 9. Wrong things are refused, and nothing changes
**Restore from a backup** → **A backup file on my computer**.
- [ ] Choose any other `.tar` (make one: `tar cf not-a-backup.tar some-file`), enter any password, tick **I understand**, **Restore**. It uploads (progress bar), then comes back with "The restore didn't work: this isn't a Linx backup file … Nothing on this server was changed."
- [ ] Choose the real file with a **wrong** password → "… wrong password … Nothing on this server was changed."
- [ ] You're still signed in as the new server's admin, and `sudo ls /var/lib/linx` on it holds no `upload-*`, `backup-file-*` or `restore-*` folders.

## 10. Restore
Same screen: the real file, the real password (from your password manager), **The newest**, tick **I understand, replace everything**, **Restore**, confirm it's you.
- [ ] The file uploads, then "Starting the restore" → "Restoring your backup" → "Linx is restarting…" → **Restored**, within a few minutes.
- [ ] **Sign in** with your **lab** account (the lab's email and password), and the **lab's** authenticator code from your app. (Your new account from step 8 is gone: it was replaced.)
- [ ] People and Extensions show exactly the lab's people and extensions.
- [ ] `sudo linx trunk list` on the copy shows "UCM landlines". (Your authenticator code working is the proof the encryption key came back: the code's secret is stored sealed with it.)
- [ ] `sudo linx doctor` on the copy: the database is fine (schema version 26); the phone line shows as unreachable (expected: the route from step 8 blocks it).
- [ ] System → Backups on the copy shows the lab's history (up to the backup). The server kept the database from before as `linx_before_restore` and the old keys as `*.before-restore`:
  ```
  sudo docker exec linx-postgres psql -U linx -tAc "SELECT datname FROM pg_database WHERE datname LIKE 'linx%'"
  sudo ls /etc/linx/secrets | grep before-restore
  ```
- [ ] The uploaded file is gone from both places: `sudo ls -a /var/lib/docker/volumes/linx_backup-transfer/_data` lists nothing but `.` and `..` (the control plane's image has no `ls` of its own).

## 11. The review's main fix, by hand (optional)
The review found that a crafted backup could run programs in the database container; restores now load a backup as a powerless role and accept only Linx's own structure. `make test-docker` proves it (`TestPostgresRestoreDocker`). By hand, on the copy:
```
sudo docker exec linx-postgres psql -U linx -tAc \
  "SELECT rolname, rolsuper, rolcanlogin FROM pg_roles WHERE rolname = 'linx_restore_loader'"
```
- [ ] `linx_restore_loader|f|f`: not a superuser, and can't sign in outside a restore.

## 12. Clean up
- Delete the second VM (and `restore.mym.ae` from DNS).
- Delete the downloaded `.tar` from your computer if you don't want to keep it (it's useless without the password, but it's your whole phone system with it).
- On the lab server, keep the daily schedule.

## Results
Add one line per run: date, servers, commit, passed or what failed.
