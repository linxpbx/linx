#!/usr/bin/env bash
# The phone bench's iOS half: two simulators, each running the app, set up
# from a setup link, one calling the other with nobody touching them
# (`Bench` in the app). internal/browsertest's TestPhoneBenchDocker runs the
# Linx it calls through and reads what the two phones reported afterwards.
#
#   bench.sh build                       # once: the app, signed to run locally
#   bench.sh prepare CA_PEM              # trust the test CA, fresh app on both
#   bench.sh call CALLER_LINK ANSWERER_LINK NUMBER SECONDS [flags…]
#
# flags: relay-only, no-udp (China: the relay over TLS on 443 only), audio,
# callkit (ring and answer through CallKit, as an iPhone outside China).
set -euo pipefail

cd "$(dirname "$0")/.."
dd=${LINX_IOS_DERIVED_DATA:-build/bench}
bundle=com.linxpbx.app
caller_device=${LINX_BENCH_CALLER:-iPhone 17}
answerer_device=${LINX_BENCH_ANSWERER:-iPad Pro 13-inch (M5)}

caller=$(tools/sim.sh "$caller_device")
answerer=$(tools/sim.sh "$answerer_device")

app() { find "$dd/Build/Products" -name Linx.app -maxdepth 2 | head -1; }

case ${1:?build, prepare or call} in
build)
  # Signed to run locally (no Apple account): the Keychain needs a signed
  # app even in a simulator.
  xcodebuild build -project Linx.xcodeproj -scheme Linx -configuration Debug \
    -destination "id=$caller" -derivedDataPath "$dd" \
    CODE_SIGN_IDENTITY=- CODE_SIGN_STYLE=Manual DEVELOPMENT_TEAM= PROVISIONING_PROFILE_SPECIFIER= \
    >"$dd.log" 2>&1 || { grep -E "error:" "$dd.log" | sort -u | head -20; exit 1; }
  echo "bench: built"
  ;;
prepare)
  ca=${2:?CA_PEM}
  for d in "$caller" "$answerer"; do
    xcrun simctl terminate "$d" "$bundle" >/dev/null 2>&1 || true
    xcrun simctl uninstall "$d" "$bundle" >/dev/null 2>&1 || true
    xcrun simctl keychain "$d" reset >/dev/null
    xcrun simctl keychain "$d" add-root-cert "$ca"
    xcrun simctl install "$d" "$(app)"
    xcrun simctl privacy "$d" grant microphone "$bundle" >/dev/null 2>&1 || true
  done
  echo "bench: prepared"
  ;;
call)
  caller_link=${2:?} answerer_link=${3:?} number=${4:?} seconds=${5:?}
  shift 5
  flags=()
  video=YES
  for f in "$@"; do
    case $f in
    relay-only) flags+=(-LinxBenchRelayOnly YES) ;;
    no-udp) flags+=(-LinxBenchNoUDP YES) ;;
    audio) video=NO ;;
    callkit) flags+=(-LinxBenchCallKit YES) ;;
    *) echo "bench: unknown flag $f" >&2; exit 2 ;;
    esac
  done
  for d in "$caller" "$answerer"; do xcrun simctl terminate "$d" "$bundle" >/dev/null 2>&1 || true; done
  xcrun simctl launch "$answerer" "$bundle" -LinxBenchSetup "$answerer_link" -LinxBenchAnswer YES \
    ${flags[@]+"${flags[@]}"} >/dev/null
  # The answerer's line first, so the call finds it.
  sleep 8
  xcrun simctl launch "$caller" "$bundle" -LinxBenchSetup "$caller_link" -LinxBenchCall "$number" \
    -LinxBenchVideo "$video" -LinxBenchHangUpAfter "$seconds" ${flags[@]+"${flags[@]}"} >/dev/null
  echo "bench: calling"
  ;;
esac
