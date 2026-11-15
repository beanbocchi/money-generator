#!/usr/bin/env bash
# bin/rollback.sh must stop the updater BEFORE pinning, or the updater pulls forward again.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/.." && pwd)
PASS=0; FAIL=0
ok() { echo "  ok: $1"; PASS=$((PASS+1)); }
no() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

setup() {
  local t; t=$(mktemp -d)
  mkdir -p "$t/bin"; cp "$ROOT/bin/rollback.sh" "$t/bin/"
  git init -q -b main "$t/nfi"
  git -C "$t/nfi" config user.email t@example.com
  git -C "$t/nfi" config user.name test
  git -C "$t/nfi" config commit.gpgsign false
  echo v1 > "$t/nfi/f"; git -C "$t/nfi" add -A; git -C "$t/nfi" commit -qm v1
  echo v2 > "$t/nfi/f"; git -C "$t/nfi" add -A; git -C "$t/nfi" commit -qm v2
  mkdir -p "$t/state" "$t/bin"
  cat > "$t/bin/docker" <<STUB
#!/usr/bin/env bash
echo "\$@" >> "$t/docker.log"
exit 0
STUB
  chmod +x "$t/bin/docker"; : > "$t/docker.log"
  echo "$t"
}
run_rb() { local t=$1; (cd "$t" && env NFI_DIR="$t/nfi" STATE_DIR="$t/state" \
  DOCKER="$t/bin/docker" TARGET_CONTAINER=bottrade UPDATER_CONTAINER=bottrade-updater \
  bash bin/rollback.sh 2>&1); }

echo "case 1: no previous_sha -> refuse"
T=$(setup)
run_rb "$T" >/dev/null; RC=$?
[ "$RC" -ne 0 ] && ok "refused without previous_sha" || no "proceeded with nothing to roll back to"
[ -s "$T/docker.log" ] && no "touched docker anyway" || ok "did not touch docker"
rm -rf "$T"

echo "case 2: rolls back, stopping the updater first"
T=$(setup)
FIRST=$(git -C "$T/nfi" rev-list --max-parents=0 HEAD)
echo "$FIRST" > "$T/state/previous_sha"
run_rb "$T" >/dev/null; RC=$?
[ "$RC" -eq 0 ] && ok "exited 0" || no "exited $RC"
[ "$(git -C "$T/nfi" rev-parse HEAD)" = "$FIRST" ] && ok "pinned to previous_sha" || no "did not pin"
[ "$(cat "$T/nfi/f")" = "v1" ] && ok "working tree is the old content" || no "working tree not rewound"
grep -q "stop bottrade-updater" "$T/docker.log" && ok "stopped the updater" || no "left the updater running"
grep -q "restart bottrade" "$T/docker.log" && ok "restarted the bot" || no "did not restart the bot"
[ "$(grep -n 'stop bottrade-updater' "$T/docker.log" | cut -d: -f1)" -lt \
  "$(grep -n 'restart bottrade' "$T/docker.log" | cut -d: -f1)" ] \
  && ok "stopped the updater before restarting the bot" \
  || no "wrong order: the updater could pull forward mid-rollback"
rm -rf "$T"

echo "case 3: garbage previous_sha -> refuse, change nothing"
T=$(setup)
HEAD_BEFORE=$(git -C "$T/nfi" rev-parse HEAD)
echo "not-a-sha" > "$T/state/previous_sha"
run_rb "$T" >/dev/null; RC=$?
[ "$RC" -ne 0 ] && ok "refused an invalid previous_sha" || no "accepted an invalid previous_sha"
[ -s "$T/docker.log" ] && no "stopped the updater before validating" || ok "left docker alone"
[ "$(git -C "$T/nfi" rev-parse HEAD)" = "$HEAD_BEFORE" ] && ok "clone untouched" || no "moved the clone"
rm -rf "$T"

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
