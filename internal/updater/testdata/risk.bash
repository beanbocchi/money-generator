# Frozen copy of risk_snapshot and risk_diff from services/updater/update.sh as it ran
# in production until 2026-11-21, when the updater moved to Go. risk_test.go checks
# that the Go snapshot, diff and digest match this byte for byte, because pending and
# approved digests written by the bash updater must still match after the switch.
risk_snapshot() {
  git -C "$NFI_DIR" show "$1:$STRATEGY_FILE" 2>/dev/null | awk -v cls="^class ${STRATEGY_FILE%.py}[(:]" -v pat="$RISK_PATTERN" '
    $0 ~ cls { inside = 1; next }
    inside && /^class / { exit }
    !inside { next }
    /NFI_SAFE_PARAMETERS = \[/ { safe = 1; next }
    safe {
      if ($0 ~ /^[ \t]*\]/) { safe = 0 }
      else if (match($0, /"[^"]+"/)) { print "NFI_SAFE_PARAMETERS has " substr($0, RSTART + 1, RLENGTH - 2) }
      next
    }
    # A setting whose value spans lines (grind_mode_coins = [ ... ]) is joined into one
    # line until its brackets balance, so a change inside the list is seen too.
    cont {
      line = line " " $0; depth += gsub(/[[({]/, "&") - gsub(/[])}]/, "&")
      if (depth <= 0) { cont = 0; gsub(/[ \t]+/, " ", line); print line }
      next
    }
    /^  [A-Za-z_][A-Za-z0-9_]* = / {
      if ($1 !~ pat) next
      sub(/^ +/, ""); depth = gsub(/[[({]/, "&") - gsub(/[])}]/, "&")
      if (depth > 0) { cont = 1; line = $0 } else print
    }
  ' | LC_ALL=C sort
}
risk_diff() {
  local a b
  a=$(mktemp); b=$(mktemp)
  risk_snapshot "$1" > "$a"; risk_snapshot "$2" > "$b"
  LC_ALL=C comm -3 "$a" "$b" | awk -F'\t' '{ if ($1 != "") print "- " $1; else print "+ " $2 }'
  rm -f "$a" "$b"
}
