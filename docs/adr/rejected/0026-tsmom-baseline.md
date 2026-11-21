# ADR-0026: A volatility-targeted trend baseline on BTC and ETH perps

- Status: rejected (pre-registered 2026-11-16, run the same day; no side met the plateau rule)
- Date: 2026-11-16; decided 2026-11-16
- Lab: `lab/tsmom.py` (backtest and report, self-test with `--self-test`), run through `bin/mg tsmom` (`internal/lab/tsmom.go`), data in `lab/data-tsmom/`
- Run: `bin/mg tsmom download`, then `bin/mg tsmom run`
- Related: ADR-0006 (method), ADR-0007 (acceptance), ADR-0003 (leverage cap)

## Context

The live bot's edge is unproven: 2x-era all-in +30.1 over 178 trades, 95% interval
-104 to +142. The lab cannot settle that, because NFI changes too fast to backtest
over more than a few weeks per version (ADR-0006) and its P&L rests on a handful of
tail events. So "is anything better than NFI?" has no answer yet; there is no ruler.

A literature search on 2026-11-16 ranked the alternatives. LLM agents that trade on their
own lost money in the one live benchmark (Alpha Arena season 1, 4 of 6 models) and cannot
be replayed (ADR-0019). Time-series foundation models forecast returns at about 50%
directional accuracy. FreqAI's RL cannot use its state in backtests. Cross-sectional
momentum on Binance perps nets to zero after costs (Arefev, SSRN 7404139, 2020-2026).
Time-series momentum with volatility scaling has the most published support (Moskowitz,
Ooi & Pedersen 2012; Liu & Tsyvinski 2021; AdaptiveTrend 2026, out-of-sample 2022-2024),
with one recent dissent that the edge has aged (Arefev, SSRN 7405060).

This ADR does not propose replacing NFI. It asks whether the simplest documented edge
exists on these two contracts, net of fees and funding, over six years. If it does, it is
a benchmark NFI can be measured against and a candidate for a forward dry-run. If it does
not, the search for "something better" starts from a known negative.

## Proposal

### Strategy, fixed before any run

- Universe: `BTC/USDT:USDT` and `ETH/USDT:USDT`. Deepest books, longest perp history, no
  delisting or survivorship question.
- Signal at the daily close t for each asset: `s = sign(close_t / close_{t-L} - 1)`.
  Long-flat arms use `max(s, 0)`, long-short arms use `s`.
- Volatility: standard deviation of the last 30 daily log returns, annualised with
  sqrt(365).
- Weight (notional as a share of equity): `w = s * 0.5 * min(2, 0.60 / vol)`. The 0.5
  splits the account evenly, 0.60 is about BTC's long-run volatility, so a full long sits
  near a 1x buy-and-hold. Per asset |w| <= 1, so gross exposure is at most 2.0, the
  ADR-0003 cap.
- Weights computed at close t are held over day t+1. Nothing at t uses data after t.
- Daily rebalance to target. Cost 0.07% of traded notional (0.05% taker plus 0.02%
  slippage). Funding is charged on each day's position from Binance's funding history:
  a long pays a positive rate, a short receives it.
- Equity compounds. No stop other than the signal; the report checks the worst intraday
  move against the position held, so a liquidation the daily model would miss is visible.

### Arms

| arm | L (days) | side |
|---|---|---|
| `lf28`, `lf56`, `lf112` | 28, 56, 112 | long-flat |
| `ls28`, `ls56`, `ls112` | 28, 56, 112 | long-short |
| `bench` | none | 50/50 long at 1x, rebalanced daily, pays funding |

No other parameter is varied. Lookbacks are the usual one, two and four months, not
chosen from this data.

### Windows

Data from 2019-12-01 (BTC perp opened 2019-09, ETH 2019-11), so every indicator is warm
by the first trading day.

| window | dates |
|---|---|
| full | 2020-07-01 .. 2026-11-14 |
| P1 | 2020-07-01 .. 2021-12-31 |
| P2 | 2022-01-01 .. 2022-12-31 |
| P3 | 2023-01-01 .. 2024-12-31 |
| P4 | 2025-01-01 .. 2026-11-14 |
| live | 2026-07-30 .. 2026-11-14, the 2x era of the live bot |

### What is measured

Per arm and window: total return, CAGR, annualised volatility, Sharpe (daily mean over
daily std times sqrt(365), no risk-free rate), max drawdown, Calmar, fees, funding,
turnover, share of days in the market, worst intraday loss on the held position.
Full-period Sharpe gets a 95% interval from a stationary block bootstrap (mean block 20
days, 2000 resamples, seed fixed), and so does the arm-minus-benchmark Sharpe difference.

## Decision rule

An arm **passes** if all three hold over the full window:

1. Sharpe at least the benchmark's, and the bootstrap lower bound of its own Sharpe
   above 0.
2. Max drawdown below two thirds of the benchmark's.
3. Positive return in at least 3 of P1-P4.

A side (long-flat or long-short) is **accepted** if at least two of its three lookbacks
pass and one of them is `L=56` (ADR-0007 rule 2: a plateau, not a spike). Acceptance
makes it a candidate for a freqtrade implementation and a forward dry-run under a new
ADR. It does not touch live money, and it does not decide anything about NFI.

If neither side is accepted, this ADR is rejected: a plain trend overlay on BTC and ETH
perps shows no edge net of costs over 2020-2026, and is not a better candidate than NFI.

The `live` window is reported beside NFI's all-in result for the same dates, as one
path each. It is descriptive and decides nothing: three and a half months of one path
cannot separate two strategies.

## Prediction

Long-flat is accepted, mostly through drawdown rather than return: Sharpe around 0.9 to
1.2 against a benchmark around 0.7 to 0.9, max drawdown around 35-45% against roughly 75%.
P4 is the weakest window for every arm. Long-short fails rule 1 or rule 3 in at least two
lookbacks, because the 2023-2024 chop and short-squeezes cost more than 2022's shorts
earned.

## Honest limits

- The idea is not out-of-sample. Whoever picked TSMOM knew 2022 was a long bear market,
  which flatters a trend filter. The parameters are textbook values, not fitted, but the
  only genuinely unseen data is a forward run.
- Two assets that move together (correlation around 0.8). Effectively one bet, so the
  bootstrap interval is the number to read, not the point Sharpe.
- Close-to-close fills, no intraday execution, no maker rebates, daily rebalance at the
  close. A freqtrade port will differ in detail.
- Funding before Binance's archive starts, if any day is missing, is charged at zero and
  counted in the report.

## Cost

One download of two pairs' daily candles and funding (a few MB), and one run of a pandas
script measured in seconds. It fits beside a running NFI backtest.

## Results (2026-11-16)

Run once, as registered, with `lab/tsmom.sh run`. Data 2019-12-01 .. 2026-11-14, no
funding days missing for either pair. Self-test 12/12. Spot checks done before reading
the verdict: an independent 50/50 calculation for P1 without funding gives +866%, and the
benchmark's +516% matches it after the 0.47 of funding a 1x long paid in 2020-2021;
`lf56`'s 2022 loss comes from long entries into the March-April, August and October rallies
that each failed, not from a sign or alignment error.

### Full window, 2020-07-01 .. 2026-11-14

| arm | total | CAGR | Sharpe [95%] | Sharpe minus bench [95%] | max DD | in market |
|---|---|---|---|---|---|---|
| lf28 | +1205% | 49.6% | 1.14 [0.24, 1.95] | [-0.08, 1.00] | 45.6% | 63% |
| lf56 | +1444% | 53.6% | 1.16 [0.27, 1.99] | [-0.06, 1.05] | 51.8% | 65% |
| lf112 | +405% | 28.9% | 0.78 [-0.09, 1.64] | [-0.42, 0.71] | 65.9% | 64% |
| ls28 | +1192% | 49.4% | 0.98 [0.21, 1.73] | [-0.62, 1.31] | 58.8% | 100% |
| ls56 | +1914% | 60.1% | 1.10 [0.30, 1.87] | [-0.46, 1.38] | 65.1% | 100% |
| ls112 | +107% | 12.0% | 0.49 [-0.31, 1.31] | [-1.15, 0.84] | 80.6% | 100% |
| bench | +301% | 24.3% | 0.66 [-0.16, 1.44] | | 77.3% | 100% |

### Return by sub-period

| arm | P1 2020-21 | P2 2022 | P3 2023-24 | P4 2025-26 |
|---|---|---|---|---|
| lf28 | +463% | -31% | +229% | +2% |
| lf56 | +521% | -39% | +262% | +12% |
| lf112 | +353% | -8% | +203% | -60% |
| ls28 | +354% | +10% | +56% | +65% |
| ls56 | +494% | -20% | +95% | +118% |
| ls112 | +226% | +72% | +39% | -73% |
| bench | +516% | -66% | +232% | -42% |

The worst intraday move against a held position was -29% (`lf28`, 2022) and -46% for the
benchmark. No arm came near liquidation at its exposure.

### Verdict by the rule

| arm | rule 1 Sharpe | rule 2 DD < 51.5% | rule 3 sub-periods | result |
|---|---|---|---|---|
| lf28 | ok | ok | 3/4 | pass |
| lf56 | ok | fail (51.8%) | 3/4 | fail |
| lf112 | fail | fail | 2/4 | fail |
| ls28 | ok | fail | 4/4 | fail |
| ls56 | ok | fail | 3/4 | fail |
| ls112 | fail | fail | 3/4 | fail |

Long-flat: 1 of 3 pass and `L=56` fails. Long-short: 0 of 3. Neither side is accepted.

`lf56` missed rule 2 by 0.3 points. That is recorded as a fail, not re-argued: moving a
bound after seeing the number it was meant to judge is the thing ADR-0007 exists to stop.

### Live window, 2026-07-30 .. 2026-11-14 (descriptive)

| | result |
|---|---|
| lf28 / lf56 / lf112 | +7.7% / +27.0% / -38.9% |
| ls28 / ls56 / ls112 | +11.7% / +68.3% / -62.6% |
| bench | -5.0% |
| NFI live, trades opened since 2026-07-30 | -36.81 USDT all-in: +172.95 realised over 178 closed trades, -127.00 booked inside 9 open trades, -82.76 open leg (snapshot 20261115T185450Z and `bin/status.sh`, 2026-11-15 18:56 UTC) |

NFI has no clean percentage for this window: the balance grew from about 270 to 925 USDT
through deposits, and entries were paused for part of it after the Binance hack. One
path each. This decides nothing.

### Prediction against outcome

- Long-flat accepted: wrong. Its Sharpe landed in the predicted 0.9-1.2, but its
  drawdown was 46-66%, not 35-45%, and the plateau failed.
- Benchmark: drawdown 77% as predicted, Sharpe 0.66, a little under the predicted range.
- P4 weakest for every arm: right for long-flat, wrong for long-short, where `ls28` and
  `ls56` had their second-best window.

## Decision

Rejected. A plain trend filter on BTC and ETH perps nearly doubles the benchmark's
Sharpe on point estimates (1.16 against 0.66), but no arm's Sharpe beats buy-and-hold by a margin
the bootstrap can tell from zero, and the drawdown cut clears the registered bound only at
`L=28`. With
two assets that move together this is effectively one bet over six years, and it is not
enough to call an edge. It is not a better candidate than NFI, and it is not a usable
ruler for NFI either: its own uncertainty is as wide as NFI's.

## Revisit when

- A trend test across many perps is proposed. Breadth is what the two-asset version
  lacked, and it is a new hypothesis with its own ADR, not a re-run of this one.
- Two more years of data exist. The registered script reruns unchanged.
