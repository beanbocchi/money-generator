# ADR-0006: Backtest methodology: version-split windows and honest metrics

- Status: accepted
- Date: 2026-07-30 to 2026-08-13
- Code: `lab/backtest.sh` (`STRATDIR`, `configs-pinned/`), `lab/analyze.py` (`split_force_exit`, `load_metrics`, `entry_count`), `lab/queue.sh`
- Related: ADR-0007 (acceptance rule), ADR-0015 (H7)

## Decision

Every backtest in `lab/` follows these rules:

1. The X7 version must predate the backtest window. Extract it with
   `git -C NostalgiaForInfinity show <sha>:NostalgiaForInfinityX7.py` into
   `lab/strategies-<date>/` and pass it with `STRATDIR=`. Both arms of a comparison use
   the same version.
2. `force_exit` rows at the backtest end are excluded from tail statistics, and any
   drawdown window ending at the cutoff is flagged.
3. Returns are quoted as `profit_total_abs` (open positions marked to market), never
   realised-only P&L.
4. Grinding is detected by counting filled entry orders, never by
   `max_stake_amount > stake_amount`.
5. Upstream configs come from `lab/configs-pinned/`, so an update cannot change them
   between two arms.
6. Runs go one at a time through `lab/queue.sh`, with `--timeframe-detail 1m` and
   `--oom-score-adj 1000`. A 272-pair run peaks around 11.4GB.

## Evidence

Rule 1: today's X7 over mid-2026 returns 528 trades and zero losses; the 2026-06-04
version over the same window returns 386 trades and one genuine `stop_loss`. Rule 2: one
run's "5 losses" were 1 real loss and 4 cutoff artifacts, and a "19.19% drawdown" ended
exactly at the cutoff. Rule 3: at wallet 270 the realised figure read +37.5% while the
account was +0.7%. Rule 4: the bad detector reported 0 of 194 trades grinding where the
entry-order count is 58 of 194 (ADR-0015).

## Original amendments

Moved verbatim from `lab/HYPOTHESES.md` on 2026-11-15; dates inside are the original ones.

**2026-07-30, amendment 2 — H7 is rejected, and the strategy version must predate the
backtest window. This replaces rule 1.**

H7 predicted losses would appear only at small wallets. At 270 USDT — the live account's
actual size — with `--timeframe-detail 1m` and live geometry, the backtest produced 528
trades and **zero** losses. Wallet size is not what separates the backtest from live.
Neither was compounding, fixed vs unlimited stake, 1m detail, nor lookahead bias:
`lookahead-analysis` reports no bias, no biased entry or exit signals, no biased
indicators.

What does separate them is the strategy version. Upstream made **995 commits to
`NostalgiaForInfinityX7.py` in the two months after the live losses, 564 of them
mentioning "protection"**, churning +20524/-15977 lines of a 70k-line file. Same window,
same config, same data, same 1m detail:

| X7 version | Trades | Genuine losses | force_exit at cutoff | Reported drawdown |
|---|---|---|---|---|
| 2026-06-04, the version that existed then | 386 | 1 (SIREN -65.25, `stop_loss`) | 4 (-131.21) | 19.19% — see below |
| today | 528 | 0 | 0 | 0.00% |

**Correction to the drawdown column.** That 19.19% is itself a cutoff artifact and must
not be read as a risk signal. Its window is `2026-07-13 17:48` → `2026-07-16 00:00`, and
`2026-07-16 00:00` is exactly the backtest end: freqtrade computes drawdown on the
closed-trade equity curve, and the four boundary force-exits land on the final day. So
the honest difference between the two versions is **1 genuine `stop_loss` versus 0, and
4 positions still underwater at the cutoff versus 0** — not a 19-point drawdown gap.
`analyze.py load_metrics()` now flags any run whose drawdown window ends at the cutoff,
so this cannot be repeated by accident.

Backtesting today's X7 over a period its protections were written for is testing a
strategy on the data used to tune it. Not lookahead bias in the technical sense — the
indicators do not peek — but the parameters were fitted knowing that window.
**Today's X7 over mid-2026 cannot be used to study tail risk at all: it produces no tail,
so the dependent variable is absent.**

Stated carefully: one genuine loss does not establish that the version explains live's 12
losses. Live also differed in settings (4 slots, ratio 0.99, leverage 3.0) and in capital
regime. Too many differences to attribute. What is firmly established is that version
materially changes results and must be controlled.

**Replacement for rule 1.** Split by strategy version rather than by time: take
`NostalgiaForInfinityX7.py` as of date T and backtest T..T+N. That is genuinely
out-of-sample with respect to the strategy's own development, needs no 2023 data, and
tests the strategy as it was rather than as it was later patched to be. Use `STRATDIR=`
with a directory holding the historical X7 plus `NFIX7Risk.py`, extracted via
`git -C NostalgiaForInfinity show <sha>:NostalgiaForInfinityX7.py`. Both arms of a
comparison must use the same version, so whatever hindsight it carries is shared and
cancels in the delta.

**Also learned, now enforced in `analyze.py`:** a backtest force-exits everything still
open at its end date. Those rows are positions marked to market at an arbitrary cutoff,
not strategy decisions. The June run's "5 losses" were 1 real `stop_loss` plus 4 cutoff
artifacts. `split_force_exit()` excludes them from every statistic and reports them
separately.
