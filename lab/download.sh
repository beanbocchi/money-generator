#!/usr/bin/env bash
# Download Binance futures candles for backtesting into lab/data.
#
# Usage:  ./lab/download.sh                                   # the live-traded pairs
#         PAIRLIST=lab/pairlists/343.json ./lab/download.sh   # another universe
#
# Deliberately NOT into user_data/data: that directory is bind-mounted into the live
# trading container, and a multi-gigabyte download plus freqtrade's own writes have no
# business sharing a directory with a running real-money bot.
#
# Two passes with different lead-ins, because NFI's indicators need very different
# amounts of history per timeframe. Its 1d informative carries long moving averages,
# so it needs ~200 days BEFORE the backtest start or the first months of any backtest
# are computed on incomplete indicators and silently wrong. 5m only needs
# startup_candle_count (800 candles = ~2.8 days), so paying for 18 months of 5m data
# would be waste.
#
# TRAP: freqtrade will not backfill earlier data into an existing file. If a pair was
# ever downloaded with a later --timerange, a subsequent run with an earlier one only
# tops up forward and logs "Requested start date ... earlier than local data start
# date. Use `--prepend`" at INFO level, which is easy to miss in thousands of lines.
# It silently leaves that pair short. Verify with `freqtrade list-data --show-timerange`
# after any partial download, and repair with --erase (or --prepend) for those pairs.
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE="${IMAGE:-freqtradeorg/freqtrade:stable}"
SLOW_FROM="${SLOW_FROM:-20250101-}"   # 1d, 4h  -- long lead-in, cheap
FAST_FROM="${FAST_FROM:-20260101-}"   # 1h, 15m, 5m, 1m -- short lead-in, expensive
PAIRLIST="${PAIRLIST:-lab/pairlists/live-traded.json}"

mapfile -t PAIRS < <(python3 -c "
import json,sys; print('\n'.join(json.load(open('$PAIRLIST'))['exchange']['pair_whitelist']))")
echo "==> ${#PAIRS[@]} pairs from $PAIRLIST"

dl() {
  local tfs="$1" from="$2"
  echo "==> downloading [$tfs] from $from"
  # No API keys are mounted: candle, mark and funding-rate history are public
  # endpoints. Keeping credentials out means this cannot touch the account.
  docker run --rm -u "$(id -u):$(id -g)" \
    -v "$PWD/lab/data:/freqtrade/user_data/data" \
    "$IMAGE" download-data \
      --exchange binance --trading-mode futures \
      --pairs "${PAIRS[@]}" \
      --timeframes $tfs --timerange "$from"
}

dl "1d 4h"      "$SLOW_FROM"
dl "1h 15m 5m"  "$FAST_FROM"
# 1m feeds --timeframe-detail 1m, which every matrix uses: NFI grinds and derisks inside
# the candle. It is about five times the volume of 5m. SKIP_1M=1 leaves it out.
[ "${SKIP_1M:-0}" = "1" ] || dl "1m" "$FAST_FROM"

echo
echo "==> done. $(find lab/data -type f | wc -l) files, $(du -sh lab/data | cut -f1)"
