# Builds one Linx Go service into a minimal, non-root, multi-arch image.
#   docker buildx build -f deploy/docker/go-service.Dockerfile \
#     --build-arg SERVICE=control-plane --platform linux/amd64,linux/arm64 .
# Go cross-compiles natively (no QEMU); the final stage only copies the binary.

# golang:1.27.1-bookworm
FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS build
ARG SERVICE
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN test -n "$SERVICE" && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X linxpbx.com/linx/internal/version.Version=${VERSION} -X linxpbx.com/linx/internal/version.Commit=${COMMIT}" \
      -o /out/service ./services/${SERVICE} && \
    mkdir -p /out/data/state /out/data/certs

# gcr.io/distroless/static-debian12:nonroot (UID 65532, no shell, no package manager)
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG SERVICE
LABEL org.opencontainers.image.source="https://github.com/linxpbx/linx" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="linx-${SERVICE}"
COPY --from=build /out/service /usr/local/bin/service
# Writable data dirs owned by nonroot, so named volumes mounted here inherit it.
COPY --from=build --chown=nonroot:nonroot /out/data /var/lib/linx
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/service"]
