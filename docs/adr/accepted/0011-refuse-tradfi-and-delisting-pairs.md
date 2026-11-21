# ADR-0011: Refuse TradFi perps and pairs scheduled for delisting

- Status: accepted
- Date: 2026-10-30
- Code: `NFIX7Risk.confirm_trade_entry`
- Tests: `go test ./internal/repo` (`TestNFIX7Risk`)

## Decision

`confirm_trade_entry` refuses Binance `TRADIFI_PERPETUAL` contracts (equities such as
SOXL and TSLA, XAU, XAG, FX, indices) and any pair with a scheduled delisting, before
NFI's own checks. freqtrade calls this hook for initial entries only, so grinding and
exits of open trades are unaffected. If the delisting lookup fails, the check is skipped
and logged once.

## Evidence

The volume pairlist ranks TradFi perps near the top. Binance rejected them only because
the account had not signed the TradFi agreement: 72 "-4411" rejections in one day's
log. A pair being delisted leaves NFI no time to wait out a drawdown, which is its only
way of recovering one.
