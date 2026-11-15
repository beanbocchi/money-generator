#!/usr/bin/env bash
# lab/queue.sh against a stubbed backtest.sh. Hermetic: no containers, no freqtrade.
# What matters is that it is resumable and that it never silently skips work -- a queue
# that quietly drops runs would corrupt a sweep in the direction of whatever ran.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/.." && pwd)
PASS=0; FAIL=0
ok() { echo "  ok: $1"; PASS=$((PASS+1)); }
no() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

setup() {
  local t; t=$(mktemp -d)
  mkdir -p "$t/lab/results" "$t/lab/logs"
  cp "$ROOT/lab/queue.sh" "$t/lab/"
  # stub backtest.sh: writes a minimal but real zip so the summary step exercises properly
  cat > "$t/lab/backtest.sh" <<'STUB'
#!/usr/bin/env bash
echo "$STRATDIR|$CONFIG|$EXTRA|${PAIRLIST:-}|$1|$2" >> "$(dirname "$0")/../calls.log"
[ "${STUB_FAIL:-0}" = "1" ] && exit 1
python3 - "$(dirname "$0")/results" "$1" <<'PY'
import json, sys, zipfile, os, time
out, strat = sys.argv[1], sys.argv[2]
os.makedirs(out, exist_ok=True)
name = f"backtest-result-{int(time.time()*1000)}.zip"
payload = {"strategy": {strat: {"total_trades": 7, "losses": 2,
                                "profit_total_abs": 12.5, "trades": []}}}
with zipfile.ZipFile(os.path.join(out, name), "w") as z:
    z.writestr("backtest-result-1.json", json.dumps(payload))
PY
STUB
  chmod +x "$t/lab/backtest.sh"
  : > "$t/calls.log"
  echo "$t"
}
# shift the directory off before writing, or it lands in the file as a bogus run whose
# name is a path -- which is how this helper originally inflated the failure count.
matrix() { local d=$1; shift; printf '%s\n' "$@" > "$d/lab/matrix.tsv"; sleep 0.05; }

T=$(setup)
matrix "$T" \
  "# a comment line that must be ignored" \
  "" \
  "$(printf 'runA\tNFIX7Risk\t20260604-20260730\tlab/sd\tlab/cfg\t--dry-run-wallet 780')" \
  "$(printf 'runB\tNFIX7Risk\t20260604-20260730\tlab/sd\tlab/cfg\t--dry-run-wallet 270')"

echo "case 1: runs every entry once and records a summary row each"
OUT=$( cd "$T" && bash lab/queue.sh lab/matrix.tsv 2>&1 )
[ -f "$T/lab/results/runA.zip" ] && ok "runA produced a named zip" || no "runA zip missing"
[ -f "$T/lab/results/runB.zip" ] && ok "runB produced a named zip" || no "runB zip missing"
[ "$(grep -c . "$T/calls.log")" = "2" ] \
  && ok "invoked backtest twice, comments and blanks ignored" \
  || no "invoked backtest $(grep -c . "$T/calls.log") times, expected 2"
grep -q 'lab/sd|lab/cfg|--dry-run-wallet 780' "$T/calls.log" \
  && ok "passed STRATDIR, CONFIG and EXTRA through" || no "did not pass the run's env through"
[ "$(grep -c . "$T/lab/results/index.tsv")" = "3" ] \
  && ok "index has a header plus one row per run" \
  || no "index has $(grep -c . "$T/lab/results/index.tsv") lines, expected 3"
grep -q $'runA\t7\t2\t12.50\t780' "$T/lab/results/index.tsv" \
  && ok "summary row carries trades, losses, total and wallet" \
  || no "summary row wrong: $(grep runA "$T/lab/results/index.tsv")"

echo "case 1b: a non-empty PAIRLIST column reaches backtest.sh"
# The original bug lived exactly here: every earlier matrix row left this column empty,
# so the expansion produced nothing and the broken assignment-prefix never executed.
T1b=$(setup)
matrix "$T1b" "$(printf 'runP\tNFIX7Risk\t2026\tlab/sd\tlab/cfg\t--wallet 780\tlab/pairlists/343.json')"
OUT=$( cd "$T1b" && bash lab/queue.sh lab/matrix.tsv 2>&1 )
grep -q 'lab/pairlists/343.json' "$T1b/calls.log" \
  && ok "PAIRLIST passed through to backtest.sh" \
  || no "PAIRLIST lost: $(cat "$T1b/calls.log")"
echo "$OUT" | grep -q 'No such file or directory' \
  && no "bash tried to execute the assignment as a command" \
  || ok "no assignment-prefix-as-command error"
[ -f "$T1b/lab/results/runP.zip" ] && ok "the run completed" || no "run did not complete"
rm -rf "$T1b"

echo "case 2: rerunning skips completed work rather than repeating it"
OUT=$( cd "$T" && bash lab/queue.sh lab/matrix.tsv 2>&1 )
[ "$(grep -c . "$T/calls.log")" = "2" ] \
  && ok "no backtest re-invoked on the second pass" \
  || no "re-ran completed entries ($(grep -c . "$T/calls.log") calls total)"
echo "$OUT" | grep -q '2 skipped' && ok "reported them as skipped" || no "did not report skips"

echo "case 3: a failing run is reported, not silently swallowed"
T2=$(setup)
matrix "$T2" "$(printf 'boom\tS\t2026\tsd\tcfg\t')"
OUT=$( cd "$T2" && STUB_FAIL=1 bash lab/queue.sh lab/matrix.tsv 2>&1 )
echo "$OUT" | grep -q 'FAILED boom' && ok "named the failing run" || no "failure not reported"
echo "$OUT" | grep -q '1 failed' && ok "counted it in the tally" || no "not counted as failed"
[ -f "$T2/lab/results/boom.zip" ] && no "left a zip for a failed run" || ok "no zip for a failed run"
[ -f "$T2/lab/logs/boom.log" ] && ok "kept the run's log for diagnosis" || no "discarded the log"

echo "case 4: a missing matrix file is an error, not an empty success"
T3=$(setup)
( cd "$T3" && bash lab/queue.sh lab/nope.tsv >/dev/null 2>&1 ); RC=$?
[ "$RC" -ne 0 ] && ok "exited non-zero for a missing matrix" || no "exited 0 with no matrix"

rm -rf "$T" "$T2" "$T3"
echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
