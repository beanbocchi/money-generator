# ADR-0003: Leverage 2.0, with a hard cap in the strategy

- Status: accepted
- Date: before 2026-07-30 (config); hard cap 2026-10-30
- Code: `user_data/config.json` `nfi_parameters`, `NFIX7Risk.MAX_LEVERAGE`
- Tests: `go test ./internal/repo` (`TestNFIX7Risk`)
- Related: ADR-0018 rejects going back to 3x

## Context

NFI defaults to 3.0. At 3x a -33% adverse move wipes a trade's margin, and NFI's doom
stop (0.20) can be gapped through on a single 5m alt candle; live trades #52 and #13
reached liquidation that way.

## Decision

`futures_mode_leverage`, `futures_mode_leverage_rebuy_mode` and
`futures_mode_leverage_grind_mode` are 2.0 in `config.json`. Since 2026-10-30
`NFIX7Risk.leverage()` also returns at most `MAX_LEVERAGE = 2.0`. The config override only
applies while upstream keeps those names in `NFI_SAFE_PARAMETERS`; if upstream drops one,
NFI falls back to 3.0 silently, and the cap holds anyway.

## Evidence

2x doubles the gap headroom and scales realised P&L down by about a third. Measured
2026-10-30 on open position NXPC: liquidation +37% away at 2x against about +17% at 3x.

## Consequences

Since upstream turned the doom stop off on 2026-04-19 (see ADR-0020), liquidation is
the only hard stop, so leverage sets the distance to it directly.
