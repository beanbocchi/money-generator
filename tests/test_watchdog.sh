#!/usr/bin/env bash
# services/watchdog/watch.sh against a local fake freqtrade API. Hermetic: loopback only, no
# containers, and the Telegram token is left empty so notify() returns early -- the
# assertions read the log lines instead.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT="$HERE/../services/watchdog/watch.sh"
PASS=0; FAIL=0
ok() { echo "  ok: $1"; PASS=$((PASS+1)); }
no() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

T=$(mktemp -d)
PORT=$(python3 -c "
import socket;s=socket.socket();s.bind(('127.0.0.1',0));print(s.getsockname()[1]);s.close()")
echo fresh > "$T/state"

cat > "$T/server.py" <<'PY'
import sys, time, os
from http.server import BaseHTTPRequestHandler, HTTPServer
port, tdir = int(sys.argv[1]), sys.argv[2]
class H(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.startswith("/api/v1/balance"):
            bad = os.path.exists(os.path.join(tdir, "creds_bad"))
            self.send_response(500 if bad else 200)
            body = b'{}'
            self.send_header("Content-Length", str(len(body)))
            self.end_headers(); self.wfile.write(body); return
        state = open(os.path.join(tdir, "state")).read().strip()
        if state == "garbage":
            body = b'{"unexpected":true}'
        elif state == "starting":      # restarted 60s ago, no process tick yet
            body = ('{"last_process_ts":null,"bot_startup_ts":%d}'
                    % (int(time.time()) - 60)).encode()
        elif state == "null_old":      # null long past any plausible startup
            body = ('{"last_process_ts":null,"bot_startup_ts":%d}'
                    % (int(time.time()) - 99999)).encode()
        elif state == "stale":                       # 99999s since last_process
            body = ('{"last_process_ts":%d}' % (int(time.time()) - 99999)).encode()
        else:
            body = ('{"last_process_ts":%d}' % int(time.time())).encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def do_POST(self):                # fake Telegram sendMessage
        bad = os.path.exists(os.path.join(tdir, "tg_bad"))
        n = int(self.headers.get("Content-Length") or 0); self.rfile.read(n)
        with open(os.path.join(tdir, "tg_sent"), "a") as f: f.write("x\n")
        self.send_response(500 if bad else 200)
        self.send_header("Content-Length", "2"); self.end_headers(); self.wfile.write(b"{}")
    def log_message(self, *a): pass
HTTPServer(("127.0.0.1", port), H).serve_forever()
PY

start_server() {
  python3 "$T/server.py" "$PORT" "$T" >/dev/null 2>&1 &
  SRV=$!
  for _ in $(seq 50); do
    curl -sf -m 1 "http://127.0.0.1:$PORT/api/v1/health" >/dev/null 2>&1 && return 0
    sleep 0.1
  done
  return 1
}
stop_server() { kill "${SRV:-}" 2>/dev/null; wait "${SRV:-}" 2>/dev/null; }
trap stop_server EXIT

env_common=(FREQTRADE__API_SERVER__USERNAME=u FREQTRADE__API_SERVER__PASSWORD=p
            FREQTRADE__TELEGRAM__TOKEN="" MAX_AGE=300 INTERVAL=1)
once()  { env API="http://127.0.0.1:$PORT" "${env_common[@]}" \
              FAILS_BEFORE_ALERT="${FBA:-3}" RUN_ONCE=1 bash "$SCRIPT" 2>&1; }
loop()  { env API="http://127.0.0.1:$PORT" "${env_common[@]}" \
              FAILS_BEFORE_ALERT="${FBA:-3}" timeout "$1" bash "$SCRIPT" 2>&1; }

start_server || { echo "fake server failed to start"; exit 1; }

echo "case 1: a healthy bot produces no alert"
OUT=$(once)
echo "$OUT" | grep -q 'ALERT' && no "alerted on a healthy bot" || ok "no alert when healthy"
echo "$OUT" | grep -q 'started:' && ok "logged its configuration" || no "no startup line"

echo "case 2: a stalled loop alerts only after FAILS_BEFORE_ALERT consecutive failures"
echo stale > "$T/state"
OUT=$(loop 5)
FIRST=$(echo "$OUT" | grep -n 'check failed' | head -1 | cut -d: -f1)
ALERTLINE=$(echo "$OUT" | grep -n 'ALERT:' | head -1 | cut -d: -f1)
[ -n "$FIRST" ] && ok "detected the stall" || no "did not detect a 99999s-old last_process"
if [ -n "$ALERTLINE" ]; then
  ok "eventually alerted"
  [ "$ALERTLINE" -gt "$FIRST" ] \
    && ok "did not alert on the first failure (debounced)" \
    || no "alerted immediately, so a routine updater restart would page you"
else
  no "never alerted on a persistent stall"; no "(debounce check skipped)"
fi
[ "$(echo "$OUT" | grep -c 'ALERT:')" = "1" ] \
  && ok "alerted once, not on every poll" \
  || no "repeated the alert $(echo "$OUT" | grep -c 'ALERT:') times while still bad"

echo "case 3: recovery is reported exactly once, within the same loop"
echo stale > "$T/state"
OUT=$( { FBA=2 loop 8 & LP=$!; sleep 4; echo fresh > "$T/state"; wait $LP; } 2>&1 )
[ "$(echo "$OUT" | grep -c 'RECOVERED')" = "1" ] \
  && ok "reported recovery exactly once" \
  || no "recovery reported $(echo "$OUT" | grep -c 'RECOVERED') times"

echo "case 4: an unreachable API is a failure, not a crash"
stop_server
echo fresh > "$T/state"
OUT=$(once); RC=$?
echo "$OUT" | grep -q 'no response' && ok "reported the API as unreachable" \
  || no "did not report an unreachable API: ${OUT:0:120}"
[ "$RC" -eq 0 ] && ok "exited cleanly rather than crashing" || no "exited $RC"

echo "case 5: null last_process_ts counts as not-processing, whatever the reason"
echo starting > "$T/state"
start_server || { echo "fake server restart failed"; exit 1; }   # case 4 stopped it
OUT=$(once)
echo "$OUT" | grep -q 'not processing yet' && ok "counted null as not-processing" \
  || no "did not treat null last_process_ts as not-processing"
echo "$OUT" | grep -q 'ALERT:' && no "paged on a single null poll" \
  || ok "one null poll does not page"
echo "$OUT" | grep -q 'bot_startup_ts' && ok "logged the full body for diagnosis" \
  || no "truncated the body, which is what blocked diagnosing the false pages"

echo "case 5b: a measured 331s restart cannot page at the default threshold"
# 24 sampled restarts took 301-331s. At INTERVAL=60 that is at most 6 polls.
[ "$(grep -c 'FAILS_BEFORE_ALERT:-15' "$SCRIPT")" -ge 1 ] \
  && ok "default threshold is 15 polls, above the 6 a 331s restart can produce" \
  || no "default threshold no longer covers a measured restart"

echo "case 6: a live loop with dead exchange credentials must still alert"
echo fresh > "$T/state"; : > "$T/creds_bad"
OUT=$(env API="http://127.0.0.1:$PORT" "${env_common[@]}" FAILS_BEFORE_ALERT=15 \
        CREDS_FAILS_BEFORE_ALERT=1 RUN_ONCE=1 bash "$SCRIPT" 2>&1)
echo "$OUT" | grep -q 'no working' && ok "detected dead exchange access while the loop was healthy" \
  || no "missed dead credentials -- the 2026-08-05 -2015 incident would pass silently"
echo "$OUT" | grep -q 'ALERT:' && ok "paged on it" || no "did not page on dead credentials"
# "] check failed" is the loop check; "] creds check failed" is this one. Matching the
# bare substring would hit both, which is what made this assertion fail spuriously.
echo "$OUT" | grep -qE '\] check failed' && no "conflated it with the loop being stalled" \
  || ok "kept it separate from the loop check (different remedy)"
rm -f "$T/creds_bad"
OUT=$(env API="http://127.0.0.1:$PORT" "${env_common[@]}" FAILS_BEFORE_ALERT=15 \
        CREDS_FAILS_BEFORE_ALERT=1 RUN_ONCE=1 bash "$SCRIPT" 2>&1)
echo "$OUT" | grep -q 'ALERT:' && no "still paging with working credentials" \
  || ok "silent once exchange access works"

# Telegram pointed at the fake server: a real token value, a local API base.
tg() { env API="http://127.0.0.1:$PORT" FREQTRADE__API_SERVER__USERNAME=u \
           FREQTRADE__API_SERVER__PASSWORD=p FREQTRADE__TELEGRAM__TOKEN=t \
           TELEGRAM_API="http://127.0.0.1:$PORT" MAX_AGE=300 INTERVAL=1 \
           CREDS_CHECK_EVERY=1000 "$@" bash "$SCRIPT" 2>&1; }

echo "case 7: a page that fails to send is retried, not counted as sent"
echo stale > "$T/state"; : > "$T/tg_bad"; rm -f "$T/tg_sent"
OUT=$( { tg FAILS_BEFORE_ALERT=2 timeout 9 & LP=$!; sleep 5; rm -f "$T/tg_bad"; wait $LP; } )
[ "$(echo "$OUT" | grep -c 'ALERT NOT DELIVERED')" -ge 2 ] \
  && ok "retried the failed send on later polls" \
  || no "did not retry a failed send ($(echo "$OUT" | grep -c 'ALERT NOT DELIVERED') attempts)"
[ "$(echo "$OUT" | grep -c 'ALERT:')" = "1" ] \
  && ok "counted it as sent exactly once, after delivery succeeded" \
  || no "ALERT: logged $(echo "$OUT" | grep -c 'ALERT:') times"
echo "$OUT" | grep -q 'ALERT REPEAT' && no "repeated without REALERT_EVERY elapsing" \
  || ok "no repeat inside the default 3h window"

echo "case 8: a failure that persists pages again every REALERT_EVERY"
echo stale > "$T/state"; rm -f "$T/tg_bad"
OUT=$(tg FAILS_BEFORE_ALERT=1 REALERT_EVERY=2 timeout 8)
[ "$(echo "$OUT" | grep -c 'ALERT REPEAT')" -ge 1 ] \
  && ok "paged again while still broken" \
  || no "never repeated the page during a persistent failure"
[ "$(echo "$OUT" | grep -c 'ALERT REPEAT')" -le 4 ] \
  && ok "repeats are spaced, not one per poll" \
  || no "repeated $(echo "$OUT" | grep -c 'ALERT REPEAT') times in 8s at REALERT_EVERY=2"

stop_server
rm -rf "$T"
echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
