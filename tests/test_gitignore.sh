#!/usr/bin/env bash
# Asserts the ignore rules that keep secrets and bulk out of git.
set -uo pipefail
cd "$(dirname "$0")/.."
PASS=0; FAIL=0
ok() { echo "  ok: $1"; PASS=$((PASS+1)); }
no() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

must_ignore() {
  if git check-ignore -q "$1"; then ok "ignored: $1"; else no "NOT ignored: $1"; fi
}
must_track() {
  if git check-ignore -q "$1"; then no "wrongly ignored: $1"; else ok "trackable: $1"; fi
}

must_ignore .env
must_ignore NostalgiaForInfinity/
must_ignore backups/db/20261115T025359Z/tradesv3.sqlite
must_ignore services/updater/state/previous_sha
must_ignore services/newsgate/state/log.jsonl
must_ignore user_data/khoakomlem_binance_futures-tradesv3.sqlite
must_ignore user_data/logs/bot.log
must_ignore user_data/data/
must_ignore user_data/nfix7-profit_max-khoakomlem-binance-USDT.json

must_track .env.example
must_track docker-compose.yml
must_track user_data/config.json
must_track user_data/strategies/NFIX7Risk.py
must_track services/updater/update.sh
must_ignore lab/data/
must_ignore lab/data-tsmom/
must_ignore lab/strategies-20260501/NostalgiaForInfinityX7.py
must_track lab/strategies-20260501/NFIX7Stop.py
must_track lab/matrices/h9-stop.tsv

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
