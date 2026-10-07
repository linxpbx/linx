# Linx

**Your calls. Your server.** Linx is a self-hosted, open-source phone system for homes and small businesses. It covers extensions, ring groups, voicemail, presence and a web phone; video meetings with guest links and the iPhone/iPad app on the App Store are coming soon. There's no subscription.

> **Status: 1.0.0 released.** The server, web client and iPhone/iPad app are built; see [`docs/ROADMAP.md`](docs/ROADMAP.md) for what each later phase adds.

## Install

On a Linux server (Intel/AMD or ARM), one line downloads the right build, checks it and starts the browser-based setup:

```sh
curl -fsSL https://raw.githubusercontent.com/linxpbx/linx/master/install.sh | sudo sh
```

Then open the one link it prints. Full walkthrough: [Installing Linx](docs/help/install-linx.md). Updating or moving a server: [`docs/ops/UPDATING.md`](docs/ops/UPDATING.md).

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
