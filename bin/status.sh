#!/usr/bin/env bash
# One command that prints everything needed to judge whether the bot is healthy and what
# its positions are doing. Written so a periodic check costs one call instead of six, and
# so it survives a context compaction: the knowledge lives here, not in a conversation.
#
# Grinding is counted from filled ENTRY ORDERS, never from max_stake_amount > stake_amount.
# That comparison is peak-versus-current stake, so it only fires when a position partially
# exits after growing; a trade that adds then closes in one exit looks like a single entry.
# Using it produced this project's worst error -- a "grinding is capital-gated" conclusion
# built on 0 of 194 trades where the true count was 58 of 194. See docs/adr/rejected/0015-*.
set -uo pipefail
cd "$(dirname "$0")/.."

U=$(grep '^FREQTRADE__API_SERVER__USERNAME=' .env | cut -d= -f2-)
P=$(grep '^FREQTRADE__API_SERVER__PASSWORD=' .env | cut -d= -f2-)
PORT=$(grep '^FREQTRADE__API_SERVER__LISTEN_PORT=' .env | cut -d= -f2-)
API="localhost:${PORT:-8989}/api/v1"
DB="user_data/$(grep '^FREQTRADE__BOT_NAME=' .env | cut -d= -f2-)_$(grep '^FREQTRADE__EXCHANGE__NAME=' .env | cut -d= -f2-)_$(grep '^FREQTRADE__TRADING_MODE=' .env | cut -d= -f2-)-tradesv3.sqlite"

echo "=== $(date -u '+%F %T') UTC / $(date '+%F %T') local ==="

echo "-- infrastructure"
docker compose ps --format '{{.Name}}\t{{.Status}}' 2>/dev/null | sed 's/^/   /'
for e in health status balance profit; do
  printf '   %-8s HTTP %s\n' "$e" "$(curl -s -o /dev/null -m 15 -w '%{http_code}' -u "$U:$P" "$API/$e")"
done
curl -s -m 15 -u "$U:$P" "$API/health" 2>/dev/null | python3 -c "
import json,sys,datetime as dt
try:
    v=json.load(sys.stdin).get('last_process_ts')
    age=int(dt.datetime.now(dt.timezone.utc).timestamp())-v if v else None
    print(f'   last_process age {age}s' + ('   <-- STALE, expect a pairlist refresh or a real stall' if age and age>900 else ''))
except Exception: print('   last_process: unreadable')"
echo "   watchdog alerts: $(docker logs bottrade-watchdog 2>&1 | grep -c 'ALERT:')   updater restarts: $(docker logs bottrade-updater 2>&1 | grep -c 'restarted ')"
[ -s services/updater/state/pending_risk ] && echo "   updater: HOLDING an update that changes risk settings -> review with bin/approve-update.sh"
docker inspect -f '{{.State.Running}}' bottrade-updater 2>/dev/null | grep -q true || echo "   updater: NOT RUNNING (auto-updates off)"
echo "   bot ERROR lines (last 500): $(tail -500 user_data/logs/bot.log 2>/dev/null | grep -c ' - ERROR - ')"
echo "   db backups: $(find backups/db -mindepth 1 -maxdepth 1 -type d 2>/dev/null | wc -l)   newest: $(find backups/db -mindepth 1 -maxdepth 1 -type d 2>/dev/null | sort | tail -1 | sed 's|.*/||')"

echo "-- account"
curl -s -m 15 -u "$U:$P" "$API/balance" 2>/dev/null > /tmp/st_bal.json
curl -s -m 15 -u "$U:$P" "$API/status"  2>/dev/null > /tmp/st_pos.json
python3 - "$DB" <<'PY'
import json, sqlite3, sys
try:
    b = json.load(open("/tmp/st_bal.json")); print(f"   balance {round(b['total'],2)} USDT")
except Exception: print("   balance: unavailable (check exchange credentials / IP allow-list)")
c = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True); c.row_factory = sqlite3.Row
T = [dict(r) for r in c.execute("select * from trades where is_open=0")]
p = [t["close_profit_abs"] for t in T]
w = [x for x in p if x > 0]
print(f"   closed {len(T)}  realised {sum(p):+.2f}  wins {len(w)}/{len(T)} ({len(w)/max(1,len(T)):.1%})"
      f"  worst {min(p) if p else 0:+.2f}")
try:
    pos = json.load(open("/tmp/st_pos.json"))
except Exception:
    pos = []
bk_tot = sum((t.get("realized_profit") or 0.0) for t in pos)
un_tot = sum(t["profit_abs"] for t in pos)
print(f"   open {len(pos)}  open-leg {un_tot:+.2f}  booked-inside-open {bk_tot:+.2f}"
      f"  ALL-IN {sum(p)+bk_tot+un_tot:+.2f}   <- matches freqtrade profit_all_coin")
for t in sorted(pos, key=lambda x: x["profit_abs"]):
    tid = t["trade_id"]
    ent = c.execute("select count(*) from orders where ft_trade_id=? and status='closed' "
                    "and ft_order_side=?", (tid, "sell" if t.get("is_short") else "buy")).fetchone()[0]
    lp, cr = t.get("liquidation_price"), t.get("current_rate")
    gap = ((lp-cr)/cr*100) if (lp and cr and t.get("is_short")) else (((cr-lp)/cr*100) if lp and cr else 0)
    flag = ""
    if gap and abs(gap) < 15: flag += "  <-- LIQUIDATION NEAR"
    if ent > 1: flag += f"  ground({ent} entries)"
    # realized_profit is money ALREADY LOST OR BANKED inside a still-open position, from
    # partial exits during grinding. It is not unrealised and it is not in profit_closed
    # either, so printing only profit_abs hides it: on 2026-08-19 trade #238 showed an
    # open leg of -1.63 while having already booked -17.91.
    booked = t.get("realized_profit") or 0.0
    bk = f" booked {booked:+.2f}" if abs(booked) >= 0.01 else ""
    print(f"     #{tid} {t['pair']:<18} {'S' if t.get('is_short') else 'L'} stake {t['stake_amount']:6.2f}"
          f" {t['profit_abs']:+8.2f} ({t['profit_ratio']*100:+6.1f}%) liq {gap:+.0f}%"
          f"{bk} open {t['open_date'][:16]}{flag}")
print("   last 5 closed:")
for t in sorted(T, key=lambda x: x["close_date"])[-5:]:
    print(f"     #{t['id']} {t['pair']:<18} {t['close_date'][:16]} {t['close_profit_abs']:+7.2f}"
          f"  {(t['exit_reason'] or '')[:30]}")
PY

echo "-- lab"
# Tail whichever queue log is newest, not a hardcoded one: the first version pointed at
# one log and kept reporting a six-hour-old line while a different queue ran.
newest_q=$(ls -t lab/logs/queue-*.log 2>/dev/null | head -1)
[ -n "$newest_q" ] && printf '   %s: %s\n' "$(basename "$newest_q")" "$(tail -1 "$newest_q")"
pgrep -f 'lab/queue.sh' >/dev/null && echo "   queue: RUNNING" || echo "   queue: idle"
df -h /home | tail -1 | awk '{print "   disk free: "$4}'
