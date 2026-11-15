#!/usr/bin/env bash
# Accept an NFI update that the updater's fuse held because it changes risk settings.
# The updater then deploys it on its next cycle (still subject to the restart cap and the
# volatility check). Approval is tied to that exact change: if upstream moves the same
# settings again before it deploys, the update is held again and needs a fresh approval.
set -euo pipefail
cd "$(dirname "$0")/.."
STATE_DIR="${STATE_DIR:-services/updater/state}"
P="$STATE_DIR/pending_risk"

if [ ! -s "$P" ]; then
  echo "nothing pending: the updater is not holding any update (no $P)."
  exit 1
fi
cat "$P"
echo
DIGEST=$(head -1 "$P" | grep -oE 'digest [0-9a-f]+' | cut -d' ' -f2)
[ -n "$DIGEST" ] || { echo "cannot read the digest from $P; nothing changed."; exit 1; }

if [ "${1:-}" != "--yes" ]; then
  read -r -p "Deploy this change to the live bot? Lines starting '-' are what runs now. [y/N] " a
  case "$a" in y|Y|yes) ;; *) echo "not approved, nothing changed."; exit 1 ;; esac
fi
echo "$DIGEST" > "$STATE_DIR/approved_risk"
echo "approved $DIGEST. The updater deploys it on its next cycle (up to UPDATE_INTERVAL)."
