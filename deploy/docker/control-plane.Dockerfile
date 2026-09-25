# The control plane with the web client built into it (ADR-037: one HTTPS
# port serves the page, the API and /sip).
#   docker buildx build -f deploy/docker/control-plane.Dockerfile \
#     --platform linux/amd64,linux/arm64 .
# The web build is architecture-independent, so it runs once on the build
# platform; Go cross-compiles natively (no QEMU).

# node:26.9.0-bookworm-slim (matches web/.nvmrc)
FROM --platform=$BUILDPLATFORM node:26.9.0-bookworm-slim@sha256:582460f614631b59b824ac6020533b9bf339c7fdf3a6d7db31abb6b4065f0212 AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
COPY api/openapi.yaml /src/api/openapi.yaml
COPY design/tokens.json /src/design/tokens.json
RUN npm run build

# golang:1.27.1-bookworm
FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X linxpbx.com/linx/internal/version.Version=${VERSION} -X linxpbx.com/linx/internal/version.Commit=${COMMIT}" \
      -o /out/service ./services/control-plane && \
    mkdir -p /out/data/state /out/data/certs

# gcr.io/distroless/static-debian12:nonroot (UID 65532, no shell, no package manager)
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
LABEL org.opencontainers.image.source="https://github.com/linxpbx/linx" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="linx-control-plane"
COPY --from=build /out/service /usr/local/bin/service
# The web client: read-only, owned by root.
COPY --from=web /src/web/dist /usr/share/linx/web
# Writable data dirs owned by nonroot, so named volumes mounted here inherit it.
COPY --from=build --chown=nonroot:nonroot /out/data /var/lib/linx
USER nonroot:nonroot
EXPOSE 8443
ENTRYPOINT ["/usr/local/bin/service"]
