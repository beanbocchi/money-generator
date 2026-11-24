"""
NFIX7Risk -- thin risk-overlay subclass of NostalgiaForInfinityX7.

Why this file exists
--------------------
Risk changes that need to live somewhere that `git pull` from upstream
(iterativv/NostalgiaForInfinity) will never clobber:

  1. leverage 3.0 -> 2.0        -> set via "nfi_parameters" in user_data/config.json, and
                                   capped here by MAX_LEVERAGE so it holds even if upstream
                                   stops honouring that config key.
  2. a per-pair cooldown        -> HAS to be a strategy attribute. This freqtrade version
                                   hard-rejects "protections" in the config file
                                   ("DEPRECATED: Setting 'protections' in the configuration
                                   is deprecated"), and NostalgiaForInfinityX7 does not
                                   define a `protections` property of its own.
  3. no TradFi perps            -> confirm_trade_entry refuses stock/commodity/FX perps.
  4. no pairs being delisted    -> confirm_trade_entry refuses a pair with a scheduled
                                   delisting.
  5. a hard stop (ADR-0028)     -> custom_stoploss and confirm_trade_exit: a stop at
                                   HARD_STOP of margin, pinned to the first entry, for
                                   trades opened from HARD_STOP_FROM on.

Editing NostalgiaForInfinityX7.py directly would work but upstream touches that file in
almost every commit, so every future pull would conflict. Subclassing keeps the upstream
file pristine and byte-identical to origin/main.

Everything else -- every entry/exit signal, grinding, derisk -- is inherited unchanged
from NostalgiaForInfinityX7. There are no doom stops to inherit: upstream set
system_v3_2_stops_enable = False on 2026-04-19, so before ADR-0028 liquidation was the
only stop.
"""

import logging
import os
import sys
from datetime import datetime, timezone

# The freqtrade resolver already inserts the strategy directory into sys.path while it
# imports this module, but make the parent import independent of that implementation
# detail.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from NostalgiaForInfinityX7 import NostalgiaForInfinityX7

logger = logging.getLogger(__name__)

# Stop exit names: a software stop that has not moved is "stop_loss"; once custom_stoploss
# has raised it above the class -0.99 freqtrade calls it "trailing_stop_loss"; an
# exchange-side stop would be "stoploss_on_exchange". NFI's confirm_trade_exit refuses the
# first two unless the price is already past liquidation.
_STOP_EXITS = ("stop_loss", "trailing_stop_loss", "stoploss_on_exchange")


class NFIX7Risk(NostalgiaForInfinityX7):
    # Hard ceiling, independent of upstream. The 2.0 in config.json only applies while
    # upstream keeps the three futures_mode_leverage* names in NFI_SAFE_PARAMETERS; if it
    # drops or renames one, NFI silently falls back to its class default of 3.0. The
    # updater's fuse holds such a change for approval -- this makes it harmless even if it
    # is approved by mistake. With no doom stop, 3x would put open positions roughly half
    # as far from liquidation (measured 2026-10-30: NXPC +37% at 2x vs ~+17% at 3x).
    MAX_LEVERAGE = 2.0

    # ADR-0028 (level from ADR-0020/H9, accepted in three backtest windows): a stop at
    # HARD_STOP of margin -- a 27.5% adverse move at 2x -- pinned to the price of the
    # trade's FIRST filled entry, so grind adds below it cannot drag it down. That is
    # what all three H9 windows modelled. NFI cannot veto it (confirm_trade_exit below).
    #
    # Only trades opened from HARD_STOP_FROM on. The class stoploss stays -0.99, so on a
    # restart freqtrade's stoploss reinitialisation leaves the trades opened before it
    # exactly as they were, instead of re-basing their stop on today's average entry.
    HARD_STOP = 0.55
    HARD_STOP_FROM = datetime(2026, 11, 24, 15, 45, tzinfo=timezone.utc)
    use_custom_stoploss = True

    def _hard_stop_applies(self, trade) -> bool:
        return trade.open_date_utc >= self.HARD_STOP_FROM

    def custom_stoploss(self, pair, trade, current_time, current_rate, current_profit,
                        **kwargs):
        if not self._hard_stop_applies(trade):
            return None
        fills = trade.select_filled_orders(trade.entry_side)
        if not fills:
            return None
        lev = trade.leverage or 1.0
        first = fills[0].safe_price
        if trade.is_short:
            stop = first * (1 + self.HARD_STOP / lev)
            dist = (stop / current_rate) - 1
        else:
            stop = first * (1 - self.HARD_STOP / lev)
            dist = 1 - (stop / current_rate)
        # freqtrade's stoploss_from_absolute, inlined so this file imports without
        # freqtrade in tests. One difference: freqtrade ignores a 0 (falsy) return, so a
        # price already past the stop would leave the -0.99 stop in place forever.
        # A tiny distance instead puts the stop just behind the price, where it fires.
        return max(min(dist, 1.0), 1e-4) * lev

    def confirm_trade_exit(self, pair, trade, order_type, amount, rate, time_in_force,
                           exit_reason, current_time, **kwargs) -> bool:
        if exit_reason in _STOP_EXITS and self._hard_stop_applies(trade):
            return True
        return super().confirm_trade_exit(pair, trade, order_type, amount, rate,
                                          time_in_force, exit_reason, current_time,
                                          **kwargs)

    def leverage(self, pair, current_time, current_rate, proposed_leverage, max_leverage,
                 entry_tag, side, **kwargs) -> float:
        lev = super().leverage(pair, current_time, current_rate, proposed_leverage,
                               max_leverage, entry_tag, side, **kwargs)
        return min(lev, self.MAX_LEVERAGE)

    def confirm_trade_entry(self, pair, order_type, amount, rate, time_in_force, current_time,
                            entry_tag, side, **kwargs) -> bool:
        """
        Refuses two kinds of new position before NFI's own checks run. freqtrade calls this
        for initial entries only, so it never blocks grinding or exits of an open trade.

        TradFi perps (contractType TRADIFI_PERPETUAL: equities like SOXL/TSLA, plus XAU,
        XAG, FX and indices). The volume pairlist ranks them near the top, but NFI's
        signals are built for crypto. Binance only rejects them today because this
        account has not signed the TradFi agreement -- 72 "-4411" rejections in one day's
        log -- and signing it would quietly start trading leveraged stock ETFs.

        A scheduled delisting. Opening into a pair the exchange is about to close leaves
        NFI no time to wait a drawdown out, which is its only way of recovering one. This
        is freqtrade's DelistFilter check, done here so that upstream's pairlist config
        stays untouched.
        """
        market = self.dp.market(pair) or {}
        if (market.get("info") or {}).get("contractType") == "TRADIFI_PERPETUAL":
            self._log_refusal(pair, "it is a TradFi perp")
            return False
        delist_at = self._delisting_time(pair)
        if delist_at is not None:
            self._log_refusal(pair, f"it is scheduled for delisting at {delist_at}")
            return False
        return super().confirm_trade_entry(pair, order_type, amount, rate, time_in_force,
                                           current_time, entry_tag, side, **kwargs)

    def _delisting_time(self, pair):
        # DataProvider has no public handle on the exchange, so this reaches the same
        # check_delisting_time() DelistFilter uses. If a freqtrade upgrade breaks it, fail
        # open -- refusing every entry would silently stop the bot trading.
        try:
            return self.dp._exchange.check_delisting_time(pair)
        except Exception as e:
            if not getattr(self, "_delist_check_warned", False):
                logger.warning(f"NFIX7Risk: delisting check unavailable ({e!r}); not filtering")
                self._delist_check_warned = True
            return None

    def _log_refusal(self, pair, why):
        # Once per pair per run: the same pair can signal every candle.
        seen = self.__dict__.setdefault("_refused_pairs", set())
        if pair not in seen:
            seen.add(pair)
            logger.info(f"NFIX7Risk: not entering {pair}, {why}")

    @property
    def protections(self):
        """
        Per-pair cooldown once a pair has recently hurt the account.

        Fixes the single largest loss pattern in this bot's live history: re-entering
        the same collapsing pair within hours.
            ZEC   2026-06-04/05: #126 -7.28 USDT, then #131 -7.30 USDT
                                 (the pair alone erased 4.5 months of net profit)
            SIREN 2026-06-13:    #163 -3.93 USDT, then #165 -3.65 USDT, 89 min apart

        LowProfitPairs is used rather than StoplossGuard deliberately. StoplossGuard
        requires exit_reason to be one of freqtrade's native stops
        (stop_loss / trailing_stop_loss / stoploss_on_exchange / liquidation) AND
        close_profit < required_profit. NFI exits its doom stops through custom_exit,
        e.g. "exit_long_tc_stoploss_doom_m ( 143 )", so StoplossGuard would have caught
        only the 2 real liquidations out of the 12 disasters and missed all 10 doom
        stops. LowProfitPairs ignores exit_reason entirely and just sums close_profit
        per pair, so it sees them.

        trade_limit=2 (not 1) is deliberate. Replayed against the SIREN sequence:
            trade_limit=1 -> blocks the +0.37 winner AND the -3.65 doom   (+3.28 USDT)
            trade_limit=2 -> blocks only the -3.65 doom                   (+3.65 USDT)
        Requiring two closed trades whose summed profit is negative lets one unlucky
        small loss through but still catches the repeated-damage pattern.

        Replayed over the 208 closed trades in the live DB:
            post-blacklist universe (what the upstream update yields): +6.83 USDT
                -> blocks ZEC #131 (-7.30), sacrifices ZEC #130 (+0.46)
            full history: +9.30 USDT, catching both the ZEC and the SIREN second doom
        Caveat: that counterfactual cannot model the capital freed by a blocked trade,
        and it rests on n=2 doom events, so treat the parameters as reasonable rather
        than optimal.

        LowProfitPairs.global_stop() returns None, so this can only ever lock the
        offending pair -- it can never halt the whole bot.
        """
        return [
            {
                "method": "LowProfitPairs",
                "lookback_period": 2880,  # 48h window of closed trades for the pair
                "trade_limit": 2,  # need >= 2 closed trades in that window
                "stop_duration": 720,  # lock the pair for 12h
                "required_profit": 0.0,  # trigger when their summed profit is negative
            }
        ]
