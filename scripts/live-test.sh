#!/usr/bin/env bash
# READ-ONLY smoke test against a real printer. It never uploads, starts, pauses, stops or changes anything.
#
#   BAMBU_PRINTER=<name> MODEL=path/to/part.stl SLOT=1 ./scripts/live-test.sh
#
# Requires a configured printer (bambu printer add + bambu auth set) and Bambu Studio. Optional: ffmpeg for the snapshot.
set -euo pipefail

BIN=${BIN:-./bin/bambu}
MODEL=${MODEL:?set MODEL to an .stl or .3mf to slice}
SLOT=${SLOT:-1}
OUT=$(mktemp -d)
trap 'rm -rf "$OUT"' EXIT
pass=0; fail=0
check() { # name, expected exit codes (space separated), command...
  local name=$1 want=$2; shift 2
  set +e; "$@" >"$OUT/last.out" 2>"$OUT/last.err"; local code=$?; set -e
  if [[ " $want " == *" $code "* ]]; then echo "ok   $name (exit $code)"; pass=$((pass+1))
  else echo "FAIL $name (exit $code, want $want)"; sed 's/^/     /' "$OUT/last.err" | head -5; fail=$((fail+1)); fi
}

check "version"            "0"   "$BIN" version --json
check "auth status --check" "0"  "$BIN" auth status --check --json
check "status"             "0"   "$BIN" status --json
check "slicer info"        "0"   "$BIN" slicer info --json
for r in prototype-pla solid-pla functional-pla functional-petg; do
  check "slice $r"         "0"   "$BIN" slice "$MODEL" --recipe "$r" --out "$OUT" --name "live-$r" --json
done
check "preflight"          "0 9" "$BIN" preflight "$OUT/live-prototype-pla.gcode.3mf" --slot "$SLOT" --json
check "print send --dry-run" "0 9" "$BIN" print send "$OUT/live-prototype-pla.gcode.3mf" --slot "$SLOT" --dry-run --json
check "send without --confirm is refused" "9" "$BIN" print send "$OUT/live-prototype-pla.gcode.3mf" --slot "$SLOT"
check "pause without --confirm is refused" "9" "$BIN" print pause
if command -v ffmpeg >/dev/null; then
  check "camera snapshot"  "0 2" "$BIN" camera snapshot -o "$OUT/snap.jpg" --json
fi
check "monitor (10s, read-only)" "0 14" "$BIN" monitor --timeout 10s --start-grace 5s --json

echo "passed $pass, failed $fail"
[[ $fail -eq 0 ]]
