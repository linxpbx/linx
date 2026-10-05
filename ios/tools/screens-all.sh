#!/usr/bin/env bash
# Every screen on all three of the devices Phase 2 is held to (owner,
# 2026-10-04: iPhone 18 Pro Max and iPad Pro 13-inch (M5)), plus the
# iPhone Duo folded shut.
#
# The Duo needs the Xcode 27.1 beta — it is the only one with that
# simulator and the fold APIs (docs/PHASE2.md §8). Its **inner** screen is
# dark until the phone is opened out, and nothing on the command line can
# open it, so that one shot is taken by hand from the device's window.
set -euo pipefail

cd "$(dirname "$0")/.."
beta=${LINX_XCODE_271:-/Applications/Xcode-27.1-beta.app}

LINX_IOS_DEVICE="iPhone 18 Pro Max" tools/screens.sh
LINX_IOS_DEVICE="iPad Pro 13-inch (M5)" tools/screens.sh

if [ -d "$beta" ]; then
  DEVELOPER_DIR="$beta" LINX_IOS_DEVICE="iPhone Duo" LINX_IOS_DISPLAY=1 \
    LINX_IOS_OUT=screenshots/iPhone-Duo tools/screens.sh
  echo "screens: the Duo's inner screen is taken by hand — open the phone out in its window,"
  echo "         then: xcrun simctl io booted screenshot --display 3 screenshots/iPhone-Duo-inner/<screen>.png"
else
  echo "screens: no Xcode 27.1 beta at $beta, so the iPhone Duo was skipped."
fi
