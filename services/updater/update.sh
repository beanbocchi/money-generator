#!/usr/bin/env bash
# Tracks upstream NostalgiaForInfinity and restarts the bot only when a file the
# bot actually loads has changed, and only if the new code imports cleanly.
set -uo pipefail

NFI_DIR="${NFI_DIR:-/nfi}"
STATE_DIR="${STATE_DIR:-/state}"
DOCKER="${DOCKER:-docker}"
TARGET_CONTAINER="${TARGET_CONTAINER:-bottrade}"
STRATEGY_MODULE="${STRATEGY_MODULE:-NFIX7Risk}"
UPDATE_INTERVAL="${UPDATE_INTERVAL:-900}"
DRY_RUN="${DRY_RUN:-0}"
RUN_ONCE="${RUN_ONCE:-0}"
WATCH_PATHS="${WATCH_PATHS:-NostalgiaForInfinityX7.py configs/}"
STRATEGY_FILE="${STRATEGY_FILE:-NostalgiaForInfinityX7.py}"

# --- the fuse -------------------------------------------------------------------------
# The import guard catches code that is broken. It cannot catch code that is valid and
# changes what the bot risks. That is not hypothetical: upstream commit 54ab6f2f6
# (2026-04-19, 19 lines, message "X7: use system_v3_2.") set system_v3_2_stops_enable =
# False -- it switched the stop off -- and it reached real money with nobody reading it.
#
# So before deploying, the class-level settings whose NAME matches RISK_PATTERN are
# compared between the running commit and upstream, plus the membership of
# NFI_SAFE_PARAMETERS (drop a name from that list and our config override for it is
# silently ignored -- that is how 2x leverage would quietly become upstream's 3x). Any
# difference holds the update at the current commit and pages once; bin/approve-update.sh
# on the host accepts it. Nothing else about the update changes.
# Broad on purpose. Measured over the 755 X7 commits from 2026-07-30 to 2026-10-30, this
# pattern would have held 2 of them (on 2 days); a narrow stop/leverage-only pattern held 1.
# Breadth costs almost nothing in interruptions, so it is not worth narrowing.
RISK_PATTERN="${RISK_PATTERN:-stop|doom|lever|derisk|grind|rebuy|buyback|stake|position_adjustment|max_entry|trailing|roi|system_}"
# Each restart is minutes of the bot not processing (301-331s measured, 540s once).
# Upstream pushes in bursts -- 6 restarts on 2026-10-23, 4 between 01:21 and 02:36 UTC on
# the night of the Binance hack -- and the bursts land when markets move.
MAX_RESTARTS_PER_DAY="${MAX_RESTARTS_PER_DAY:-4}"
# Never restart into a fast market: if BTC's 30-minute range exceeds VOL_LIMIT_PCT the
# deploy waits for a later cycle. Fails closed -- no price data means no restart.
# 0 disables the check.
VOL_LIMIT_PCT="${VOL_LIMIT_PCT:-2}"
VOL_URL="${VOL_URL:-https://fapi.binance.com/fapi/v1/klines?symbol=BTCUSDT&interval=5m&limit=6}"

log() { echo "[updater $(date -u +%FT%TZ)] $*"; }

TELEGRAM_API="${TELEGRAM_API:-https://api.telegram.org}"

# Non-zero when the message did not go out, so "paged once" is only recorded once true.
# --data-urlencode, not -d: a held update's diff lines start with "+", which -d sends raw
# and the server decodes as a space.
notify() {
  [ -n "${FREQTRADE__TELEGRAM__TOKEN:-}" ] || return 0
  curl -sf -m 10 -X POST \
    "$TELEGRAM_API/bot${FREQTRADE__TELEGRAM__TOKEN}/sendMessage" \
    --data-urlencode chat_id="${FREQTRADE__TELEGRAM__CHAT_ID:-}" \
    --data-urlencode text="$1" >/dev/null && return 0
  log "telegram notify failed"
  return 1
}

# Approve / Keep-old buttons for held updates: approval_enabled, send_approval, listen,
# selftest. Sourced, so it shares log, notify and STATE_DIR with this script.
# shellcheck source=approval.sh
source "$(dirname "${BASH_SOURCE[0]}")/approval.sh"

# Importing inside the running container validates the syntax of upstream's 3MB
# generated strategy, our subclass, and that both class bodies execute -- under the
# exact python and freqtrade version that will run the code. py_compile is not used:
# it writes __pycache__ next to the source, which is on a read-only mount, so it
# would fail for the wrong reason. Import failures to write bytecode are ignored
# silently by python, so read-only is fine here.
#
# python3 -P and the __file__ assertion are both load-bearing. Without -P, python
# prepends the process's cwd (/freqtrade) to sys.path, so a stray NostalgiaForInfinity
# checkout or a leftover strategy file sitting at /freqtrade/ can satisfy the import
# and the guard passes having validated a file the bot will never load. Observed for
# real against the pre-cutover container, which had exactly such a stray file.
guard() {
  "$DOCKER" exec "$TARGET_CONTAINER" python3 -P -c \
    "import sys; sys.path.insert(0,'/freqtrade/user_data/strategies'); import ${STRATEGY_MODULE} as m; assert m.__file__.startswith('/freqtrade/user_data/strategies/'), 'guard imported the wrong file: '+str(m.__file__)" \
  && config_guard
}

# The strategy is not the only thing pulled from upstream. config.json includes five
# files out of NostalgiaForInfinity/configs/ -- blacklist, pairlist, exampleconfig and
# the secret template -- and a malformed one takes the bot down at the next restart,
# which the import guard would happily wave through. show-config exercises the whole
# merge chain in the live container, read-only, in about two seconds.
config_guard() {
  "$DOCKER" exec "$TARGET_CONTAINER" \
    freqtrade show-config --config user_data/config.json >/dev/null 2>&1
}

# Prints the risk-relevant settings of the strategy at git revision $1, one per line,
# sorted: class-level "name = value" lines whose name matches RISK_PATTERN, then one
# "NFI_SAFE_PARAMETERS has <name>" line per overridable parameter. Read from git objects,
# so it needs no checkout and no python (this image has neither to spare).
risk_snapshot() {
  git -C "$NFI_DIR" show "$1:$STRATEGY_FILE" 2>/dev/null | awk -v cls="^class ${STRATEGY_FILE%.py}[(:]" -v pat="$RISK_PATTERN" '
    $0 ~ cls { inside = 1; next }
    inside && /^class / { exit }
    !inside { next }
    /NFI_SAFE_PARAMETERS = \[/ { safe = 1; next }
    safe {
      if ($0 ~ /^[ \t]*\]/) { safe = 0 }
      else if (match($0, /"[^"]+"/)) { print "NFI_SAFE_PARAMETERS has " substr($0, RSTART + 1, RLENGTH - 2) }
      next
    }
    /^  [A-Za-z_][A-Za-z0-9_]* = / { if ($1 ~ pat) { sub(/^ +/, ""); print } }
  ' | LC_ALL=C sort
}

# "- old line" / "+ new line" for every risk setting that differs between $1 and $2.
risk_diff() {
  local a b
  a=$(mktemp); b=$(mktemp)
  risk_snapshot "$1" > "$a"; risk_snapshot "$2" > "$b"
  LC_ALL=C comm -3 "$a" "$b" | awk -F'\t' '{ if ($1 != "") print "- " $1; else print "+ " $2 }'
  rm -f "$a" "$b"
}

# Restarts in the last 24h, from the log of past restart times.
restarts_today() {
  local now cutoff
  now=$(date -u +%s); cutoff=$((now - 86400))
  [ -f "$STATE_DIR/restarts" ] || { echo 0; return; }
  awk -v c="$cutoff" '$1 >= c' "$STATE_DIR/restarts" | wc -l | tr -d ' '
}

# BTC's high-low range over the kline window, as a percent of the last close. Prints
# nothing when the data is missing or unparseable, which callers treat as "do not restart".
btc_range_pct() {
  curl -sf -m 10 "$VOL_URL" 2>/dev/null | sed 's/\],\[/\n/g' | tr -d '[]"' | awk -F, '
    NF >= 5 { h = $3 + 0; l = $4 + 0; c = $5 + 0
              if (n == 0 || h > hi) hi = h
              if (n == 0 || l < lo) lo = l
              last = c; n++ }
    END { if (n > 0 && last > 0) printf "%.2f", (hi - lo) / last * 100 }'
}

one_cycle() {
  if ! git -C "$NFI_DIR" fetch -q origin main; then
    log "fetch failed (network?), retrying next cycle"
    return 0
  fi

  local old new changed
  old=$(git -C "$NFI_DIR" rev-parse HEAD)
  new=$(git -C "$NFI_DIR" rev-parse origin/main)
  if [ "$old" = "$new" ]; then
    log "up to date at ${old:0:8}"
    return 0
  fi

  # WATCH_PATHS is deliberately unquoted: it is a space-separated pathspec list.
  changed=$(git -C "$NFI_DIR" diff --name-only "$old" "$new" -- $WATCH_PATHS)

  if [ -z "$changed" ]; then
    git -C "$NFI_DIR" reset --hard -q origin/main
    log "advanced ${old:0:8}->${new:0:8}, no watched file changed, not restarting"
    return 0
  fi
  log "watched files changed: $(echo $changed | tr '\n' ' ')"

  # Everything from here until the reset decides whether to deploy at all, and reads only
  # git objects. The working tree is what the bot loads on its next start -- including a
  # start nobody planned, like an OOM kill or a host reboot -- so it must not move until
  # the update is actually going out.
  mkdir -p "$STATE_DIR"
  local rdiff digest
  rdiff=$(risk_diff "$old" "$new")
  if [ -n "$rdiff" ]; then
    digest=$(printf '%s\n' "$rdiff" | sha256sum | cut -c1-16)
    if [ "$(cat "$STATE_DIR/approved_risk" 2>/dev/null)" = "$digest" ]; then
      log "risk settings change ${digest} was approved, deploying"
    else
      { echo "commit ${old:0:8} -> ${new:0:8}  digest $digest"; echo "$rdiff"; } > "$STATE_DIR/pending_risk"
      log "HELD at ${old:0:8}: ${new:0:8} changes $(echo "$rdiff" | wc -l | tr -d ' ') risk setting line(s), approve with bin/approve-update.sh"
      echo "$rdiff" | head -20 | sed 's/^/    /' >&2
      # Page once per distinct change, not every 15 minutes while it waits.
      if [ "$(cat "$STATE_DIR/notified_risk" 2>/dev/null)" != "$digest" ]; then
        local msg="NFI update HELD: ${new:0:8} changes risk settings. The bot stays on ${old:0:8} until you decide.
Lines with - are what runs now, + is upstream's new value:
$(echo "$rdiff" | head -25)"
        if approval_enabled; then
          send_approval "$digest" "$msg" && { echo "$digest" > "$STATE_DIR/notified_risk"; log "paged about held update $digest (buttons)"; }
        else
          notify "$msg
Approve on the host: bin/approve-update.sh" && { echo "$digest" > "$STATE_DIR/notified_risk"; log "paged about held update $digest"; }
        fi
      fi
      return 0
    fi
  fi
  rm -f "$STATE_DIR/pending_risk"

  local n_today
  n_today=$(restarts_today)
  if [ "$n_today" -ge "$MAX_RESTARTS_PER_DAY" ]; then
    log "deferred ${new:0:8}: already $n_today restarts in 24h (limit $MAX_RESTARTS_PER_DAY)"
    return 0
  fi

  if [ "$VOL_LIMIT_PCT" != "0" ]; then
    local vol
    vol=$(btc_range_pct)
    if [ -z "$vol" ]; then
      log "deferred ${new:0:8}: no BTC price data, not restarting blind"
      return 0
    fi
    if awk -v v="$vol" -v l="$VOL_LIMIT_PCT" 'BEGIN { exit !(v >= l) }'; then
      log "deferred ${new:0:8}: BTC 30m range ${vol}% >= ${VOL_LIMIT_PCT}%, market moving"
      return 0
    fi
  fi

  # The import guard runs against the working tree inside the bot container, so the tree
  # advances here, after every reason not to deploy has been checked. It is put back if the
  # guard fails or this is a dry run.
  git -C "$NFI_DIR" reset --hard -q "$new"

  # Log both outcomes. With only the failure logged, a passing guard leaves no trace,
  # and "guard ran and passed" becomes indistinguishable from "guard was skipped" --
  # which is not a distinction to lose in the audit trail of a safety check that stands
  # between upstream's latest commit and real money.
  if ! guard; then
    git -C "$NFI_DIR" reset --hard -q "$old"
    log "guard FAILED for ${new:0:8}, rolled back to ${old:0:8}, bot untouched"
    notify "NFI ${new:0:8} failed to import - rolled back, bot untouched"
    return 0
  fi
  log "guard passed for ${new:0:8} (${STRATEGY_MODULE} imports from the strategy dir)"

  if [ "$DRY_RUN" = "1" ]; then
    # Put the tree back. Leaving it advanced meant the bot's next start of any kind ran
    # code that this "dry" run had only looked at.
    git -C "$NFI_DIR" reset --hard -q "$old"
    log "DRY_RUN: would restart $TARGET_CONTAINER for ${old:0:8}->${new:0:8}, tree left at ${old:0:8}"
    return 0
  fi

  mkdir -p "$STATE_DIR"
  echo "$old" > "$STATE_DIR/previous_sha"
  if "$DOCKER" restart "$TARGET_CONTAINER"; then
    date -u +%s >> "$STATE_DIR/restarts"
    log "restarted $TARGET_CONTAINER at ${new:0:8}"
    notify "NFI ${old:0:8}->${new:0:8}, bot restarted"
  else
    log "restart FAILED after applying ${new:0:8}"
    notify "NFI ${new:0:8} applied but restart FAILED - check the host"
  fi
}

log "started: interval=${UPDATE_INTERVAL}s target=$TARGET_CONTAINER dry_run=$DRY_RUN watch='$WATCH_PATHS' max_restarts_per_day=$MAX_RESTARTS_PER_DAY vol_limit=${VOL_LIMIT_PCT}%"
if [ "${APPROVAL_SELFTEST:-0}" = "1" ]; then selftest; exit $?; fi
if approval_enabled; then
  log "approval buttons on: chat $APPROVAL_CHAT_ID, approvers:$APPROVER_IDS"
elif [ -n "$APPROVAL_TELEGRAM_TOKEN" ]; then
  log "approval buttons OFF: APPROVER_IDS is not set (required for a group chat); approve with bin/approve-update.sh"
else
  log "approval buttons off (no APPROVAL_TELEGRAM_TOKEN); approve with bin/approve-update.sh"
fi
while true; do
  one_cycle
  [ "$RUN_ONCE" = "1" ] && break
  # Between cycles, listen for button presses instead of sleeping; an approval cuts the
  # wait short so the deploy does not sit for up to UPDATE_INTERVAL after you press it.
  if approval_enabled; then
    listen $(( $(date -u +%s) + UPDATE_INTERVAL )) || true
  else
    sleep "$UPDATE_INTERVAL"
  fi
done
