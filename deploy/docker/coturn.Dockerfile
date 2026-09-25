# linx-coturn: the official coturn image (ADR-039), unchanged, plus Linx's
# small Go entrypoint (services/coturn-entrypoint), which renders coturn's
# configuration, runs it, and reloads its certificate on renewal.
#   docker buildx build -f deploy/docker/coturn.Dockerfile --platform linux/amd64,linux/arm64 .
# Go cross-compiles natively (no QEMU); the coturn image is multi-arch.

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
      -o /out/coturn-entrypoint ./services/coturn-entrypoint

# coturn/coturn:4.18.0 (Debian 13), multi-arch index
FROM coturn/coturn:4.18.0@sha256:bbefd3e1fdfdc0d58770fe01b581fd8b00d9f3a5580d00acb77cf719a6bc78e3
LABEL org.opencontainers.image.source="https://github.com/linxpbx/linx" \
      org.opencontainers.image.licenses="Apache-2.0 AND BSD-3-Clause" \
      org.opencontainers.image.title="linx-coturn"
COPY --from=build /out/coturn-entrypoint /usr/local/bin/coturn-entrypoint
# The image's turnserver carries a file capability (bind ports below 1024),
# and exec of such a file fails under compose's cap_drop: ALL +
# no-new-privileges. Linx's coturn listens on 3478/5349 only (the front
# door maps 443), so a plain copy, which drops the capability, is all it
# needs: no capability is added back.
USER root
RUN cp /usr/bin/turnserver /usr/bin/turnserver.plain && mv /usr/bin/turnserver.plain /usr/bin/turnserver
# The Linx services' nonroot user and group (65532): reads the certs
# volume's private key and the relay secret, both group 65532.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/coturn-entrypoint"]
