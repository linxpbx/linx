#!/usr/bin/env bash
# Puts Google's WebRTC where the app can build against it (ADR-006): one
# prebuilt XCFramework, pinned by version and checked against the SHA-256
# below before anything is unpacked. It is BSD-licensed, which is why it may
# ship inside the app at all (CLAUDE.md "Only permissive licences").
#
#   make ios-deps          # fetches it if it isn't there, or if it changed
#   LINX_WEBRTC_ZIP=/path  # use a copy already on this machine
#
# The framework is not in git (about 100 MB unpacked): ios/Vendor is ignored,
# and this script is what fills it on a new machine and in CI.
set -euo pipefail

version=154.0.0
sha256=a2bcdda93578c82452ceb6e49d54a2746e1bcb4caf7c2fa601ffac8028b58c16
url="https://github.com/stasel/WebRTC/releases/download/$version/WebRTC-M154.xcframework.zip"

cd "$(dirname "$0")/.."
vendor=Vendor
stamp="$vendor/WebRTC.xcframework/.linx-version"

if [ -f "$stamp" ] && [ "$(cat "$stamp")" = "$version $sha256" ]; then
  echo "webrtc: $version already here"
  exit 0
fi

zip=${LINX_WEBRTC_ZIP:-build/webrtc/WebRTC-$version.zip}
if [ ! -f "$zip" ]; then
  mkdir -p "$(dirname "$zip")"
  echo "webrtc: downloading $version (45 MB)…"
  curl -fsSL --retry 3 -o "$zip.part" "$url"
  mv "$zip.part" "$zip"
fi

got=$(shasum -a 256 "$zip" | cut -d' ' -f1)
if [ "$got" != "$sha256" ]; then
  echo "webrtc: that download isn't the pinned one. Expected $sha256, got $got." >&2
  echo "webrtc: nothing was unpacked; delete $zip and try again." >&2
  exit 1
fi

rm -rf "$vendor/WebRTC.xcframework"
mkdir -p "$vendor"
unzip -q "$zip" -d "$vendor"
[ -d "$vendor/WebRTC.xcframework" ] || { echo "webrtc: no WebRTC.xcframework in the zip" >&2; exit 1; }
echo "$version $sha256" > "$stamp"
echo "webrtc: $version ready in ios/$vendor (BSD licence: ios/$vendor/WebRTC.xcframework/LICENSE)"
