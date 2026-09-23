# Docker Compose

`compose.yaml` is the base stack. `linx setup` installs it as `/etc/linx/compose.yaml` (the copy is embedded in the `linx` binary) and writes `.env` (non-secret settings) and `secrets/` next to it, so you should never need to edit it by hand. Service images are `ghcr.io/linxpbx/linx-<service>:sha-<commit>`, matching the commit `linx` was built from. Per-profile overrides arrive with later Phase 0b items.

Services so far:
- `linx-certd`: public certificate (see `docs/ops/CERT_RELOAD.md`). Settings: `LINX_DOMAIN`, `LINX_DNS_PROVIDER` (`cloudflare` or `duckdns`), `LINX_ACME_EMAIL`, `LINX_ACME_STAGING` (default `true`), `LINX_CERT_WILDCARD` (default `true`). Secret: `secrets/linx_dns_token` (root, group 65532, mode 0440).
- `linx-step-ca`: internal certificate authority (see `docs/ops/INTERNAL_CA.md`). `linx setup` creates it in the external volume `linx-step-ca` before the first `up`. Secret: `secrets/linx_step_ca_password` (root-owned, group 1000, mode 0440).

Run once without the daemon: `sudo docker compose --file /etc/linx/compose.yaml run --rm certd -once`. Setup does this before starting the stack, so a wrong token or domain stops setup with the DNS provider's error.
