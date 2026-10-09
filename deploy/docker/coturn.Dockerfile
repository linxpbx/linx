# linx-coturn: the official coturn image (ADR-039), unchanged, plus Linx's
# small Go entrypoint (services/coturn-entrypoint), which renders coturn's
# configuration, runs it, and reloads its certificate on renewal. Alpine's
# security updates are applied at build time.
#   docker buildx build -f deploy/docker/coturn.Dockerfile --platform linux/amd64,linux/arm64 .
# Go cross-compiles natively (no QEMU); the coturn image is multi-arch.

# golang:1.27.2-bookworm
FROM --platform=$BUILDPLATFORM golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
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

# coturn/coturn:4.18.0-alpine, multi-arch index: the same coturn release as
# the Debian variant, about 87 MB unpacked instead of about 400 (install
# demo, 2026-09-28: the smallest servers count every hundred megabytes).
FROM coturn/coturn:4.18.0-alpine@sha256:c29135c08565810d37053f384584392b7c24b69570fc81a721b878d6fe63e04e
LABEL org.opencontainers.image.source="https://github.com/linxpbx/linx" \
      org.opencontainers.image.licenses="Apache-2.0 AND BSD-3-Clause" \
      org.opencontainers.image.title="linx-coturn"
COPY --from=build /out/coturn-entrypoint /usr/local/bin/coturn-entrypoint
# The image's turnserver may carry a file capability (bind ports below
# 1024), and exec of such a file fails under compose's cap_drop: ALL +
# no-new-privileges. Linx's coturn listens on 3478/5349 only (the front
# door maps 443), so a plain copy, which drops any capability, is all it
# needs: no capability is added back.
USER root
# Alpine's security updates since the coturn image was built (like the
# Asterisk image, which installs current packages), and without the DNS
# tools the image carries only for its own entrypoint's external-IP
# detection, which Linx doesn't use.
RUN apk upgrade --no-cache \
 && apk del --no-cache bind-tools \
 && cp /usr/bin/turnserver /usr/bin/turnserver.plain && mv /usr/bin/turnserver.plain /usr/bin/turnserver
# The Linx services' nonroot user and group (65532): reads the certs
# volume's private key and the relay secret, both group 65532.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/coturn-entrypoint"]
