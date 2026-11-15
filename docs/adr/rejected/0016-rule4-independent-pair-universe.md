# ADR-0016: Rule 4: confirm effects on an independent pair universe

- Status: rejected (retired for H1 on 2026-08-14)
- Date: 2026-07-30
- Lab: `lab/matrices/u2-343.tsv`, `u3-343-no1m.tsv`, `d-disjoint.tsv`; `lab/pairlists/343.json`, `disjoint.json`

## Summary

A universe independent of the pairs the live bot trades is also independent of the risk:
130 trades across four windows on 120 disjoint pairs produced zero losing trades, with
the same X7 builds that produced 13 loss events on the live-traded pairs. Do not rebuild
this experiment.

## Original record

Moved verbatim from `lab/HYPOTHESES.md` on 2026-11-15; dates inside are the original ones.

## Rule 4 is unsatisfiable as written — 2026-08-14

Acceptance rule 4 asks an effect to hold on both the 97 pairs the live bot traded and
upstream's static list. Attempting it produced a structural dead end worth recording so
nobody spends machine time on it again.

Upstream's full 272-pair list peaks at ~11.4GB RSS and was OOM-killed 5 times across two
attempts on a 19GB machine that typically has 4-6GB free. Dropping
`--timeframe-detail 1m` did **not** help (11.54 → 11.37GB) — the memory is NFI computing
indicators over pairs × 5 timeframes, not 1m data. What actually killed those runs was
contention: two backtests overlapping, plus an unrelated 6GB service. A single run of any
of these sizes survives, at ~10-12GB.

So a 120-pair universe with **zero** overlap with universe 1 was built instead, which is a
cleaner independence test than 272 pairs sharing 43. All 8 runs completed. The result:

| Window | trades | loss events | delta | universe-1 delta |
|---|---|---|---|---|
| 20260301 | 21 | 0 | +0.00 | +0.00 |
| 20260401 | 21 | 0 | +0.00 | +0.00 |
| 20260501 | 47 | 0 | +0.00 | **−10.95** |
| 20260604 | 41 | 0 | +0.00 | +0.00 |

**130 trades across four windows and not one losing trade.** The overlay's trigger needs
two closed losing trades on a pair within 48h, so +0.00 everywhere is a tautology, not
evidence.

It is not a version artifact: these runs use the same historical X7 builds that produced
5, 5, 2 and 1 loss events on universe 1. Same versions, same windows, different pairs, and
the tail vanishes entirely — along with most of the activity (21-47 trades against
universe 1's ~200).

**The reason is the point.** The pairs upstream lists but the live bot never traded are the
quiet ones; the live bot uses a *volume* pairlist, so it trades precisely the active pairs
where the tail lives. A second universe that is genuinely independent of universe 1 is
therefore also independent of the risk, and cannot test a risk-management rule. Any
universe with enough tail events to test H1 must include the active pairs, at which point
it is no longer independent.

Rule 4 is retired for H1. **H1 remains unresolved**, and its justification still rests on
the live n=2 counterfactual (ZEC and SIREN re-entering hours after dooming) plus universe
1's single firing, which cost +9.15. No further compute is being spent chasing rule 4.
