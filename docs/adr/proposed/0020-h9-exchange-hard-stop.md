# ADR-0020: H9: a hard stop NFI cannot veto

- Status: proposed; results in 2026-11-16, the rule accepts 0.55 and 0.70; not deployed, awaiting the owner
- Date: 2026-10-30
- Lab: `lab/matrices/h9-stop.tsv`, `lab/strategies-20260501/NFIX7Stop.py`, `lab/strategies-20260604/NFIX7Stop.py`, report `lab/h9.py`
- Run: `nohup bash lab/queue.sh lab/matrices/h9-stop.tsv >/dev/null 2>&1 &`, then `python3 lab/h9.py`

## Deviation (2026-11-15, before any arm result)

The pre-registered baselines `p1-with-*` ran on 2026-07-30. Since then Binance delisted
EPIC, STG and STORJ, and freqtrade drops inactive markets from the whitelist, so every new
arm runs on a smaller universe than those baselines. `p1-with-20260501` traded STG. The
built-in consistency check would fail for that reason alone. The baselines are rerun
under the same image (built 2026-07-30), config and data as `base-20260501` and
`base-20260604`, and `lab/h9.py` compares against those. The decision rule's bound was
already relative (10% below the baseline's account total), so the absolute figures
104.19 and 127.86 below no longer apply; the rerun baselines set the bound.

## Original record

Moved verbatim from `lab/HYPOTHESES.md` on 2026-11-15; dates inside are the original ones.

## H9 — a hard stop NFI cannot veto

Pre-registered 2026-10-30, before any of its runs. A 4-pair smoke test was run first to
check that the mechanism fires at all, and was deleted; its numbers come from a different
universe and capital split and are not evidence for or against anything below.

### Why this is testable now and was not before

The live bot has no stop. Upstream commit `54ab6f2f6` (2026-04-19) moved X7 to
`system_v3_2` with `system_v3_2_stops_enable = False`, and every live trade since the
2x cutover is tagged `system_v3_2`. The remaining `stoploss = -0.99` is dead as well:
NFI's `confirm_trade_exit` returns False for every `stop_loss` exit unless the rate is
already past the liquidation price. So the only thing that bounds a losing position today
is liquidation, about 49% from the average entry at 2x, and all-in for the 2x era is
+30.1 over 178 trades with a 95% interval of -104 to +142 (independent audit,
2026-10-30). On 2026-10-29/30 the bot held a record 10 positions, 9 long, through the
Binance hack.

An exchange-side stop (`order_types.stoploss_on_exchange`) is filled by Binance, so no
strategy hook can cancel it. `NFIX7Stop` simulates exactly that: stop exits pass
`confirm_trade_exit`, every other exit goes to NFI unchanged.

freqtrade pins a static stop to the first entry (`adjust_stop_loss(initial=True)` is a
no-op once set; a static stop only walks up), and every grind add fills above it, so a
stop at S of margin bounds the whole position's loss at S of its margin regardless of how
much NFI adds. Checked in the smoke test: ZEC first entry 553.56, stop 442.85 =
553.56 x (1 - 0.40/2).

### Arms

Same rig as H8 arm A: `lab/config-h7.json`, `--timeframe-detail 1m --dry-run-wallet 780
--enable-protections`, default pairlist (97 live-traded pairs), freqtrade image built
2026-07-30.

| window | strategy version | baseline (exists, same image) | new arms |
|---|---|---|---|
| 20260501-20260701 | `lab/strategies-20260501` | `p1-with-20260501` | `stop{40,55,70,85}-20260501` |
| 20260604-20260730 | `lab/strategies-20260604` | `p1-with-20260604` | `stop{40,55,70,85}-20260604` |

Levels are the stop as a share of margin: 0.40, 0.55, 0.70, 0.85, i.e. a 20%, 27.5%,
35% and 42.5% adverse move from the first entry at 2x. Both strategy versions run
`system_v3_2` with stops disabled, like live, and both predate their windows.

Consistency check built in: every trade opened before an arm's first stop fill must be
identical to the baseline's. If not, the arms differ in something other than the stop and
nothing below is read.

### What is measured

Per arm and window:

1. **Account total** (`profit_total_abs`, open positions marked to market at the cutoff).
   This is the return. Closed-only net is reported beside it, with `analyze.py`'s
   bootstrap interval, and never quoted as a return.
2. **Worst single closed loss** and the count of closed losses worse than -20% of margin.
3. **Stop fills**: count, summed P&L.
4. **Counterfactual per stop fill**: the baseline trade on the same pair with the same
   open date, and what it closed at. Each fill is classed *saved* (baseline did worse,
   including liquidation or still underwater at the cutoff) or *cost* (baseline
   recovered to better than the stop's loss). This is the cost side of the insurance,
   and it is observed on every fill, where the benefit side rests on two or three
   events per window.
5. Positions underwater at the cutoff, and max drawdown with `load_metrics()`'s cutoff
   flag.

### Decision rule

Rule 3's primary metric, max drawdown, was unavailable in H8: both arms' drawdown
windows ended at the cutoff. It is reported here and does not decide anything. In its
place:

- A level is **acceptable** in a window if account total is no more than 10% below the
  baseline's (baseline 115.77 and 142.07, so at least 104.19 and 127.86) **and** its
  worst closed loss is smaller than the baseline's.
- A level is **accepted** if it is acceptable in both windows **and** an adjacent level is
  too (rule 2, plateau). The recommendation is the tightest accepted level.
- If no level is accepted, the rig recommends no stop, and the account-total cost of each
  level is reported as the price of the insurance. Paying it is then the owner's
  decision, made knowing the rig never saw a market-wide crash like 2026-10-29.

### Prediction

0.40 costs materially. Large-wallet runs ground positions 30-50% below their first entry
back to profit, and a 20% stop kills those. 0.85 almost never fires except where the
baseline nearly liquidated. If anything is accepted it is 0.70 or 0.85.

### Honest limits

- Two windows, both before 2026-07-30. The live tail events since (TUT, BR, the hack
  night) are outside the data.
- The benefit side is two or three baseline tail events per window. A clean result here
  says the insurance is cheap, not that it pays.
- Backtest stops fill at the stop price, or at the 1m open when gapped through. Live,
  a market stop slips, and freqtrade cancels and re-places the exchange stop after each
  grind fill, leaving a short unprotected gap each time.
- A stop fill is a closed loss, so `LowProfitPairs` sees it and may lock the pair, which
  it never does for a position the baseline keeps open. That is part of the mechanism
  being tested, not a confound to remove.

### Cost

Eight runs at ~54 minutes, serial (two concurrent runs do not fit in memory). Runs carry
`--oom-score-adj 1000` so the kernel kills a backtest before the live bot.

## Results (2026-11-16)

All ten runs finished 2026-11-16 00:38 UTC (`lab/logs/queue-h9-stop.log`), report from
`python3 lab/h9.py`. The consistency check held for every arm.

| window | arm | account total | bound | worst closed | stop fills | saved / cost / unmatched | underwater at cutoff | verdict |
|---|---|---|---|---|---|---|---|---|
| 20260501 | baseline | +92.95 | | -66.58 | | | 6 (-227.18) | |
| | stop40 | +0.92 | +83.65 | -29.38 | 18 (-425.35) | 8 / 9 / 1 | 1 (+2.28) | not acceptable |
| | stop55 | +109.83 | | -37.20 | 11 (-341.05) | 7 / 3 / 1 | 1 (-14.12) | acceptable |
| | stop70 | +166.38 | | -53.55 | 6 (-232.22) | 5 / 1 / 0 | 3 (-77.28) | acceptable |
| | stop85 | +185.88 | | -67.34 | 4 (-183.38) | 4 / 0 / 0 | 4 (-102.09) | not acceptable (worst loss) |
| 20260604 | baseline | +123.73 | | -85.39 | | | 5 (-206.89) | |
| | stop40 | +19.79 | +111.36 | -29.73 | 16 (-374.12) | 5 / 11 / 0 | 1 (-4.84) | not acceptable |
| | stop55 | +142.56 | | -37.41 | 8 (-261.83) | 4 / 4 / 0 | 2 (-21.79) | acceptable |
| | stop70 | +145.48 | | -45.04 | 6 (-231.45) | 3 / 3 / 0 | 3 (-54.71) | acceptable |
| | stop85 | +206.09 | | -48.95 | 4 (-178.93) | 3 / 1 / 0 | 3 (-59.00) | acceptable |

Bounds are 90% of each rerun baseline's account total. stop85 failed the 20260501 window
by 0.76 USDT on worst closed loss (-67.34 against -66.58): its PLAY stop filled at -67.34.

By the rule, 0.55 and 0.70 are acceptable in both windows and adjacent, so both are
accepted, and the recommendation is the tightest: **0.55 of margin**, a 27.5% adverse
move from the first entry at 2x.

### Prediction against outcome

- 0.40 costs materially: right. It cut the account total by 92 and 104 USDT, and 20 of
  its 34 fills were cost.
- "If anything is accepted it is 0.70 or 0.85": half right. 0.70 is accepted, 0.85
  missed one window by 0.76 USDT, and 0.55 was not expected to pass.

### Reading it carefully

- Every stop level from 0.55 up beat its baseline's account total in both windows.
  Most of that gap is the baselines' positions underwater at the cutoff (-227 and -207),
  marked to market at an arbitrary date. Some of them could have recovered after it. The
  rule uses account total by design (ADR-0006 rule 3), and the gap is recorded so it is
  not read as the stop making money.
- 0.70 had the higher total in both windows. The rule picks the tightest accepted level;
  choosing 0.70 instead is the owner's call, and it was not the registered rule.
- The limits listed before the run still hold: two windows, both before the 2x era, no
  market-wide crash like 2026-10-29, and backtest stops fill without slippage.

## Before deployment

Deploying changes live money and needs the owner's approval. A static stop pins to each
trade's first entry, so open positions that have already moved past 27.5% from their
first entry would be stopped at once. That list must be checked against the open trades
on the day, and the owner decides whether those positions get the stop.
