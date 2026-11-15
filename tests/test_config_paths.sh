#!/usr/bin/env bash
# Every add_config_files path in user_data/config.json must resolve, and the
# strategy symlink must point at a real upstream file.
set -uo pipefail
cd "$(dirname "$0")/.."
PASS=0; FAIL=0
ok() { echo "  ok: $1"; PASS=$((PASS+1)); }
no() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

# config.json uses // comments (freqtrade parses it with rapidjson in comment mode),
# so strip them before feeding it to python's json.
INCLUDES=$(sed 's://.*::' user_data/config.json | python3 -c '
import json,sys
print("\n".join(json.load(sys.stdin)["add_config_files"]))')

[ -n "$INCLUDES" ] && ok "config.json parses and lists includes" || no "config.json unparsable or has no add_config_files"

while IFS= read -r inc; do
  [ -z "$inc" ] && continue
  # paths are relative to the directory holding config.json
  if [ -f "user_data/$inc" ]; then ok "include resolves: $inc"; else no "include MISSING: $inc"; fi
done <<< "$INCLUDES"

STRAT_VALUE=$(sed 's://.*::' user_data/config.json | python3 -c '
import json,sys; print(json.load(sys.stdin).get("strategy",""))')
[ "$STRAT_VALUE" = "NFIX7Risk" ] && ok "strategy=NFIX7Risk" || no "strategy is '$STRAT_VALUE', expected NFIX7Risk"

LINK=user_data/strategies/NostalgiaForInfinityX7.py
[ -L "$LINK" ] && ok "$LINK is a symlink" || no "$LINK is not a symlink"
[ -e "$LINK" ] && ok "symlink resolves on host" || no "symlink dangling"
[ "$(readlink "$LINK")" = "../../NostalgiaForInfinity/NostalgiaForInfinityX7.py" ] \
  && ok "symlink is relative to the project root" \
  || no "symlink target is '$(readlink "$LINK")', expected ../../NostalgiaForInfinity/NostalgiaForInfinityX7.py"

python3 -c "import ast,sys; ast.parse(open('user_data/strategies/NFIX7Risk.py').read())" 2>/dev/null \
  && ok "NFIX7Risk.py is syntactically valid" || no "NFIX7Risk.py has a syntax error"

grep -q 'def protections' user_data/strategies/NFIX7Risk.py \
  && ok "NFIX7Risk defines protections" || no "NFIX7Risk lost its protections property"

# leverage must stay at 2.0 on all three keys
for k in futures_mode_leverage futures_mode_leverage_rebuy_mode futures_mode_leverage_grind_mode; do
  V=$(sed 's://.*::' user_data/config.json | python3 -c "
import json,sys; print(json.load(sys.stdin)['nfi_parameters']['$k'])")
  [ "$V" = "2.0" ] && ok "$k=2.0" || no "$k is $V, expected 2.0"
done

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
