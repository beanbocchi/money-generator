# ADR-0023: H4: an altcoin-breadth entry gate

- Status: proposed; pre-registered, never run
- Date: 2026-07-30
- Related: ADR-0007 (acceptance rule), ADR-0014 (in-sample rejection on live history)

## Original record

Moved verbatim from `lab/HYPOTHESES.md` on 2026-11-15; dates inside are the original ones.

**H4 — an altcoin-breadth entry gate avoids the worst days.**
Block new entries while the median 24h move across the whitelist is below a threshold.
Grid: threshold {−3, −5, −7}% × lock {12, 24}h. Requires a custom indicator; NFI ships
only `btc_info_timeframes`. Prior: weak. On live history the worst breadth day
(2026-06-05, median −8.0%, 68% of pairs below −5%) did produce 45% of all losses — but
the second, third and fourth worst breadth days were all profitable. One event is not a
signal.
