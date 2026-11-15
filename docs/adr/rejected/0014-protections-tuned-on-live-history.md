# ADR-0014: Stops and gates tuned on the live trade history

- Status: rejected
- Date: 2026-07-30
- Code: `lab/analyze.py live`
- Related: ADR-0021 to ADR-0024 retest these with backtests

## Proposal

Reduce the tail by adding a tighter hard stop, a `MaxDrawdown` portfolio gate, a
BTC-regime gate, or an altcoin-breadth gate, tuned on the 208 closed live trades
(2026-02-28 to 2026-07-15, net +14.48 USDT).

## Evidence

The record had a 94.2% win rate and its P&L was decided by 12 losses: average win
+0.311, average loss -3.870. The worst 10 trades hold 93% of all losses. 8 of 95 days
had a loss, and the 3 days with more than one hold -32.63 of -46.44.

- Hard-stop sweep: +1.76 at 8%, -0.24 at 10%, +2.41 at 12%, -3.77 at 15%; worst -14.59.
  Live MAE p90 is 16.8%.
- `MaxDrawdown`: +3.72 at best, -9.74 at worst, and it only avoided the one loss
  `LowProfitPairs` already catches.
- BTC regime: loss days had a median BTC move of -0.21% against -0.02% overall.
- Breadth: explains 2026-06-05 and nothing else; three of the four worst breadth days
  were profitable.

## Reason for rejection

Every change moved net P&L by less than ±4 USDT on +14.48, with non-monotonic sweeps.
Twelve tail events cannot separate an improvement from luck, and losses cluster on the
same day, so a rule reacting to a closed loss fires after the damage. This is why the
backtest rig in `lab/` exists.
