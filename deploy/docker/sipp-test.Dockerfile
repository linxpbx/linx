# SIPp with TLS, for the phone engine's automated call tests only
# (internal/calltest, `make test-docker`). Never shipped, never part of the
# Linx stack. Debian's own sip-tester package is built without TLS, so this
# builds SIPp's release source, pinned by SHA-256.
#   docker buildx build -f deploy/docker/sipp-test.Dockerfile --load -t linx-sipp-test:dev .

ARG SIPP_VERSION=3.7.7
ARG SIPP_SHA256=e55b15f567760e9febeef366a1ab51a5239d197a132ce931b78c826d22d31e69

# debian:bookworm-slim (multi-arch index digest), as deploy/docker/asterisk.Dockerfile
FROM debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251 AS build
ARG SIPP_VERSION
ARG SIPP_SHA256
RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends \
      build-essential cmake libssl-dev libncurses-dev libgsl-dev \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /usr/src
ADD --checksum=sha256:${SIPP_SHA256} \
    https://github.com/SIPp/sipp/releases/download/v${SIPP_VERSION}/sipp-${SIPP_VERSION}.tar.gz sipp.tar.gz
RUN tar xzf sipp.tar.gz && cd "sipp-${SIPP_VERSION}" \
    && cmake . -DUSE_SSL=1 -DUSE_PCAP=0 -DUSE_SCTP=0 -DUSE_GSL=1 \
    && make -j"$(nproc)" && install -m 0755 sipp /usr/local/bin/sipp

FROM debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251
RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends libssl3 libncursesw6 libtinfo6 libgsl27 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /usr/local/bin/sipp /usr/local/bin/sipp
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/sipp"]
