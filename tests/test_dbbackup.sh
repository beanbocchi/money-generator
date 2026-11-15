#!/usr/bin/env bash
# services/dbbackup/backup.sh guards the only record of which real positions are open, so its
# behaviour is checked here: consistent snapshot while a writer holds the WAL, integrity
# verification, extra files carried along, and pruning that keeps the newest.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT="$HERE/../services/dbbackup/backup.sh"
PASS=0; FAIL=0
ok() { echo "  ok: $1"; PASS=$((PASS+1)); }
no() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

T=$(mktemp -d)
DB="$T/live.sqlite"
# Build a WAL-mode database with an open writer connection still holding uncommitted-ish
# state, which is what a plain cp gets wrong.
python3 - "$DB" <<'PY'
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
c.execute("pragma journal_mode=wal")
c.execute("create table trades (id integer primary key, pair text, is_open int)")
c.executemany("insert into trades values (?,?,?)",
              [(1, "ZEC/USDT:USDT", 0), (2, "APE/USDT:USDT", 1), (3, "UAI/USDT:USDT", 1)])
c.commit()
PY
echo "profit-cache" > "$T/nfi-profit_max.json"

run() { env DB="$DB" OUT_DIR="$T/out" KEEP="${KEEP:-30}" RUN_ONCE=1 \
            EXTRA_FILES="$T/nfi-profit_max.json" bash "$SCRIPT" 2>&1; }

echo "case 1: snapshot is consistent and complete"
OUT=$(run)
SNAP=$(find "$T/out" -mindepth 1 -maxdepth 1 -type d | head -1)
[ -n "$SNAP" ] && ok "created a snapshot directory" || no "no snapshot directory"
[ -f "$SNAP/live.sqlite" ] && ok "copied the database" || no "database missing from snapshot"
N=$(python3 -c "
import sqlite3;c=sqlite3.connect('$SNAP/live.sqlite');print(c.execute('select count(*) from trades').fetchone()[0])" 2>/dev/null)
[ "$N" = "3" ] && ok "all 3 rows present in the snapshot" || no "snapshot has '$N' rows, expected 3"
OPEN=$(python3 -c "
import sqlite3;c=sqlite3.connect('$SNAP/live.sqlite');print(c.execute('select count(*) from trades where is_open=1').fetchone()[0])" 2>/dev/null)
[ "$OPEN" = "2" ] && ok "open-trade rows preserved" || no "snapshot has '$OPEN' open trades, expected 2"
[ -f "$SNAP/nfi-profit_max.json" ] && ok "carried the extra file along" || no "extra file not copied"
echo "$OUT" | grep -q 'wrote ' && ok "logged the write" || no "did not log the write"

echo "case 2: snapshot survives integrity_check"
python3 -c "
import sqlite3;c=sqlite3.connect('file:$SNAP/live.sqlite?mode=ro',uri=True)
assert c.execute('pragma integrity_check').fetchone()[0]=='ok'" 2>/dev/null \
  && ok "integrity_check ok" || no "snapshot failed integrity_check"

echo "case 3: pruning keeps the newest KEEP directories"
for i in 1 2 3 4 5; do mkdir -p "$T/out/2020010${i}T000000Z"; done
KEEP=3 run >/dev/null
LEFT=$(find "$T/out" -mindepth 1 -maxdepth 1 -type d | wc -l)
[ "$LEFT" = "3" ] && ok "pruned down to KEEP=3" || no "left $LEFT directories, expected 3"
# 6 dirs existed (case 1's snapshot + the 5 stubs); this run adds a 7th and prunes 4,
# so 20200101..04 go and 20200105 is the oldest survivor.
find "$T/out" -mindepth 1 -maxdepth 1 -type d | sort | head -1 | grep -q '20200105' \
  && ok "kept the newest, dropped the oldest" || no "pruned the wrong end"

echo "case 3b: two snapshots in the same second must both survive"
S1=$(env DB="$DB" OUT_DIR="$T/out2" RUN_ONCE=1 bash "$SCRIPT" 2>&1 | grep -oE '/[^ ]*out2/[^ ]*')
S2=$(env DB="$DB" OUT_DIR="$T/out2" RUN_ONCE=1 bash "$SCRIPT" 2>&1 | grep -oE '/[^ ]*out2/[^ ]*')
N2=$(find "$T/out2" -mindepth 1 -maxdepth 1 -type d | wc -l)
[ "$N2" = "2" ] && ok "same-second snapshots both kept ($N2)" \
  || no "same-second collision destroyed one: $N2 dirs from 2 runs"

echo "case 4: a missing database fails loudly and leaves no partial snapshot"
BEFORE=$(find "$T/out" -mindepth 1 -maxdepth 1 -type d | wc -l)
env DB="$T/nope.sqlite" OUT_DIR="$T/out" RUN_ONCE=1 bash "$SCRIPT" 2>&1 | grep -q 'BACKUP FAILED' \
  && ok "reported failure for a missing database" || no "silent on a missing database"
AFTER=$(find "$T/out" -mindepth 1 -maxdepth 1 -type d | wc -l)
[ "$BEFORE" = "$AFTER" ] && ok "left no partial snapshot behind" || no "left a partial snapshot"

rm -rf "$T"
echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
