# ADR-0015: H7: account size changes the failure mode

- Status: retracted (rejected 2026-07-30, supported 2026-07-31, retracted 2026-08-13)
- Date: 2026-07-30
- Lab: `lab/config-h7.json`, results `H7-w40`, `A-with-protections` (wallet 270), `H7-w780`, `H7-w2700`
- Code: `lab/analyze.py entry_count`

## Summary

H7 predicted that losses appear only at small wallets because grinding needs free
capital. Against today's X7 it was rejected, because that version produces no losses at
any wallet. Redone against the 2026-06-04 X7 it looked supported, with "0 of 194 trades
grinding at wallet 40". That count came from a detector comparing peak and final stake.
Counted from entry orders, grinding fires on 30-37% of trades at every wallet, 58 of 194
at wallet 40. The P&L differences by wallet (-16.6%, +0.7%, +18.2%, +17.9%) are still
measured but have no mechanism, and each rests on 1 to 3 loss events.

The rejection of the first run produced the version-split rule in ADR-0006.

## Original record

Moved verbatim from `lab/HYPOTHESES.md` on 2026-11-15; dates inside are the original ones.

**H7 — account size changes the risk profile in kind, not degree.** Added 2026-07-30
after the first two backtests, before running it.

Sweep `dry_run_wallet` over {40, 270, 2700, 10000} holding live's capital geometry fixed
(`stake_amount: unlimited`, 12 slots, `tradable_balance_ratio: 0.90`, which gives a
free-capital-to-stake ratio of 1.33 at every wallet size), and measure at which wallet
size losing trades start to appear.

The prediction: losses appear only at small wallets. Evidence for it — with a 10,000
wallet the backtest returned 450 trades, **zero** losses, because the only two positions
that went more than 20% against were ground to 31.9x and 28.4x their original stake and
exited positive; whereas live, at 10-43 USDT, ground 1 trade in 208 and took 12 doom
losses. Grinding is the difference, and MAE measured from the first entry can reach -52%
while the position's own loss never approaches NFI's doom threshold, because averaging
down moves the entry.

Why this is the most useful experiment available: it is not a tuning question, it is a
question about the bot as it exists now. The account went from ~10 to 270 USDT on
2026-07-30 and grinding has started for the first time (trade #91, ground to 16.76x
across 13 entries). If the prediction holds, the forward risk profile is rarer losses
that are individually much larger, because each failure involves a position ground to
10-30x the base stake. A backtest reporting zero losses over 450 trades is then not
reassurance — it means the sample contains no instance of grinding failing, which is
precisely the tail being asked about.

Accept/reject is not the point here. The output is a description of how the failure mode
changes with capital, which then determines whether H1-H6 are even asking the right
questions.

## H7 redo, pre-registered 2026-07-31 before running

The original H7 was run against **today's** X7, which produces zero losses over mid-2026
regardless of anything. Sweeping wallet size against a strategy that cannot lose measures
nothing, so "H7 rejected" was premature: the instrument had no dependent variable. Redone
against the **2026-06-04** X7, which does produce a genuine loss.

Design: same version, same window 20260604-20260730, same live geometry (12 slots, ratio
0.90, unlimited stake, leverage 2.0), `--timeframe-detail 1m`, wallets **40 / 270 / 2700**.
270 is already run: 199 real trades, 1 genuine loss (SIREN -53.98, from a position ground
11.5 → 142.1).

Prediction: at 40 USDT a slot is ~3.0 USDT and a 2x notional is ~6 USDT, at or below
Binance's minimum notional for many pairs, so grind increments should be rejected and
positions should reach doom stops un-ground — the live pattern. At 2700 grinding should
have ample room and losses should be rarer but individually larger relative to the stake.

What would count: a monotone relationship between wallet size and (a) how often grinding
fires, (b) the number of genuine losses. Absence of monotonicity means capital is not the
mechanism and I should stop attributing live's losses to it — I have already been wrong
about that once.

**H7 redo — RETRACTED 2026-08-13. The grinding gate was a measurement artifact.**

The mechanism claim below is wrong and I am leaving it visible rather than deleting it,
because it was quoted repeatedly, written into `user_data/config.json`'s comments, and
stored in memory.

Grinding was detected with `max_stake_amount > stake_amount`. That is the PEAK stake
against the CURRENT (or final) stake, so the two differ only when a position partially
exits *after* growing. A trade that adds and then closes in one exit has peak == final and
the test silently reports a single entry. Recounted from `orders[].ft_is_entry`, which is
the only sound test:

| Wallet | trades | grinding by entry count | as I reported it |
|---|---|---|---|
| 40 | 194 | **58 (30%)** | 0 (0%) |
| 270 | 199 | 73 (37%) | 29 (15%) |
| 780 | 199 | 71 (36%) | 34 (17%) |
| 2700 | 195 | 69 (35%) | 32 (16%) |

**Grinding fires at 30-37% at every wallet size, including 40. There is no gate.** The
0/194 figure that the whole conclusion rested on -- the one I called stronger evidence
than the P&L, because it was a count over 194 trades rather than a mean over one loss --
was produced entirely by the detector, not by the strategy.

What survives: the P&L-versus-wallet numbers are still measured (−16.6% / +0.7% / +18.2% /
+17.9%), and wallet 40 still produced 2 liquidations where the larger wallets produced
none. But they are now **unexplained**, and each rests on 1 to 3 loss events with intervals
that include zero at wallet 270. The claim that a deposit was "worth more than any
parameter change" has lost its mechanism and should not be relied on.

Separately, and measured correctly with entry counts, the **live** account did change:
3 of 209 trades added a second entry before the capital increase (1.4%) against 11 of 35
after (31.4%). That jump is real, but the backtest says wallet size alone does not cause
it -- wallet 40 grinds at 30% -- so something else suppressed grinding live, most likely
the older strategy versions the bot was running. Open question, not a finding.

`analyze.py entry_count()` now does this properly and its self-test covers the
add-then-close-fully case the old test could not see.

**Original, incorrect text follows:**

**H7 redo — supported. NFI is capital-gated, and the gate is grinding.**
Run 2026-07-31. 2026-06-04 X7, window 20260604-20260730, live geometry, 1m detail,
`force_exit` rows excluded from every figure below.

| Wallet | Real trades | Genuine losses | Liquidations | Grinding materialised | Realised only | **Account total** | Max grind multiple |
|---|---|---|---|---|---|---|---|
| 40 | 194 | 3 | **2** | **0 / 194 (0%)** | −3.6% | **−16.6%** | 1.0x |
| 270 | 199 | 1 | 0 | 29 / 199 (15%) | +37.5% | **+0.7%** | 20.1x |
| 2700 | 195 | 1 | 0 | 32 / 195 (16%) | +45.4% | **+17.9%** | 150.8x |

Monotone on every column, across two orders of magnitude of capital.

**Correction, 2026-08-01.** The first version of this table reported only the
realised-only column and called it "net % of wallet". That is wrong as a return
figure and I quoted it as one. Excluding `force_exit` rows is right for *tail
statistics* -- they are cutoff artifacts, not decisions -- but you cannot delete four
deeply underwater open positions from an account's performance. `profit_total_abs`,
which marks them to market, is what a real statement would show: at wallet 270 the
eight weeks were **+0.7%, essentially flat**, not +37.5%.

The gap between the two columns is not an accounting quirk, it is the strategy's
defining characteristic: NFI carries large unrealised drawdowns on ground positions
while it waits them out. Realised P&L can look excellent while the account is flat,
because the losers are still open. Any forward estimate must use the account total.

For context, the window was not a bull run: BTC +1.0%, median altcoin **−17.8%**,
freqtrade's own `market_change` −1.9%. Flat is a respectable relative result; it is
not a basis for expecting double-digit monthly returns.

**The mechanism is stronger evidence than the P&L.** NFI *attempts* grinding at every
wallet size — 56 grind-entry log lines at wallet 40, 139 at 270, 639 at 2700 — but at
wallet 40 not one attempt ever materialised into an increased stake, across 194 trades.
Above that level 15-16% of trades grind. So the strategy's core risk-management mechanism
is switched off below some capital threshold, and that is an observation about 194 trades
rather than a difference between a handful of loss events.

**It reproduces the live failure mode, and only at live's capital level.** The live bot
took exactly 2 liquidations in its real history. The wallet-40 run produced exactly 2
liquidations (ZEC, SIREN). At 270 and 2700 there are none. Different pairs, same kind of
death — and it appears only in the capital-starved arm.

**Honest limits.** 3 loss events at wallet 40 and 1 at each larger wallet: the P&L columns
rest on very few observations and the exact percentages should not be quoted as forecasts.
One window, one strategy version, 97 pairs. The likely reason grind increments are
rejected at wallet 40 is Binance's minimum notional — a slot is ~3.0 USDT there, ~6 USDT
notional at 2x — but freqtrade does not log the rejection at this verbosity, so that
specific cause is inferred, not shown. Note also that only the wallet-40 drawdown (51.19%,
window ending 2026-06-13) is genuine; the other two end exactly at the cutoff and are
artifacts.

**What this means for the bot.** The deposit that took the account from ~43 to ~270 USDT
on 2026-07-30 was, on this evidence, worth more than any parameter change tested in this
whole exercise: −3.61% versus +37.50% of wallet over the same eight weeks. Before it, the
bot was running NFI with its main mechanism disabled, which is a sufficient explanation for
12 doom losses and 2 liquidations without invoking anything about signals. Going forward
the tail changes shape rather than disappearing: losses become rarer and smaller as a share
of the account, but individual positions grind to large multiples — 20x at this wallet,
150x at ten times it — so a failure arrives as one very large position rather than several
small ones.

## H7 addendum, 2026-08-05: it plateaus, and the intervals are wide

Wallet 780 added, matching the live account after the second deposit, plus resampled
95% intervals on realised P&L that should have been there from the start.

| Wallet | Account total | Realised | 95% CI (realised) | Grinding | Loss events |
|---|---|---|---|---|---|
| 40 | −16.6% | −1.44 | −43.81 .. +31.38 | 0 / 194 | 3 |
| 270 | +0.7% | +101.24 | −22.34 .. +175.64 | 29 / 199 | 1 |
| 780 | **+18.2%** | +350.76 | **+149.62 .. +473.03** | 34 / 199 | 1 |
| 2700 | +17.9% | +1227.14 | +683 .. +1573 | 32 / 195 | 1 |

Two corrections to how the earlier table was presented. It is **not monotone, it
plateaus**: 780 and 2700 are indistinguishable, and grinding rate is flat from 270 up.
The only large step is crossing the grinding gate, somewhere between 40 and 270. And the
270 interval **includes negative**, so the 40-vs-270 ordering is not established at 95%
even though I presented it as a clean monotone result.

What survives is the mechanism count — 0/194 versus ~17% above the gate — because that is
a count over ~200 trades rather than a mean dominated by one tail event. Every P&L figure
here still rests on 1 to 3 loss events and should be read as a direction.

Practical consequence: the account is now at the plateau. Further capital multiplies
absolute P&L and absolute tail loss together without buying percentage return.

## Forward observations from live trading

Not backtests. Recorded because the rig has exactly one sample of grinding failing and
live now has one too, and they disagree in a way worth keeping.

**2026-08-07, trade #222 TUT/USDT:USDT — first genuine loss at the new capital level, and
the first observed failure of a ground short.** Shorted at 0.03077 average into a rally
that ran +44% in 13 hours. It ground to max_stake 62.55, partially derisked along the way
(realising −14.20 in the process), and never recovered. Force-closed by the operator at
0.03906 with liquidation 9.8% away, for **−32.05 USDT = −4.1% of a 777 USDT account**.

Two things it says. In absolute terms it is the largest loss in the bot's history, 4.4x
the previous worst (ZEC −7.30); as a share of account it is *milder*, −4.1% against ZEC's
−17% on a 43 USDT account, which is the direction H7 predicted for larger capital. And
the mechanism is the one the backtest showed working: grind a position that has gone
against you. In the SIREN backtest case grinding eventually won. Here it did not, because
the move continued. One sample each, opposite outcomes.

Frequency is the uncomfortable part: the backtest at wallet 780 produced 1 loss event per
8 weeks. Live produced one within 2 days of reaching that capital level. n=1 on both
sides, so this is a note to revisit, not a conclusion.
