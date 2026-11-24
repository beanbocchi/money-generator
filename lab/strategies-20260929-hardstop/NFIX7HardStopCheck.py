"""Backtest-only check for ADR-0028: the live NFIX7Risk hard stop, with HARD_STOP_FROM at
the window start so every trade gets it, must reproduce H9's stop55-20261001.

Two live checks are switched off because a backtest cannot replay them honestly: the
delisting lookup reads today's exchange state (lookahead), and the TradFi check reads
today's market list. Neither applies to the window's crypto pairs.
"""
import os
import sys
from datetime import datetime, timezone

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from NFIX7Risk import NFIX7Risk


class NFIX7HardStopCheck(NFIX7Risk):
    HARD_STOP_FROM = datetime(2026, 10, 1, tzinfo=timezone.utc)

    def _delisting_time(self, pair):
        return None
