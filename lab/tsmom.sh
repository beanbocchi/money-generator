#!/usr/bin/env bash
# ADR-0026: the trend baseline. Its own data directory, so a download never rewrites a
# feather file that a running NFI backtest is reading from lab/data.
#
# Usage:  lab/tsmom.sh download     # BTC and ETH daily candles and funding since 2019-12
#         lab/tsmom.sh test         # the script's self-test
#         lab/tsmom.sh run          # report and verdict, also saved to lab/results/
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE="${IMAGE:-freqtradeorg/freqtrade:stable}"
DATA=lab/data-tsmom
mkdir -p "$DATA" lab/results

# pandas lives in the image's user site, so no -I (it implies -s); -P and -E keep the
# working directory and PYTHON* variables out of the import path instead.
py() {
  docker run --rm -u "$(id -u):$(id -g)" -w /tmp \
    -v "$PWD/lab/tsmom.py:/code/tsmom.py:ro" \
    -v "$PWD/$DATA:/data:ro" \
    --entrypoint python3 "$IMAGE" -P -E /code/tsmom.py "$@"
}

case "${1:-}" in
  download)
    # Public endpoints only, no keys mounted. Futures mode also fetches funding and mark.
    docker run --rm -u "$(id -u):$(id -g)" \
      -v "$PWD/$DATA:/freqtrade/user_data/data" \
      "$IMAGE" download-data \
        --exchange binance --trading-mode futures \
        --pairs BTC/USDT:USDT ETH/USDT:USDT \
        --timeframes 1d --timerange 20191201-
    ;;
  test) py --self-test ;;
  run)  py /data | tee lab/results/tsmom-report.txt ;;
  *)    sed -n '2,8p' "$0"; exit 2 ;;
esac
