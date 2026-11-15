"""
NFIX7Cap -- NFIX7Risk plus a ceiling on concurrent deployed margin.

Backtest only. This file lives in lab/strategies-<date>/ and never in
user_data/strategies/, so the live bot cannot load it.

What it gates and why
---------------------
H8 measured deployed margin, the sum of open positions' stake divided by the wallet,
sampled after every filled order. Across 32 stored runs the median peak is 85.8% of
wallet, and at its peak every one of the 32 held 80% or more of its open positions on one
side. Live reached 68% on 2026-08-22, when NFI opened six positions between 08:31 and
08:47, all long, on one market-wide dip. Twelve slots are independent on paper and close
to one position when the whole market moves together.

Only NEW positions are refused. An entry into a pair that already has an open position is
a grind increment and always passes. Grinding is NFI's main risk mechanism, firing on
30-37% of trades at every wallet size (ADR-0015), and the one measured case
of it being switched off is the wallet-40 arm, which took 2 liquidations and -16.6%.
Capping the thing that rescues losing positions would reproduce that.

freqtrade calls confirm_trade_entry for a new trade and for a position adjustment with no
argument distinguishing them, so an add is identified by a trade already being open on the
pair.

Stake is read from the open trades rather than recomputed, because trade.stake_amount is
already margin for a futures position and is reduced by partial exits. That matches
analyze.py deployed_margin(), so the gate and the measurement use one definition.

Any failure returns True. An overlay that raises inside confirm_trade_entry would refuse
every entry for the rest of the run, which is a larger risk than the one it exists to
bound.

A single A/B settles nothing here. ADR-0007 rule 2 wants a plateau across a sweep of
max_deployed_ratio, and rule 3 wants drawdown to improve with profit not falling more than
10%.
"""

import os
import sys

from freqtrade.persistence import Trade

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from NFIX7Risk import NFIX7Risk


class NFIX7Cap(NFIX7Risk):
    # Share of the wallet that may already be deployed as margin before a new position
    # is refused. Read from NFI_MAX_DEPLOYED so a sweep needs no edit to this file.
    # backtest.sh has to forward the variable into the container for that to take effect.
    # `or` rather than a get() default: docker -e VAR sets an empty string when the
    # host variable is unset, and float("") raises at class-definition time, which
    # aborts the whole run before the first candle.
    max_deployed_ratio = float(os.environ.get("NFI_MAX_DEPLOYED") or 0.50)

    def confirm_trade_entry(self, pair: str, order_type: str, amount: float, rate: float,
                            time_in_force: str, current_time, entry_tag=None,
                            side: str = "long", **kwargs) -> bool:
        try:
            open_trades = Trade.get_trades_proxy(is_open=True)
            if any(t.pair == pair for t in open_trades):
                return True
            wallet = self.wallets.get_total(self.config["stake_currency"])
            if not wallet:
                return True
            lev = float(getattr(self, "futures_mode_leverage", 1.0)) or 1.0
            deployed = sum(float(t.stake_amount or 0.0) for t in open_trades)
            if (deployed + amount * rate / lev) / wallet > self.max_deployed_ratio:
                return False
        except Exception:
            return True
        return True
