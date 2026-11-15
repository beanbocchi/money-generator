# ADR-0021: H2: a tighter hard stoploss

- Status: proposed; pre-registered, never run
- Date: 2026-07-30
- Related: ADR-0007 (acceptance rule), ADR-0014 (in-sample rejection on live history)

## Original record

Moved verbatim from `lab/HYPOTHESES.md` on 2026-11-15; dates inside are the original ones.

**H2 — a tighter hard stoploss cuts the tail more than it costs in winners.**
Rejected in-sample on live history, retested here with power. Grid: 5, 8, 10, 12, 15%
price move. Prior: fails, because live MAE p90 is 16.8% — one trade in ten goes 17%
against before recovering, so drawdown tolerance appears to *be* the edge rather than a
flaw in it. Needs `--timeframe-detail 1m` to be trustworthy at all, since without it a
stop is only evaluated at 5m boundaries.
