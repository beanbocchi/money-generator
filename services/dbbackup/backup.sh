#!/usr/bin/env bash
# Periodic consistent snapshots of the live trade database.
#
# The sqlite file is the only record of which real positions are open. Losing it does not
# lose money directly, but it loses the bot's ability to reconcile with the exchange:
# freqtrade would start believing it holds nothing while the account holds real futures
# positions. That is worth a few hundred kilobytes a day.
#
# Uses sqlite3's online backup API (via python, which is in the freqtrade image) rather
# than cp. The bot writes in WAL mode, so a plain copy can catch a torn state: the main
# file and the -wal can disagree. The backup API takes a consistent snapshot of a live
# database without stopping the writer, which is exactly the situation here.
set -uo pipefail

DB="${DB:?set DB to the sqlite path}"
OUT_DIR="${OUT_DIR:-/backups}"
KEEP="${KEEP:-30}"
INTERVAL="${INTERVAL:-21600}"     # 6h
EXTRA_FILES="${EXTRA_FILES:-}"    # e.g. the nfi profit-target cache
RUN_ONCE="${RUN_ONCE:-0}"

log() { echo "[dbbackup $(date -u +%FT%TZ)] $*"; }

snapshot() {
  local stamp out tmp
  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  out="$OUT_DIR/$stamp"

  # Write to a private temp directory and rename into place only once verified.
  #
  # Not cosmetic. The first version wrote straight into "$out" and removed "$out" on
  # failure, and a failing run landing in the SAME SECOND as a previous good snapshot
  # resolved "$out" to that existing directory -- mkdir -p accepted it and the cleanup
  # deleted a good backup. A backup script whose error path destroys backups is worse
  # than no backup script. Caught by tests/test_dbbackup.sh case 4.
  tmp="$OUT_DIR/.tmp-$$-$stamp"
  rm -rf "$tmp"
  mkdir -p "$tmp" || { log "cannot create $tmp"; return 1; }

  if ! python3 - "$DB" "$tmp/$(basename "$DB")" <<'PY'
import sqlite3, sys
src, dst = sys.argv[1], sys.argv[2]
s = sqlite3.connect(f"file:{src}?mode=ro", uri=True)
d = sqlite3.connect(dst)
with d:
    s.backup(d)          # online backup: consistent even while the bot writes
n = d.execute("select count(*) from trades").fetchone()[0]
o = d.execute("select count(*) from trades where is_open=1").fetchone()[0]
d.close(); s.close()
print(f"{n} trades, {o} open", file=sys.stderr)
PY
  then
    log "BACKUP FAILED for $DB"
    rm -rf "$tmp"
    return 1
  fi

  for f in $EXTRA_FILES; do
    [ -f "$f" ] && cp -p "$f" "$tmp/" || true
  done

  # Verify the snapshot opens and its schema is intact before trusting it.
  if ! python3 -c "
import sqlite3,sys
c=sqlite3.connect('file:$tmp/$(basename "$DB")?mode=ro',uri=True)
assert c.execute('pragma integrity_check').fetchone()[0]=='ok'
c.execute('select id,pair,is_open from trades limit 1')" 2>/dev/null; then
    log "snapshot failed integrity_check, discarding"
    rm -rf "$tmp"
    return 1
  fi

  # Never clobber an existing snapshot: if this second already has one, keep both.
  while [ -e "$out" ]; do out="${out%Z}-$RANDOM Z"; out="${out// /}"; done
  if ! mv "$tmp" "$out"; then
    log "could not move snapshot into place, discarding"
    rm -rf "$tmp"
    return 1
  fi

  log "wrote $out ($(du -sh "$out" | cut -f1))"

  # Prune oldest, keeping KEEP. Sorted lexically, which is why the stamp is ISO-ish.
  local n
  n=$(find "$OUT_DIR" -mindepth 1 -maxdepth 1 -type d | wc -l)
  if [ "$n" -gt "$KEEP" ]; then
    find "$OUT_DIR" -mindepth 1 -maxdepth 1 -type d | sort | head -n "$((n - KEEP))" \
      | while read -r old; do log "pruning $old"; rm -rf "$old"; done
  fi
}

log "started: db=$DB out=$OUT_DIR keep=$KEEP interval=${INTERVAL}s"
while true; do
  snapshot
  [ "$RUN_ONCE" = "1" ] && break
  sleep "$INTERVAL"
done
