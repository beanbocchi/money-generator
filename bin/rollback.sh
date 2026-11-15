#!/usr/bin/env bash
# Pin upstream back one update, for when a new version imports fine but trades badly
# -- the case the updater's guard cannot detect.
set -euo pipefail
cd "$(dirname "$0")/.."

NFI_DIR="${NFI_DIR:-NostalgiaForInfinity}"
STATE_DIR="${STATE_DIR:-services/updater/state}"
DOCKER="${DOCKER:-docker}"
TARGET_CONTAINER="${TARGET_CONTAINER:-bottrade}"
UPDATER_CONTAINER="${UPDATER_CONTAINER:-bottrade-updater}"

if [ ! -f "$STATE_DIR/previous_sha" ]; then
  echo "no $STATE_DIR/previous_sha - the updater has not applied an update yet."
  echo "To pin a specific commit by hand:"
  echo "  $DOCKER stop $UPDATER_CONTAINER"
  echo "  git -C $NFI_DIR reset --hard <sha>   # pick one from git -C $NFI_DIR log"
  echo "  $DOCKER restart $TARGET_CONTAINER"
  exit 1
fi
SHA=$(cat "$STATE_DIR/previous_sha")

# Validate before touching anything. Without this, a truncated or garbage previous_sha
# stops the updater, then dies on the reset -- leaving auto-updates off and the bot
# still running the version you were trying to get away from, which is the worst of
# both outcomes.
if ! git -C "$NFI_DIR" cat-file -e "${SHA}^{commit}" 2>/dev/null; then
  echo "$STATE_DIR/previous_sha does not name a commit in $NFI_DIR: '$SHA'"
  echo "Nothing was changed. Pick a commit by hand from: git -C $NFI_DIR log --oneline"
  exit 1
fi

# Order matters: stop the updater FIRST. Otherwise its next cycle pulls forward again
# and silently undoes the rollback.
echo "==> stopping $UPDATER_CONTAINER so it cannot pull forward"
"$DOCKER" stop "$UPDATER_CONTAINER" || true

echo "==> pinning $NFI_DIR to $SHA"
git -C "$NFI_DIR" reset --hard -q "$SHA"

echo "==> restarting $TARGET_CONTAINER"
"$DOCKER" restart "$TARGET_CONTAINER"

echo
echo "Bot now runs ${SHA:0:8}. The updater is STOPPED, so this sticks."
echo "Re-enable auto-updates with: $DOCKER start $UPDATER_CONTAINER"
echo "(it will pull to latest again on its next cycle)"
