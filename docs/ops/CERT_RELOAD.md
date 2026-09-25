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
| Asterisk (PJSIP TLS/WSS) | `pjsip.conf`'s transport points straight at `current/{fullchain,privkey}.pem` (`internal/asteriskconf`). The container's entrypoint stays beside Asterisk and checks `current`'s target once a minute; when it changes it runs `asterisk -rx "module reload res_pjsip.so"` (`services/asterisk-entrypoint`, with `internal/certs.Watcher`), retrying at the next check if that fails. Verified on the real image: new connections get the new certificate, phones already connected keep their connection, and a call in progress carries on (call suite, "picks up a renewed certificate during a call"). Without the reload Asterisk keeps serving the old one until restarted. (`pjsip reload` isn't an Asterisk 22 command.) | No |
| coturn | `turnserver.conf` points at `current/{fullchain,privkey}.pem` (`internal/turnconf`). coturn's entrypoint (`services/coturn-entrypoint`) stays beside it and checks `current`'s target once a minute (`internal/certs.Watcher`, shared with Asterisk's); when it changes it sends coturn `SIGUSR2`, which makes coturn re-read the files. The container's health check fails if the TLS port doesn't serve exactly the deployed certificate. Verified on the real image (`internal/turnconf`'s Docker test, "picks up a renewed certificate"): new TLS connections get the new certificate and a relayed session open across the renewal keeps working | No |
| LiveKit / livekit-sip | _Phase 1_ | |
| Front doors (Pangolin's Traefik, nginx/HAProxy, `linx-sni` HAProxy) | Nothing: they pass TLS through undecrypted by name (Phase 1C steps 6–7), so they hold no Linx certificate; browsers get the control plane's and coturn's, reloaded as above. An HTTP-only proxy (Caddy, ...) serves its own certificate and checks Linx's on each new connection, so Linx's renewals need nothing there either | No |
| Web/API (control plane, HTTPS 8443) | Nothing to reload: `tls.Config.GetCertificate` reads `current`'s target on each handshake and loads the new pair once it changes (`internal/certs.ServingCert`, Phase 1C step 5). Open connections keep theirs. The container's health check fails unless 8443 serves exactly the deployed certificate (`certs.ServesCurrent`, the same check as coturn's). Tested: `services/control-plane` health check test (a renewal is picked up; a server holding the old certificate is unhealthy) | No |
| Asterisk's browser websocket (internal CA, not certd; Phase 1C) | Asterisk's web server won't start without this certificate, and a reload doesn't bring it back, so Asterisk's entrypoint waits up to 2 minutes for it before starting Asterisk (Phase 1C step 5; the control plane writes it moments after it starts). The control plane issues a 24 h step-ca certificate for `linx-sipws` (renewed at two-thirds of its lifetime, `services/control-plane/sipws.go`) and writes it in certd's layout (`current` symlink swap) to the memory-only `sipws-certs` volume. Asterisk's entrypoint watches that `current` too and runs `asterisk -rx "module reload http"`, which re-reads the files and restarts only the HTTPS listener. Verified on the real image: a websocket open across the renewal keeps working, and new ones get the new certificate (call suite, "browsers over the secure websocket") | No |
| Control plane's ARI websocket (internal CA, not certd) | Its own 24 h step-ca certificate, re-issued in the process at two-thirds of its lifetime and served through `tls.Config.GetCertificate` (`internal/stepca`, Phase 1B step 4). Nothing to reload; Asterisk's open connection keeps working and the next reconnect sees the new certificate | No |

## Checking by hand
- `docker compose logs certd`: shows "certificate deployed" or "certificate is current".
- `sudo linx doctor` checks the certificate service is running, the certificate covers every hostname, the full chain up to a trusted (or, for test certificates, Let's Encrypt's test) authority, and expiry: a warning under 21 days left, a problem under 7. From Phase 1 it also checks that each service serves the current certificate.
