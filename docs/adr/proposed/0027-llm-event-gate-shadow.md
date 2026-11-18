# ADR-0027: An LLM news-event entry gate, run in shadow first

- Status: proposed; shadow period restarted 2026-11-18 on a revised prompt, runs to 2027-02-10 (see second deviation)
- Date: 2026-11-16
- Code: `services/newsgate/newsgate.py` (self-test with `--self-test`), `bin/newsgate.sh`
- Tests: `tests/test_newsgate.sh`
- Run: `bin/newsgate.sh once` to check, then `bin/newsgate.sh start`; `bin/newsgate.sh windows` lists would-block windows
- Related: ADR-0019 (why an LLM layer cannot be replayed), ADR-0011 (scheduled delistings), ADR-0026

## Context

ADR-0019 rejected an LLM entry or exit layer for the lab because a model trained through
2026 recalls what happened, so a backtest of it measures memory, not skill. That ADR named
the use that stays legitimate: an event gate on text the price series does not contain,
validated forward-only.

NFI buys dips and waits them out, and liquidation is its only stop (ADR-0020). That
works when a dip is noise and fails when the dip has a cause that keeps working: the
ZEC collapse of 2026-06-05, the Binance hack night of 2026-10-29/30 with nine longs open.
ADR-0011 already refuses pairs with a delisting the exchange has announced through its
API. What no API reports is an exploit, a depeg, a regulator's action or an exchange
incident. Those arrive as text.

The evidence on news for price prediction is negative: ChatGPT news sentiment for Bitcoin
improves the in-sample fit but not out-of-sample forecasts (J Big Data, 2026), because
headlines mostly describe a move that has already happened. This gate does not predict
prices. Its only question is narrower: after a headline names a real negative event for a
coin, is buying that coin's dip over the next day worse than buying any other dip?

## Proposal

A sidecar, `services/newsgate/`, in shadow mode. It never talks to the bot.

1. Every 5 minutes it fetches new items from Binance's announcement feed and a short list
   of crypto news RSS feeds. Which feeds, and whether Binance's public announcement
   endpoint answers from this IP, is settled at build time and recorded here before the
   shadow period starts.
2. New items are sent in one batch to Claude, with structured output: per item an event
   type (`exploit`, `hack`, `depeg`, `delisting`, `regulatory`, `exchange_incident`,
   `none`), a scope (`MARKET` or a list of base tickers), a severity 0-3 and a one-line
   reason.
3. Severity 2 or more opens a **would-block** window of 24 hours on the named tickers, or
   on every pair for `MARKET`.
4. Every item and every decision goes to an append-only JSONL log with three timestamps:
   published, fetched, decided. The gate is judged from the decided time, because that is
   when it could have acted.

If it is accepted later, wiring it in is a separate change: `NFIX7Risk.confirm_trade_entry`
reads the current windows and refuses initial entries inside them. That direction is
fail-safe. The gate can only stop a new entry; it can never open, close or resize a trade,
so a manipulated or misread headline costs at most a missed entry.

## Evaluation, fixed before the shadow period

The shadow period is 12 weeks (84 days) from the first logged decision.

Measured:

1. For each ticker-specific window, the perp's return over 24h and 72h from the decided
   time, minus the median return of the bot's whitelist pairs over the same span
   (abnormal return). Prices come from Binance's public klines.
2. Live counterfactual: initial entries the live bot made on an affected pair inside a
   window, taken from the 6-hourly database snapshots, and each one's all-in result.
3. `MARKET` windows: count, and the share of hours they would have gated.
4. API cost, and how long after publication each decision came.

## Decision rule

The gate is **accepted** for wiring in if all of these hold:

- (a) at least 6 ticker-specific windows with severity 2 or more;
- (b) their mean 72h abnormal return is below -3%, and at least two thirds are negative;
- (c) the live entries that fell inside windows have a combined all-in of zero or less,
  meaning blocking them would not have cost money. If fewer than 3 live entries fell
  inside windows, (c) is not read;
- (d) `MARKET` windows would have gated less than 5% of hours.

It is **rejected** if (b), (c) or (d) fails. If (a) fails, the period is extended once by
another 12 weeks under the same rule. If (a) still fails, the gate is rejected as acting
on events too rare to matter for this bot.

## Prediction

Few ticker-specific events: 3 to 10 in 12 weeks. Their abnormal returns are negative on
average but noisy. The most likely first outcome is an extension under (a), not a verdict.
`MARKET` windows are rare, one or two, and (d) holds.

## Honest limits

- Forward-only, so the model's memory of the past does not leak in. The other side of
  that is a sample of weeks, not years.
- Headlines lag the move. Measuring from the decided time, not the event, is what makes
  that visible instead of hidden.
- Coverage is only as good as the feeds. An event that reaches X hours before any RSS
  feed is outside what this gate sees.
- Headlines are untrusted input to the model. In shadow mode a manipulated one produces a
  log line. Live, it can only block entries.

## Cost

The skill's default model is `claude-opus-5-5` ($4 input, $20 output per million tokens).
At about 150 calls a day with a short cached prompt, that is an estimated $40-50 a month,
large next to a 925 USDT account. `claude-haiku-5-5` ($0.10 / $0.50) would cost about
$1-2 a month for the same calls. Choosing the model is the owner's decision. The estimate
is checked against the API's usage figures in the first week. The sidecar needs its own
`ANTHROPIC_API_KEY` in `.env`, separate from any other credential on the machine.

## Build notes (2026-11-16, before the shadow period)

Settled at build time, as the proposal required:

- **Feeds.** Binance's public announcement endpoint answers from this IP without a key.
  The gate reads three of its catalogs: Latest Binance News (49), Maintenance Updates
  (157) and Delisting (161). Listings, activities, airdrops and API notices are left out.
  RSS: CoinDesk, Cointelegraph, Decrypt, The Block. A test fetch returned 175 items,
  all seven sources up.
- **Model.** `claude-opus-5-5` at effort `low`, with structured output. The owner chose
  it on 2026-11-16: calls go through the owner's local proxy on `127.0.0.1:8317`, which
  has no per-token bill, so the cost estimate above does not apply. The request asks for
  server-side refusal fallbacks; if the proxy rejects that beta, the gate logs it once
  and continues without them.
- **Batches.** Up to 25 headlines per call. Items published more than 24 hours before
  they are first fetched are logged as `skipped_old` and never classified, so the first
  cycle does not judge a day-old backlog as if it were news.
- **Gaps.** The proxy runs only while the owner's machine does. Every cycle writes a
  heartbeat line and every failed call an `api_error` line, so outages show up in the
  log. Unclassified items are retried on the next cycle.
- **Isolation.** A standalone container, `bottrade-newsgate`, outside
  `docker-compose.yml`, with no access to the bot's API or database. Its log is
  `services/newsgate/state/log.jsonl` (gitignored).
- **Credentials.** A key of its own in the proxy's `access.api-keys`, commented
  `newsgate`, stored in `.env` as `NEWSGATE_ANTHROPIC_AUTH_TOKEN`. Revoking it stops
  only the gate.

## Shadow period

Started 2026-11-16. First logged decision 05:40:48 UTC, so the 84 days end
2027-02-08 05:40 UTC.

The first cycle classified 20 fresh headlines (155 older ones skipped) and opened one
window: ASTER, severity 2, `exploit`, "Aster CEO suspects foul play after token plunges
32% in minutes". The live bot had closed its last ASTER long (#395, +1.55) at 2026-11-15
20:17 UTC, about nine hours earlier, and held none when the window opened. Recorded as
the first observation; it counts towards (a) like any other.

The proxy's usage figures are not real token counts (the first call reported 2 input
tokens for 20 headlines), so measurement 4 records calls and latency only.

## Deviation (2026-11-16, one hour into the shadow period)

The owner asked for an OpenAI model in place of Claude. The gate now calls
`gpt-6.1-sol` through the same proxy with the `openai` SDK (pinned 3.40.0) and a strict
JSON schema. The proxy key is unchanged and is now read from `NEWSGATE_PROXY_KEY`, with
`NEWSGATE_BASE_URL` defaulting to `http://127.0.0.1:8317/v1`.

A model change mid-period would mix two classifiers in one sample, so the shadow
period restarts. The Claude hour is kept in `services/newsgate/state/claude-20261116/`
and is not counted. Its one window (ASTER) does not count towards (a).

The first GPT cycle, on the same 21 headlines, put the Coinone customer-data breach at
`MARKET` severity 2, which would have gated every pair for 24 hours. The prompt's
definition of `MARKET` was narrowed to Binance or a top-five exchange halting or being
hacked, a USDT or USDC depeg, or a trading ban in a major economy. That cycle's log is
kept in `services/newsgate/state/build-gpt-20261116/` and is not counted either. The
change was made before the restarted period began and the prompt is fixed from here.

On the rerun GPT rated the ASTER headline severity 0 ("a price plunge and unspecified
suspicion of foul play do not establish a hack or exploit"), where Claude gave it 2.

The restarted period's first decision was logged 2026-11-16 06:41:39 UTC, so the 84
days end 2027-02-08 06:41 UTC. The decision rule is unchanged.

Token counts from the proxy are real for this model: the first call read 1,870 input
tokens and wrote 897 output tokens for 21 headlines. A call with one headline is about
1,100 input tokens, most of it the fixed prompt and schema.

## Amendment (2026-11-16, before any counted window)

One event reported by several feeds opens one window per headline, because items are
deduplicated by feed id, not by story. Left as registered, criterion (a) would count a
story three times if three outlets ran it. From here, windows on the same target whose
spans overlap are merged into one event before (a), (b) and (d) are read. The merged
event starts at its earliest decided time, which is when the gate could first have
acted. The log keeps every window as written.

No prompt cache applies. The fixed prompt and schema are about 834 tokens on
`gpt-6.1-sol`, under the 1,024-token minimum for automatic caching. Five test calls on
2026-11-16 reported 0 cached tokens, with and without `prompt_cache_key`.

## Second deviation (2026-11-18, two days into the shadow period)

After 51 hours the gate had opened four windows, and three were wrong:

| decided (UTC) | target | headline | problem |
|---|---|---|---|
| 11-16 07:51 | BTC | Bitcoin miners were hijacked and their BTC rewards redirected | the victim was the Luxor pool, not BTC |
| 11-16 16:46 | LIT | Lighter suspends all withdrawals after $10.4 million crypto exploit | a real new event |
| 11-16 22:31 | MARKET | Binance hack may push institutions to outside custody | commentary on the 2026-10-29 hack |
| 11-17 09:29 | MARKET | Binance hacker moves $63 million in stolen ether | a follow-up of the same hack |

The two `MARKET` windows overlapped and covered 11-16 22:31 to 11-18 09:29, about 35 of
51 hours, against criterion (d)'s 5%. The one live entry in that span, SHELL (#397) at
11-18 03:26, would have been blocked. LIT rose 8.7% in the 41 hours after its window
opened, after Lighter reopened withdrawals and returned about $5M.

The prompt did not separate a new event from commentary on or follow-ups of an old one,
and it let a coin count as affected when it was only the asset stolen. The owner chose
to fix the prompt and restart the period rather than run 12 weeks towards a certain
rejection under (d). The prompt now:

- allows severity 2 or 3 only for a headline that reports the event as it happens, and
  caps commentary, analysis, attacker fund movements, frozen or returned funds,
  reopenings, lawsuits and post-mortems at severity 1;
- limits `MARKET` to events hitting trading across the market right now;
- counts a coin as affected only if its own protocol, chain or issuer was attacked,
  depegged, delisted or acted against.

Replayed on the same 205 headlines, the revised prompt opened windows only on the two
Lighter headlines (LIT, one event under the merge rule) and none on `MARKET`. That
replay is in-sample: the prompt was written after reading those headlines, so it shows
the fix works on the cases that prompted it and nothing more. The previous log and the
replay are kept in `services/newsgate/state/gpt-prompt1-20261116/` and are not counted.

The restarted period's first decision was logged 2026-11-18 10:16:59 UTC, so the 84
days end 2027-02-10 10:16 UTC. Its first cycle re-classified the 84 headlines from the
preceding 24 hours, which overlap the replay set; items published before the restart at
2026-11-18 10:13 UTC are not counted. The decision rule is unchanged, and the prompt is
fixed from here.
