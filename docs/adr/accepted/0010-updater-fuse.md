# ADR-0010: Updater fuse: hold risk-setting changes, cap restarts, avoid fast markets

- Status: accepted
- Date: 2026-10-30
- Code: `internal/updater` (`risk.go`, `cycle.go`, `approval.go`, `approve.go`)
- Tests: `go test ./internal/updater`

## Context

Upstream commit `54ab6f2f6` (2026-04-19, 19 lines, "X7: use system_v3_2.") set
`system_v3_2_stops_enable = False` and reached real money unread. The import guard
(ADR-0002) cannot catch valid code that changes what the bot risks. Upstream also pushes
in bursts: 6 restarts on 2026-10-23, 4 between 01:21 and 02:36 UTC on the night of the
Binance hack.

## Decision

Before the working tree moves, compare between the running and the new commit every
class-level setting whose name matches `RISK_PATTERN` (stop, doom, lever, derisk, grind,
rebuy, buyback, stake, position_adjustment, max_entry, trailing, roi, system_) and the
membership of `NFI_SAFE_PARAMETERS`. Any difference holds the update, pages once with
the diff, and waits for `bin/approve-update.sh`, which approves that exact diff. With
`APPROVAL_TELEGRAM_TOKEN` set, a separate bot posts Approve / Keep-old buttons; in a
group only `APPROVER_IDS` count.

At most `MAX_RESTARTS_PER_DAY` (4) restarts per 24h. No restart while BTC's 30-minute
range is at or above `VOL_LIMIT_PCT` (2%), or when the price cannot be fetched.

## Evidence

Over the 755 X7 commits from 2026-07-30 to 2026-10-30 the pattern would have held 2,
on 2 days, including `54ab6f2f6`. A narrow stop/leverage-only pattern held 1.
