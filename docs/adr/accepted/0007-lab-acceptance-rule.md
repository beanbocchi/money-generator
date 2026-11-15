# ADR-0007: Lab acceptance rule and pre-registration

- Status: accepted; rule 1 superseded by ADR-0006, rule 4 retired by ADR-0016
- Date: 2026-07-30
- Code: `lab/analyze.py compare`

## Decision

Hypotheses, grids and the decision rule are written before any run. Results are added
below the pre-registration, and amendments are recorded, never edited in.

## Original record

Moved verbatim from `lab/HYPOTHESES.md` on 2026-11-15; dates inside are the original ones.

Written 2026-07-30, **before any backtest had been run**, deliberately. With hundreds of
tail events and dozens of knobs, testing freely and then deciding which results to
believe manufactures findings. Roughly six hypotheses times ten parameter settings is
sixty tests; at a nominal 5% threshold that is three false positives by construction.
Fixing the hypotheses, the grids and the decision rule in advance is what makes any
later conclusion mean something.

## Amendments

Recorded rather than edited in place. A pre-registration that can be quietly rewritten
after seeing data is not a pre-registration.

**2026-07-30, amendment 1 — the out-of-sample window is not available as specified.**
Rule 1 called for development on 2023-2024 and confirmation on 2025-2026. Downloading
that span cost over ten hours and had not finished 1h/15m/5m, because most of these 97
pairs did not exist in 2023: freqtrade tries a bulk archive per missing day, fails, and
falls back to per-day REST calls. 1m is ~5x the volume, projecting to days. The window
is also weaker than assumed — with only a fraction of the pairs listed in 2023, it is a
different universe rather than the same system in an earlier period, which is not what
rule 1 was meant to test.

So 1m is fetched for 2026 only, and **H7 runs first**, on the live period, where its
output is comparable to the 208 real trades. H1-H6 remain unrun, and rule 1 still
applies to them: they need either a genuine earlier window (a longer download, or
upstream's 343-pair list which has more history) or an explicit replacement rule. They
must not be run on 2026 alone and reported as if the acceptance rule had been met.

## Decision rule

(Rule 1 is superseded by amendment 2. Rules 2-4 stand.)

A change is accepted only if **all four** hold:

1. **Out-of-sample.** Develop on 2023-01-01..2024-12-31, confirm on
   2025-01-01..2026-07-15. An effect that appears only in the development window is
   rejected, not re-tuned.
2. **Plateau, not a spike.** The parameter sweep must show a broad region of
   improvement, not one lucky setting. This is the specific tell that killed every
   candidate on live history: the hard-stop sweep read +1.76 at 8%, −0.24 at 10%,
   +2.41 at 12%, −3.77 at 15%. Non-monotonic sweeps are noise wearing a result's
   clothing.
3. **Both metrics move the right way.** Primary: max drawdown. Secondary: net profit.
   Accept only if max drawdown improves materially and net profit does not fall more
   than 10%. Net profit alone is too noisy in a strategy whose P&L is 12 events wide;
   drawdown is what actually threatens the account.
4. **Survives a pair-universe cut.** The effect must hold on both upstream's 343-pair
   static list and the 100 pairs the live bot traded. An effect that exists in only one
   universe is a property of that universe.

## The null

**H0: none of the below beats baseline `NostalgiaForInfinityX7` out of sample.**

This is the expected outcome, and stating it up front matters. NFI is heavily tuned by
many people over years; the prior that a few hours of parameter search improves it is
low. What the rig is really for is establishing whether the two changes already running
on live money help, hurt, or do nothing.
