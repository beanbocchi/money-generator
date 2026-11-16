# money-generator

Freqtrade running NostalgiaForInfinityX7 with a risk overlay, tracking upstream
automatically.

Decisions and their evidence: `docs/adr/README.md`. Original design:
`docs/superpowers/specs/2026-07-30-freqtrade-nfi-project-design.md`

## Layout

- `user_data/` is yours: `config.json` (risk overrides), `strategies/NFIX7Risk.py` (the
  risk overlay), the trade database, logs.
- `NostalgiaForInfinity/` is upstream. A gitignored partial clone that only the updater
  writes to; local edits there are destroyed by `git reset --hard`.
- `services/` holds the sidecars described under "What runs": `updater/`
  (`update.sh`, with the Telegram approval buttons in `approval.sh`), `watchdog/`,
  `dbbackup/`, and `newsgate/`, which runs outside the compose stack.
- `bin/` holds the host scripts: `status.sh`, `approve-update.sh`, `rollback.sh`, `newsgate.sh`,
  `setup.sh`. Each runs from any directory.
- `backups/db/` receives the 6-hourly database snapshots (gitignored).
- `docs/adr/` records every risk decision and lab hypothesis, filed by status:
  `accepted/`, `rejected/`, `proposed/`.
- `lab/` is the backtest rig; see `lab/README.md`.
- `tests/` holds plain-bash tests for all of the above.

## Setup on a new machine

```bash
git clone <this repo> money-generator && cd money-generator
bin/setup.sh         # clones upstream, creates .env, tells you what to fill in
$EDITOR .env
bin/setup.sh         # starts the stack
```

`.env.example` ships `FREQTRADE__DRY_RUN=true`. A fresh machine paper-trades until
you deliberately change that.

To move a **live** bot, also copy its state into `user_data/` before the first start:

```bash
cp /old/user_data/<botname>_<exchange>_<mode>-tradesv3.sqlite* user_data/
cp /old/user_data/nfi*-profit_max-*.json user_data/
```

Do that with the old bot stopped: copying a sqlite file with an active WAL can give
you an inconsistent snapshot. `FREQTRADE__BOT_NAME`, `FREQTRADE__EXCHANGE__NAME` and
`FREQTRADE__TRADING_MODE` must match the database filename, or freqtrade starts
against an empty database and loses track of open positions.

## What runs

| Container | Job |
|---|---|
| `bottrade` | the bot |
| `bottrade-updater` | pulls upstream NFI, restarts the bot behind an import guard |
| `bottrade-watchdog` | polls `/api/v1/health`; alerts to Telegram if the bot stops responding or its loop stalls |
| `bottrade-dbbackup` | consistent sqlite snapshots every 6h into `backups/db/`, keeps 30 |
| `bottrade-newsgate` | ADR-0027's shadow news gate. Not part of the compose stack; started with `bin/newsgate.sh start`, never talks to the bot |

The watchdog exists because freqtrade's own Telegram only reports trades, so silence is
ambiguous -- "no signals today" and "died three days ago" look the same. It answers one
question and alerts after 3 consecutive failures, debounced because the updater restarts
the bot legitimately whenever upstream changes a watched file.

The backup exists because the sqlite file is the only record of which real positions are
open. It uses sqlite's online backup API, not `cp`: the bot writes in WAL mode and a plain
copy can catch the main file and the `-wal` disagreeing. Each snapshot is verified with
`integrity_check` before being kept. Restore by stopping the bot and copying a snapshot
back into `user_data/`.

## How auto-updates work

The `updater` container loops every `UPDATE_INTERVAL` seconds (default 900):

1. `git fetch origin main`; nothing new → sleep.
2. Did any path in `WATCH_PATHS` change? Those are the strategy file and the five
   `configs/` files `config.json` includes. Anything else → advance the clone,
   do not restart.
3. **The fuse**, read from git objects before the working tree moves:
   - Risk settings: class-level settings whose name matches `RISK_PATTERN` (stop,
     doom, leverage, derisk, grind, rebuy, stake, `system_*`, …) plus the membership of
     `NFI_SAFE_PARAMETERS`. Any difference → **held** at the running commit, one
     Telegram page with the diff, nothing restarted. Approve with `bin/approve-update.sh`.
     Measured over 755 upstream commits (2026-07-30 → 10-30) this holds about one update
     a month; it would have held the 2026-04-19 commit that switched the stop off.
   - At most `MAX_RESTARTS_PER_DAY` (4) restarts per 24h, else wait.
   - No restart while BTC's 30-minute range is ≥ `VOL_LIMIT_PCT` (2%), or when the price
     cannot be fetched.
4. Import the strategy inside the running bot container. Fails → roll the clone back,
   notify, leave the bot alone.
5. Passes → record the previous commit, `docker restart`, notify.

Follow it with `docker compose logs -f updater`. A held update shows in `bin/status.sh`.

### Approving from Telegram

With `APPROVAL_TELEGRAM_TOKEN` set, a held update arrives as a message with **✅ Approve**
and **❌ Keep old** buttons; Approve deploys within seconds (restart cap and volatility
gate still apply). Requirements, all enforced:

- A bot of its own (BotFather `/newbot`), not freqtrade's — Telegram allows one
  `getUpdates` consumer per token — and with no webhook set.
- In a group, only user ids in `APPROVER_IDS` can approve. Find yours by running
  `docker compose run --rm --no-deps -e APPROVAL_SELFTEST=1 updater` and pressing the test
  button; run it again after setting `APPROVER_IDS` to confirm it passes.
- A button only approves the exact change it was sent for.

`bin/approve-update.sh` keeps working as the fallback.

The guard catches broken code, not unprofitable code. For a version that loads fine
but trades badly:

```bash
bin/rollback.sh     # pins one update back and stops the updater so it sticks
docker start bottrade-updater   # re-enable auto-updates
```

## Everyday commands

```bash
bin/status.sh                   # one-shot health + positions + lab progress
docker compose logs -f freqtrade
docker compose restart freqtrade
docker compose run --rm freqtrade show-config --config user_data/config.json
docker compose run --rm freqtrade list-strategies --userdir /freqtrade/user_data
bash tests/test_updater.sh      # all tests are plain bash, no framework
```

## Tests

`tests/*.sh` are hermetic: temp git repos and stubbed `docker`/`git` binaries, no
network, no containers, nothing touched outside `mktemp -d`. Run them all:

```bash
for t in tests/test_*.sh; do echo "== $t"; bash "$t" || echo "FAILED: $t"; done
```
