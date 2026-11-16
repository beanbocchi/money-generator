# Architecture decision records

Every decision about how the live bot trades, and every hypothesis tested in `lab/`,
has one file here. The folder is the current status:

- `accepted/` is what runs on real money, or the rule the lab works by.
- `rejected/` holds proposals that were turned down, retracted or retired. The
  `Status` line says which.
- `proposed/` holds ideas written down or pre-registered and not yet decided.

## Workflow

1. Copy the template below into `proposed/` with the next free number. Numbers are never
   reused, and a file keeps its number when it changes folder.
2. For a lab hypothesis, write the prediction, the arms and the decision rule before any
   run (ADR-0007). Add the matrix to `lab/matrices/` and name its runs after the ADR.
3. Append results under the pre-registration. Write the numbers into the ADR itself:
   `lab/results/` and `lab/data/` are gitignored and exist on one machine.
4. Decide, update `Status`, and `git mv` the file to `accepted/` or `rejected/`. A later
   correction is appended with its date; the earlier text stays.

```markdown
# ADR-NNNN: <decision or hypothesis>

- Status: proposed | accepted | rejected | retracted | superseded by ADR-NNNN
- Date: <proposed>; <decided>
- Code: <files that implement it>
- Lab: <matrix, strategy overlay, result names, report script>

## Context
## Proposal
## Evidence
## Decision
## Revisit when
```

Records from before 2026-11-15 were moved out of `lab/HYPOTHESES.md` and keep its
original wording under an "Original record" heading.

## Accepted

| ADR | Title | Status | Date |
|---|---|---|---|
| [ADR-0001](accepted/0001-own-project-upstream-as-dependency.md) | Own project, upstream NFI as a replaceable dependency | accepted | 2026-07-30 |
| [ADR-0002](accepted/0002-auto-update-behind-guards.md) | Track upstream automatically behind an import guard and a config guard | accepted | 2026-07-30; config guard added 2026-08-05 |
| [ADR-0003](accepted/0003-leverage-2x.md) | Leverage 2.0, with a hard cap in the strategy | accepted | before 2026-07-30 (config); hard cap 2026-10-30 |
| [ADR-0004](accepted/0004-tradable-balance-ratio-and-slots.md) | tradable_balance_ratio 0.90 with 12 slots | accepted | 2026-07-30; reasoning revised 2026-08-05, 2026-08-13, 2026-10-05 |
| [ADR-0005](accepted/0005-lowprofitpairs-cooldown.md) | LowProfitPairs per-pair cooldown (H1) | accepted, on weak evidence; H1 unresolved | before 2026-07-30; H1 tested 2026-07-31 to 2026-08-14 |
| [ADR-0006](accepted/0006-backtest-methodology.md) | Backtest methodology: version-split windows and honest metrics | accepted | 2026-07-30 to 2026-08-13 |
| [ADR-0007](accepted/0007-lab-acceptance-rule.md) | Lab acceptance rule and pre-registration | accepted; rule 1 superseded by ADR-0006, rule 4 retired by ADR-0016 | 2026-07-30 |
| [ADR-0008](accepted/0008-trade-database-backups.md) | Snapshot the trade database every 6 hours | accepted | 2026-07-31 |
| [ADR-0009](accepted/0009-liveness-watchdog.md) | Liveness watchdog on /api/v1/health and exchange access | accepted | 2026-07-31; credentials check 2026-08-05; re-paging 2026-10-30 |
| [ADR-0010](accepted/0010-updater-fuse.md) | Updater fuse: hold risk-setting changes, cap restarts, avoid fast markets | accepted | 2026-10-30 |
| [ADR-0011](accepted/0011-refuse-tradfi-and-delisting-pairs.md) | Refuse TradFi perps and pairs scheduled for delisting | accepted | 2026-10-30 |
| [ADR-0012](accepted/0012-api-loopback-no-force-entry.md) | Bind the API to loopback and disable force entries | accepted | 2026-10-30 |
| [ADR-0013](accepted/0013-durable-pause-switch.md) | Pause entries with initial_state and /stopentry together | accepted | 2026-10-30 |

## Rejected

| ADR | Title | Status | Date |
|---|---|---|---|
| [ADR-0014](rejected/0014-protections-tuned-on-live-history.md) | Stops and gates tuned on the live trade history | rejected | 2026-07-30 |
| [ADR-0015](rejected/0015-h7-account-size-changes-failure-mode.md) | H7: account size changes the failure mode | retracted (rejected 2026-07-30, supported 2026-07-31, retracted 2026-08-13) | 2026-07-30 |
| [ADR-0016](rejected/0016-rule4-independent-pair-universe.md) | Rule 4: confirm effects on an independent pair universe | rejected (retired for H1 on 2026-08-14) | 2026-07-30 |
| [ADR-0017](rejected/0017-h8-deployed-margin-ceiling.md) | H8: cap concurrent deployed margin | rejected (measurement accepted, ceiling rejected) | 2026-10-05 |
| [ADR-0018](rejected/0018-leverage-3x.md) | Raise leverage to 3x | rejected | 2026-10-30 |
| [ADR-0019](rejected/0019-llm-entry-exit-layer.md) | An LLM entry or exit layer | rejected (not testable on this rig) | 2026-07-30 |
| [ADR-0026](rejected/0026-tsmom-baseline.md) | A volatility-targeted trend baseline on BTC and ETH perps | rejected (no side met the plateau rule) | 2026-11-16 |

## Proposed

| ADR | Title | Status | Date |
|---|---|---|---|
| [ADR-0020](proposed/0020-h9-exchange-hard-stop.md) | H9: a hard stop NFI cannot veto | proposed; pre-registered, queue stopped during its first run on 2026-10-30, no results | 2026-10-30 |
| [ADR-0021](proposed/0021-h2-tighter-hard-stop.md) | H2: a tighter hard stoploss | proposed; pre-registered, never run | 2026-07-30 |
| [ADR-0022](proposed/0022-h3-portfolio-drawdown-halt.md) | H3: a portfolio-level drawdown halt | proposed; pre-registered, never run | 2026-07-30 |
| [ADR-0023](proposed/0023-h4-breadth-entry-gate.md) | H4: an altcoin-breadth entry gate | proposed; pre-registered, never run | 2026-07-30 |
| [ADR-0024](proposed/0024-h5-volatility-scaled-exposure.md) | H5: exposure scaled by volatility regime | proposed; pre-registered, never run | 2026-07-30 |
| [ADR-0025](proposed/0025-h6-pair-age-filter.md) | H6: a longer minimum pair age | proposed; pre-registered, never run | 2026-07-30 |
