# ADR-0019: An LLM entry or exit layer

- Status: rejected (not testable on this rig)
- Date: 2026-07-30

## Original record

Moved verbatim from `lab/HYPOTHESES.md` on 2026-11-15; dates inside are the original ones.

## What is deliberately not tested

**An LLM entry or exit layer.** Not because it is a bad idea, but because it cannot be
validated by replay, which is the only validation this rig performs. A model trained
through 2026 already knows ZEC collapsed on 2026-06-05; asking it in a backtest whether
to hold ZEC on 2026-06-04 measures recall, not skill, and the resulting improvement
would be unfalsifiable. Legitimate LLM uses live outside this rig: an event gate on text
the price series does not contain (delistings, exploits, depegs), validated forward-only
in dry-run; and offline analysis of the bot's own trade history, which is what produced
the LowProfitPairs rule in the first place.
