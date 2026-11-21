# ADR-0009: Liveness watchdog on /api/v1/health and exchange access

- Status: accepted
- Date: 2026-07-31; credentials check 2026-08-05; re-paging 2026-10-30
- Code: `internal/watchdog`
- Tests: `go test ./internal/watchdog`

## Context

freqtrade's Telegram reports trades only, so a quiet day and a dead bot look the same.
`restart: unless-stopped` covers crashes, and the healthcheck detects but acts on
nothing.

## Decision

Poll `/api/v1/health` every 60s and page on Telegram when the API does not answer or
`last_process_ts` is older than 900s, after 15 consecutive failures. Every 5th poll
also calls `/api/v1/balance`, which needs signed exchange access, and pages separately
after 3 failures. While a check keeps failing, the page repeats every 3h. A page counts
as sent only when Telegram accepts it. Log parsing, rule engines and metrics are out of
scope; the old setup's 736-line watchdog had them.

## Evidence

- Debounce: 3 polls paged 15 times in two days, all on the updater's own restarts,
  which take 301-331s.
- 2026-08-05: the Binance key was rejected for 4h44m after the home IP changed. Health
  stayed 200; the watchdog paged a stalled loop after 1776s, which is what MAX_AGE 900
  plus 15 polls predicts, but could not say why. The balance check was added for that.
- 2026-10-05: an IP allow-list outage ran 4.6 days on one page. 2026-10-12: the only
  page for an outage failed to send and was never retried. Re-paging and send
  confirmation were added for those.
