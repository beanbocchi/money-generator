# ADR-0004: tradable_balance_ratio 0.90 with 12 slots

- Status: accepted
- Date: 2026-07-30; reasoning revised 2026-08-05, 2026-08-13, 2026-10-05
- Code: `user_data/config.json` `tradable_balance_ratio`
- Evidence: ADR-0015 (H7 retraction), ADR-0017 (H8 deployed margin)

## Decision

`tradable_balance_ratio` is 0.90 (was 0.99) with 12 open slots and unlimited stake. At
any wallet W each slot gets 0.90·W/12 and free capital is 0.10·W, a free-to-stake ratio
of 1.33, so the geometry does not change with deposits.

## Evidence

The headroom is for NFI's grinding, which can add several times a slot's stake. Counted
from entry orders, grinding fires on 30-37% of trades at every wallet size tested
(ADR-0015). An earlier justification, that grinding switches off below some capital
level, was retracted on 2026-08-13.

H8 (ADR-0017) measured what the ratio caps: across 32 backtests the median peak is 85.8%
of wallet held as margin, 85.9% at wallet 780, with all 12 slots full and every position
long. Live reached 68% on 2026-08-22.

12 slots was originally forced by exchange minimums: at 267 USDT the ~20 USDT stake only
cleared BTC's minNotional through freqtrade's top-up. Since the 2026-08-05 deposit
(~282 to ~782 USDT) the ~58.65 stake clears every pair, so slot count is a free choice.
It stays at 12 because every parameter change tested against live history landed inside
noise (ADR-0014).
