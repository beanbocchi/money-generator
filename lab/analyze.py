#!/usr/bin/env python3
"""One diagnostic panel, two possible inputs: the live sqlite trade database or a
freqtrade backtest result JSON.

Why one code path matters: validating the backtest rig means comparing its output to
the live history. If each side is measured by its own ad-hoc script, any difference
could come from the measuring code rather than from the data, and the comparison proves
nothing. So both inputs are normalised into the same record shape and run through the
same functions.

Usage:
    python3 lab/analyze.py live user_data/khoakomlem_binance_futures-tradesv3.sqlite
    python3 lab/analyze.py backtest lab/results/NFIX7Risk-20260228-20260716.json
    python3 lab/analyze.py compare <A> <B>      # any mix of live:/backtest: prefixes
    python3 lab/analyze.py margin <A> [wallet]  # wallet read from a backtest export
    python3 lab/analyze.py --self-test
"""

import json
import sqlite3
import statistics as st
import sys
from collections import defaultdict
from datetime import datetime, timezone

# --------------------------------------------------------------------------- loading


def _norm(pair, profit_abs, stake, leverage, is_short, open_date, close_date,
          open_rate, min_rate, max_rate, exit_reason, enter_tag):
    return dict(pair=pair, profit=float(profit_abs or 0.0), stake=float(stake or 0.0),
                leverage=float(leverage or 1.0), is_short=bool(is_short),
                open=str(open_date), close=str(close_date),
                open_rate=float(open_rate or 0.0),
                min_rate=float(min_rate) if min_rate else None,
                max_rate=float(max_rate) if max_rate else None,
                exit_reason=exit_reason or "?", enter_tag=enter_tag or "?")


def load_live(path):
    con = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    out = []
    for r in con.execute("select * from trades where is_open=0"):
        out.append(_norm(r["pair"], r["close_profit_abs"], r["stake_amount"],
                         r["leverage"], r["is_short"], r["open_date"], r["close_date"],
                         r["open_rate"], r["min_rate"], r["max_rate"],
                         r["exit_reason"], r["enter_tag"]))
    con.close()
    return out


def load_backtest(path):
    d = json.load(open(path))
    strats = d.get("strategy", {})
    if not strats:
        raise SystemExit(f"{path}: no 'strategy' key -- is this a backtest export?")
    if len(strats) > 1:
        print(f"note: {len(strats)} strategies in file, using all of them",
              file=sys.stderr)
    out = []
    for trades in (s.get("trades", []) for s in strats.values()):
        for t in trades:
            if t.get("is_open"):
                continue
            out.append(_norm(t["pair"], t.get("profit_abs"), t.get("stake_amount"),
                             t.get("leverage"), t.get("is_short"), t["open_date"],
                             t["close_date"], t.get("open_rate"), t.get("min_rate"),
                             t.get("max_rate"), t.get("exit_reason"),
                             t.get("enter_tag")))
    return out


def load_metrics(path):
    """Headline metrics straight from a backtest export, plus a judgement on whether the
    reported drawdown is real.

    freqtrade's drawdown is computed on the closed-trade equity curve, and a backtest
    force-closes everything still open on its last day. So a run whose drawdown window
    ENDS at the backtest cutoff is usually reporting the mark-to-market of positions that
    were still working, not a drawdown the strategy actually suffered. That happened here:
    a run reported 19.19% drawdown ending exactly at 2026-07-16 00:00:00, the cutoff, and
    all of it came from 4 boundary force-exits."""
    d = json.load(open(path))
    name, s = next(iter(d["strategy"].items()))
    end = str(s.get("backtest_end", "")) or str(s.get("drawdown_end", ""))
    dd_end = str(s.get("drawdown_end", ""))
    suspect = bool(dd_end) and bool(s.get("backtest_end")) and \
        dd_end[:16] == str(s["backtest_end"])[:16]
    return dict(
        strategy=name, trades=s.get("total_trades"), wins=s.get("wins"),
        losses=s.get("losses"), winrate=s.get("winrate"),
        profit_abs=s.get("profit_total_abs"), profit_pct=s.get("profit_total"),
        dd_abs=s.get("max_drawdown_abs"), dd_pct=s.get("max_drawdown_account"),
        dd_start=str(s.get("drawdown_start", "")), dd_end=dd_end,
        backtest_end=str(s.get("backtest_end", "")),
        dd_is_cutoff_artifact=suspect,
        profit_factor=s.get("profit_factor"),
    )


def _is_bt(spec):
    """Same guess load() makes, so the force_exit rule matches the input type."""
    return spec.startswith("backtest:") or spec.endswith(".json")


def load(spec):
    """Accepts 'live:PATH', 'backtest:PATH', or a bare path guessed by extension."""
    if spec.startswith("live:"):
        return load_live(spec[5:])
    if spec.startswith("backtest:"):
        return load_backtest(spec[9:])
    return load_backtest(spec) if spec.endswith(".json") else load_live(spec)


# ------------------------------------------------------------------------- measuring


def mae(t):
    """Max adverse excursion as a positive price fraction. None when unknowable."""
    o = t["open_rate"]
    if not o:
        return None
    ref = t["max_rate"] if t["is_short"] else t["min_rate"]
    if not ref:
        return None
    return (ref / o - 1) if t["is_short"] else (1 - ref / o)


def summary(T):
    pnl = [t["profit"] for t in T]
    wins = [p for p in pnl if p > 0]
    losses = [p for p in pnl if p <= 0]
    s = sorted(pnl)
    tail = {}
    for k in (1, 3, 5, 10):
        if len(s) >= k and losses:
            tail[k] = (sum(s[:k]), sum(s[:k]) / sum(losses))
    return dict(
        n=len(T), net=sum(pnl), n_wins=len(wins), n_losses=len(losses),
        win_sum=sum(wins), loss_sum=sum(losses),
        win_rate=(len(wins) / len(T)) if T else 0.0,
        avg_win=st.mean(wins) if wins else 0.0,
        avg_loss=st.mean(losses) if losses else 0.0,
        tail=tail,
    )


def bootstrap_net(T, iters=4000, seed=7):
    """Resampled interval for net P&L, and the count of loss events it rests on.

    Exists because of a specific failure: this session repeatedly produced point
    estimates from one or two loss events and stated them as if they were precise. A
    strategy that wins 94% of the time and loses 12x as much when it loses has almost all
    of its variance in the tail, so a mean computed over a window containing ONE tail
    event carries no information about the next window. Printing an interval alongside
    every figure makes that visible instead of leaving it to be remembered.

    Plain resampling with replacement, no dependencies. It cannot manufacture information
    that is not in the sample -- with n_losses=1 the interval will be embarrassingly wide,
    which is the point."""
    import random
    pnl = [t["profit"] for t in T]
    if not pnl:
        return None
    rng = random.Random(seed)
    n = len(pnl)
    sums = []
    for _ in range(iters):
        sums.append(sum(pnl[rng.randrange(n)] for _ in range(n)))
    sums.sort()
    lo = sums[int(0.025 * iters)]
    hi = sums[int(0.975 * iters) - 1]
    return dict(lo=lo, hi=hi, n_losses=sum(1 for x in pnl if x <= 0), n=n)


def loss_days(T):
    """Losses clustering by calendar close-day. Simultaneous clusters are the reason
    reactive protections fire too late, so 'days with more than one loss' is the
    number that matters, not 'days with a loss'."""
    by = defaultdict(lambda: [0, 0.0, 0, 0.0])
    for t in T:
        d = t["close"][:10]
        i = 0 if t["profit"] > 0 else 2
        by[d][i] += 1
        by[d][i + 1] += t["profit"]
    ld = {d: v for d, v in by.items() if v[2] > 0}
    multi = {d: v for d, v in ld.items() if v[2] > 1}
    return dict(
        trading_days=len(by), loss_days=len(ld), multi_loss_days=len(multi),
        multi_loss_sum=sum(v[3] for v in multi.values()),
        all_loss_sum=sum(v[3] for v in ld.values()),
        detail=sorted((d, v[2], v[3], v[0], v[1]) for d, v in ld.items()),
    )


def both_ways_pairs(T):
    """Pairs that produced both wins and at least one loss -- the signature that says
    pair selection cannot fix the problem, because filtering the pair removes the wins
    too."""
    per = defaultdict(list)
    for t in T:
        per[t["pair"]].append(t["profit"])
    out = []
    for p, v in per.items():
        w = [x for x in v if x > 0]
        l = [x for x in v if x <= 0]
        if w and l:
            out.append((sum(v), p, len(w), sum(w), len(l), sum(l)))
    return sorted(out), len(per)


def entry_count(t):
    """How many entry orders actually filled -- the only sound test for "did this position
    add to itself".

    Do NOT use max_stake_amount > stake_amount for this. max_stake_amount is the PEAK stake
    and stake_amount is the CURRENT (or final) stake, so they differ only when a position
    partially exited after growing. A trade that added and then closed in one exit has
    peak == final, and that test reports it as a single entry.

    This was not hypothetical. Using the max_stake test, H7 reported grinding on 0 of 194
    trades at a 40 USDT wallet versus 29 of 199 at 270, and that gap was the entire basis
    for a "grinding is capital-gated" conclusion. Recounted from entry orders the real
    figures are 58/194 (30%) and 73/199 (37%): grinding fires at every wallet size and the
    gate does not exist. The backtest export carries orders[].ft_is_entry; the live database
    has one row per order in the orders table."""
    return sum(1 for o in (t.get("orders") or []) if o.get("ft_is_entry"))


def split_force_exit(T, is_backtest=True):
    """In a BACKTEST, force_exit rows are everything still open on the final day, marked
    to market at an arbitrary cutoff rather than outcomes the strategy chose; counting
    them corrupts the tail statistics this file exists to produce. One run reported 5
    losses of which 4 were such artifacts and only 1 was a genuine stop_loss.

    In the LIVE database the same exit_reason means the opposite: an operator closed the
    position on purpose, which is a real outcome with real money. Excluding those would
    quietly delete manual interventions from the record -- which is what happened on
    2026-08-05 after trade #91 was force-closed for +0.48, and the panel dropped it.
    So the split only applies to backtest inputs."""
    if not is_backtest:
        return T, []
    real = [t for t in T if t["exit_reason"] != "force_exit"]
    forced = [t for t in T if t["exit_reason"] == "force_exit"]
    return real, forced


def panel(T, label, is_backtest=True):
    T, forced = split_force_exit(T, is_backtest)
    s = summary(T)
    print(f"=== {label} ===")
    if forced:
        print(f"  NOTE: excluded {len(forced)} force_exit rows "
              f"({sum(t['profit'] for t in forced):+.2f}) -- positions closed at the"
              f" backtest cutoff, not strategy decisions")
    if not T:
        print("  no closed trades")
        return
    print(f"  trades {s['n']}  net {s['net']:+.2f}  win rate {s['win_rate']:.1%}")
    b = bootstrap_net(T)
    if b:
        width = b["hi"] - b["lo"]
        warn = ""
        if b["n_losses"] <= 2:
            warn = f"  <-- rests on {b['n_losses']} loss event(s); treat as a direction, not a number"
        elif width > 3 * abs(s["net"]) and s["net"] != 0:
            warn = "  <-- interval is wider than 3x the estimate"
        print(f"  net 95% resampled interval: {b['lo']:+.2f} .. {b['hi']:+.2f}{warn}")
    print(f"  wins {s['n_wins']} ({s['win_sum']:+.2f}, avg {s['avg_win']:+.3f})   "
          f"losses {s['n_losses']} ({s['loss_sum']:+.2f}, avg {s['avg_loss']:+.3f})")
    if s["avg_win"] and s["avg_loss"]:
        print(f"  loss/win size ratio {abs(s['avg_loss'] / s['avg_win']):.1f}x")
    if s["tail"]:
        bits = "  ".join(f"worst{k} {v[0]:+.2f} ({v[1]:.0%})"
                         for k, v in s["tail"].items())
        print(f"  tail concentration: {bits}")
    ms = sorted(x for x in (mae(t) for t in T) if x is not None)
    if ms:
        qs = "  ".join(f"p{int(q*100)} {ms[max(0, int(q*len(ms))-1)]*100:.1f}%"
                       for q in (0.5, 0.75, 0.9, 0.95, 0.99))
        print(f"  MAE (adverse move before recovery or death): {qs}  max {ms[-1]*100:.1f}%")
    ld = loss_days(T)
    print(f"  loss days {ld['loss_days']}/{ld['trading_days']}   "
          f"days with >1 loss {ld['multi_loss_days']} "
          f"holding {ld['multi_loss_sum']:+.2f} of {ld['all_loss_sum']:+.2f}")
    bw, npairs = both_ways_pairs(T)
    print(f"  pairs traded {npairs}; {len(bw)} produced both wins and losses")
    for net, p, nw, sw, nl, sl in bw[:5]:
        print(f"    {p:<18} net {net:+7.2f}  {nw:>3} wins {sw:+7.2f}  "
              f"{nl} losses {sl:+7.2f}")
    worst = sorted(T, key=lambda t: t["profit"])[:5]
    print("  worst 5 trades:")
    for t in worst:
        m = mae(t)
        print(f"    {t['close'][:10]}  {t['pair']:<18} {t['profit']:+7.2f}  "
              f"MAE {m*100 if m is not None else float('nan'):5.1f}%  {t['exit_reason'][:44]}")


# -------------------------------------------------------------- deployed margin

_OPEN_EPS = 0.01  # USDT of remaining basis below which a position counts as closed


def load_fills_live(path):
    """Every filled order in the live database, oldest first.

    The live orders table carries no ft_is_entry column, so an entry is the order whose
    side matches the trade's direction: buy for a long, sell for a short. Cancelled and
    unfilled orders carry filled = 0 and are dropped."""
    con = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    meta = {r["id"]: (float(r["leverage"] or 1.0), bool(r["is_short"]))
            for r in con.execute("select id, leverage, is_short from trades")}
    out = []
    for o in con.execute("select ft_trade_id, ft_order_side, filled, average, price,"
                         " order_filled_date from orders"
                         " where filled > 0 and status = 'closed'"
                         " order by order_filled_date"):
        m = meta.get(o["ft_trade_id"])
        if m is None or not o["order_filled_date"]:
            continue
        lev, short = m
        out.append(dict(ts=str(o["order_filled_date"])[:19], trade=o["ft_trade_id"],
                        leverage=lev, is_short=short,
                        is_entry=o["ft_order_side"] == ("sell" if short else "buy"),
                        amount=float(o["filled"]),
                        price=float(o["average"] or o["price"] or 0.0)))
    con.close()
    return out


def load_fills_backtest(path):
    """Every filled order in a backtest export, oldest first.

    Trades still open at the cutoff are kept. Their capital was deployed while they ran,
    which is what this measures."""
    d = json.load(open(path))
    out = []
    for sname, s in d.get("strategy", {}).items():
        for i, t in enumerate(s.get("trades", [])):
            lev = float(t.get("leverage") or 1.0)
            short = bool(t.get("is_short"))
            for o in (t.get("orders") or []):
                ms = o.get("order_filled_timestamp")
                if ms is None:
                    continue
                out.append(dict(
                    ts=datetime.fromtimestamp(ms / 1000, timezone.utc)
                               .strftime("%Y-%m-%d %H:%M:%S"),
                    trade=(sname, i), leverage=lev, is_short=short,
                    is_entry=bool(o.get("ft_is_entry")),
                    amount=float(o.get("amount") or 0.0),
                    price=float(o.get("safe_price") or 0.0)))
    out.sort(key=lambda f: f["ts"])
    return out


def load_fills(spec):
    """Accepts the same 'live:PATH' / 'backtest:PATH' / bare path forms as load()."""
    if spec.startswith("live:"):
        return load_fills_live(spec[5:])
    if spec.startswith("backtest:"):
        return load_fills_backtest(spec[9:])
    return load_fills_backtest(spec) if spec.endswith(".json") else load_fills_live(spec)


def backtest_wallet(path):
    """The starting balance a backtest ran with, for use as its wallet."""
    d = json.load(open(path))
    s = next(iter(d["strategy"].values()))
    return float(s.get("starting_balance") or 0.0)


def deployed_margin(fills, wallet):
    """Margin held across every open position, sampled after each filled order and divided
    by the wallet.

    Do NOT compute this from max_stake_amount. That field holds a trade's cumulative entry
    cost, so a position that grinds in and out repeatedly reports a figure far above
    anything it held at once: on the live database it gives KAITO #238 as 127.72 USDT
    where the true peak was 42.98, a factor of 3.0. It is the same field whose
    peak-versus-current comparison produced the 2026-08-13 retraction. Basis here is
    tracked per position, entry fills adding cost and exit fills removing it pro rata, so
    the sum is what the account had at risk at that instant.

    wallet is a number, or a callable taking a timestamp and returning the wallet then,
    for a period where deposits moved it.

    The cutoff artifact that load_metrics() warns about does not reach this measurement.
    freqtrade force-exits every open position on the backtest's final day, and those fills
    are exits, so they drive margin toward zero rather than inflating a peak. Margin
    before the cutoff is capital that was genuinely deployed."""
    w_at = wallet if callable(wallet) else (lambda _ts: float(wallet))
    amount, basis, lev, short = {}, {}, {}, {}
    shares, day_max = [], {}
    peak = None
    for f in fills:
        t = f["trade"]
        lev[t], short[t] = f["leverage"], f["is_short"]
        if f["is_entry"]:
            amount[t] = amount.get(t, 0.0) + f["amount"]
            basis[t] = basis.get(t, 0.0) + f["amount"] * f["price"]
        elif amount.get(t, 0.0) > 0:
            a = min(f["amount"], amount[t])
            basis[t] -= basis[t] / amount[t] * a
            amount[t] -= a
        if basis.get(t, 0.0) <= _OPEN_EPS:
            basis.pop(t, None)
            amount.pop(t, None)
        margin = sum(b / lev[k] for k, b in basis.items())
        w = w_at(f["ts"])
        share = margin / w if w else 0.0
        shares.append(share)
        day = f["ts"][:10]
        day_max[day] = max(day_max.get(day, 0.0), share)
        if peak is None or share > peak["share"]:
            peak = dict(ts=f["ts"], margin=margin, share=share,
                        notional=sum(basis.values()), wallet=w,
                        n_open=len(basis),
                        n_long=sum(1 for k in basis if not short[k]))
    if not shares:
        return None
    q = sorted(shares)

    def pct(p):
        return q[max(0, min(len(q) - 1, int(p * len(q)) - 1))]

    return dict(n=len(shares), p50=pct(0.50), p95=pct(0.95), p99=pct(0.99), max=q[-1],
                above={k: sum(1 for s in shares if s >= k / 100)
                       for k in (30, 40, 50, 60)},
                days_above_40=sum(1 for v in day_max.values() if v >= 0.40),
                trading_days=len(day_max), peak=peak)


def margin_panel(fills, wallet, label):
    m = deployed_margin(fills, wallet)
    print(f"=== deployed margin: {label} ===")
    if not m:
        print("  no filled orders")
        return
    p = m["peak"]
    print(f"  fill events {m['n']} over {m['trading_days']} days, "
          f"wallet {p['wallet']:.0f} USDT")
    print(f"  share of wallet held as margin: p50 {m['p50']:.1%}  p95 {m['p95']:.1%}  "
          f"p99 {m['p99']:.1%}  max {m['max']:.1%}")
    print("  fill events at or above  " + "   ".join(
        f"{k}%: {v}" for k, v in m["above"].items()))
    print(f"  days at or above 40%: {m['days_above_40']} of {m['trading_days']}")
    print(f"  peak {p['ts']}  margin {p['margin']:.2f}  notional {p['notional']:.2f} "
          f"({p['notional'] / p['wallet']:.0%} of wallet)")
    print(f"       {p['n_open']} positions open, {p['n_long']} long / "
          f"{p['n_open'] - p['n_long']} short")
    if m["days_above_40"] <= 1:
        print("  NOTE: one day or fewer above 40%. The maximum rests on a single episode"
              " and carries no more weight than any other n=1 observation.")


# ------------------------------------------------------------------------- self-test


def _self_test():
    def mk(pair, profit, open_rate=100.0, min_rate=None, close="2026-01-01",
           is_short=False, reason="x"):
        return _norm(pair, profit, 10, 3.0, is_short, close + " 00:00:00",
                     close + " 01:00:00", open_rate, min_rate, open_rate * 1.01,
                     reason, "tag")

    fails = []
    total = [0]

    def ck(cond, msg):
        total[0] += 1
        print(("  ok: " if cond else "  FAIL: ") + msg)
        if not cond:
            fails.append(msg)

    T = [mk("A/USDT", 1.0), mk("A/USDT", -5.0), mk("B/USDT", 2.0), mk("C/USDT", -1.0)]
    s = summary(T)
    ck(s["n"] == 4 and abs(s["net"] - (-3.0)) < 1e-9, "net and count")
    ck(s["n_wins"] == 2 and s["n_losses"] == 2, "win/loss split")
    ck(abs(s["win_rate"] - 0.5) < 1e-9, "win rate")
    ck(abs(s["tail"][1][0] - (-5.0)) < 1e-9, "worst-1 value")
    ck(abs(s["tail"][1][1] - (5.0 / 6.0)) < 1e-9, "worst-1 share of losses")

    # MAE: long opened at 100 that dipped to 80 is a 20% adverse excursion
    ck(abs(mae(mk("A/USDT", -1, 100.0, 80.0)) - 0.20) < 1e-9, "MAE long")
    # short opened at 100 whose max_rate hit 101 (set by mk) is +1% against
    sh = _norm("A/USDT", -1, 10, 3.0, True, "d", "d", 100.0, 90.0, 101.0, "x", "t")
    ck(abs(mae(sh) - 0.01) < 1e-9, "MAE short uses max_rate")
    ck(mae(_norm("A", 0, 1, 1, False, "d", "d", 100.0, None, None, "x", "t")) is None,
       "MAE None when min_rate missing")

    # clustering: two losses on one day must be counted as a multi-loss day
    C = [mk("A/USDT", -2.0, close="2026-02-01"), mk("B/USDT", -3.0, close="2026-02-01"),
         mk("C/USDT", -1.0, close="2026-02-05"), mk("D/USDT", +9.0, close="2026-02-05")]
    ld = loss_days(C)
    ck(ld["loss_days"] == 2 and ld["multi_loss_days"] == 1, "loss-day counting")
    ck(abs(ld["multi_loss_sum"] - (-5.0)) < 1e-9, "multi-loss-day sum")
    ck(ld["trading_days"] == 2, "trading-day counting")

    F = [mk("A/USDT", -9.0, reason="force_exit"), mk("A/USDT", -1.0, reason="stop_loss")]
    real, forced = split_force_exit(F)
    ck(len(real) == 1 and len(forced) == 1, "backtest force_exit rows are separated out")
    ck(abs(summary(real)["net"] - (-1.0)) < 1e-9, "cutoff force_exits excluded from stats")
    lreal, lforced = split_force_exit(F, is_backtest=False)
    ck(len(lreal) == 2 and len(lforced) == 0,
       "a LIVE force_exit is a real outcome and stays in the statistics")
    ck(_is_bt("x.json") and not _is_bt("db.sqlite"), "input type inferred from the path")

    B1 = bootstrap_net([mk("A", 1.0), mk("A", 1.0), mk("A", 1.0)])
    ck(abs(B1["lo"] - 3.0) < 1e-9 and abs(B1["hi"] - 3.0) < 1e-9,
       "zero-variance sample gives a zero-width interval")
    B2 = bootstrap_net([mk("A", 1.0)] * 20 + [mk("A", -30.0)])
    ck(B2["n_losses"] == 1, "counts the loss events the estimate rests on")
    ck(B2["lo"] < sum([1.0] * 20 + [-30.0]) < B2["hi"],
       "interval brackets the observed net")
    ck(B2["hi"] - B2["lo"] > 20, "one tail event in the sample gives a wide interval")
    ck(bootstrap_net([]) is None, "empty input returns None rather than dividing by zero")

    # A trade that added and then closed in one exit: peak == final, so the old
    # max_stake test called it a single entry. entry_count must see two.
    added_then_closed = dict(orders=[{"ft_is_entry": True}, {"ft_is_entry": True},
                                     {"ft_is_entry": False}],
                             stake=10.0, max_stake=10.0)
    ck(entry_count(added_then_closed) == 2,
       "counts a position that added then closed fully, which max_stake cannot see")
    ck(entry_count(dict(orders=[{"ft_is_entry": True}, {"ft_is_entry": False}])) == 1,
       "a single-entry trade counts as one")
    ck(entry_count(dict()) == 0, "a trade with no order detail counts as zero, not an error")

    bw, npairs = both_ways_pairs(T)
    ck(npairs == 3, "distinct pair count")
    ck(len(bw) == 1 and bw[0][1] == "A/USDT", "both-ways pair detection")

    # a backtest export must parse and must skip still-open trades
    import tempfile, os
    fixture = {"strategy": {"S": {"trades": [
        {"pair": "A/USDT", "profit_abs": 1.5, "stake_amount": 10, "leverage": 2,
         "is_short": False, "open_date": "2026-01-01 00:00:00",
         "close_date": "2026-01-01 05:00:00", "open_rate": 100, "min_rate": 95,
         "max_rate": 105, "exit_reason": "roi", "enter_tag": "t", "is_open": False},
        {"pair": "B/USDT", "profit_abs": 0.0, "stake_amount": 10, "leverage": 2,
         "is_short": False, "open_date": "2026-01-02 00:00:00",
         "close_date": None, "open_rate": 100, "min_rate": 99, "max_rate": 101,
         "exit_reason": None, "enter_tag": "t", "is_open": True}],
        "total_trades": 2, "wins": 0, "losses": 2, "winrate": 0.0,
        "profit_total_abs": 1.5, "profit_total": 0.015, "max_drawdown_abs": 3.0,
        "max_drawdown_account": 0.25, "profit_factor": 1.1,
        "drawdown_start": "2026-01-01 00:00:00", "drawdown_end": "2026-01-05 00:00:00",
        "backtest_end": "2026-01-05 00:00:00"}}}
    fd, p = tempfile.mkstemp(suffix=".json")
    with os.fdopen(fd, "w") as fh:
        json.dump(fixture, fh)
    M = load_metrics(p)
    ck(M["losses"] == 2 and M["dd_pct"] == 0.25, "load_metrics reads headline numbers")
    ck(M["dd_is_cutoff_artifact"] is True,
       "drawdown ending at the backtest cutoff is flagged as an artifact")
    B = load_backtest(p)
    os.unlink(p)
    ck(len(B) == 1, "backtest loader skips open trades")
    ck(abs(B[0]["profit"] - 1.5) < 1e-9 and abs(mae(B[0]) - 0.05) < 1e-9,
       "backtest loader normalises profit and MAE")

    # ---- deployed margin
    def fl(trade, entry, amount, price, ts, lev=2.0, short=False):
        return dict(ts=ts, trade=trade, leverage=lev, is_short=short,
                    is_entry=entry, amount=amount, price=price)

    M = deployed_margin([fl(1, True, 10, 10, "2026-01-01 00:00:00")], 100.0)
    ck(abs(M["peak"]["margin"] - 50.0) < 1e-9, "margin is notional divided by leverage")
    ck(abs(M["max"] - 0.5) < 1e-9, "margin is reported as a share of the wallet")

    M = deployed_margin([fl(1, True, 10, 10, "2026-01-01 00:00:00"),
                         fl(1, False, 10, 11, "2026-01-01 01:00:00")], 100.0)
    ck(abs(M["p50"]) < 1e-9, "a full exit returns deployed margin to zero")

    # The shape that defeats max_stake_amount: grind in, partially exit, grind in again.
    # Cumulative entry cost is 300 and nothing was ever held above 200 of notional.
    K = [fl(1, True, 10, 10, "2026-01-01 00:00:00"),
         fl(1, True, 10, 10, "2026-01-01 01:00:00"),
         fl(1, False, 15, 10, "2026-01-01 02:00:00"),
         fl(1, True, 10, 10, "2026-01-01 03:00:00")]
    M = deployed_margin(K, 1000.0)
    cumulative = sum(f["amount"] * f["price"] for f in K if f["is_entry"])
    ck(abs(M["peak"]["margin"] - 100.0) < 1e-9,
       "peak margin is what was held at once, not what was spent over the trade")
    ck(abs(M["peak"]["notional"] - 200.0) < 1e-9 and abs(cumulative - 300.0) < 1e-9,
       "a ground position does not report its cumulative cost as exposure")

    M = deployed_margin([fl(1, True, 10, 10, "2026-01-01 00:00:00"),
                         fl(2, True, 10, 10, "2026-01-01 01:00:00")], 100.0)
    ck(M["peak"]["n_open"] == 2 and abs(M["peak"]["margin"] - 100.0) < 1e-9,
       "concurrent positions sum into one figure")

    M = deployed_margin([fl(1, True, 10, 10, "2026-01-01 00:00:00", short=True)], 100.0)
    ck(M["peak"]["n_long"] == 0 and abs(M["peak"]["margin"] - 50.0) < 1e-9,
       "a short holds margin and is counted on the short side")

    M = deployed_margin([fl(1, True, 10, 10, "2026-01-01 00:00:00"),
                         fl(1, False, 10, 10, "2026-01-02 00:00:00"),
                         fl(2, True, 10, 10, "2026-02-01 00:00:00")],
                        lambda ts: 100.0 if ts < "2026-02" else 500.0)
    ck(abs(M["max"] - 0.5) < 1e-9, "a wallet moved by a deposit is applied per fill")

    M = deployed_margin([fl(1, True, 10, 10, "2026-01-01 00:00:00"),
                         fl(1, False, 10, 10, "2026-01-01 23:00:00"),
                         fl(2, True, 2, 10, "2026-01-02 00:00:00")], 100.0)
    ck(M["days_above_40"] == 1 and M["trading_days"] == 2,
       "a day counts once, at its own highest reading")

    ck(deployed_margin([], 100.0) is None,
       "empty input returns None rather than dividing by zero")

    bt = {"strategy": {"S": {"trades": [
        {"pair": "A/USDT", "leverage": 2.0, "is_short": False, "orders": [
            {"ft_is_entry": True, "amount": 10, "safe_price": 10.0,
             "order_filled_timestamp": 1767225600000},
            {"ft_is_entry": False, "amount": 10, "safe_price": 11.0,
             "order_filled_timestamp": 1767229200000}]}],
        "starting_balance": 100.0}}}
    fd, p2 = tempfile.mkstemp(suffix=".json")
    with os.fdopen(fd, "w") as fh:
        json.dump(bt, fh)
    FB = load_fills_backtest(p2)
    ck(len(FB) == 2 and FB[0]["is_entry"] and not FB[1]["is_entry"],
       "backtest fill loader reads the entry flag off each order")
    ck(FB[0]["ts"] == "2026-01-01 00:00:00",
       "backtest millisecond timestamps become UTC strings that sort")
    ck(abs(backtest_wallet(p2) - 100.0) < 1e-9, "starting_balance is read as the wallet")
    ck(abs(deployed_margin(FB, backtest_wallet(p2))["max"] - 0.5) < 1e-9,
       "backtest fills and live fills produce the same figures")
    os.unlink(p2)

    print(f"passed={total[0] - len(fails)} failed={len(fails)}")
    return 1 if fails else 0


# ------------------------------------------------------------------------------ main

if __name__ == "__main__":
    a = sys.argv[1:]
    if not a or a[0] in ("-h", "--help"):
        print(__doc__)
        sys.exit(0)
    if a[0] == "--self-test":
        sys.exit(_self_test())
    if a[0] == "compare":
        if len(a) != 3:
            sys.exit("compare needs exactly two inputs")
        panel(load(a[1]), a[1], _is_bt(a[1]))
        print()
        panel(load(a[2]), a[2], _is_bt(a[2]))
        sys.exit(0)
    if a[0] == "delta":
        if len(a) != 3:
            sys.exit("delta needs exactly two backtest export json paths")
        A, B = load_metrics(a[1]), load_metrics(a[2])
        print(f"A = {A['strategy']}  ({a[1].split('/')[-1]})")
        print(f"B = {B['strategy']}  ({a[2].split('/')[-1]})")
        print()
        for k, lbl, fmt in (("trades", "trades", "{:.0f}"),
                            ("losses", "losses", "{:.0f}"),
                            ("winrate", "win rate", "{:.4f}"),
                            ("profit_abs", "profit USDT", "{:+.2f}"),
                            ("dd_abs", "max drawdown USDT", "{:.2f}"),
                            ("dd_pct", "max drawdown %", "{:.4f}"),
                            ("profit_factor", "profit factor", "{:.3f}")):
            va, vb = A.get(k), B.get(k)
            if va is None or vb is None:
                continue
            d = va - vb
            print(f"  {lbl:22} A {fmt.format(va):>12}   B {fmt.format(vb):>12}   "
                  f"A-B {fmt.format(d):>12}")
        print()
        for tag, M in (("A", A), ("B", B)):
            if M["dd_is_cutoff_artifact"]:
                print(f"  WARNING {tag}: drawdown window ends at the backtest cutoff "
                      f"({M['dd_end']}) -- it is the mark-to-market of positions still")
                print(f"           open at the end, not a drawdown the strategy suffered."
                      f" Do not use it as the primary metric.")
        print("  Acceptance (ADR-0007 rules 2-4): drawdown is primary, profit may")
        print("  not fall more than 10%, the effect must plateau across the sweep and")
        print("  survive a second pair universe. A single A/B pair settles none of that.")
        sys.exit(0)
    if a[0] == "margin":
        if len(a) < 2:
            sys.exit("margin needs an input path, and a wallet for a live database")
        spec = a[1]
        path = spec.split(":", 1)[1] if spec.startswith(("live:", "backtest:")) else spec
        if len(a) >= 3:
            w = float(a[2])
        elif _is_bt(spec):
            w = backtest_wallet(path)
        else:
            sys.exit("a live database does not record its wallet -- pass one, and keep "
                     "the window to a period where no deposit moved it")
        margin_panel(load_fills(spec), w, spec)
        sys.exit(0)
    if a[0] in ("live", "backtest"):
        panel(load(f"{a[0]}:{a[1]}"), f"{a[0]} {a[1]}", a[0] == "backtest")
        sys.exit(0)
    panel(load(a[0]), a[0], _is_bt(a[0]))
