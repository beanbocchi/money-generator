# ADR-0024: H5: exposure scaled by volatility regime

- Status: proposed; pre-registered, never run
- Date: 2026-07-30
- Related: ADR-0007 (acceptance rule), ADR-0014 (in-sample rejection on live history)

## Original record

Moved verbatim from `lab/HYPOTHESES.md` on 2026-11-15; dates inside are the original ones.

**H5 — cutting exposure in high-volatility regimes beats cutting it always.**
Scale `max_open_trades` or stake by realised volatility instead of holding it fixed.
Grid: 2 or 3 volatility buckets. This is the only hypothesis not derived from a pattern
already visible in live history, so it carries the least prior and the most risk of
being a fishing expedition. Test last.
