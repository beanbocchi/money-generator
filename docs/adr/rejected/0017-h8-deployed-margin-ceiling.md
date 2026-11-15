# ADR-0017: H8: cap concurrent deployed margin

- Status: rejected (measurement accepted, ceiling rejected)
- Date: 2026-10-05
- Lab: `lab/matrices/h8-cap.tsv`, `lab/strategies-20260501/NFIX7Cap.py`, results `p1-with-20260501`, `cap50-20260501`
- Code: `lab/analyze.py margin` (`deployed_margin`)

## Summary

The measurement holds: the median backtest peaks at 85.8% of wallet deployed, every peak
is all long, and live reached 68%. A ceiling at 0.50 on new entries cut the peak to 64.3%
but left twelve long positions open at the peak in both arms, and account total fell
31.8% against a 10% bound. The variable that describes the risk is `max_open_trades` and
directional concentration, which this hook cannot see.

## Original record

Moved verbatim from `lab/HYPOTHESES.md` on 2026-11-15; dates inside are the original ones.

## H8 — concurrent deployed margin

Pre-registered 2026-10-05, before any measurement was run.

The live account has opened 136 positions since 2026-07-30 at a capital level no window in
this rig covers. Every backtest window run so far ends 2026-07-30, and the second deposit
landed 2026-08-05, taking the wallet from ~282 to ~782 USDT.

### Dependent variable

H1's two arms came out byte-identical because `LowProfitPairs` found 0 qualifying windows
in 203 trades. H7 against today's X7 produced 0 losses in 528 trades. Rule 4's second
universe produced 130 trades and 0 losses across four windows. Each of those tests needed
tail events to have something to measure, and had between none and three.

Deployed margin is defined at every filled order. A run with zero losses still
yields a complete timeline. The live database gives 806 observations; a 528-trade backtest
gives more.

### Definition

At each filled order, sum the open positions' basis divided by leverage, then divide by the
wallet. Report per window: p50, p95, p99 and maximum share; the share of fill events
above 30, 40, 50 and 60 percent; the maximum count of concurrent positions; and the
long/short split at the peak.

Basis is reconstructed by walking fills in order, adding cost on entry fills and reducing
basis pro rata on exit fills. `max_stake_amount` is unusable here. It holds cumulative
entry cost rather than peak concurrent exposure, which is the field behind the 2026-08-13
retraction; on the live database it reports 127.72 USDT for KAITO #238 whose true peak was
42.98, a factor of 3.0.

Summed over the 342 closed live trades, the reconstruction reproduces `close_profit_abs` to
a mean error of +0.009 USDT per trade. The residual is fees and funding, which the model
omits.

### Live measurement

| statistic | value |
|---|---|
| p50 | 4.8% |
| p95 | 26.7% |
| p99 | 38.5% |
| maximum | 68.0% |
| fill events at or above 40% | 7 of 806 |
| distinct days at or above 40% | 1 |

The peak fell on 2026-08-22 08:47:21: nine positions open, nine long and no short, 531.45
USDT of margin against a 782 USDT wallet, 1062.90 USDT of notional at 2x. Six of the nine
opened between 08:31 and 08:47. All nine closed green, for +17.49 together.

A correlated drawdown at that moment, assuming every position moves together and no stop
fires:

| move | loss | share of wallet |
|---|---|---|
| −10% | −106.29 | −13.6% |
| −20% | −212.58 | −27.2% |
| −30% | −318.87 | −40.8% |

### Prediction

Deployed margin has a low median with rare spikes: p50 below 10%, p99 above 30%, and at least
one episode per window above 50%. At those episodes the open positions sit 80% or more on
one side, because NFI fills many slots from a single market-wide dip.

### What would count

The output is a description, so rules 2 and 3 do not apply as accept or reject. What it
decides is whether an exposure ceiling is worth testing.

If the peak share stays below 30% of wallet across windows, there is nothing to cap and
this line closes. If episodes above 50% appear with homogeneous direction, a ceiling
becomes testable on peak share and time-above-threshold, which carry hundreds of
observations per run, where net P&L carries one to three loss events.

A ceiling is a different mechanism from the one H3 proposes. H3's prior states that losses
cluster simultaneously and that `MaxDrawdown` reacts to the first closed loss too late to
help. A ceiling bounds deployed capital before the move, without waiting for a loss to
close.

### Cost

No new backtests. The measurement runs over the result files already in `lab/results`.

### Honest limits

The live peak is one episode, on 2026-08-22, and may not recur.

A heavily loaded moment that never meets an adverse move costs nothing. The nine
positions at the peak earned +17.49 together, which is the cost side of any ceiling that
would have blocked some of them.

The stress table assumes correlation 1 across nine alt longs and no stop firing. Real
correlation in a selloff runs below 1, and NFI's doom stop bounds a position near −20%, so
the −30% row requires the stop to be gapped through. Live trades #52 and #13 reached
liquidation that way, so the row is reachable, and the table is still an upper bound and
should be quoted as one.

A backtest force-exits every open position at its end date, which inflates exposure on the
final day. The boundary is excluded from these statistics, the way
`split_force_exit()` already excludes it from tail statistics.

Backtest fills are modelled and live fills are real. The two sets of numbers are comparable
in shape; their levels move with wallet, slot count and pairlist.

### Results

Run 2026-10-05 over the 32 result archives in `lab/results` and the live database. No new
backtests.

| arm | wallet | p50 | p95 | max | peak positions | days at or above 40% |
|---|---|---|---|---|---|---|
| live, 2026-08-05 onward | 782 | 15.9% | 32.7% | 68.0% | 9 (9L/0S) | 1 of 53 |
| live, 2026-02-28 to 07-30 | ~43 | 17.1% | 62.0% | 89.8% | 4 (4L/0S) | 14 of 107 |
| A-with-protections | 270 | 50.0% | 72.3% | 97.4% | 11 (11L/0S) | 31 of 57 |
| H7-w40 | 40 | 34.7% | 76.6% | 97.8% | 7 (7L/0S) | 33 of 47 |
| H7-w780 | 780 | 22.6% | 47.3% | 85.9% | 12 (12L/0S) | 8 of 57 |
| h1-with-20260301 | 780 | 9.1% | 22.0% | 29.1% | 6 (6L/0S) | 0 of 56 |
| h1-with-20260501 | 780 | 28.6% | 64.5% | 85.8% | 12 (12L/0S) | 17 of 59 |

Across all 32 runs the median p50 is 18.7% and the median peak is 85.8%.

#### Prediction scorecard

Directional homogeneity held in 32 of 32 runs. At its peak every run held 80% or more of
its open positions on one side, and in each run above that share is 100% long.

The p50 prediction failed. I wrote "p50 below 10%" from a live figure I had not yet
computed per window; the live window reads 15.9% and the median run reads 18.7%. The
median run holds a fifth of the wallet at any given moment.

"At least one episode above 50% per window" held in 18 of 32 runs. The 14 that stayed
below are the 20260301 and 20260401 windows, which peak at 22-29%. Loading is a property
of the window as much as of the configuration.

#### Decision

The pre-registered rule: below 30% across windows the line closes, and episodes above 50%
with homogeneous direction make a ceiling testable. 18 runs exceed 50% and all 32 are
homogeneous at their peak, so a ceiling is testable.

The live account has reached 68%. The same 12-slot configuration in backtest reaches 85.9%
at wallet 780 and 97.4% at 270, with every slot full and every position long. 2026-08-22
is a mild instance of what this configuration does rather than an outlier.

#### Amendment 1

Written 2026-10-05, before the results above were read. The pre-registration said a backtest's force-exit boundary inflates exposure and would be
excluded from these statistics. That is wrong, and `deployed_margin()` does not do it.
freqtrade's cutoff force-exits are exit fills, so they drive deployed margin toward zero
rather than lifting a peak, and margin held before the cutoff was capital genuinely at
risk. The artifact `load_metrics()` flags for drawdown has no counterpart here. The
function excludes nothing and carries no cutoff flag.

#### Confounds

These numbers establish no causal link to any loss. The Feb-Jul live window ran at p50
17.1% with 14 of 107 days above 40% and took 12 doom losses and 2 liquidations; the
current window runs lower and has taken 1. Strategy version, leverage and the grinding
mechanism all changed between those windows, so the association has at least three other
explanations.

The ~43 USDT denominator for the Feb-Jul window is one number for a period in which the
account compounded from ~10, so the real shares early in that window are higher than the
table shows.

`h1-with` and `h1-without` produce identical margin at every window, which matches the
earlier finding that `LowProfitPairs` never fired.

## H8 follow-on -- a deployed-margin ceiling

Run 2026-10-05. Arm A is the existing `p1-with-20260501`. Arm B repeats that matrix row
with one field changed, the strategy, and is `lab/results/cap50-20260501.zip`. Both use
`lab/strategies-20260501`, `lab/config-h7.json`, `--timeframe-detail 1m --dry-run-wallet
780 --enable-protections`, over 20260501-20260701.

`NFIX7Cap` refuses a new position when deployed margin already exceeds
`max_deployed_ratio`, 0.50 here. An entry into a pair that already holds a position is a
grind increment and always passes, because the one measured case of grinding switched off
is the wallet-40 arm with its 2 liquidations.

### Where the ceiling bound

| | A, no ceiling | B, ceiling 0.50 |
|---|---|---|
| p50 | 28.5% | 31.0% |
| p95 | 64.5% | 54.7% |
| max | 85.8% | 64.3% |
| fill events at or above 60% | 96 | 16 |
| positions at the peak | 12 (12L/0S) | 12 (12L/0S) |

Grind increments are exempt and carry deployment past the ceiling once the slots are
taken, so a nominal 0.50 permits 64.3%.

Peak concurrency is identical. Both arms reach twelve open positions, all long. The gate
moves entries later while leaving the state H8 identified intact: twelve slots behaving as
one position when the market moves together.

### P&L reads two ways

On closed trades, with the cutoff force-exits excluded:

| | A | B |
|---|---|---|
| trades | 204 | 166 |
| net | +343.02 | +316.48 |
| genuine losses | 2 (SIREN -66.60, ZEC -43.91) | 1 (ZEC -42.32) |
| 95% interval | +142.40 .. +482.65 | +199.90 .. +401.78 |

On account total, which marks open positions to market:

| | A | B |
|---|---|---|
| profit_total_abs | +115.77 | +78.98 |
| positions underwater at the cutoff | 6 (-227.25) | 9 (-237.50) |

Blocking entries while the account is loaded pushes them later, so more positions are
young and underwater when the window ends. Most of the 31.8% gap sits there rather than in
a strategy decision. The two intervals overlap across most of their range.

### Verdict

Rejected. Max drawdown reads 20.24% against 21.66%, and `analyze.py` flags both as cutoff
artifacts whose windows end at 2026-07-01 00:00, the backtest end. Rule 3's primary metric
is unavailable, so the rule cannot be satisfied at any profit figure, and on account total
the secondary metric falls 31.8% against a 10% bound. The one removed tail event is a
single observation inside overlapping intervals.

This supports H0.

### The sweep

Rule 2 wants a plateau across `NFI_MAX_DEPLOYED`, and three further runs at 54 minutes
each were not spent on one. The mechanism result answers ahead of the parameter: a gate on
new entries leaves peak concurrency at twelve long positions at every setting, a tighter
ceiling blocks more entries and costs more profit, and grind carries deployment past the
ceiling regardless. The design gates capital already committed, where the state under
study is how many correlated positions open at once.

That state is described by `max_open_trades` and by directional concentration. Neither is
tested here, and `confirm_trade_entry` is the wrong hook for either, since it sees one
entry at a time with no view of what the rest of the whitelist is doing on the same candle.

`lab/strategies-20260501/NFIX7Cap.py` is tracked. The ignore rule for
`lab/strategies-*/` covers only the 3MB X7 copies, which regenerate from the upstream
clone. The file is backtest-only and absent from `user_data/strategies`, so the live bot
cannot load it. `lab/matrices/h8-cap.tsv` holds the row written out above.
