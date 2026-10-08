#!/usr/bin/env bash
# Screenshots of every app screen, light and dark, into ios/screenshots/.
# Compare them with the mockups in docs/ui/ after changing a screen.
#
#   ios/tools/screens.sh                 # the default device
#   LINX_IOS_DEVICE="iPad Pro 13-inch (M5)" ios/tools/screens.sh
#   make ios-screens-all                 # all three devices in one go
#
# A foldable has two screens, and `simctl io ... enumerate` lists them:
# LINX_IOS_DISPLAY picks one, and LINX_IOS_OUT says where the shots go so a
# second display doesn't write over the first. The Duo's outer screen is 1
# and its inner one is 3, and the inner one stays dark until the simulated
# phone is opened out — there is **no command for that** in `simctl` on the
# 27.1 beta (checked 2026-10-05: no fold verb, nothing in `simctl ui`, and
# no Simulator.app left to drive with AppleScript — Xcode 27 shows
# simulators in DeviceHub). So the inner-screen shots are taken by hand,
# from the device's own window, and `ios/LinxTests/BigScreenTests.swift`
# is what holds the unfolded layout to its rules meanwhile.
#
# The call screens are also shot with the phone on its side (the app asks the
# system to turn, `-LinxOrientation landscape`), because the layout a call
# uses is decided by the shape of the screen and not by the device
# (`CallLayout`): a phone upright, a phone on its side, and an iPad or an
# unfolded iPhone Duo each get a different one.
set -euo pipefail

cd "$(dirname "$0")/.."
device=${LINX_IOS_DEVICE:-iPhone 17}
out=${LINX_IOS_OUT:-screenshots/$(echo "$device" | tr ' ' '-' | tr -d '()')}
dd=${LINX_IOS_DERIVED_DATA:-build/screens}
bundle=com.linxpbx.app

# Every screen the app can be launched straight into. The app reads -LinxScreen
# in debug builds; more screens join this list as they are built.
screens=(setup-phone signed-in this-phone keypad calls team more voicemail settings
  in-call call-details incoming-call video-call set-up-again)
# The ones worth having on their side as well.
sideways=(video-call in-call)

udid=$(tools/sim.sh "$device")
display=${LINX_IOS_DISPLAY:+--display $LINX_IOS_DISPLAY}
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
  # Long enough for the screen to settle: a list with a search field slides
  # it in, and a shot taken during that catches it half-faded.
  sleep 3
  xcrun simctl io "$udid" screenshot --type=png $display "$out/$name-$appearance.png" >/dev/null 2>&1
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
