# ADR-0018: Raise leverage to 3x

- Status: rejected
- Date: 2026-10-30
- Related: ADR-0003

## Proposal

The operator asked on 2026-10-30 about going back to 3x for more size.

## Reason for rejection

With no doom stop (ADR-0020) liquidation is the only hard stop, and 3x roughly halves
the distance to it: NXPC was +37% from liquidation at 2x against about +17% at 3x. The
alternative given was more capital at 2x. `NFIX7Risk.MAX_LEVERAGE` enforces 2.0.
