#!/bin/sh
# Linx one-line installer. Detects the CPU architecture, downloads the signed
# release of the `linx` program from GitHub, checks it, installs it, and starts
# the browser-based setup. Linux server only (the app is on the App Store).
#
#   curl -fsSL https://raw.githubusercontent.com/linxpbx/linx/master/install.sh | sudo sh
#
# Options (set as environment variables before the pipe, e.g.
#   curl -fsSL …/install.sh | sudo LINX_VERSION=1.0.0 sh ):
#   LINX_VERSION=1.0.0           install an exact version, not the latest stable
#   LINX_NO_SETUP=1              install the program only; don't run `linx setup`
#   LINX_BINDIR=/usr/local/bin   where the program goes
#
# Security: HTTPS only (TLS is never disabled), the download is checked against
# the release's SHA256SUMS, and — when cosign is installed — SHA256SUMS itself
# is verified against this repository's release workflow (keyless Sigstore).
set -eu

REPO="linxpbx/linx"
BINDIR="${LINX_BINDIR:-/usr/local/bin}"

say() { printf '  %s\n' "$*"; }
die() { printf 'linx install: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# --- root: installs to a system path and, in setup, installs Docker ---
[ "$(id -u)" = 0 ] || die "run it with sudo:  curl -fsSL https://raw.githubusercontent.com/$REPO/master/install.sh | sudo sh"

# --- Linux only ---
[ "$(uname -s)" = "Linux" ] || die "the Linx server runs on Linux; this machine is $(uname -s). (The iPhone/iPad app is on the App Store.)"

# --- CPU architecture ---
case "$(uname -m)" in
	x86_64 | amd64) ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	*) die "unsupported CPU architecture '$(uname -m)' — Linx ships amd64 and arm64" ;;
esac

# --- tools (a fresh server has curl or wget, and coreutils' sha256sum) ---
if have curl; then
	get() { curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"; }
	cat_url() { curl -fsSL --proto '=https' --tlsv1.2 "$1"; }
elif have wget; then
	get() { wget -q --https-only -O "$2" "$1"; }
	cat_url() { wget -q --https-only -O - "$1"; }
else
	die "need curl or wget"
fi
if have sha256sum; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif have shasum; then
	sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	die "need sha256sum (coreutils)"
fi

# --- which version ---
VERSION="${LINX_VERSION:-}"
if [ -z "$VERSION" ]; then
	say "Finding the latest Linx release…"
	VERSION=$(cat_url "https://api.github.com/repos/$REPO/releases/latest" |
		grep -m1 '"tag_name"' | sed -E 's/.*"tag_name"[^"]*"([^"]+)".*/\1/')
	[ -n "$VERSION" ] || die "couldn't find the latest release (is the network up?)"
fi
case "$VERSION" in v*) TAG="$VERSION" ;; *) TAG="v$VERSION" ;; esac
BASE="https://github.com/$REPO/releases/download/$TAG"
say "Installing Linx $TAG for linux/$ARCH"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM

get "$BASE/linx-linux-$ARCH" "$TMP/linx" || die "couldn't download $BASE/linx-linux-$ARCH"
get "$BASE/SHA256SUMS" "$TMP/SHA256SUMS" || die "couldn't download the checksums"

# --- check the download against the release's checksums ---
say "Checking the download…"
want=$(awk -v f="linx-linux-$ARCH" '$2 == f {print $1}' "$TMP/SHA256SUMS")
[ -n "$want" ] || die "no checksum for linx-linux-$ARCH in SHA256SUMS"
got=$(sha256 "$TMP/linx")
[ "$got" = "$want" ] || die "checksum mismatch — refusing to install (got $got, expected $want)"

# --- verify the signature when cosign is present (supply-chain proof) ---
if have cosign; then
	say "Verifying the release signature with cosign…"
	get "$BASE/SHA256SUMS.sigstore.json" "$TMP/SHA256SUMS.sigstore.json" || die "couldn't download the signature"
	cosign verify-blob "$TMP/SHA256SUMS" \
		--bundle "$TMP/SHA256SUMS.sigstore.json" \
		--certificate-identity-regexp "^https://github.com/$REPO/\.github/workflows/release\.yml@refs/tags/" \
		--certificate-oidc-issuer "https://token.actions.githubusercontent.com" >/dev/null 2>&1 ||
		die "cosign could not verify the release signature — refusing to install"
	say "Signature OK — built by $REPO's release workflow."
else
	say "(cosign not installed: verified by SHA-256 over HTTPS. Install cosign for a signature check too.)"
fi

# --- install ---
install -m 0755 "$TMP/linx" "$BINDIR/linx"
say "Installed: $("$BINDIR/linx" version)"

# --- start setup (asks nothing in the terminal; prints one link for a browser) ---
if [ "${LINX_NO_SETUP:-}" = "1" ]; then
	say "Done. When you're ready, start the install with:  sudo linx setup"
	exit 0
fi
say "Starting the Linx installer — it will print one link to open in a browser."
if [ -e /dev/tty ]; then
	exec "$BINDIR/linx" setup </dev/tty
else
	exec "$BINDIR/linx" setup
fi
