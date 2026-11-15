#!/usr/bin/env bash
# bin/setup.sh against stubbed git and docker compose, in a throwaway project dir.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/.." && pwd)
PASS=0; FAIL=0
ok() { echo "  ok: $1"; PASS=$((PASS+1)); }
no() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

setup() {                        # throwaway project containing only what setup.sh needs
  local t; t=$(mktemp -d)
  mkdir -p "$t/bin"; cp "$ROOT/bin/setup.sh" "$t/bin/"; cp "$ROOT/.env.example" "$t/"
  mkdir -p "$t/bin"
  cat > "$t/bin/git" <<STUB
#!/usr/bin/env bash
echo "\$@" >> "$t/git.log"
# emulate a clone by creating the directory it would produce
if [ "\$1" = "clone" ]; then mkdir -p "\${@: -1}/.git"; fi
exit 0
STUB
  cat > "$t/bin/compose" <<STUB
#!/usr/bin/env bash
echo "\$@" >> "$t/compose.log"
exit 0
STUB
  chmod +x "$t/bin/git" "$t/bin/compose"
  : > "$t/git.log"; : > "$t/compose.log"
  echo "$t"
}
run_setup() { local t=$1; (cd "$t" && env GIT="$t/bin/git" COMPOSE="$t/bin/compose" bash bin/setup.sh 2>&1); }

echo "case 1: no .env -> create it, explain, and refuse to start"
T=$(setup)
OUT=$(run_setup "$T"); RC=$?
[ "$RC" -ne 0 ] && ok "exited non-zero" || no "exited 0 with an unconfigured .env"
[ -f "$T/.env" ] && ok "created .env" || no "did not create .env"
grep -q clone "$T/git.log" && ok "cloned upstream" || no "did not clone"
[ -s "$T/compose.log" ] && no "started the stack without configuration" || ok "did not start the stack"
echo "$OUT" | grep -q "FREQTRADE__EXCHANGE__KEY" && ok "named the empty variables" || no "did not say what to fill in"
[ -d "$T/user_data/logs" ] && ok "created user_data/logs" || no "missing user_data/logs"
[ -d "$T/services/updater/state" ] && ok "created services/updater/state" || no "missing services/updater/state"
REAL_UID=$(id -u)
grep -q "^HOST_UID=${REAL_UID}$" "$T/.env" && ok "HOST_UID set to real id -u" || no "HOST_UID not set to $REAL_UID"
grep -q "^DOCKER_GID=.\+$" "$T/.env" && ok "DOCKER_GID is non-empty" || no "DOCKER_GID is empty or missing"
rm -rf "$T"

echo "case 2: configured .env -> start the stack"
T=$(setup)
sed 's/^\([A-Z_]*\)=$/\1=filled/' "$T/.env.example" > "$T/.env"
OUT=$(run_setup "$T"); RC=$?
[ "$RC" -eq 0 ] && ok "exited 0" || no "exited $RC with a configured .env: $OUT"
grep -q "up -d" "$T/compose.log" && ok "brought the stack up" || no "did not bring the stack up"
rm -rf "$T"

echo "case 3: clone already present -> do not re-clone"
T=$(setup)
mkdir -p "$T/NostalgiaForInfinity/.git"
sed 's/^\([A-Z_]*\)=$/\1=filled/' "$T/.env.example" > "$T/.env"
run_setup "$T" >/dev/null
grep -q clone "$T/git.log" && no "re-cloned over an existing checkout" || ok "skipped the clone"
rm -rf "$T"

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
