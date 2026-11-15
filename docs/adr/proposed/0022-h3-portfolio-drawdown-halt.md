# ADR-0022: H3: a portfolio-level drawdown halt

- Status: proposed; pre-registered, never run
- Date: 2026-07-30
- Related: ADR-0007 (acceptance rule), ADR-0014 (in-sample rejection on live history)

## Original record

Moved verbatim from `lab/HYPOTHESES.md` on 2026-11-15; dates inside are the original ones.

**H3 — a portfolio-level drawdown halt beats a per-pair one.**
freqtrade `MaxDrawdown` protection. Grid: lookback {24, 48, 72}h × threshold {3, 5, 8}%
of wallet × lock {12, 24, 48}h. Prior: fails as specified, because losses cluster
*simultaneously* — on 2026-06-05 three positions doomed together, so a mechanism that
reacts to the first closed loss is structurally too late. Worth testing precisely
because that reasoning could be wrong at scale.
