# Checks NFIX7Risk.py against a stub NostalgiaForInfinityX7 (no freqtrade, no 3MB file).
# Run by internal/repo/python_test.go with the strategy and the stub copied into argv[1].
import sys, datetime as dt
sys.path.insert(0, sys.argv[1])
from NFIX7Risk import NFIX7Risk
P = F = 0
def ok(c, m):
    global P, F
    print(("  ok: " if c else "  FAIL: ") + m); P += c; F += (not c)

class Ex:
    def __init__(self, delist=None, boom=False): self.delist, self.boom = delist, boom
    def check_delisting_time(self, pair):
        if self.boom: raise AttributeError("gone in a freqtrade upgrade")
        return self.delist.get(pair) if self.delist else None
class DP:
    def __init__(self, markets, ex): self.markets, self._exchange = markets, ex
    def market(self, pair): return self.markets.get(pair)

def bot(markets=None, ex=None, lev=3.0, confirm=True):
    b = NFIX7Risk(); b.lev = lev; b.confirm = confirm
    b.dp = DP(markets or {}, ex or Ex()); NFIX7Risk.calls = 0
    return b
now = dt.datetime(2026, 10, 30)
args = ("market", 1.0, 1.0, "GTC", now, "4", "long")
COIN = {"info": {"contractType": "PERPETUAL"}}
TRADFI = {"info": {"contractType": "TRADIFI_PERPETUAL"}}

print("case 1: leverage is capped at 2.0 whatever upstream returns")
ok(bot(lev=3.0).leverage("A", now, 1, 3, 75, "4", "long") == 2.0, "upstream 3.0 -> 2.0")
ok(bot(lev=1.5).leverage("A", now, 1, 3, 75, "4", "long") == 1.5, "lower values pass through")

print("case 2: TradFi perps are refused before upstream's checks")
b = bot({"SOXL/USDT:USDT": TRADFI, "XAU/USDT:USDT": TRADFI})
ok(b.confirm_trade_entry("SOXL/USDT:USDT", *args) is False, "equity perp refused")
ok(b.confirm_trade_entry("XAU/USDT:USDT", *args) is False, "gold perp refused")
ok(NFIX7Risk.calls == 0, "upstream confirm not consulted for them")

print("case 3: a pair with a scheduled delisting is refused")
b = bot({"OLD/USDT:USDT": COIN}, Ex({"OLD/USDT:USDT": now + dt.timedelta(days=3)}))
ok(b.confirm_trade_entry("OLD/USDT:USDT", *args) is False, "refused")

print("case 4: ordinary crypto pairs get upstream's own decision, unchanged")
ok(bot({"BTC/USDT:USDT": COIN}, confirm=True).confirm_trade_entry("BTC/USDT:USDT", *args) is True, "upstream yes -> yes")
ok(bot({"BTC/USDT:USDT": COIN}, confirm=False).confirm_trade_entry("BTC/USDT:USDT", *args) is False, "upstream no -> no")
ok(bot({}, confirm=True).confirm_trade_entry("NEW/USDT:USDT", *args) is True, "unknown market info -> upstream decides")

print("case 5: a broken delisting check fails open, not closed")
b = bot({"BTC/USDT:USDT": COIN}, Ex(boom=True))
ok(b.confirm_trade_entry("BTC/USDT:USDT", *args) is True, "still trades when the check is unavailable")

print(f"passed={P} failed={F}")
sys.exit(1 if F else 0)
