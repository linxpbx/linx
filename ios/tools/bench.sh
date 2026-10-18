#!/usr/bin/env bash
# The phone bench's iOS half: two simulators, each running the app, set up
# from a setup link, one calling the other with nobody touching them
# (`Bench` in the app). internal/browsertest's TestPhoneBenchDocker runs the
# Linx it calls through and reads what the two phones reported afterwards.
#
#   bench.sh build                       # once: the app, signed to run locally
#   bench.sh prepare CA_PEM              # trust the test CA, fresh app on both
#   bench.sh call CALLER_LINK ANSWERER_LINK NUMBER SECONDS [flags…]
#   bench.sh shot DIR NAME               # both screens, mid-call, into DIR
#   bench.sh away SECONDS                # both phones to another app, then back
#
# flags: relay-only, mobile-data (the picture through the relay alone), no-udp (China: the relay over TLS on 443 only), audio,
# callkit (ring and answer through CallKit, as an iPhone outside China),
# hd (both phones press HD five seconds in), ring=N (the answerer rings N
# seconds before answering; one by default).
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
shot)
  dir=${2:?DIR}
  mkdir -p "$dir"
  xcrun simctl io "$caller" screenshot --type=png "$dir/caller-${3:?NAME}.png" >/dev/null 2>&1
  xcrun simctl io "$answerer" screenshot --type=png "$dir/answerer-$3.png" >/dev/null 2>&1
  ;;
away)
  away=${2:?SECONDS}
  # Another app in front of the call on both phones (Settings), as a person
  # switching apps does, then the call brought back: the call goes on in
  # its small window meanwhile (CallPictureInPicture).
  for d in "$caller" "$answerer"; do xcrun simctl launch "$d" com.apple.Preferences >/dev/null; done
  sleep 4
  if [ -n "${LINX_BENCH_SHOTS:-}" ]; then tools/bench.sh shot "$LINX_BENCH_SHOTS" away; fi
  sleep $((away - 4))
  for d in "$caller" "$answerer"; do xcrun simctl launch "$d" "$bundle" >/dev/null; done
  if [ -n "${LINX_BENCH_SHOTS:-}" ]; then sleep 3; tools/bench.sh shot "$LINX_BENCH_SHOTS" back; fi
  echo "bench: back"
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
  late=
  for f in "$@"; do
    case $f in
    relay-only) flags+=(-LinxBenchRelayOnly YES) ;;
    no-udp) flags+=(-LinxBenchNoUDP YES) ;;
    audio) video=NO ;;
    callkit) flags+=(-LinxBenchCallKit YES) ;;
    hd) flags+=(-LinxBenchHD YES) ;;
    ring=*) flags+=(-LinxBenchRingFor "${f#ring=}") ;;
    late-answerer) late=1 ;;
    no-early) flags+=(-LinxBenchNoEarly YES) ;;
    no-direct-sound) flags+=(-LinxBenchNoDirectSound YES) ;;
    no-dtx) flags+=(-LinxBenchNoDTX YES) ;;
    mobile-data) flags+=(-LinxBenchMobileData YES) ;;
    *) echo "bench: unknown flag $f" >&2; exit 2 ;;
    esac
  done
  for d in "$caller" "$answerer"; do xcrun simctl terminate "$d" "$bundle" >/dev/null 2>&1 || true; done
  answerer_up() {
    xcrun simctl launch "$answerer" "$bundle" -LinxBenchSetup "$answerer_link" -LinxBenchAnswer YES \
      ${flags[@]+"${flags[@]}"} >/dev/null
  }
  if [ -n "$late" ]; then
    # The phone being rung starts after the caller, its line only just open
    # when the call comes: a phone woken for the call. The caller waits for it.
    xcrun simctl launch "$caller" "$bundle" -LinxBenchSetup "$caller_link" -LinxBenchCall "$number" \
      -LinxBenchVideo "$video" -LinxBenchHangUpAfter "$seconds" -LinxBenchCallAfter 12 \
      ${flags[@]+"${flags[@]}"} >/dev/null
    sleep 8
    answerer_up
  else
    answerer_up
    # The answerer's line first, so the call finds it.
    sleep 8
    xcrun simctl launch "$caller" "$bundle" -LinxBenchSetup "$caller_link" -LinxBenchCall "$number" \
      -LinxBenchVideo "$video" -LinxBenchHangUpAfter "$seconds" ${flags[@]+"${flags[@]}"} >/dev/null
  fi
  echo "bench: calling"
  ;;
esac
