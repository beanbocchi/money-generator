#!/usr/bin/env bash
# ADR-0027: the news-event gate in shadow mode. A standalone container, deliberately not
# in docker-compose.yml: it never talks to the bot, so it has no business in the stack
# that carries real money, and starting or stopping it cannot restart anything there.
#
# Usage:  bin/newsgate.sh start | stop | once | logs | windows
#
# Calls Claude through the local proxy on 127.0.0.1:8317, hence --network host. The
# token comes from NEWSGATE_ANTHROPIC_AUTH_TOKEN in .env and is passed by name, so it
# never appears in `ps` or `docker inspect` argv.
set -euo pipefail
cd "$(dirname "$0")/.."

NAME=bottrade-newsgate
IMAGE=money-generator-newsgate
STATE=services/newsgate/state
mkdir -p "$STATE"

run() {
  TOKEN=$(grep -E '^NEWSGATE_ANTHROPIC_AUTH_TOKEN=' .env | cut -d= -f2- || true)
  [ -n "$TOKEN" ] || { echo "NEWSGATE_ANTHROPIC_AUTH_TOKEN is not set in .env"; exit 2; }
  BASE=$(grep -E '^NEWSGATE_ANTHROPIC_BASE_URL=' .env | cut -d= -f2- || true)
  export ANTHROPIC_AUTH_TOKEN="$TOKEN" ANTHROPIC_BASE_URL="${BASE:-http://127.0.0.1:8317}"
  docker build -q -t "$IMAGE" services/newsgate >/dev/null
  docker run "$@" --network host -u "$(id -u):$(id -g)" \
    -e ANTHROPIC_AUTH_TOKEN -e ANTHROPIC_BASE_URL \
    -v "$PWD/$STATE:/state" "$IMAGE" ${ARGS:-}
}

case "${1:-}" in
  start)   docker rm -f "$NAME" >/dev/null 2>&1 || true
           run -d --name "$NAME" --restart unless-stopped --memory 256m
           echo "started $NAME; log at $STATE/log.jsonl" ;;
  stop)    docker rm -f "$NAME" ;;
  once)    ARGS=--once run --rm ;;
  logs)    tail -n "${2:-30}" "$STATE/log.jsonl" ;;
  windows) python3 - "$STATE/log.jsonl" <<'PY'
import json, sys
from datetime import datetime, timezone
now = datetime.now(timezone.utc)
for line in open(sys.argv[1]):
    r = json.loads(line)
    w = r.get("window")
    if w:
        live = datetime.fromisoformat(w["until"]) > now
        print(f"{'OPEN  ' if live else 'closed'} {w['from']} {','.join(w['targets']):12s} "
              f"sev{r['severity']} {r['event_type']:17s} {r['title'][:70]}")
PY
           ;;
  *)       sed -n '2,11p' "$0"; exit 2 ;;
esac
