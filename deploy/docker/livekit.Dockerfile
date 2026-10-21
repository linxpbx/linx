# linx-livekit: the LiveKit server (ADR-002, ADR-088), built from its
# released source, on distroless as the nonroot user, with Linx's small Go entrypoint
# (services/livekit-entrypoint) that renders its configuration.
#   docker buildx build -f deploy/docker/livekit.Dockerfile --platform linux/amd64,linux/arm64 .
# Go cross-compiles natively (no QEMU); both binaries are static, so they
# run on distroless as they are.

# golang:1.27.3-bookworm
FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm@sha256:5c3e9b43cfd3bb95c7937462e29d87ada57b481a96fa23ad0eb61778d7a00110 AS build
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

# LiveKit server v1.13.9 (Apache-2.0), built from its own source rather
# than taken from its image: the released binary was built with Go 1.26.7
# and golang.org/x/net v0.59.0, which carry four HIGH CVEs (CVE-2026-78667,
# -78669, -97031) that CI's scan blocks on. Same source, tag checked
# against its commit, built with Linx's pinned Go and x/net v0.60.0.
# golang:1.27.3-bookworm
FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm@sha256:5c3e9b43cfd3bb95c7937462e29d87ada57b481a96fa23ad0eb61778d7a00110 AS livekit
ARG TARGETOS
ARG TARGETARCH
ARG LIVEKIT_TAG=v1.13.9
ARG LIVEKIT_COMMIT=6fbe7f7e648aab4b2f734d6044a416bc2cbc482c
WORKDIR /livekit
RUN git clone --quiet --depth 1 --branch ${LIVEKIT_TAG} https://github.com/livekit/livekit.git . \
    && test "$(git rev-parse HEAD)" = "${LIVEKIT_COMMIT}" \
    && go get golang.org/x/net@v0.60.0 \
    && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" \
       -o /livekit-server ./cmd/server

# gcr.io/distroless/static-debian12:nonroot (UID 65532, no shell, no package manager)
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
LABEL org.opencontainers.image.source="https://github.com/linxpbx/linx" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="linx-livekit"
COPY --from=livekit /livekit-server /usr/local/bin/livekit-server
COPY --from=build /out/livekit-entrypoint /usr/local/bin/livekit-entrypoint
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/livekit-entrypoint"]
