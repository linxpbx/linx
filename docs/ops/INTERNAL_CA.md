# Internal certificate authority (step-ca)

Linx runs its own certificate authority, `linx-step-ca` (ADR-011). It issues:
- **service certificates** (24 h) so Linx's own parts can trust each other, and
- **device certificates** (7 days) for enrolled apps.

It only listens on the private network (`https://linx-step-ca:9000` on `linx-private`). Nothing outside the server can reach it.

## What setup creates

`linx setup` creates the CA once. Running setup again keeps the existing CA.

| Where | What | Who can read it |
|---|---|---|
| volume `linx-step-ca` | `ca.json`, root and intermediate certificates, encrypted intermediate key, database | the CA container |
| `/etc/linx/secrets/linx_step_ca_password` | unlocks the intermediate key | root, group 1000 (the CA) |
| `/etc/linx/secrets/linx_ca_services_password` | `linx-services` provisioner (24 h service certificates) | root, group 1000 |
| `/etc/linx/secrets/linx_ca_devices_password` | `linx-devices` provisioner (7-day device certificates); later mounted into the control plane only | root, group 1000 |
| `/etc/linx/ca-backup/` | `root_ca.crt` and `root_ca_key` (encrypted with your backup passphrase) | root |

The root key is never stored on the server unencrypted, and the CA volume never holds it. The backup passphrase is shown once at the end of setup and is not saved anywhere.

## After setup: take the root key off the server

1. Write down the backup passphrase setup showed you.
2. Copy the backup folder to your computer, then to a USB stick or password manager:
   `scp -r root@<server>:/etc/linx/ca-backup .`
3. Delete it from the server: `sudo rm -r /etc/linx/ca-backup`

You need the backup and passphrase only to renew the CA (its certificates last 10 years) or to rebuild it after losing the server's secrets.

## Checking it

`sudo linx doctor` checks that the CA answers, that its intermediate belongs to its root, how long both have left (a warning under 180 days, a problem under 30), and warns while the root key backup is still on the server.

`docker ps` shows `linx-step-ca` as `healthy` when the CA answers. By hand:

```
docker exec linx-step-ca step ca health --ca-url https://localhost:9000 --root /home/step/certs/root_ca.crt
```

`make test-docker` runs the whole bootstrap against a throwaway volume. It checks health, both lifetime limits, the backup passphrase, and that no root key is left in the volume.

## Container notes

The container runs as the image's `step` user (UID/GID 1000). Its filesystem is read-only and every capability is dropped except `NET_BIND_SERVICE`. The `step-ca` binary carries that one as a file capability and won't start without it. It lets a process listen on ports below 1024, nothing more.

## Rebuilding the intermediate (rare)

If the intermediate expires or `linx_step_ca_password` is lost, sign a new intermediate with the offline root. Do this on a trusted machine with the backup and passphrase, using the same step-ca image:

```
step certificate create "Linx Internal CA Intermediate CA" intermediate_ca.crt intermediate_ca_key \
  --profile intermediate-ca --ca root_ca.crt --ca-key root_ca_key --ca-password-file <passphrase file> \
  --kty EC --crv P-256 --not-after 87600h --password-file <new linx_step_ca_password>
```

Put both files into the volume's `certs/` and `secrets/` folders, update the secret file, and restart `linx-step-ca`. Service certificates renew within a day. A future `linx` command will automate this.
