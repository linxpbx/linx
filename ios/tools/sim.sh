#!/usr/bin/env bash
# Prints the UDID of a booted simulator of the wanted device type, creating and
# booting one if there isn't one. Usage: sim.sh "iPhone 17"
set -euo pipefail

want=${1:?usage: sim.sh "<device type name>"}

udid=$(xcrun simctl list devices available --json \
  | /usr/bin/python3 -c '
import json, sys
want = sys.argv[1]
data = json.load(sys.stdin)["devices"]
best = None
for runtime, devices in sorted(data.items()):
    for d in devices:
        if d["name"] == want or d["name"] == "Linx " + want:
            # A booted one wins; otherwise the newest runtime.
            if d["state"] == "Booted":
                print(d["udid"]); sys.exit(0)
            best = d["udid"]
print(best or "")
' "$want")

if [ -z "$udid" ]; then
  # The wanted device type, or — on a machine with an older Xcode, such as a CI
  # runner — the newest iPhone it does have.
  type=$(xcrun simctl list devicetypes --json \
    | /usr/bin/python3 -c '
import json, re, sys
want = sys.argv[1]
types = json.load(sys.stdin)["devicetypes"]
for t in types:
    if t["name"] == want:
        print(t["identifier"]); sys.exit(0)
iphones = [t for t in types if re.fullmatch(r"iPhone \d+", t["name"])]
if iphones:
    newest = max(iphones, key=lambda t: int(t["name"].split()[1]))
    print(newest["identifier"], file=sys.stdout)
    print("sim: no \"%s\"; using %s" % (want, newest["name"]), file=sys.stderr)
' "$want")
  [ -n "$type" ] || { echo "sim: no iPhone device type to fall back to" >&2; exit 1; }
  # The newest installed iOS runtime that can actually run that device.
  runtime=$(xcrun simctl list runtimes --json \
    | /usr/bin/python3 -c '
import json, sys
want = sys.argv[1]
rs = [r for r in json.load(sys.stdin)["runtimes"]
      if r.get("isAvailable") and r.get("platform") == "iOS"
      and any(d["identifier"] == want for d in r.get("supportedDeviceTypes", []))]
print(sorted(rs, key=lambda r: [int(p) for p in r["version"].split(".")])[-1]["identifier"] if rs else "")
' "$type")
  [ -n "$runtime" ] || { echo "sim: no iOS simulator runtime installed" >&2; exit 1; }
  udid=$(xcrun simctl create "Linx $want" "$type" "$runtime")
fi

state=$(xcrun simctl list devices --json | /usr/bin/python3 -c '
import json, sys
udid = sys.argv[1]
for devices in json.load(sys.stdin)["devices"].values():
    for d in devices:
        if d["udid"] == udid:
            print(d["state"])
' "$udid")
[ "$state" = "Booted" ] || xcrun simctl bootstatus "$udid" -b >/dev/null

echo "$udid"
