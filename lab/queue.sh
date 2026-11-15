#!/usr/bin/env bash
# Runs a matrix of backtests one at a time, unattended and resumable.
#
# Serial on purpose. The machine has 19GB of RAM with ~8.6GB free, and its 35GB of
# "swap" is mostly /dev/zram0 -- compressed RAM, which does not add capacity because the
# compressed pages still occupy physical RAM. Only the 16GB /swapfile is real extra
# space, and it is disk. One 1m-detail backtest fits in the current headroom; two would
# push a live real-money bot into swap, and a stalled trading loop is exactly the failure
# we spent this project learning to detect.
#
# What this buys is not speed, it is unattended throughput: the real constraint on every
# conclusion in docs/adr/ is the number of tail events observed, and the only way
# to raise that is to run many more configurations. Launching them one at a time by hand
# is what limited this to four wallet sizes.
#
# Matrix format, one run per line, '#' comments and blank lines ignored:
#   NAME<TAB>STRATEGY<TAB>TIMERANGE<TAB>STRATDIR<TAB>CONFIG<TAB>EXTRA<TAB>PAIRLIST
# PAIRLIST is optional; blank falls back to backtest.sh's default universe.
# NAME must be unique; a run whose lab/results/NAME.zip already exists is skipped, so the
# queue can be killed and restarted without repeating work.
set -uo pipefail
cd "$(dirname "$0")/.."

MATRIX="${1:?usage: lab/queue.sh lab/matrices/<name>.tsv}"
INDEX="${INDEX:-lab/results/index.tsv}"
[ -f "$MATRIX" ] || { echo "no matrix file: $MATRIX"; exit 2; }
mkdir -p lab/results lab/logs
# Every queue keeps its own log under lab/logs, which status.sh tails.
exec > >(tee -a "lab/logs/queue-$(basename "$MATRIX" .tsv).log") 2>&1

log() { echo "[queue $(date -u +%FT%TZ)] $*"; }

[ -f "$INDEX" ] || printf 'name\ttrades\tlosses\taccount_total\twallet\tstrategy\ttimerange\n' > "$INDEX"

total=0; ran=0; skipped=0; failed=0
while IFS=$'\t' read -r name strategy timerange stratdir config extra pairlist; do
  case "${name:-}" in ''|'#'*) continue ;; esac
  total=$((total + 1))
  if [ -f "lab/results/$name.zip" ]; then
    log "skip $name (already present)"
    skipped=$((skipped + 1))
    continue
  fi

  log "run $name: $strategy $timerange"
  # `env` is load-bearing: an assignment prefix must be a literal word, so a
  # ${pairlist:+PAIRLIST=...} produced by expansion lands in the command position and bash
  # tries to execute a program by that name. That failed all 8 universe-2 runs, and the
  # tests missed it because every matrix row they used left PAIRLIST empty.
  if env STRATDIR="$stratdir" CONFIG="$config" EXTRA="$extra" \
       ${pairlist:+PAIRLIST="$pairlist"} \
       ./lab/backtest.sh "$strategy" "$timerange" > "lab/logs/$name.log" 2>&1; then
    # backtest.sh leaves a timestamped zip; claim it under the run's name
    newest=$(find lab/results -maxdepth 1 -name 'backtest-result-*.zip' -newer "$MATRIX" \
             -printf '%T@ %p\n' 2>/dev/null | sort -rn | head -1 | cut -d' ' -f2-)
    if [ -n "$newest" ]; then
      mv "$newest" "lab/results/$name.zip"
      ran=$((ran + 1))
      # one summary line per run so a sweep can be read without unzipping anything
      python3 - "lab/results/$name.zip" "$name" "$strategy" "$timerange" "$extra" >> "$INDEX" <<'PY'
import json, sys, zipfile, re, io
z, name, strat, tr, extra = sys.argv[1:6]
with zipfile.ZipFile(z) as f:
    j = [n for n in f.namelist() if re.search(r'\d\.json$', n)][0]
    s = list(json.load(io.TextIOWrapper(f.open(j)))["strategy"].values())[0]
m = re.search(r'--dry-run-wallet\s+(\S+)', extra)
print("\t".join([name, str(s.get("total_trades")), str(s.get("losses")),
                 f"{s.get('profit_total_abs', 0):.2f}", m.group(1) if m else "",
                 strat, tr]))
PY
      log "  done $name -> lab/results/$name.zip"
    else
      log "  WARNING $name produced no zip"
      failed=$((failed + 1))
    fi
  else
    log "  FAILED $name (see lab/logs/$name.log)"
    failed=$((failed + 1))
  fi
done < "$MATRIX"

log "matrix complete: $total entries, $ran ran, $skipped skipped, $failed failed"
log "summary table: $INDEX"
