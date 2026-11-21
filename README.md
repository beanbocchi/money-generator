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
- `cmd/mg` and `internal/` are the Go tooling: one binary, `mg`, whose subcommands are
  the sidecars (`mg updater`, `mg watchdog`, `mg dbbackup`, `mg newsgate run`) and the
  host tools (`mg status`, `mg approve-update`, `mg rollback`, `mg setup`, and the lab's
  `mg backtest`, `mg download`, `mg queue`, `mg tsmom`). Each command is a package
  under `internal/`. `bin/mg` is the built binary (gitignored) and runs from any
  directory. `Dockerfile` builds the same binary into the sidecar image.
- `services/<name>/state/` holds each sidecar's runtime state (gitignored).
- `backups/db/` receives the 6-hourly database snapshots (gitignored).
- `docs/adr/` records every risk decision and lab hypothesis, filed by status:
  `accepted/`, `rejected/`, `proposed/`.
- `lab/` is the backtest rig; see `lab/README.md`.

## Setup on a new machine

```bash
git clone <this repo> money-generator && cd money-generator
make build           # needs Go 1.27; or build in a container:
                     # docker run --rm -v "$PWD":/src -w /src golang:1.27.4-alpine go build -o bin/mg ./cmd/mg
bin/mg setup         # clones upstream, creates .env, tells you what to fill in
$EDITOR .env
bin/mg setup         # starts the stack
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
| `bottrade-newsgate` | ADR-0027's shadow news gate; reads news feeds, asks an LLM through the local proxy, never talks to the bot |

The watchdog exists because freqtrade's own Telegram only reports trades, so silence is
ambiguous -- "no signals today" and "died three days ago" look the same. It answers one
question and alerts after 3 consecutive failures, debounced because the updater restarts
the bot legitimately whenever upstream changes a watched file.

The backup exists because the sqlite file is the only record of which real positions are
open. It uses `VACUUM INTO` through a read-only connection, not `cp`: the bot writes in
WAL mode and a plain copy can catch the main file and the `-wal` disagreeing. The copy is
switched to WAL like the live file and verified with `integrity_check` before being kept. Restore by stopping the bot and copying a snapshot
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
     Telegram page with the diff, nothing restarted. Approve with `bin/mg approve-update`.
     Measured over 755 upstream commits (2026-07-30 → 10-30) this holds about one update
     a month; it would have held the 2026-04-19 commit that switched the stop off.
   - At most `MAX_RESTARTS_PER_DAY` (4) restarts per 24h, at least `MIN_RESTART_GAP`
     (6h) apart, else wait.
   - No restart while BTC's 30-minute range is ≥ `VOL_LIMIT_PCT` (2%) or its 4-hour range
     ≥ `VOL_LONG_LIMIT_PCT` (4%), or when the price cannot be fetched.
   - No restart while the bot has logged exchange errors (`-2015`, `Could not load
     markets`) in the last 15 minutes. On 2026-11-17 a restart during an IP allow-list
     failure left the bot unable to start for five hours.
4. Import the strategy inside the running bot container. Fails → roll the clone back,
   notify, leave the bot alone.
5. Passes → record the previous commit, `docker restart`, notify.

Follow it with `docker compose logs -f updater`. A held update shows in `bin/mg status`.

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

`bin/mg approve-update` keeps working as the fallback.

The guard catches broken code, not unprofitable code. For a version that loads fine
but trades badly:

```bash
bin/mg rollback     # pins one update back and stops the updater so it sticks
docker start bottrade-updater   # re-enable auto-updates
```

## Everyday commands

```bash
bin/mg status                   # one-shot health + positions + lab progress
docker compose logs -f freqtrade
docker compose restart freqtrade
docker compose run --rm freqtrade show-config --config user_data/config.json
docker compose run --rm freqtrade list-strategies --userdir /freqtrade/user_data
```

After changing Go code, rebuild the sidecars without touching the bot:
`docker compose up -d --build --no-deps updater watchdog dbbackup newsgate`.

## Tests

```bash
make check                      # gofmt, go vet, go test -race ./...
```

The tests are hermetic: temp git repos, fake runners in place of docker, `httptest`
servers in place of Telegram, Binance and the freqtrade API, no containers, nothing
written outside temp directories. `internal/repo` also checks the ignore rules and runs
the self-tests of `NFIX7Risk.py` and `lab/analyze.py` when `python3` is present.
