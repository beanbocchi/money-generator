# ADR-0025: H6: a longer minimum pair age

- Status: proposed; pre-registered, never run
- Date: 2026-07-30
- Related: ADR-0007 (acceptance rule), ADR-0014 (in-sample rejection on live history)

## Original record

Moved verbatim from `lab/HYPOTHESES.md` on 2026-11-15; dates inside are the original ones.

**H6 — excluding recently-listed pairs removes tail risk cheaply.**
`AgeFilter` already runs at 60 days. Grid: 60 (baseline), 120, 180, 365 days. Prior:
plausible on inspection — several of the 12 live losers (ARIA, CYS, UB, SIREN, AKE,
HYPER, DODOX) look like young low-liquidity listings — but note ZEC, the single worst
pair at −12.86, is a 2016 coin. So an age filter cannot address the largest loss.
