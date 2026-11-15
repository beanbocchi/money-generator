#!/usr/bin/env bash
# Run an NFI backtest against lab/data, writing results to lab/results.
#
# Usage:  ./lab/backtest.sh [STRATEGY] [TIMERANGE]
#   ./lab/backtest.sh NFIX7Risk 20260228-20260716
#   ./lab/backtest.sh NostalgiaForInfinityX7 20260228-20260716
#
# Runs a throwaway container with its own datadir and result dir. The live bot's
# user_data is mounted READ-ONLY and only for the strategy files -- a backtest has no
# business being able to write near a live trade database.
set -euo pipefail
cd "$(dirname "$0")/.."

STRATEGY="${1:-NFIX7Risk}"
TIMERANGE="${2:-20260228-20260716}"
IMAGE="${IMAGE:-freqtradeorg/freqtrade:stable}"
NFI="NostalgiaForInfinity"
EXTRA="${EXTRA:-}"
# DATADIR exists so a backtest can run against a snapshot while a download is writing
# to lab/data. freqtrade rewrites feather files whole, so reading one mid-write can
# yield a truncated frame -- and a short frame is not always an error, it can just be
# silently less data than you think.
DATADIR="${DATADIR:-lab/data}"
CONFIG="${CONFIG:-lab/config-backtest.json}"
# STRATDIR lets a run use a historical NostalgiaForInfinityX7.py. Upstream made 995
# commits to that file in two months, 564 of them adding protections; backtesting
# today's version over the period those protections were written for is testing a
# strategy on the data used to tune it.
STRATDIR="${STRATDIR:-user_data/strategies}"
# PAIRLIST exists so a run can target a different universe. Acceptance rule 4 requires an
# effect to hold on both the 97 pairs the live bot traded and upstream's 343-pair static
# list; only 43 pairs overlap, so these are genuinely different universes.
PAIRLIST="${PAIRLIST:-lab/pairlists/live-traded.json}"

mkdir -p lab/results lab/logs
OUT="lab/results/${STRATEGY}-${TIMERANGE}.json"

echo "==> $STRATEGY over $TIMERANGE"

# Configs come from lab/configs-pinned/, not from the live NostalgiaForInfinity clone.
# The updater runs `git reset --hard` on that clone every 15 minutes, so an update landing
# between arm A and arm B of a comparison would silently give the two arms different
# blacklists, pairlists or exampleconfig -- measuring two things with two different rulers
# and calling it an A/B. Refresh the pinned copies deliberately, never incidentally.
#
# Note on what is deliberately NOT passed:
#   configs/blacklist-binance.json -- today's blacklist encodes which pairs later blew
#   up. Applying it to 2026 history is lookahead bias, and tail risk is exactly what
#   this rig exists to measure. Upstream's own test recipe does include it; for
#   measuring how bad the bad days were, that would quietly delete the evidence.
# --oom-score-adj: a run peaks at ~13GB on a host that also carries the live bot and ~25
# other containers with no memory limits. If the kernel has to kill something, make it this.
docker run --rm -u "$(id -u):$(id -g)" --oom-score-adj "${BT_OOM_SCORE_ADJ:-1000}" \
  -v "$PWD/$DATADIR:/freqtrade/user_data/data" \
  -v "$PWD/lab/results:/freqtrade/user_data/backtest_results" \
  -v "$PWD/lab/logs:/freqtrade/user_data/logs" \
  -v "$PWD/$CONFIG:/freqtrade/lab-config.json:ro" \
  -v "$PWD/$PAIRLIST:/freqtrade/lab-pairlist.json:ro" \
  -v "$PWD/$STRATDIR:/freqtrade/user_data/strategies:ro" \
  -v "$PWD/$NFI:/freqtrade/$NFI:ro" \
  -v "$PWD/lab/configs-pinned:/freqtrade/pinned:ro" \
  -e COLUMNS=200 \
  -e NFI_MAX_DEPLOYED \
  "$IMAGE" backtesting \
    --strategy "$STRATEGY" \
    --timerange "$TIMERANGE" \
    --config /freqtrade/pinned/trading_mode-futures.json \
    --config /freqtrade/pinned/exampleconfig.json \
    --config /freqtrade/lab-pairlist.json \
    --config /freqtrade/lab-config.json \
    --cache none --breakdown day \
    --export signals \
    --export-filename "user_data/backtest_results/${STRATEGY}-${TIMERANGE}.json" \
    $EXTRA

echo
echo "==> results: $OUT"
