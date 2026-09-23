# Docker Compose

`compose.yaml` is the base stack. `linx setup` writes `.env` (non-secret settings) and `secrets/` (mode 0600) next to it, so you should never need to edit it by hand. Per-profile overrides arrive with later Phase 0b items.

Services so far:
- `linx-certd`: public certificate (see `docs/ops/CERT_RELOAD.md`). Settings: `LINX_DOMAIN`, `LINX_DNS_PROVIDER` (`cloudflare` or `duckdns`), `LINX_ACME_EMAIL`, `LINX_ACME_STAGING` (default `true`), `LINX_CERT_WILDCARD` (default `true`). Secret: `secrets/linx_dns_token`.
- `linx-step-ca`: internal certificate authority (see `docs/ops/INTERNAL_CA.md`). `linx setup` creates it in the external volume `linx-step-ca` before the first `up`. Secret: `secrets/linx_step_ca_password` (root-owned, group 1000, mode 0440).

Run once without the daemon: `docker compose run --rm certd -once`.
