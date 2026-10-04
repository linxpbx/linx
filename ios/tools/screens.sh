#!/usr/bin/env bash
# Screenshots of every app screen, light and dark, into ios/screenshots/.
# Compare them with the mockups in docs/ui/ after changing a screen.
#
#   ios/tools/screens.sh                 # the default device
#   LINX_IOS_DEVICE="iPad Pro 11-inch (M5)" ios/tools/screens.sh
#   LINX_IOS_DEVICE="iPhone Duo" DEVELOPER_DIR=/Applications/Xcode-27.1-beta.app ios/tools/screens.sh
#
# The call screens are also shot with the phone on its side (the app asks the
# system to turn, `-LinxOrientation landscape`), because the layout a call
# uses is decided by the shape of the screen and not by the device
# (`CallLayout`): a phone upright, a phone on its side, and an iPad or an
# unfolded iPhone Duo each get a different one.
set -euo pipefail

cd "$(dirname "$0")/.."
device=${LINX_IOS_DEVICE:-iPhone 17}
out=screenshots/$(echo "$device" | tr ' ' '-' | tr -d '()')
dd=${LINX_IOS_DERIVED_DATA:-build/screens}
bundle=com.linxpbx.app

# Every screen the app can be launched straight into. The app reads -LinxScreen
# in debug builds; more screens join this list as they are built.
screens=(setup-phone signed-in this-phone keypad calls team more voicemail settings
  in-call incoming-call video-call set-up-again)
# The ones worth having on their side as well.
sideways=(video-call in-call)

udid=$(tools/sim.sh "$device")
mkdir -p "$out"

echo "screens: building…"
xcodebuild build -project Linx.xcodeproj -scheme Linx -configuration Debug \
  -destination "id=$udid" -derivedDataPath "$dd" \
  CODE_SIGNING_ALLOWED=NO >/dev/null

app=$(find "$dd/Build/Products" -name Linx.app -maxdepth 2 | head -1)
xcrun simctl install "$udid" "$app"

shoot() { # screen appearance orientation name
  local screen=$1 appearance=$2 orientation=$3 name=$4
  xcrun simctl ui "$udid" appearance "$appearance" >/dev/null
  xcrun simctl terminate "$udid" "$bundle" >/dev/null 2>&1 || true
  xcrun simctl launch "$udid" "$bundle" -LinxScreen "$screen" -LinxOrientation "$orientation" >/dev/null
  # Wait for the first frame: the app is a host process once it is drawing.
  for _ in $(seq 40); do
    pgrep -f "$(basename "$app")/Linx" >/dev/null && break
    sleep 0.25
  done
  sleep 1.5
  xcrun simctl io "$udid" screenshot --type=png "$out/$name-$appearance.png" >/dev/null 2>&1
  echo "screens: $out/$name-$appearance.png"
}

for screen in "${screens[@]}"; do
  for appearance in light dark; do
    shoot "$screen" "$appearance" portrait "$screen"
  done
done

for screen in "${sideways[@]}"; do
  shoot "$screen" light landscape "$screen-sideways"
done

xcrun simctl ui "$udid" appearance light >/dev/null
xcrun simctl terminate "$udid" "$bundle" >/dev/null 2>&1 || true
