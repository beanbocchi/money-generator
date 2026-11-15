#!/usr/bin/env bash
# Minimal liveness watchdog: is the bot responding, and is it still processing?
#
# One request to /api/v1/health answers all three things worth knowing:
#   - the API responds at all            (container up, port reachable)
#   - the credentials still work         (a rotated or revoked key shows up here)
#   - last_process_ts is recent          (the loop is running, not hung)
#
# That is the whole design. The point of a watchdog is not to reproduce freqtrade's own
# reporting -- Telegram already sends trade notifications -- it is to break the ambiguity
# of silence, where "no signals today" and "dead for three days" look identical.
#
# Deliberately not included: log parsing, a rule engine, Prometheus metrics, per-signal
# analysis. The old setup's 736-line watchdog had all of them, and none of them answer
# "is it alive".
set -uo pipefail

API="${API:-http://freqtrade:8989}"
USER_="${FREQTRADE__API_SERVER__USERNAME:?}"
PASS="${FREQTRADE__API_SERVER__PASSWORD:?}"
# One rule: N consecutive polls where the bot is not demonstrably processing.
#
# Earlier versions special-cased the health fields -- null last_process_ts means starting,
# unless bot_startup_ts is old, unless it is absent... That produced three wrong versions
# and 15 false pages in two days, every one of them landing within seconds of an updater
# restart. The failure was never the threshold, it was reasoning about which field means
# what during a restart, when the API answers before the worker is ready.
#
# So: not-processing is not-processing, whatever the reason. Restarts take a measured
# 301-331s (24 restarts sampled), so 15 polls at 60s cannot page on one, while a bot that
# has genuinely died pages within 15 minutes. No startup grace, no field semantics.
MAX_AGE="${MAX_AGE:-900}"
INTERVAL="${INTERVAL:-60}"
FAILS_BEFORE_ALERT="${FAILS_BEFORE_ALERT:-15}"
RUN_ONCE="${RUN_ONCE:-0}"
# While still broken, page again every REALERT_EVERY seconds. One page per incident let the
# 2026-10-05 IP allow-list outage run 4.6 days on a single message that was easy to miss.
REALERT_EVERY="${REALERT_EVERY:-10800}"
TELEGRAM_API="${TELEGRAM_API:-https://api.telegram.org}"

# stderr, not stdout: check() runs inside `reason=$(check)`, so anything it writes to
# stdout is captured into the variable instead of reaching the logs. Docker captures both
# streams, so this changes nothing visible except that log lines actually appear.
log() { echo "[watchdog $(date -u +%FT%TZ)] $*" >&2; }

# Returns non-zero when the message did not go out, so callers only count a page as sent
# when it was. The old version marked every incident alerted regardless: on 2026-10-12 the
# only page for an outage failed to send and nothing ever retried it.
notify() {
  [ -n "${FREQTRADE__TELEGRAM__TOKEN:-}" ] || return 0
  curl -sf -m 10 -X POST \
    "$TELEGRAM_API/bot${FREQTRADE__TELEGRAM__TOKEN}/sendMessage" \
    -d chat_id="${FREQTRADE__TELEGRAM__CHAT_ID:-}" -d text="$1" >/dev/null && return 0
  log "telegram send failed"
  return 1
}

# Pages for one failing check, given its state: $1 reason, $2 epoch of the last page that
# actually went out (0 = none yet), $3 epoch the failure began. Prints the new last-page
# epoch -- unchanged when nothing was due or the send failed, so a failed send is retried
# on the next poll instead of being forgotten.
page() {
  local reason=$1 last=$2 since=$3 now
  now=$(date -u +%s)
  if [ "$last" = "0" ]; then
    if notify "PROBLEM: $reason"; then log "ALERT: $reason"; echo "$now"; return; fi
  elif [ "$((now - last))" -ge "$REALERT_EVERY" ]; then
    local m=$(( (now - since) / 60 ))
    if notify "STILL BROKEN for ${m}m: $reason"; then
      log "ALERT REPEAT (failing for ${m}m): $reason"; echo "$now"; return
    fi
  else
    echo "$last"; return
  fi
  log "ALERT NOT DELIVERED (retrying next poll): $reason"
  echo "$last"
}

# Echoes a reason on failure, nothing on success.
check() {
  local body age now
  body=$(curl -sf -m 10 -u "$USER_:$PASS" "$API/api/v1/health" 2>/dev/null) || {
    echo "no response from $API/api/v1/health (down, unreachable, or bad credentials)"
    return 1
  }
  # Parse without jq: the image may not have it, and one integer does not justify a dep.
  now=$(date -u +%s)
  age=$(printf '%s' "$body" | grep -oE '"last_process_ts":[0-9]+' | grep -oE '[0-9]+$')
  if [ -z "$age" ]; then
    # Null during a restart, null while broken -- same signal, and the consecutive-failure
    # count is what tells them apart. Body logged in full because truncating it to 100
    # chars is what blocked diagnosing the false pages.
    echo "not processing yet (no last_process_ts): $body"
    return 1
  fi
  if [ "$((now - age))" -gt "$MAX_AGE" ]; then
    echo "bot has not processed for $((now - age))s (limit ${MAX_AGE}s) -- loop stalled"
    return 1
  fi
  return 0
}

# /api/v1/health proves the bot's own loop is alive. It says nothing about whether the bot
# can still reach the exchange with working credentials, and those are different failures
# with different remedies. On 2026-08-05 the Binance key started returning
# -2015 "Invalid API-key, IP, or permissions" -- the loop kept ticking, health kept
# returning 200, last_process_ts stayed fresh, and the watchdog reported everything fine
# while the bot could not fetch a balance or place an order. /api/v1/balance is the
# cheapest endpoint that actually exercises signed exchange access.
#
# Polled every CREDS_CHECK_EVERY iterations rather than every one, because it reaches the
# exchange and there is no reason to spend rate limit on a 60s cadence.
CREDS_CHECK_EVERY="${CREDS_CHECK_EVERY:-5}"
CREDS_FAILS_BEFORE_ALERT="${CREDS_FAILS_BEFORE_ALERT:-3}"

creds_check() {
  local code
  code=$(curl -s -o /dev/null -w '%{http_code}' -m 20 -u "$USER_:$PASS" \
         "$API/api/v1/balance" 2>/dev/null)
  [ "$code" = "200" ] && return 0
  echo "cannot query balance (HTTP ${code:-none}) -- the bot is running but has no working
 exchange access; check the API key, its IP allow-list, and its futures permission"
  return 1
}

fails=0
alerted_at=0
fail_since=0
iter=0
creds_fails=0
creds_alerted_at=0
creds_since=0
log "started: api=$API max_age=${MAX_AGE}s interval=${INTERVAL}s fails_before_alert=$FAILS_BEFORE_ALERT realert_every=${REALERT_EVERY}s"
while true; do
  if reason=$(check); then
    # Recovery is worth one message: an alert with no all-clear trains you to ignore it.
    if [ "$alerted_at" != "0" ]; then
      log "RECOVERED: bot is responding and processing again"
      notify "OK: bot recovered - responding and processing again"
      alerted_at=0
    fi
    fails=0
  else
    [ "$fails" -eq 0 ] && fail_since=$(date -u +%s)
    fails=$((fails + 1))
    log "check failed ($fails/$FAILS_BEFORE_ALERT): $reason"
    # Debounced on purpose. The updater restarts the bot whenever upstream changes a
    # watched file, so a single failed poll is expected routine, not an incident.
    if [ "$fails" -ge "$FAILS_BEFORE_ALERT" ]; then
      alerted_at=$(page "$reason" "$alerted_at" "$fail_since")
    fi
  fi
  iter=$((iter + 1))
  if [ "$((iter % CREDS_CHECK_EVERY))" -eq 1 ] || [ "$RUN_ONCE" = "1" ]; then
    if creds_reason=$(creds_check); then
      if [ "$creds_alerted_at" != "0" ]; then
        log "RECOVERED: exchange access works again"
        notify "OK: exchange access recovered"
        creds_alerted_at=0
      fi
      creds_fails=0
    else
      [ "$creds_fails" -eq 0 ] && creds_since=$(date -u +%s)
      creds_fails=$((creds_fails + 1))
      log "creds check failed ($creds_fails/$CREDS_FAILS_BEFORE_ALERT): $creds_reason"
      if [ "$creds_fails" -ge "$CREDS_FAILS_BEFORE_ALERT" ]; then
        creds_alerted_at=$(page "$creds_reason" "$creds_alerted_at" "$creds_since")
      fi
    fi
  fi

  [ "$RUN_ONCE" = "1" ] && break
  sleep "$INTERVAL"
done
