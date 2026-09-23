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
| Asterisk (PJSIP TLS/WSS) | _Phase 1_ | |
| coturn | _Phase 1_ | |
| LiveKit / livekit-sip | _Phase 1_ | |
| Web/API (control plane) | _Phase 1_ (Go: re-read on `tls.Config.GetCertificate`) | No |

## Checking by hand
- `docker compose logs certd`: shows "certificate deployed" or "certificate is current".
- `linx doctor` (Phase 0b item 5) checks expiry, issuer and that each service serves the current certificate.
