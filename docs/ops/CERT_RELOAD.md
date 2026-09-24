# Certificate files and reload

`linx-certd` keeps the public certificate in the `certs` volume:

```
/var/lib/linx/certs/
  current -> 20260923T101500.000000000Z   (symlink, swapped atomically)
  20260923T101500.000000000Z/fullchain.pem  (leaf + intermediates)
  20260923T101500.000000000Z/privkey.pem    (0640, group 65532)
  20260923T101500.000000000Z/meta.json      (names, issuer, staging, expiry)
```

Each renewal writes a new directory and then swaps `current`, so a reader never sees a new certificate paired with an old key. The two newest versions are kept.

## How a service uses it
- Mount the volume **read-only**: `certs:/var/lib/linx/certs:ro`.
- Add `group_add: ["65532"]` so it can read `privkey.pem`.
- Always open `current/fullchain.pem` and `current/privkey.pem`, never a versioned path.

## Reload per component
Filled in as each component lands (Phase 1). Every method must be verified by checking the certificate the service actually serves after reload.

| Component | Method | Drops calls? |
|---|---|---|
| Asterisk (PJSIP TLS/WSS) | `pjsip.conf`'s transport points straight at `current/{fullchain,privkey}.pem` (`internal/asteriskconf`). The container's entrypoint stays beside Asterisk and checks `current`'s target once a minute; when it changes it runs `asterisk -rx "module reload res_pjsip.so"` (`services/asterisk-entrypoint/certwatch.go`), retrying at the next check if that fails. Verified on the real image: new connections get the new certificate, phones already connected keep their connection, and a call in progress carries on (call suite, "picks up a renewed certificate during a call"). Without the reload Asterisk keeps serving the old one until restarted. (`pjsip reload` isn't an Asterisk 22 command.) | No |
| coturn | _Phase 1_ | |
| LiveKit / livekit-sip | _Phase 1_ | |
| Web/API (control plane) | _Phase 1_ (Go: re-read on `tls.Config.GetCertificate`) | No |
| Control plane's ARI websocket (internal CA, not certd) | Its own 24 h step-ca certificate, re-issued in the process at two-thirds of its lifetime and served through `tls.Config.GetCertificate` (`internal/stepca`, Phase 1B step 4). Nothing to reload; Asterisk's open connection keeps working and the next reconnect sees the new certificate | No |

## Checking by hand
- `docker compose logs certd`: shows "certificate deployed" or "certificate is current".
- `sudo linx doctor` checks the certificate service is running, the certificate covers every hostname, the full chain up to a trusted (or, for test certificates, Let's Encrypt's test) authority, and expiry: a warning under 21 days left, a problem under 7. From Phase 1 it also checks that each service serves the current certificate.
