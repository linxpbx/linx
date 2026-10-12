# linx-livekit: the official LiveKit server (ADR-002, ADR-088), unchanged,
# on distroless as the nonroot user, with Linx's small Go entrypoint
# (services/livekit-entrypoint) that renders its configuration.
#   docker buildx build -f deploy/docker/livekit.Dockerfile --platform linux/amd64,linux/arm64 .
# Go cross-compiles natively (no QEMU); the LiveKit image is multi-arch and
# its binary is static, so it runs on distroless as it is.

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
      -o /out/livekit-entrypoint ./services/livekit-entrypoint

# livekit/livekit-server:v1.13.9, multi-arch index (Apache-2.0).
FROM livekit/livekit-server:v1.13.9@sha256:d0c04791bf63ca8dcea123571827d56ba9504bd57c8d5de60ee03b5408cc3154 AS livekit

# gcr.io/distroless/static-debian12:nonroot (UID 65532, no shell, no package manager)
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
LABEL org.opencontainers.image.source="https://github.com/linxpbx/linx" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="linx-livekit"
COPY --from=livekit /livekit-server /usr/local/bin/livekit-server
COPY --from=build /out/livekit-entrypoint /usr/local/bin/livekit-entrypoint
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/livekit-entrypoint"]
