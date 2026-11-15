# ADR-0005: LowProfitPairs per-pair cooldown (H1)

- Status: accepted, on weak evidence; H1 unresolved
- Date: before 2026-07-30; H1 tested 2026-07-31 to 2026-08-14
- Code: `NFIX7Risk.protections`
- Lab: `lab/matrices/h1-protections.tsv`, results `p1-with-*` (overlay on) against `h1-without-*`; `h1-with-*` is the first attempt, run without `--enable-protections`
- Related: ADR-0016 (rule 4 attempt)

## Decision

`LowProfitPairs`: lookback 2880m, trade_limit 2, stop_duration 720m, required_profit
0.0. It locks a single pair for 12h after two closed trades on it within 48h sum
negative, and never halts the whole bot. It is declared on `NFIX7Risk` because this
freqtrade version rejects `protections` in the config file.

## Evidence for

Replayed over the 208 closed live trades, it would have blocked the second ZEC doom on
2026-06-05 (#131, -7.30) and the second SIREN doom on 2026-06-13 (#165, -3.65): +9.30
USDT on full history, +6.83 on the post-blacklist universe. That is n=2 events, and the
replay cannot model the capital a blocked trade frees. `LowProfitPairs` is used instead
of `StoplossGuard` because NFI exits doom stops through `custom_exit`, which
`StoplossGuard` does not count.

## Evidence against

The one time the overlay fired in backtests (window 20260501, universe 1, ~815 trades
across four windows) it blocked a ZEC trade worth +9.15, net -10.95. The backtest did not
reproduce the repeated-doom pattern the rule targets, so this is a direction only.
Acceptance rules 2-4 (ADR-0007) are unmet in both directions.

## Revisit when

A window with repeated losses on the same pair inside 48h is available, or live records
another lock and what the locked pair did next.

## H1 record

Moved verbatim from `lab/HYPOTHESES.md` on 2026-11-15; dates inside are the original ones.

**H1 — the protections overlay already in production earns its place.**
`NFIX7Risk` (LowProfitPairs: 2880m lookback, trade_limit 2, 720m lock, required_profit
0.0) vs plain `NostalgiaForInfinityX7`. This gets tested first because it is already
trading real money on the strength of a counterfactual over **n=2 events**, which the
config comment admits. Grid: lookback {1440, 2880, 5760} × trade_limit {2, 3} ×
stop_duration {360, 720, 1440}.

**H1 — untestable on this rig. The mechanism never fired.**
Run 2026-07-31: both arms on the 2026-06-04 X7, window 20260604-20260730 (out-of-sample
for that version), wallet 270, live geometry, `--timeframe-detail 1m`. `NFIX7Risk` and
plain `NostalgiaForInfinityX7` came out **byte-identical on every metric**: 203 trades,
198 wins, +1.911 USDT, profit factor 1.012, delta exactly zero everywhere.

`LowProfitPairs` requires ≥2 closed trades on one pair within 48h summing negative.
Scanning all 203 trades: **0 qualifying windows.** SIREN's two trades closed ~50h apart —
just outside the lookback — and the first was a win. So the overlay could not fire, and
the identical results are the absence of a triggering condition, not evidence of no
effect. Its value still rests entirely on the live n=2 counterfactual, and this rig as
configured cannot improve on that: it does not reproduce the repeated-damage pattern the
overlay exists for.

To make H1 answerable, a window and universe containing double-losses is needed — a much
longer backtest, upstream's 343-pair list, and an older strategy version. Until then H1 is
open, not answered.

**Incidental finding, and the first instance of grinding failing anywhere.** The one
genuine loss was SIREN on 2026-06-13: stake 11.5 ground to **142.1 (12.4x)**, held two
days, then `stop_loss` for -53.98. The position reached 53% of the 270 wallet and the loss
took 20% of the account. Worth stating precisely, against my own earlier overstatement:
in account-percentage terms this is *not* worse than history — the worst live loss was
-7.30 on a ~43 USDT account, also 17%. What differs is the mechanism, not the magnitude:
formerly an un-ground position hitting a doom stop, now a position ground to 12.4x then
stopped out.

The reported 26.76% drawdown on both arms is again a cutoff artifact — window ending
exactly at `2026-07-30 00:00`, from 4 boundary force-exits. `analyze.py` flagged it
automatically, which is the fix earning its place on the first run after it was written.

Rerun 2026-08-08 with `--enable-protections`, which the first attempt omitted (from
commit dd07af5): the overlay fired once in ~815 trades across four windows and blocked
ZEC on 2026-06-06 worth +9.15, net -10.95.
