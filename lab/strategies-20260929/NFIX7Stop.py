"""
NFIX7Stop -- NFIX7Risk plus a hard stop that NFI cannot veto.

Backtest only. This file lives in lab/strategies-<date>/ and never in
user_data/strategies/, so the live bot cannot load it. H9 (ADR-0020 in docs/adr/) is the
pre-registration; read it before changing anything here.

Why an overlay is needed at all
-------------------------------
Since upstream commit 54ab6f2f6 (2026-04-19) X7 runs system_v3_2 with
system_v3_2_stops_enable = False, so no live trade has a doom stop; stoploss = -0.99 is
the only one left. And that one is dead too: NFI's confirm_trade_exit returns False for
every "stop_loss" / "trailing_stop_loss" exit unless the rate is already past the
liquidation price. Setting stoploss = -0.6 on a subclass would therefore change nothing,
in backtest or live.

A stop placed ON THE EXCHANGE (order_types.stoploss_on_exchange) is different: Binance
fills it, and freqtrade only learns afterwards, so no strategy hook can cancel it. This
overlay simulates that by letting stop exits through confirm_trade_exit and deferring
every other exit to NFI unchanged.

What the stop bounds
--------------------
freqtrade pins a static stop at the FIRST entry: adjust_stop_loss(initial=True) is a no-op
once a stop is set, and a static stop only ever walks up. Every grind add therefore fills
above the stop price, the average entry stays above it, and a stop at S of margin caps the
whole position's loss at S of its margin however much NFI added. At 2x, S = 0.40 is a 20%
adverse move from the first entry; liquidation sits near 49%.

One class per level, so a matrix row selects the level through its STRATEGY column and
backtest.sh needs no new variable.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from NFIX7Risk import NFIX7Risk

# freqtrade's backtest names a stop exit "stop_loss"; live, an exchange-side stop that
# filled is recorded as "stoploss_on_exchange". Both must pass.
_STOP_EXITS = ("stop_loss", "stoploss_on_exchange")


class NFIX7Stop(NFIX7Risk):
  def confirm_trade_exit(self, pair, trade, order_type, amount, rate, time_in_force,
                         exit_reason, current_time, **kwargs) -> bool:
    if exit_reason in _STOP_EXITS:
      return True
    return super().confirm_trade_exit(pair, trade, order_type, amount, rate, time_in_force,
                                      exit_reason, current_time, **kwargs)


class NFIX7Stop40(NFIX7Stop):
  stoploss = -0.40


class NFIX7Stop55(NFIX7Stop):
  stoploss = -0.55


class NFIX7Stop70(NFIX7Stop):
  stoploss = -0.70


class NFIX7Stop85(NFIX7Stop):
  stoploss = -0.85
