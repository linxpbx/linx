# Builds linx-asterisk: Asterisk 22 LTS compiled from the official signed
# source release, with only the modules Linx uses (ADR-031, docs/PBX.md §2),
# plus the small Go entrypoint that renders its config at container start.
#   docker buildx build -f deploy/docker/asterisk.Dockerfile \
#     --platform linux/amd64,linux/arm64 .
#
# Asterisk has no official Docker image (community images are one-person
# projects with unclear patch speed), so we own the build. A new Asterisk
# 22.x release means bumping ASTERISK_VERSION/_SHA256 below; nothing else in
# this file changes for a routine security release.

ARG ASTERISK_VERSION=22.11.0
# SHA-256 of the release tarball, pinned from a build that verified its GPG
# signature (see below) — not just copied from the download page.
ARG ASTERISK_SHA256=3bd5ee040509a3d3cd9b1ba9520c18e6ec0a7e7981ca68c457dcd36ba3c54d94
# Asterisk Development Team <asteriskteam@digium.com>, fetched from
# keyserver.ubuntu.com and pinned here; deploy/docker/asterisk/asterisk-pubkey.asc
# must export to this exact fingerprint.
ARG ASTERISK_GPG_FINGERPRINT=F2FC93DB7587BD1FB49E045A5D984BE337191CE7
# English prompts ("number not in service", "nobody is available"). Asterisk's
# own sounds Makefile downloads these without checking anything, so we fetch
# the tarball ourselves, pinned by SHA-256 (captured from a download whose
# SHA-1 matched downloads.asterisk.org's published .sha1), and leave it where
# the Makefile finds it.
ARG CORE_SOUNDS=asterisk-core-sounds-en-gsm-1.6.1.tar.gz
ARG CORE_SOUNDS_SHA256=d79c3d2044d41da8f363c447dfccc140be86b4fcc41b1ca5a60a80da52f24f2d

# debian:bookworm-slim (multi-arch index digest)
FROM debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251 AS asterisk-build
ARG ASTERISK_VERSION
ARG ASTERISK_SHA256
ARG ASTERISK_GPG_FINGERPRINT
ARG CORE_SOUNDS
ARG CORE_SOUNDS_SHA256

RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends \
      build-essential pkg-config bzip2 patch curl ca-certificates gnupg \
      libedit-dev libjansson-dev libsqlite3-dev uuid-dev libxml2-dev \
      libssl-dev libsrtp2-dev unixodbc-dev libnewt-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /usr/src
ADD --checksum=sha256:${ASTERISK_SHA256} \
    https://downloads.asterisk.org/pub/telephony/asterisk/releases/asterisk-${ASTERISK_VERSION}.tar.gz \
    asterisk.tar.gz
COPY deploy/docker/asterisk/asterisk-pubkey.asc deploy/docker/asterisk/asterisk-${ASTERISK_VERSION}.tar.gz.asc ./

# Verify the pinned key's fingerprint, then the tarball's signature. The
# checksum above already pins the exact bytes we build; this proves that
# checksum was captured from a release Sangoma actually signed.
RUN gpg --with-colons --import-options show-only --import asterisk-pubkey.asc \
      | grep -q "^fpr:::::::::${ASTERISK_GPG_FINGERPRINT}:" \
      || (echo "asterisk-pubkey.asc does not match the pinned fingerprint" >&2 && exit 1) \
    && export GNUPGHOME="$(mktemp -d)" \
    && gpg --batch --import asterisk-pubkey.asc \
    && gpg --batch --verify "asterisk-${ASTERISK_VERSION}.tar.gz.asc" asterisk.tar.gz \
    && rm -rf "$GNUPGHOME"

RUN tar xzf asterisk.tar.gz && mv "asterisk-${ASTERISK_VERSION}" asterisk
ADD --checksum=sha256:${CORE_SOUNDS_SHA256} \
    https://downloads.asterisk.org/pub/telephony/sounds/releases/${CORE_SOUNDS} \
    asterisk/sounds/${CORE_SOUNDS}
WORKDIR /usr/src/asterisk

# --with-pjproject-bundled: Asterisk has no PJSIP support without it, and
# Debian doesn't package a compatible pjproject. The bundled source's own
# checksum (third-party/pjproject/pjproject-2.17.tar.bz2.md5) is baked into
# this signed release tarball, so it's covered by the GPG verification above.
RUN ./configure --with-pjproject-bundled --with-jansson-bundled=no

# menuselect.makeopts needs `make` run twice the first time: the first run
# regenerates the top-level Makefile from ./configure's output and asks to
# be restarted (a known Asterisk build-system quirk, not an error).
RUN make menuselect.makeopts || make menuselect.makeopts

# Only what Linx uses (docs/PBX.md §2): PJSIP + SRTP + RTP, ODBC realtime,
# ARI/Stasis, dialplan basics, app_echo, func_odbc, ulaw/alaw/g722/Opus
# (Opus is passthrough only: res_format_attr_opus negotiates it, nothing
# transcodes it — Asterisk ships no bundled Opus transcoder). No chan_sip
# (gone in 22 anyway), no AGI, no AMI-over-network, no telephony cards, no
# add-ons, no unit tests, no voicemail/queues/conferencing/fax/presence yet
# (later Phase 1B/1C slices) — trimmed at the category level, not by
# hand-picking every res_pjsip_* submodule menuselect enables together.
RUN menuselect/menuselect \
      --disable-category MENUSELECT_APPS --disable-category MENUSELECT_RES \
      --disable-category MENUSELECT_CHANNELS --disable-category MENUSELECT_CODECS \
      --disable-category MENUSELECT_FUNCS --disable-category MENUSELECT_PBX \
      --disable-category MENUSELECT_BRIDGES --disable-category MENUSELECT_FORMATS \
      --disable-category MENUSELECT_CDR --disable-category MENUSELECT_CEL \
      --disable-category MENUSELECT_TESTS --disable-category MENUSELECT_AGIS \
      --disable-category MENUSELECT_MOH --disable MOH-OPSOUND-WAV \
      --enable app_dial --enable app_echo --enable app_playback --enable app_verbose --enable app_stack \
      --enable res_rtp_asterisk \
      --enable res_ari --enable res_ari_applications --enable res_ari_asterisk \
      --enable res_ari_bridges --enable res_ari_channels --enable res_ari_device_states \
      --enable res_ari_endpoints --enable res_ari_events --enable res_ari_model \
      --enable res_ari_playbacks --enable res_ari_recordings --enable res_ari_sounds \
      --enable res_config_odbc --enable res_config_sqlite3 \
      --enable res_format_attr_opus \
      --enable res_odbc --enable res_odbc_transaction \
      --enable res_pjproject \
      --enable res_pjsip --enable res_pjsip_acl --enable res_pjsip_authenticator_digest \
      --enable res_pjsip_caller_id --enable res_pjsip_dtmf_info \
      --enable res_pjsip_endpoint_identifier_ip --enable res_pjsip_endpoint_identifier_user \
      --enable res_pjsip_exten_state --enable res_pjsip_logger --enable res_pjsip_nat \
      --enable res_pjsip_registrar --enable res_pjsip_sdp_rtp --enable res_pjsip_session \
      --enable res_realtime --enable res_security_log \
      --enable res_sorcery_astdb --enable res_sorcery_config --enable res_sorcery_memory \
      --enable res_sorcery_memory_cache --enable res_sorcery_realtime \
      --enable res_srtp \
      --enable res_stasis --enable res_stasis_answer --enable res_stasis_device_state \
      --enable res_stasis_playback --enable res_stasis_recording --enable res_stasis_snoop \
      --enable res_timing_timerfd --enable res_websocket_client \
      --enable ENABLE_SRTP_AES_192 --enable ENABLE_SRTP_AES_256 --enable ENABLE_SRTP_AES_GCM \
      --enable chan_pjsip \
      --enable codec_ulaw --enable codec_alaw --enable codec_g722 --enable codec_gsm --enable codec_resample \
      --enable func_odbc --enable func_channel --enable func_callerid \
      --enable pbx_config \
      --enable bridge_simple --enable bridge_native_rtp \
      --enable format_gsm --enable format_sln \
      menuselect.makeopts \
    && menuselect/menuselect --check-deps menuselect.makeopts

# third-party (pjproject) has real missing-dependency races under -j
# (pjsip/pjproject#2626: libpjnath and others link before their own build
# finishes) — build it serially first, then the rest of Asterisk in
# parallel, which doesn't share that problem.
RUN make -C third-party
RUN NOISY_BUILD=yes make -j"$(nproc)" && make install

# --- Go entrypoint (config renderer + exec into asterisk) ---
# golang:1.27.1-bookworm
FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS go-build
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
      -o /out/asterisk-entrypoint ./services/asterisk-entrypoint

# --- Runtime ---
FROM debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251
LABEL org.opencontainers.image.source="https://github.com/linxpbx/linx" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="linx-asterisk"
# Asterisk itself stays GPLv2 in this image (ADR-031); Linx's own code
# (the entrypoint) is Apache-2.0 like the rest of the repo.

RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends \
      libxml2 libsqlite3-0 libssl3 libjansson4 libedit2 libodbc2 libsrtp2-1 odbc-postgresql \
    && rm -rf /var/lib/apt/lists/* \
    # The Debian package installs psqlodbcw.so under an architecture triplet
    # directory (/usr/lib/<triplet>/odbc/); symlink it to one fixed path so
    # internal/asteriskconf's rendered odbcinst.ini doesn't need to know the
    # image's architecture.
    && ln -s "$(find /usr/lib -name psqlodbcw.so)" /usr/lib/psqlodbcw.so \
    && addgroup --system --gid 101 asterisk \
    && adduser --system --uid 100 --gid 101 --home /var/lib/asterisk --no-create-home asterisk

COPY --from=asterisk-build /usr/sbin/asterisk /usr/sbin/asterisk
COPY --from=asterisk-build /usr/lib/asterisk /usr/lib/asterisk
COPY --from=asterisk-build /usr/lib/libasterisk*.so* /usr/lib/
# Asterisk's data (sound prompts, ARI's REST model, docs) lives read-only in
# the image, not in the asterisk-state volume mounted over /var/lib/asterisk:
# a volume keeps its first copy forever, so an image update would never
# reach it. internal/asteriskconf points astdatadir here.
# Asterisk needs its XML docs at startup: without them it refuses to
# register config options ("Stasis initialization failed").
COPY --from=asterisk-build /var/lib/asterisk /usr/share/asterisk
RUN install -d -o asterisk -g asterisk -m 0750 /var/lib/asterisk
COPY --from=go-build /out/asterisk-entrypoint /usr/local/bin/asterisk-entrypoint

RUN ldconfig

USER asterisk:asterisk
ENTRYPOINT ["/usr/local/bin/asterisk-entrypoint"]
