"""ADR-0026: volatility-targeted time-series momentum on BTC and ETH perps.

Usage (inside the freqtrade image, via lab/tsmom.sh):
    python3 tsmom.py DATADIR        # full report and the pre-registered verdict
    python3 tsmom.py --self-test

Every constant below is fixed by ADR-0026's pre-registration. Changing one after seeing
results is a new hypothesis with a new ADR, not an edit here.
"""

import glob
import os
import sys

import numpy as np
import pandas as pd

PAIRS = ["BTC_USDT_USDT", "ETH_USDT_USDT"]
LOOKBACKS = [28, 56, 112]
VOL_WINDOW = 30
VOL_TARGET = 0.60
MAX_SCALE = 2.0          # per asset |w| <= 0.5 * 2 = 1, gross <= 2 (ADR-0003)
COST = 0.0007            # per unit of traded notional: 0.05% taker + 0.02% slippage
ANN = 365
WINDOWS = {
    "full": ("2020-07-01", "2026-11-14"),
    "P1": ("2020-07-01", "2021-12-31"),
    "P2": ("2022-01-01", "2022-12-31"),
    "P3": ("2023-01-01", "2024-12-31"),
    "P4": ("2025-01-01", "2026-11-14"),
    "live": ("2026-07-30", "2026-11-14"),
}
SUBPERIODS = ["P1", "P2", "P3", "P4"]
BOOT_ITERS = 2000
BOOT_BLOCK = 20
SEED = 7


# ---------------------------------------------------------------- data

def load(datadir):
    """Daily closes/highs/lows and daily summed funding, indexed by UTC date."""
    base = os.path.join(datadir, "binance", "futures")
    close, high, low, fund, missing = {}, {}, {}, {}, {}
    for p in PAIRS:
        d = pd.read_feather(os.path.join(base, f"{p}-1d-futures.feather"))
        d = d.set_index(d["date"].dt.tz_convert(None).dt.normalize())
        close[p], high[p], low[p] = d["close"], d["high"], d["low"]
        files = glob.glob(os.path.join(base, f"{p}-*-funding_rate.feather"))
        if len(files) != 1:
            raise SystemExit(f"expected one funding file for {p}, found {files}")
        f = pd.read_feather(files[0])
        day = f["date"].dt.tz_convert(None).dt.normalize()
        daily = f.groupby(day)["open"].sum()
        fund[p] = daily.reindex(d.index)
        missing[p] = fund[p].isna()
        fund[p] = fund[p].fillna(0.0)
    df = lambda x: pd.DataFrame(x).sort_index()
    return df(close), df(high), df(low), df(fund), df(missing)


# ---------------------------------------------------------------- strategy

def target_weights(close, lookback, long_short):
    """Weight held over day t+1, computed from data up to and including close t."""
    if lookback is None:                       # benchmark: 50/50 long at 1x
        return pd.DataFrame(0.5, index=close.index, columns=close.columns)
    sig = np.sign(close / close.shift(lookback) - 1.0)
    if not long_short:
        sig = sig.clip(lower=0.0)
    vol = np.log(close).diff().rolling(VOL_WINDOW).std() * np.sqrt(ANN)
    scale = (VOL_TARGET / vol).clip(upper=MAX_SCALE)
    return (sig * 0.5 * scale).fillna(0.0)


def simulate(close, high, low, fund, w):
    """Daily net returns. Row t = P&L over day t on the weights set at close t-1,
    minus funding paid during day t, minus the cost of rebalancing at close t."""
    r = close.pct_change(fill_method=None).fillna(0.0)
    w_prev = w.shift(1).fillna(0.0)            # held during day t
    gross = (w_prev * r).sum(axis=1)
    funding = (w_prev * fund).sum(axis=1)      # long pays a positive rate
    # Weights drift with prices during the day; rebalancing trades the gap to target.
    drift = w_prev * (1.0 + r)
    drift = drift.div((1.0 + gross).replace(0, np.nan), axis=0).fillna(0.0)
    turnover = (w - drift).abs().sum(axis=1)
    cost = COST * turnover
    net = (1.0 + gross - funding) * (1.0 - cost) - 1.0
    # Worst case inside day t for the position held: longs at the low, shorts at the high.
    prev = close.shift(1)
    adverse = np.where(w_prev > 0, low / prev - 1.0, high / prev - 1.0) * w_prev
    worst_intraday = pd.Series(np.nan_to_num(adverse).sum(axis=1), index=close.index)
    return pd.DataFrame({
        "net": net, "gross": gross, "funding": funding, "cost": cost,
        "turnover": turnover, "exposure": w_prev.abs().sum(axis=1),
        "worst_intraday": worst_intraday,
    })


# ---------------------------------------------------------------- metrics

def sharpe(x):
    sd = x.std()
    return float(x.mean() / sd * np.sqrt(ANN)) if sd > 0 else 0.0


def max_drawdown(x):
    eq = (1.0 + x).cumprod()
    return float((1.0 - eq / eq.cummax()).max())


def metrics(sim):
    x = sim["net"]
    eq = (1.0 + x).cumprod()
    years = len(x) / ANN
    total = float(eq.iloc[-1] - 1.0)
    cagr = float(eq.iloc[-1] ** (1.0 / years) - 1.0) if years > 0 else 0.0
    mdd = max_drawdown(x)
    return {
        "days": len(x), "total": total, "cagr": cagr,
        "vol": float(x.std() * np.sqrt(ANN)), "sharpe": sharpe(x), "mdd": mdd,
        "calmar": cagr / mdd if mdd > 0 else float("nan"),
        "fees": float(sim["cost"].sum()), "funding": float(sim["funding"].sum()),
        "turnover": float(sim["turnover"].sum()),
        "in_market": float((sim["exposure"] > 1e-9).mean()),
        "worst_intraday": float(sim["worst_intraday"].min()),
    }


def block_indices(n, rng):
    """Stationary bootstrap (Politis-Romano), geometric blocks with mean BOOT_BLOCK."""
    idx = np.empty(n, dtype=np.int64)
    i = 0
    while i < n:
        start = rng.integers(n)
        length = min(rng.geometric(1.0 / BOOT_BLOCK), n - i)
        idx[i:i + length] = (start + np.arange(length)) % n
        i += length
    return idx


def bootstrap_sharpe(series_by_arm, bench_key):
    """95% intervals for each arm's Sharpe and for arm minus benchmark, same resamples."""
    rng = np.random.default_rng(SEED)
    keys = list(series_by_arm)
    mat = np.column_stack([series_by_arm[k].to_numpy() for k in keys])
    n = mat.shape[0]
    out = {k: [] for k in keys}
    diff = {k: [] for k in keys}
    b = keys.index(bench_key)
    for _ in range(BOOT_ITERS):
        s = mat[block_indices(n, rng)]
        sd = s.std(axis=0, ddof=1)
        sh = np.where(sd > 0, s.mean(axis=0) / sd * np.sqrt(ANN), 0.0)
        for j, k in enumerate(keys):
            out[k].append(sh[j])
            diff[k].append(sh[j] - sh[b])
    q = lambda v: (float(np.percentile(v, 2.5)), float(np.percentile(v, 97.5)))
    return {k: q(out[k]) for k in keys}, {k: q(diff[k]) for k in keys}


# ---------------------------------------------------------------- report

ARMS = [(f"{'ls' if ls else 'lf'}{L}", L, ls) for ls in (False, True) for L in LOOKBACKS]


def run(datadir):
    close, high, low, fund, missing = load(datadir)
    start, end = WINDOWS["full"]
    warm_from = close.index[0]
    print(f"data {warm_from.date()} .. {close.index[-1].date()}, "
          f"{len(close)} days, pairs {', '.join(PAIRS)}")
    if close.loc[start:end].isna().any().any():
        raise SystemExit("gaps in daily closes inside the full window")
    for p in PAIRS:
        m = int(missing[p].loc[start:end].sum())
        print(f"funding missing inside the full window for {p}: {m} days (charged at 0)")

    sims = {}
    for name, L, ls in ARMS + [("bench", None, False)]:
        sims[name] = simulate(close, high, low, fund, target_weights(close, L, ls))

    table = {}
    for wname, (a, b) in WINDOWS.items():
        table[wname] = {k: metrics(s.loc[a:b]) for k, s in sims.items()}

    full = {k: s.loc[start:end, "net"] for k, s in sims.items()}
    ci, ci_diff = bootstrap_sharpe(full, "bench")

    cols = ["total", "cagr", "vol", "sharpe", "mdd", "calmar", "fees", "funding",
            "turnover", "in_market", "worst_intraday"]
    for wname, (a, b) in WINDOWS.items():
        print(f"\n== {wname}  {a} .. {b}")
        print(f"{'arm':6s} " + " ".join(f"{c:>10s}" for c in cols))
        for k, m in table[wname].items():
            print(f"{k:6s} " + " ".join(f"{m[c]:10.3f}" for c in cols))

    print("\n== full-window Sharpe, 95% block-bootstrap interval")
    for k in full:
        lo, hi = ci[k]
        dlo, dhi = ci_diff[k]
        print(f"{k:6s} sharpe {table['full'][k]['sharpe']:6.3f}  [{lo:6.3f}, {hi:6.3f}]"
              f"   minus bench [{dlo:6.3f}, {dhi:6.3f}]")

    print("\n== decision rule (ADR-0026)")
    bench = table["full"]["bench"]
    passed = {}
    for name, L, ls in ARMS:
        m = table["full"][name]
        r1 = m["sharpe"] >= bench["sharpe"] and ci[name][0] > 0
        r2 = m["mdd"] < bench["mdd"] * 2.0 / 3.0
        pos = sum(table[p][name]["total"] > 0 for p in SUBPERIODS)
        r3 = pos >= 3
        passed[name] = r1 and r2 and r3
        print(f"{name:6s} rule1 {'ok' if r1 else 'FAIL'}  rule2 {'ok' if r2 else 'FAIL'}"
              f"  rule3 {'ok' if r3 else 'FAIL'} ({pos}/4 positive)"
              f"  -> {'PASS' if passed[name] else 'fail'}")
    for side, ls in (("long-flat", False), ("long-short", True)):
        names = [n for n, L, s in ARMS if s == ls]
        mid = [n for n, L, s in ARMS if s == ls and L == 56][0]
        n_pass = sum(passed[n] for n in names)
        acc = n_pass >= 2 and passed[mid]
        print(f"{side:10s} {n_pass}/3 pass, L=56 {'pass' if passed[mid] else 'fail'}"
              f"  -> {'ACCEPTED' if acc else 'not accepted'}")


# ---------------------------------------------------------------- self-test

def _self_test():
    fails = 0

    def ck(cond, msg):
        nonlocal fails
        print(f"  {'ok' if cond else 'FAIL'}: {msg}")
        fails += 0 if cond else 1

    idx = pd.date_range("2020-01-01", periods=400, freq="D")
    rng = np.random.default_rng(1)
    noise = rng.normal(0, 0.03, (400, 2))
    up = pd.DataFrame(100 * np.exp(np.cumsum(0.004 + noise, axis=0)), index=idx,
                      columns=PAIRS)
    zero = pd.DataFrame(0.0, index=idx, columns=PAIRS)

    w = target_weights(up, 56, False)
    ck((w.abs() <= 1.0 + 1e-12).all().all(), "per-asset |w| <= 1")
    ck((w.abs().sum(axis=1) <= 2.0 + 1e-12).all(), "gross exposure <= 2")
    ck((w.iloc[:56] == 0).all().all(), "no position before the lookback is warm")
    ck((target_weights(10000 / up, 56, False) >= 0).all().all(), "long-flat never shorts")

    future = up.copy()
    future.iloc[300:] *= 0.5
    ck(target_weights(future, 56, True).iloc[:300].equals(
        target_weights(up, 56, True).iloc[:300]), "weights at t ignore prices after t")

    sim = simulate(up, up, up, zero, w)
    ck(metrics(sim.iloc[150:])["total"] > 0, "a steady uptrend makes money long-flat")

    flat = pd.DataFrame(100.0, index=idx, columns=PAIRS)
    fund = pd.DataFrame(0.001, index=idx, columns=PAIRS)
    wb = target_weights(flat, None, False)
    s = simulate(flat, flat, flat, fund, wb)
    ck(abs(s["net"].iloc[10] - (-0.001)) < 1e-12, "flat price, 1x long pays funding exactly")
    s = simulate(flat, flat, flat, fund, -wb)
    ck(abs(s["net"].iloc[10] - 0.001) < 1e-12, "a short receives positive funding")

    wflip = pd.DataFrame(0.0, index=idx, columns=PAIRS)
    wflip.iloc[10:] = 0.5
    s = simulate(flat, flat, flat, zero, wflip)
    ck(abs(s["cost"].iloc[10] - COST * 1.0) < 1e-12, "entry costs COST x traded notional")
    ck(s["cost"].iloc[11] < 1e-12, "holding at flat prices trades nothing")

    lo = flat.copy()
    lo.iloc[20] = 60.0
    s = simulate(flat, flat, lo, zero, wb)
    ck(abs(s["worst_intraday"].iloc[20] - (-0.4)) < 1e-12,
       "worst intraday sees a 40% wick on a 1x long")

    x = pd.Series([0.1, -0.5, 0.2])
    ck(abs(max_drawdown(x) - 0.5) < 1e-12, "max drawdown from a peak")

    print(f"self-test failed={fails}")
    return fails == 0


if __name__ == "__main__":
    if len(sys.argv) == 2 and sys.argv[1] == "--self-test":
        sys.exit(0 if _self_test() else 1)
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    run(sys.argv[1])
