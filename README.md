# Linx

**Your calls. Your server.** Linx is a self-hosted, open-source phone system for homes and small businesses. It covers extensions, ring groups, voicemail, video meetings with guest links, and presence, with web and iPhone/iPad apps. There's no subscription.

> **Status: early development (Phase 0).** Linx is not ready for use yet. See [`docs/ROADMAP.md`](docs/ROADMAP.md).

## Documentation
- [Architecture](docs/ARCHITECTURE.md)
- [Decisions (ADRs)](docs/DECISIONS.md)
- [Threat model](docs/THREAT_MODEL.md)
- [Roadmap](docs/ROADMAP.md)
- [Design tokens](docs/ui/DESIGN_TOKENS.md)

## For developers
Requirements: Go (version in `go.mod`), Node.js with npm, GNU Make.

```sh
make setup-dev   # install web dependencies
make lint        # formatting, vet, token + type checks
make test        # all tests
make build       # binaries in bin/, web app in web/dist/
make tokens      # after editing design/tokens.json
```

## Important notices
- **Emergency calls** work only if your connected phone line provider supports them. Linx can't guarantee emergency calling.
- **VoIP is regulated in some countries** (for example the United Arab Emirates). Check your local rules before using Linx with external phone lines or over the internet.

## Licence
[Apache-2.0](LICENSE). Linx also runs separately licensed components; see [NOTICE](NOTICE).
