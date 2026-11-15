#!/usr/bin/env bash
# Hermetic tests for services/updater/update.sh: temp git repos + a stubbed docker binary.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT="$HERE/../services/updater/update.sh"
PASS=0; FAIL=0
ok() { echo "  ok: $1"; PASS=$((PASS+1)); }
no() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

setup() {                       # echoes a fresh tmpdir with upstream/ + nfi/ + state/ + bin/
  local t; t=$(mktemp -d)
  git init -q -b main "$t/upstream"
  git -C "$t/upstream" config user.email t@example.com
  git -C "$t/upstream" config user.name  test
  git -C "$t/upstream" config commit.gpgsign false
  mkdir -p "$t/upstream/configs"
  echo "class X: pass"  > "$t/upstream/NostalgiaForInfinityX7.py"
  echo '{"a":1}'        > "$t/upstream/configs/blacklist-binance.json"
  echo "docs"           > "$t/upstream/README.md"
  git -C "$t/upstream" add -A
  git -C "$t/upstream" commit -qm init
  git clone -q "$t/upstream" "$t/nfi"
  mkdir -p "$t/state" "$t/bin"
  # 5m BTC klines: [openTime, open, high, low, close, ...]. calm ranges 0.4%, wild 5%.
  echo '[[1,"100","100.2","99.8","100",0],[2,"100","100.1","99.9","100",0],[3,"100","100.2","99.9","100",0]]' > "$t/calm.json"
  echo '[[1,"100","100","99","100",0],[2,"100","103","99","98",0],[3,"98","99","97","100",0]]' > "$t/wild.json"
  echo "$t"
}

# A minimal strategy shaped like upstream's: class-level settings, an __init__ holding
# NFI_SAFE_PARAMETERS, and a second class whose attributes must be ignored.
strategy() {   # $1=stops_enable  $2=startup_candle_count  $3=extra safe param line (may be empty)
  cat <<PYEOF
class NostalgiaForInfinityX7(IStrategy):
  stoploss = -0.99
  system_v3_2_stops_enable = $1
  startup_candle_count = $2

  def __init__(self, config):
    NFI_SAFE_PARAMETERS = [
      "futures_mode_leverage",
$3
    ]

class Cache:
  stoploss = 1
PYEOF
}

stub_docker() {   # $1=tmpdir  $2=exit for the import guard  $3=exit for the config guard
  cat > "$1/bin/docker" <<STUB
#!/usr/bin/env bash
echo "\$@" >> "$1/docker.log"
if [ "\$1" = "exec" ]; then
  # the config guard is the exec that invokes freqtrade show-config
  case "\$*" in *show-config*) exit ${3:-0} ;; *) exit $2 ;; esac
fi
exit 0
STUB
  chmod +x "$1/bin/docker"
  : > "$1/docker.log"
}

upstream_commit() {             # $1=tmpdir  $2=file  $3=content
  echo "$3" > "$1/upstream/$2"
  git -C "$1/upstream" add -A
  git -C "$1/upstream" commit -qm "change $2"
}

run_once() {                    # $1=tmpdir, rest=extra env assignments
  local t=$1; shift
  env NFI_DIR="$t/nfi" STATE_DIR="$t/state" DOCKER="$t/bin/docker" \
      TARGET_CONTAINER=bottrade STRATEGY_MODULE=NFIX7Risk \
      WATCH_PATHS="NostalgiaForInfinityX7.py configs/" \
      RUN_ONCE=1 UPDATE_INTERVAL=1 FREQTRADE__TELEGRAM__TOKEN="" \
      VOL_URL="file://$t/calm.json" \
      "$@" bash "$SCRIPT" 2>&1
}
head_of() { git -C "$1/nfi" rev-parse HEAD; }

echo "case 1: no upstream change -> no restart"
T=$(setup); stub_docker "$T" 0
run_once "$T" >/dev/null
grep -q restart "$T/docker.log" && no "restarted with nothing to do" || ok "no restart"
rm -rf "$T"

echo "case 2: watched file changed -> guard, restart, previous_sha"
T=$(setup); stub_docker "$T" 0
OLD=$(head_of "$T")
upstream_commit "$T" NostalgiaForInfinityX7.py "class X: pass  # v2"
run_once "$T" >/dev/null
grep -q "restart bottrade" "$T/docker.log" && ok "restarted the bot" || no "did not restart"
grep -q "^exec bottrade" "$T/docker.log" && ok "ran the import guard" || no "skipped the guard"
[ "$(cat "$T/state/previous_sha" 2>/dev/null)" = "$OLD" ] \
  && ok "previous_sha records the pre-update commit" \
  || no "previous_sha is '$(cat "$T/state/previous_sha" 2>/dev/null)', expected $OLD"
[ "$(head_of "$T")" != "$OLD" ] && ok "working tree advanced" || no "working tree did not advance"
rm -rf "$T"

echo "case 3: only an unwatched file changed -> advance, no restart"
T=$(setup); stub_docker "$T" 0
OLD=$(head_of "$T")
upstream_commit "$T" README.md "docs v2"
run_once "$T" >/dev/null
grep -q restart "$T/docker.log" && no "restarted for an unwatched file" || ok "no restart"
[ "$(head_of "$T")" != "$OLD" ] && ok "still advanced the clone" || no "clone did not advance"
rm -rf "$T"

echo "case 4: guard fails -> roll back, never restart"
T=$(setup); stub_docker "$T" 1
OLD=$(head_of "$T")
upstream_commit "$T" NostalgiaForInfinityX7.py "def broken("
run_once "$T" >/dev/null
grep -q restart "$T/docker.log" && no "restarted despite a failing guard" || ok "no restart"
[ "$(head_of "$T")" = "$OLD" ] && ok "rolled the clone back" || no "left the bad commit checked out"
[ -f "$T/state/previous_sha" ] && no "wrote previous_sha on a failed update" || ok "no previous_sha written"
rm -rf "$T"

echo "case 5: DRY_RUN=1 -> decide, do not act"
T=$(setup); stub_docker "$T" 0
upstream_commit "$T" NostalgiaForInfinityX7.py "class X: pass  # v3"
OLD=$(head_of "$T")
OUT=$(run_once "$T" DRY_RUN=1)
grep -q restart "$T/docker.log" && no "restarted in DRY_RUN" || ok "no restart in DRY_RUN"
[ "$(head_of "$T")" = "$OLD" ] && ok "DRY_RUN left the tree on the running commit" \
  || no "DRY_RUN advanced the tree, so the bot's next start would run unreviewed code"
echo "$OUT" | grep -qi "would restart" && ok "logged the would-restart decision" || no "no decision logged"
[ -f "$T/state/previous_sha" ] && no "wrote previous_sha in DRY_RUN" || ok "no previous_sha in DRY_RUN"
rm -rf "$T"

echo "case 6: a broken upstream config is caught even when the strategy imports fine"
T=$(setup); stub_docker "$T" 0 1        # import ok, show-config fails
OLD=$(head_of "$T")
upstream_commit "$T" configs/blacklist-binance.json '{"broken":'
run_once "$T" >/dev/null
grep -q restart "$T/docker.log" && no "restarted with an unloadable config" || ok "no restart on a bad config"
[ "$(head_of "$T")" = "$OLD" ] && ok "rolled the clone back" || no "left the bad config checked out"
grep -q 'show-config' "$T/docker.log" && ok "ran the config guard" || no "skipped the config guard"
rm -rf "$T"

# --- the fuse ---------------------------------------------------------------------------
fuse_setup() {   # a repo whose running commit already has the realistic strategy shape
  local t; t=$(setup)
  strategy True 800 '      "num_cores_indicators_calc",' > "$t/upstream/NostalgiaForInfinityX7.py"
  git -C "$t/upstream" commit -qam "realistic strategy"
  git -C "$t/nfi" pull -q
  echo "$t"
}

echo "case 7: a change that switches the stop off is held, not deployed"
T=$(fuse_setup); stub_docker "$T" 0
OLD=$(head_of "$T")
upstream_commit "$T" NostalgiaForInfinityX7.py "$(strategy False 800 '      "num_cores_indicators_calc",')"
OUT=$(run_once "$T")
grep -q restart "$T/docker.log" && no "restarted into a stop-disabling change" || ok "no restart"
[ "$(head_of "$T")" = "$OLD" ] && ok "tree stayed on the running commit" || no "tree advanced to the held commit"
grep -q '^- system_v3_2_stops_enable = True' "$T/state/pending_risk" 2>/dev/null \
  && grep -q '^+ system_v3_2_stops_enable = False' "$T/state/pending_risk" \
  && ok "pending_risk shows old and new value" || no "pending_risk missing or wrong: $(cat "$T/state/pending_risk" 2>/dev/null)"
echo "$OUT" | grep -q 'HELD' && ok "logged the hold" || no "no HELD line"
echo "$OUT" | grep -q 'paged about held' && ok "paged on first sight" || no "did not page"
OUT=$(run_once "$T")
echo "$OUT" | grep -q 'paged about held' && no "paged again for the same held change" \
  || ok "did not re-page the same change every cycle"
grep -q 'Cache\|stoploss = 1' "$T/state/pending_risk" && no "read a setting from another class" \
  || ok "ignored settings outside the strategy class"

echo "case 8: approving deploys it; a further change needs a fresh approval"
STATE_DIR="$T/state" bash "$HERE/../bin/approve-update.sh" --yes >/dev/null
[ -s "$T/state/approved_risk" ] && ok "approve-update.sh recorded the approval" || no "no approval written"
OUT=$(run_once "$T")
grep -q "restart bottrade" "$T/docker.log" && ok "deployed after approval" || no "still held after approval: $OUT"
[ -f "$T/state/pending_risk" ] && no "pending_risk left behind after deploy" || ok "cleared pending_risk"
: > "$T/docker.log"
upstream_commit "$T" NostalgiaForInfinityX7.py "$(strategy None 800 '      "num_cores_indicators_calc",')"
run_once "$T" >/dev/null
grep -q restart "$T/docker.log" && no "an old approval let a different risk change through" \
  || ok "a different change is held despite the earlier approval"
rm -rf "$T"

echo "case 9: dropping a name from NFI_SAFE_PARAMETERS is held (it silently voids our override)"
T=$(fuse_setup); stub_docker "$T" 0
upstream_commit "$T" NostalgiaForInfinityX7.py "$(strategy True 800 '')"
run_once "$T" >/dev/null
grep -q restart "$T/docker.log" && no "deployed a change that drops a config-overridable name" || ok "held"
grep -q '^- NFI_SAFE_PARAMETERS has num_cores_indicators_calc' "$T/state/pending_risk" 2>/dev/null \
  && ok "named the dropped parameter" || no "did not name it: $(cat "$T/state/pending_risk" 2>/dev/null)"
rm -rf "$T"

echo "case 10: a non-risk setting change deploys normally"
T=$(fuse_setup); stub_docker "$T" 0
upstream_commit "$T" NostalgiaForInfinityX7.py "$(strategy True 900 '      "num_cores_indicators_calc",')"
run_once "$T" >/dev/null
grep -q "restart bottrade" "$T/docker.log" && ok "deployed" || no "held a change that touches no risk setting"
rm -rf "$T"

echo "case 11: restart cap -- the 5th restart inside 24h waits"
T=$(setup); stub_docker "$T" 0
OLD=$(head_of "$T"); now=$(date -u +%s)
for i in 1 2 3 4; do echo $((now - i * 600)) >> "$T/state/restarts"; done
echo $((now - 90000)) >> "$T/state/restarts"          # older than 24h, must not count
upstream_commit "$T" NostalgiaForInfinityX7.py "class X: pass  # v4"
OUT=$(run_once "$T")
grep -q restart "$T/docker.log" && no "restarted past the daily cap" || ok "deferred at the cap"
[ "$(head_of "$T")" = "$OLD" ] && ok "tree not advanced while deferred" || no "tree advanced while deferred"
echo "$OUT" | grep -q 'already 4 restarts' && ok "counted only the last 24h" || no "wrong count: $OUT"
OUT=$(run_once "$T" MAX_RESTARTS_PER_DAY=5)
grep -q "restart bottrade" "$T/docker.log" && ok "deploys once under the cap" || no "did not deploy under the cap"
[ "$(wc -l < "$T/state/restarts")" = "6" ] && ok "recorded the restart" || no "restart not recorded"
rm -rf "$T"

echo "case 12: volatility gate -- no restart into a fast market, or without price data"
T=$(setup); stub_docker "$T" 0
upstream_commit "$T" NostalgiaForInfinityX7.py "class X: pass  # v5"
OUT=$(run_once "$T" VOL_URL="file://$T/wild.json")
grep -q restart "$T/docker.log" && no "restarted in a 6% BTC range" || ok "deferred in a fast market"
echo "$OUT" | grep -qE 'range 6\.[0-9]+%' && ok "logged the measured range" || no "range not logged: $OUT"
OUT=$(run_once "$T" VOL_URL="file://$T/missing.json")
grep -q restart "$T/docker.log" && no "restarted with no price data" || ok "fails closed without data"
OUT=$(run_once "$T" VOL_URL="file://$T/wild.json" VOL_LIMIT_PCT=0)
grep -q "restart bottrade" "$T/docker.log" && ok "VOL_LIMIT_PCT=0 disables the gate" || no "gate not disabled by 0"
rm -rf "$T"

echo "case 13: approve-update.sh with nothing pending changes nothing"
T=$(mktemp -d)
STATE_DIR="$T" bash "$HERE/../bin/approve-update.sh" --yes >/dev/null 2>&1 && no "claimed success with nothing pending" \
  || ok "refused"
[ -f "$T/approved_risk" ] && no "wrote an approval anyway" || ok "wrote nothing"
rm -rf "$T"

# --- Telegram approval buttons ---------------------------------------------------------
# One fake Telegram server for all cases. Started here when python3 exists; to run these
# cases inside the updater image (no python), start tests/fake_telegram.py on the host and
# pass FAKE_TG_PORT and FAKE_TG_DIR, with that dir mounted at the same path.
TGD="${FAKE_TG_DIR:-}"; TGPID=""
if [ -n "${FAKE_TG_PORT:-}" ]; then
  TGPORT=$FAKE_TG_PORT
elif command -v python3 >/dev/null 2>&1; then
  TGD=$(mktemp -d)
  TGPORT=$(python3 -c "import socket;s=socket.socket();s.bind(('127.0.0.1',0));print(s.getsockname()[1]);s.close()")
  python3 "$HERE/fake_telegram.py" "$TGPORT" "$TGD" >/dev/null 2>&1 & TGPID=$!
  for _ in $(seq 50); do curl -s -m 1 "http://127.0.0.1:$TGPORT/botx/getMe" >/dev/null 2>&1 && break; sleep 0.1; done
fi

if [ -n "${TGPORT:-}" ]; then
APP=(APPROVAL_TELEGRAM_TOKEN=apptok APPROVAL_CHAT_ID=111 TELEGRAM_API="http://127.0.0.1:$TGPORT")
tg_reset() { : > "$TGD/calls.jsonl"; echo '[]' > "$TGD/updates.json"; }
calls()  { jq -c --arg m "$1" 'select(.method == $m)' "$TGD/calls.jsonl" 2>/dev/null; }
press()  {   # $1 callback data  $2 presser's user id  $3 chat id (default 111)
  jq -nc --arg d "$1" --argjson f "$2" --argjson c "${3:-111}" \
    '[{update_id: 7, callback_query: {id: "cq1", from: {id: $f}, data: $d,
       message: {message_id: 42, chat: {id: $c}}}}]' > "$TGD/updates.json"
}
run_loop() {   # $1 tmpdir  $2 seconds, rest=extra env; the real loop, killed by timeout
  local t=$1 secs=$2; shift 2
  env NFI_DIR="$t/nfi" STATE_DIR="$t/state" DOCKER="$t/bin/docker" \
      TARGET_CONTAINER=bottrade STRATEGY_MODULE=NFIX7Risk \
      WATCH_PATHS="NostalgiaForInfinityX7.py configs/" FREQTRADE__TELEGRAM__TOKEN="" \
      VOL_URL="file://$t/calm.json" RUN_ONCE=0 UPDATE_INTERVAL=600 \
      "$@" timeout "$secs" bash "$SCRIPT" 2>&1
}
held_with_buttons() {   # leaves T held on a stop-disabling change, DIG its digest
  T=$(fuse_setup); stub_docker "$T" 0; tg_reset
  upstream_commit "$T" NostalgiaForInfinityX7.py "$(strategy False 800 '      "num_cores_indicators_calc",')"
  run_once "$T" "${APP[@]}" >/dev/null
  DIG=$(head -1 "$T/state/pending_risk" | grep -oE 'digest [0-9a-f]+' | cut -d' ' -f2)
}

echo "case 14: a held update sends Approve / Keep-old buttons to the private chat"
held_with_buttons
M=$(calls sendMessage | head -1)
[ "$(jq -r .token <<<"$M")" = "apptok" ] && ok "sent by the approval bot, not freqtrade's" || no "wrong bot: $M"
[ "$(jq -r .params.chat_id <<<"$M")" = "111" ] && ok "sent to APPROVAL_CHAT_ID" || no "wrong chat: $M"
jq -r .params.reply_markup <<<"$M" | grep -q "approve:$DIG" && jq -r .params.reply_markup <<<"$M" | grep -q "reject:$DIG" \
  && ok "buttons carry this change's digest" || no "buttons missing or wrong: $M"
jq -r .params.text <<<"$M" | grep -qF '+ system_v3_2_stops_enable = False' \
  && ok "the diff arrives intact, '+' included" || no "diff mangled: $(jq -r .params.text <<<"$M")"
grep -q restart "$T/docker.log" && no "restarted while waiting for a decision" || ok "nothing deployed yet"

echo "case 15: pressing Approve deploys on the spot, without waiting out the interval"
press "approve:$DIG" 111
OUT=$(run_loop "$T" 6 "${APP[@]}")
grep -q "restart bottrade" "$T/docker.log" && ok "deployed within seconds of the press" || no "did not deploy: $OUT"
[ "$(cat "$T/state/approved_risk" 2>/dev/null)" = "$DIG" ] && ok "recorded the approval" || no "approval not recorded"
calls answerCallbackQuery | grep -q cq1 && ok "acknowledged the press" || no "never answered the callback"
[ "$(cat "$T/state/tg_offset" 2>/dev/null)" = "8" ] && ok "advanced the update offset (no replay after restart)" \
  || no "offset is $(cat "$T/state/tg_offset" 2>/dev/null)"
rm -rf "$T"

echo "case 16: a press from anyone but APPROVAL_CHAT_ID is ignored"
held_with_buttons; press "approve:$DIG" 222
OUT=$(run_loop "$T" 4 "${APP[@]}")
grep -q restart "$T/docker.log" && no "deployed on a stranger's press" || ok "not deployed"
[ -f "$T/state/approved_risk" ] && no "recorded a stranger's approval" || ok "no approval recorded"
calls answerCallbackQuery | grep -q 'Not authorised' && ok "told them so" || no "no refusal sent"
rm -rf "$T"

echo "case 17: a button for an older change cannot approve the current one"
held_with_buttons; press "approve:0123456789abcdef" 111
OUT=$(run_loop "$T" 4 "${APP[@]}")
grep -q restart "$T/docker.log" && no "a stale button deployed the current change" || ok "not deployed"
calls answerCallbackQuery | grep -q 'Outdated' && ok "reported it as outdated" || no "stale press not reported"
rm -rf "$T"

echo "case 18: Keep old leaves the bot on the running commit"
held_with_buttons; OLD=$(head_of "$T"); press "reject:$DIG" 111
OUT=$(run_loop "$T" 4 "${APP[@]}")
grep -q restart "$T/docker.log" && no "deployed after Keep old" || ok "not deployed"
[ "$(head_of "$T")" = "$OLD" ] && ok "tree still on the running commit" || no "tree moved"
[ -f "$T/state/approved_risk" ] && no "Keep old recorded an approval" || ok "no approval recorded"
calls sendMessage | grep -q 'Kept the running version' && ok "confirmed the decision in the chat" || no "no confirmation"
rm -rf "$T"

echo "case 19: without an approval bot, the hold still pages as text with the CLI hint"
T=$(fuse_setup); stub_docker "$T" 0; tg_reset
upstream_commit "$T" NostalgiaForInfinityX7.py "$(strategy False 800 '      "num_cores_indicators_calc",')"
run_once "$T" FREQTRADE__TELEGRAM__TOKEN=maintok TELEGRAM_API="http://127.0.0.1:$TGPORT" >/dev/null
M=$(calls sendMessage | head -1)
[ "$(jq -r .token <<<"$M")" = "maintok" ] && jq -r .params.text <<<"$M" | grep -q 'approve-update.sh' \
  && [ "$(jq -r '.params.reply_markup // empty' <<<"$M")" = "" ] \
  && ok "plain text page on the main bot, no buttons" || no "unexpected page: $M"
rm -rf "$T"
echo "case 20: in a group, only APPROVER_IDS can approve; without it the buttons stay off"
GRP=(APPROVAL_TELEGRAM_TOKEN=apptok APPROVAL_CHAT_ID=-500 APPROVER_IDS=111 TELEGRAM_API="http://127.0.0.1:$TGPORT")
T=$(fuse_setup); stub_docker "$T" 0; tg_reset
upstream_commit "$T" NostalgiaForInfinityX7.py "$(strategy False 800 '      "num_cores_indicators_calc",')"
run_once "$T" "${GRP[@]}" >/dev/null
DIG=$(head -1 "$T/state/pending_risk" | grep -oE 'digest [0-9a-f]+' | cut -d' ' -f2)
[ "$(calls sendMessage | head -1 | jq -r .params.chat_id)" = "-500" ] && ok "buttons posted in the group" || no "not posted in the group"
press "approve:$DIG" 333 -500
run_loop "$T" 4 "${GRP[@]}" >/dev/null
grep -q restart "$T/docker.log" && no "a group member who is not an approver deployed it" || ok "non-approver in the group ignored"
press "approve:$DIG" 111 -500; echo 7 > "$T/state/tg_offset"
run_loop "$T" 6 "${GRP[@]}" >/dev/null
grep -q "restart bottrade" "$T/docker.log" && ok "the approver's press in the group deployed it" || no "approver's press did not deploy"
rm -rf "$T"
T=$(fuse_setup); stub_docker "$T" 0; tg_reset
upstream_commit "$T" NostalgiaForInfinityX7.py "$(strategy False 800 '      "num_cores_indicators_calc",')"
OUT=$(run_once "$T" APPROVAL_TELEGRAM_TOKEN=apptok APPROVAL_CHAT_ID=-500 TELEGRAM_API="http://127.0.0.1:$TGPORT")
echo "$OUT" | grep -q 'approval buttons OFF' && ok "refused to enable group buttons without APPROVER_IDS" || no "enabled buttons anyone could press"
[ -z "$(calls sendMessage | jq -r 'select(.token == "apptok")')" ] && ok "sent no buttons" || no "sent buttons anyway"
rm -rf "$T"
else
  echo "  skip: approval-button cases need python3 or FAKE_TG_PORT"
fi
[ -n "$TGPID" ] && { kill "$TGPID" 2>/dev/null; wait "$TGPID" 2>/dev/null; rm -rf "$TGD"; }

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
