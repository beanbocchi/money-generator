# ADR-0028: Deploy the H9 hard stop at 0.55 of margin, for new trades only

- Status: proposed; built and checked 2026-11-24, waiting for the owner's go-ahead to restart the bot
- Date: 2026-11-24
- Code: `lab/strategies-20260929-hardstop/NFIX7Risk.py` (moves to `user_data/strategies/NFIX7Risk.py` on deploy)
- Tests: `lab/strategies-20260929-hardstop/pending-tests/` (move to `internal/repo/testdata/` on deploy; run by `go test ./internal/repo`)
- Lab: `lab/matrices/adr28-check.tsv`, `lab/strategies-20260929-hardstop/NFIX7HardStopCheck.py`, result `hardstop-check-20261001`
- Related: ADR-0020 (H9, the level), ADR-0003 (2x leverage), ADR-0013 (pause switch)

## Context

ADR-0020's rule accepted a stop at 0.55 and 0.70 of margin in all three backtest windows
and recommends 0.55, a 27.5% adverse move from the first entry at 2x. The live bot has no
stop but liquidation. On 2026-11-24 it held 9 longs, all ground, with -199.46 already
booked inside them and an all-in of -86.16 for the 2x era.

Two facts about freqtrade 2026.1 decide how the stop can be deployed:

- Setting the class `stoploss` to -0.55 would not reproduce the backtest for open trades.
  On restart freqtrade's stoploss reinitialisation re-bases every open trade's stop on its
  current average entry, which after grinding is far below the first entry the backtest
  used (KAITO: 0.130 against 0.358).
- When `custom_stoploss` raises a stop above the initial -0.99, freqtrade names the exit
  `trailing_stop_loss`. NFI's `confirm_trade_exit` refuses both `stop_loss` and
  `trailing_stop_loss` unless the price is past liquidation, so the overlay must let those
  exits through.

## Decision

`NFIX7Risk` gains:

- `custom_stoploss` returning a stop at `first_entry × (1 − 0.55 / leverage)` for longs
  (mirrored for shorts), where the first entry is the price of the trade's first filled
  entry order. Grind adds below it cannot move it, because freqtrade only ever raises a
  long's stop.
- `confirm_trade_exit` that returns True for `stop_loss`, `trailing_stop_loss` and
  `stoploss_on_exchange` exits of those trades, and defers everything else to NFI.
- Both apply only to trades opened at or after `HARD_STOP_FROM`. The class `stoploss`
  stays -0.99, so the trades open at deployment keep their current stop (liquidation) and
  nothing is closed by the deployment itself.
- If the price is already past the stop when `custom_stoploss` first runs, it returns a
  distance of 1e-4 instead of 0, because freqtrade ignores a 0 and would keep -0.99.

The stop is a software stop, checked by the bot every loop. `stoploss_on_exchange` stays
off: Binance's stop-order path has never run on this account, freqtrade cancels and
re-places an exchange stop after every grind fill, and upstream advises against it for
NFI. That choice is revisited separately (see below).

## Evidence

- Three H9 windows (ADR-0020): at 0.55 the account total stayed within the 10% bound and
  the worst loss fell from -66.58 / -85.39 / -77.57 to -37.20 / -37.41 / -39.06.
- The deployed code, run on window 3 with `HARD_STOP_FROM` at the window start and the
  live-only delisting lookup switched off, reproduces H9's `stop55-20261001`: the same 157
  trades, the same 8 stops, account total +58.77 against +58.75. One trade differs by
  0.02 USDT of rounding. Its stops are named `trailing_stop_loss`, as predicted above.
- The overlay's own checks: the stop is 72.5 for a first entry of 100 at 2x and stays
  72.5 after adds at 80 and 70; shorts mirror it; old trades get None; a price already
  past the stop gives a tiny distance; NFI still decides every non-stop exit and every
  stop on an old trade.

## Deployment

1. Set `HARD_STOP_FROM` to the deployment time, copy the overlay into
   `user_data/strategies/` and the tests into `internal/repo/testdata/`, run
   `make check`.
2. Record every open trade's `stop_loss_abs` from `/api/v1/status`.
3. Run the updater's import guard against the new file inside `bottrade`, then
   `docker restart bottrade`. This needs the owner's go-ahead.
4. After the restart: the bot is processing, every pre-existing trade shows the same
   `stop_loss_abs` as in step 2, and the log has no strategy error.

Rollback: restore the previous `NFIX7Risk.py` from git and restart. Trades opened while
the stop was live keep their raised stop in the database; it only matters if they fall
27.5%, and the restored overlay would let NFI veto it again.

## Limits

- The bot was down for about 13 hours on 2026-11-17 and again on 2026-11-22 (power loss,
  then the exchange rejecting a changed home IP). A software stop does nothing while the
  bot is down. That is the case for an exchange-side stop or a VPS, not a reason to wait.
- The 9 trades open at deployment stay without a stop. Closing or protecting them is a
  separate decision for the owner.
- Backtest stops fill at the stop price. Live, a software stop exits with NFI's order
  settings (limit, then market after the unfilled timeout) and will slip on a fast candle.
- The level comes from three windows, two of them overlapping, all before 2026-11-21.

## Revisit when

- An exchange-side stop is considered: test it in dry-run first, including the
  cancel-and-replace after grind fills.
- 30 trades opened under the stop have closed: compare their stop fills with what NFI
  did on the same pairs in the backtest windows.
