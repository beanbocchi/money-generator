# lab — backtest rig

Exists because the live trade history cannot answer the questions we want to ask of it.
Over 2026-02-28..2026-07-15 the bot closed 208 trades with a 94.2% win rate, and its
entire P&L was decided by **12 losing trades**, 70% of which landed on 3 days. Every
change tested against that history — tighter stops, a `MaxDrawdown` portfolio gate, a
BTC-regime gate, an altcoin-breadth gate — moved net P&L by less than ±4 USDT on a base
of +14.48, with non-monotonic parameter sweeps. That is what "no measurable signal"
looks like. With 12 tail events you cannot separate a real improvement from luck.

This rig buys statistical power: a backtest over years and hundreds of pairs produces
thousands of trades and hundreds of tail events, which is enough to test the same
hypotheses for real.

## Layout

- `bin/mg download` fetches Binance futures candles (1d/4h, 1h/15m/5m, 1m) into
  `lab/data`. `PAIRLIST=` picks the universe.
- `bin/mg backtest` runs one strategy over one timerange into `lab/results`. `IMAGE=`
  pins the freqtrade image, e.g. by digest.
- `bin/mg queue` runs a matrix of backtests one at a time, skips runs whose result already
  exists, and logs to `lab/logs/queue-<matrix>.log`. The code is in `internal/lab`.
- `analyze.py` reports on result archives; `h9.py` is the pre-registered H9 report.
- `tsmom.py` is ADR-0026's trend baseline on BTC and ETH, run with `bin/mg tsmom
  download|test|run`: a pandas backtest with its own data in `lab/data-tsmom/`, small
  enough to run beside an NFI backtest.
- Pre-registrations, results and retractions are ADRs in `docs/adr/`, filed under
  `proposed/`, `accepted/` or `rejected/`.
- `matrices/` has one TSV per experiment, named after its hypothesis or result prefix
  (`h1-`, `h8-`, `h9-`, `u2-`, `u3-`, `d-`).
- `pairlists/live-traded.json` is the 100 pairs the live bot traded, plus BTC (NFI needs
  BTC data for `btc_info_timeframes`). Six of them (COS, D, FUN, MLN, RLS, TON) have been
  delisted from Binance futures and have no downloadable history. `343.json` is
  upstream's static backtest list and `disjoint.json` a 120-pair universe with no overlap.
- `config-backtest.json` reproduces the early live settings (4 slots, ratio 0.99,
  leverage 3.0); `config-h7.json` the 12-slot 2x setup used from H7 on.
- `configs-pinned/` holds upstream configs frozen for the rig, so an update landing
  between two arms cannot change them.
- `strategies-<date>/` holds the X7 version that existed on that date (gitignored,
  regenerate with `git -C NostalgiaForInfinity show <sha>:NostalgiaForInfinityX7.py`)
  next to the overlays tested against it. Pass one with `STRATDIR=`.

Nothing here touches `user_data/`. The live bot's `user_data/strategies` is mounted
read-only so backtests run the same `NFIX7Risk.py` the bot runs; everything else is
separate. A multi-gigabyte download and a live real-money trade database do not belong
in the same directory.

## Usage

```bash
bin/mg download                                                   # once, then to refresh
bin/mg backtest NFIX7Risk 20260228-20260716                       # one run
nohup bin/mg queue lab/matrices/h9-stop.tsv >/dev/null 2>&1 &     # a whole matrix
python3 lab/analyze.py backtest lab/results/<name>.zip
```

Run one backtest at a time. A 272-pair run peaks around 11.4GB, and two at once push the
live bot into swap.

Two lead-in windows, because NFI's timeframes need very different amounts of history:
its 1d informative carries long moving averages and needs ~200 days before the backtest
start or the first months are computed on incomplete indicators and quietly wrong. 5m
needs only `startup_candle_count` (800 candles ≈ 2.8 days). So 1d/4h are fetched from
2025-01-01 and 1h/15m/5m from 2026-01-01.

## What this rig can and cannot tell you

**It cannot reproduce the live results trade-for-trade, and should not be judged on
that.** Five reasons, all structural:

1. **The strategy file changed constantly.** Upstream touches
   `NostalgiaForInfinityX7.py` in almost every commit; the live bot ran many different
   versions across those five months. A backtest runs one version over the whole period.
2. **The pairlist is selected after the fact.** Live used a dynamic volume pairlist
   drawn from a much larger universe. `pairlists/live-traded.json` contains the 100 pairs
   the bot happened to trade, which is survivorship bias by construction.
3. **Exits are simulated on candles.** Matrices pass `--timeframe-detail 1m`, which
   NFI needs because it grinds and derisks inside the candle, but a live fill still
   slips in ways a 1m candle does not show.
4. **Balance and settings moved.** The account compounded from ~10 to ~43 USDT and the
   slot count and leverage changed. `config-backtest.json` freezes the early-period
   settings (4 slots, ratio 0.99, leverage 3.0).
5. **No blacklist is applied, on purpose.** Today's `blacklist-binance.json` encodes
   which pairs later blew up; applying it to 2026 history is lookahead bias that would
   quietly delete the evidence we are looking for. Upstream's own test recipe does
   include it — do not copy that here.

**What it is good for: comparing two configurations over the same long period.** The
biases above are shared by both arms of such a comparison, so they largely cancel.
"Does the `LowProfitPairs` overlay beat plain X7 over three years and 300 pairs" is a
question this rig answers. "What would my account be worth" is not.
