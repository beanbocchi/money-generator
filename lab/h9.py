"""H9 report: a hard stop NFI cannot veto, against the no-stop baseline.

Computes exactly what H9 (docs/adr/proposed/0020-*) pre-registered, from the result archives, and
applies its decision rule. Written before any H9 run finished; do not tune it afterwards.

    python3 lab/h9.py
"""
import json
import os
import sys
import zipfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from analyze import bootstrap_net  # noqa: E402

R = os.path.join(os.path.dirname(os.path.abspath(__file__)), "results")
# Rerun baselines, not p1-with-*: Binance delisted EPIC, STG and STORJ after those ran, so
# the old baselines trade pairs the arms cannot (see ADR-0020 "Deviation").
WINDOWS = {"20260501": "base-20260501", "20260604": "base-20260604",
           "20261001": "base-20261001"}
# Window 3 (pre-registered 2026-11-21) cuts off inside a drawdown, so its worst loss also
# counts force_exit rows at the cutoff, for every arm and the baseline.
WORST_WITH_CUTOFF = {"20261001"}
LEVELS = ["40", "55", "70", "85"]
STOP_EXITS = ("stop_loss", "stoploss_on_exchange")


def strat(name):
    p = os.path.join(R, name + ".zip")
    if not os.path.exists(p):
        return None
    with zipfile.ZipFile(p) as z:
        n = [x for x in z.namelist() if x.endswith(".json")
             and not x.endswith("_config.json") and "meta" not in x][0]
        return next(iter(json.loads(z.read(n))["strategy"].values()))


def key(t):
    return (t["pair"], t["open_date"][:16])


def report(base, arm, with_cutoff=False):
    T = [t for t in arm["trades"] if not t.get("is_open")]
    real = [t for t in T if t["exit_reason"] != "force_exit"]
    forced = [t for t in T if t["exit_reason"] == "force_exit"]
    stops = [t for t in real if t["exit_reason"] in STOP_EXITS]
    worst_closed = min((t["profit_abs"] for t in real), default=0.0)
    worst = min((t["profit_abs"] for t in (T if with_cutoff else real)), default=0.0)
    deep = sum(1 for t in real if t["profit_ratio"] < -0.20)
    bs = bootstrap_net([{"profit": t["profit_abs"]} for t in real])
    dd_cut = str(arm.get("drawdown_end", ""))[:16] == str(arm.get("backtest_end", ""))[:16]

    consistent = None
    if base is not None and stops:
        first = min(t["close_date"] for t in stops)
        sig = lambda L: sorted((t["pair"], t["open_date"][:16], t["close_date"][:16],
                                round(t["profit_abs"], 4)) for t in L
                               if t["close_date"] < first)
        consistent = sig(base["trades"]) == sig(arm["trades"])

    cf = []
    bmap = {key(t): t for t in base["trades"]} if base else {}
    for t in stops:
        b = bmap.get(key(t))
        if b is None:
            cf.append((t, None, "unmatched"))
        elif b["exit_reason"] == "force_exit" or b["profit_abs"] <= t["profit_abs"]:
            cf.append((t, b, "saved"))
        else:
            cf.append((t, b, "cost"))
    return dict(total=arm["profit_total_abs"], closed=sum(t["profit_abs"] for t in real),
                bs=bs, worst=worst, worst_closed=worst_closed, deep=deep, n_stops=len(stops),
                stop_pnl=sum(t["profit_abs"] for t in stops), forced=len(forced),
                forced_pnl=sum(t["profit_abs"] for t in forced),
                dd=arm.get("max_drawdown_account"), dd_cut=dd_cut,
                consistent=consistent, cf=cf, trades=len(T))


def main():
    rows, verdict = [], {}
    for w, bname in WINDOWS.items():
        base = strat(bname)
        if base is None:
            print(f"missing baseline {bname}")
            continue
        wc = w in WORST_WITH_CUTOFF
        b = report(None, base, wc)
        closed_note = f" (closed only {b['worst_closed']:+.2f})" if wc else ""
        print(f"\n=== window {w}  baseline {bname}: account total {b['total']:+.2f}  "
              f"worst {'incl. cutoff' if wc else 'closed'} {b['worst']:+.2f}"
              f"{closed_note}  closed {b['closed']:+.2f} "
              f"[{b['bs']['lo']:+.0f}..{b['bs']['hi']:+.0f}]  underwater at cutoff "
              f"{b['forced']} ({b['forced_pnl']:+.2f})")
        bound = b["total"] - 0.10 * abs(b["total"])
        for lv in LEVELS:
            arm = strat(f"stop{lv}-{w}")
            if arm is None:
                print(f"  stop{lv}: not run yet")
                continue
            r = report(base, arm, wc)
            ok = r["total"] >= bound and r["worst"] > b["worst"]
            verdict[(lv, w)] = ok
            saved = sum(1 for *_, c in r["cf"] if c == "saved")
            cost = sum(1 for *_, c in r["cf"] if c == "cost")
            if wc:
                ok_closed = r["total"] >= bound and r["worst_closed"] > b["worst_closed"]
                print(f"  stop{lv}: closed-only reading (not decisive) -> "
                      f"{'acceptable' if ok_closed else 'not acceptable'}")
            print(f"  stop{lv}: total {r['total']:+8.2f} (bound {bound:+.2f})  worst {r['worst']:+7.2f}  "
                  f"stops {r['n_stops']} ({r['stop_pnl']:+.2f}) saved/cost/unmatched "
                  f"{saved}/{cost}/{r['n_stops']-saved-cost}  <-20%: {r['deep']}  "
                  f"underwater {r['forced']} ({r['forced_pnl']:+.2f})  "
                  f"closed [{r['bs']['lo']:+.0f}..{r['bs']['hi']:+.0f}]  "
                  f"dd {r['dd']*100 if r['dd'] else 0:.1f}%{' (cutoff)' if r['dd_cut'] else ''}  "
                  f"consistent={r['consistent']}  -> {'ACCEPTABLE' if ok else 'not acceptable'}")
            for t, bt, c in r["cf"]:
                tail = (f"baseline {bt['exit_reason'][:28]} {bt['profit_abs']:+.2f}" if bt else "no baseline twin")
                print(f"      {t['pair']:<18} {t['close_date'][:16]} stop {t['profit_abs']:+7.2f}  {c:<9} {tail}")
    decide(verdict, ["20260501", "20260604"], "windows 1-2, as registered 2026-10-30")
    decide(verdict, list(WINDOWS), "all three windows, deployment rule registered 2026-11-21")


def decide(verdict, windows, label):
    print(f"\n=== decision ({label}): acceptable in every window AND an adjacent level too")
    both = {lv: all(verdict.get((lv, w)) for w in windows) for lv in LEVELS
            if all((lv, w) in verdict for w in windows)}
    if len(both) < len(LEVELS):
        print(f"  incomplete: {len(both)}/{len(LEVELS)} levels have every window")
        return
    acc = [lv for i, lv in enumerate(LEVELS) if both.get(lv) and
           ((i > 0 and both.get(LEVELS[i-1])) or (i + 1 < len(LEVELS) and both.get(LEVELS[i+1])))]
    print(f"  accepted levels: {acc or 'none'}")
    if acc:
        print(f"  recommendation: tightest accepted = 0.{acc[0]} of margin")
    else:
        print("  recommendation: no stop from this rig; quote each level's cost as the insurance price")


if __name__ == "__main__":
    main()
