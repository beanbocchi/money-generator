# Freqtrade + NostalgiaForInfinity project: design

Date: 2026-07-30

## 1. Context

A live freqtrade bot (`bottrade`, real money, Binance USDT futures, strategy
`NFIX7Risk`, 208 closed trades) has been running out of
`/home/beanbocchi/other/NostalgiaForInfinity` — a clone of
`iterativv/NostalgiaForInfinity` with the operator's own files living inside it.

That directory was moved to `/home/beanbocchi/other/money-generator/backupRepoOld`.
The container survived only because bind mounts follow inodes across a rename. Its
five mount *sources* now point at a path that no longer exists, and Docker
re-creates bind mounts at container **start**, materialising a missing source as an
empty directory. So `/freqtrade/NostalgiaForInfinityX7.py` would become a directory
and the strategy would fail to load: **the bot currently runs but cannot restart.**
A host reboot, a `docker restart`, or a Docker daemon restart takes it down with no
way back up.

Three of the operator's own artifacts were absent from `backupRepoOld` entirely,
surviving only inside the running container and the `nfi-watchdog:local` image:
`NFIX7Risk.py`, `docker-compose.override.yml`, and a 736-line `nfi_watchdog` Python
package. All were recovered before any further work (see §10).

Mixing operator files into an upstream clone is the root cause of both problems: it
made `git pull` risky, and it made "back up my work" indistinguishable from "back up
someone else's repo".

## 2. Goals

- A project the operator owns, where upstream code is a replaceable dependency
  rather than the container for their work.
- Continuous, unattended tracking of upstream NFI: pull, and restart the bot when a
  file the bot actually uses has changed.
- The live bot's trading state carried over intact — same trade database, same open
  positions.
- Fast setup on a second machine.

## 3. Non-goals

- Reinstating `nfi_watchdog`. Explicitly descoped by the operator (KISS). Its source
  is archived, not integrated (§10).
- Reproducing the old `docker-compose.yml` / `override.yml` pair. The new compose
  file is written fresh.
- Backtesting, hyperopt, or any change to trading logic beyond what already exists
  in `config.json` and `NFIX7Risk.py`.
- Running two bot instances on one host. `container_name` is fixed.

## 4. Layout

```
money-generator/                     # the operator's git repo
├── .env                             # gitignored — secrets
├── .env.example                     # tracked
├── .gitignore
├── docker-compose.yml               # tracked — services: freqtrade, updater
├── setup.sh                         # tracked — bootstrap for a fresh machine
├── rollback.sh                      # tracked — pin NFI back to last good commit
├── updater/
│   ├── Dockerfile                   # FROM docker:cli + git + bash + curl
│   ├── update.sh
│   └── state/                       # gitignored — last_good_sha
├── user_data/
│   ├── config.json                  # tracked — risk settings
│   ├── strategies/
│   │   ├── NFIX7Risk.py             # tracked
│   │   └── NostalgiaForInfinityX7.py -> ../../NostalgiaForInfinity/NostalgiaForInfinityX7.py
│   ├── logs/                        # gitignored
│   ├── data/                        # gitignored
│   ├── *.sqlite*                    # gitignored — live trade database
│   └── nfix7-profit_max-*.json      # gitignored — X7 per-pair profit target cache
├── NostalgiaForInfinity/            # gitignored — upstream clone, never edited
├── backups/                         # gitignored
└── backupRepoOld/                   # gitignored — fallback, delete once verified
```

The boundary is a single rule: **operator code lives in `user_data/strategies/`;
`NostalgiaForInfinity/` is upstream-only.** The updater enforces it with
`git reset --hard`, which destroys local edits there. Nothing else in the project
writes to that directory.

### Why a symlink instead of a file mount

A single-file bind mount resolves to an inode. `git` writes a new file and renames
it over the old one, so the inode changes and a file-level mount keeps serving the
old content. Mounting the *directory* has no such problem, so NFI is mounted at
directory level and `user_data/strategies/` holds a relative symlink into it.

`../../NostalgiaForInfinity/NostalgiaForInfinityX7.py` resolves inside the container
because `user_data` is mounted at `/freqtrade/user_data` and NFI at
`/freqtrade/NostalgiaForInfinity`. `user_data/strategies` is also freqtrade's default
strategy directory, so `--strategy-path` is not needed. `NFIX7Risk.py` already does
`sys.path.insert(0, dirname(__file__))`, which is the directory the symlink sits in,
so `import NostalgiaForInfinityX7` resolves.

### Why `NFIX7Risk.py` is kept

It is ~15 lines of code: a `protections` property returning a `LowProfitPairs`
entry (48h lookback, 2 closed trades, 12h pair lock, negative summed profit). It
exists because this freqtrade version rejects `protections` in the config file, so
the setting has to be a strategy class attribute, and because editing
`NostalgiaForInfinityX7.py` directly would conflict on every upstream pull.

It addresses the largest loss pattern in the live history — re-entering the same
collapsing pair within hours (ZEC 2026-06-04/05: -7.28 then -7.30 USDT, erasing 4.5
months of net profit; SIREN 2026-06-13: -3.93 then -3.65, 89 minutes apart).
`LowProfitPairs` rather than `StoplossGuard` because NFI exits its doom stops through
`custom_exit`, which `StoplossGuard` does not match. Removing the subclass reverts to
the old behaviour.

Leverage (3.0 → 2.0) needs no code: it goes through `nfi_parameters` in
`config.json`, which NFI whitelists.

## 5. Component: freqtrade service

```yaml
services:
  freqtrade:
    image: freqtradeorg/freqtrade:stable
    container_name: bottrade
    restart: unless-stopped
    env_file: [.env]
    ports: ["${FREQTRADE__API_SERVER__LISTEN_PORT:-8989}:${FREQTRADE__API_SERVER__LISTEN_PORT:-8989}"]
    volumes:
      - ./user_data:/freqtrade/user_data
      - ./NostalgiaForInfinity:/freqtrade/NostalgiaForInfinity:ro
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:${FREQTRADE__API_SERVER__LISTEN_PORT:-8989}"]
      interval: 90s
      timeout: 10s
      retries: 3
      start_period: 40s
    command: >
      trade
      --config user_data/config.json
      --db-url sqlite:////freqtrade/user_data/${FREQTRADE__BOT_NAME}_${FREQTRADE__EXCHANGE__NAME}_${FREQTRADE__TRADING_MODE}-tradesv3.sqlite
      --log-file user_data/logs/bot.log
```

No image build. The live container runs `freqtradeorg/freqtrade:stable` directly;
upstream's `docker/Dockerfile.custom` (which installs `tests/requirements.txt`) was
never in use, so it is dropped.

`--db-url` with `BOT_NAME=khoakomlem`, `binance`, `futures` yields
`khoakomlem_binance_futures-tradesv3.sqlite` — byte-for-byte the filename the live
bot uses, which is what makes trade history continuous across the cutover.

`FREQTRADE__LOGFILE` is removed from `.env` as it duplicates `--log-file`.

NFI is mounted `:ro` here: the bot has no reason to write into upstream, and `:ro`
makes that structural rather than a convention.

## 6. Component: updater

```yaml
  updater:
    build: ./updater
    container_name: bottrade-updater
    restart: unless-stopped
    env_file: [.env]
    environment:
      - TARGET_CONTAINER=${TARGET_CONTAINER:-bottrade}
      - UPDATE_INTERVAL=${UPDATE_INTERVAL:-900}
      - WATCH_PATHS=${WATCH_PATHS:-NostalgiaForInfinityX7.py configs/trading_mode-futures.json configs/pairlist-volume-binance-usdt.json configs/blacklist-binance.json configs/exampleconfig.json configs/exampleconfig_secret.json}
      - DRY_RUN=${UPDATER_DRY_RUN:-0}
      - RUN_ONCE=${UPDATER_RUN_ONCE:-0}
    volumes:
      - ./NostalgiaForInfinity:/nfi
      - ./updater/state:/state
      - /var/run/docker.sock:/var/run/docker.sock
```

One `while true; sleep` loop, no cron:

1. `git -C /nfi fetch -q origin main`
2. `OLD=$(rev-parse HEAD)`, `NEW=$(rev-parse origin/main)`; equal → sleep.
3. `CHANGED=$(git diff --name-only OLD NEW -- $WATCH_PATHS)`
4. `git reset --hard -q origin/main` (always advance, so the local copy never drifts)
5. `CHANGED` empty → log and sleep. No restart.
6. Otherwise run the import guard (below). On success: write `OLD` to
   `/state/previous_sha`, `docker restart $TARGET_CONTAINER`, notify. On failure:
   `git reset --hard -q OLD`, notify, **do not restart**.
7. `DRY_RUN=1` logs the restart decision instead of acting; `RUN_ONCE=1` exits after
   one iteration. Together they let the updater be tested before cutover without
   touching the live bot.

Step 4 advances the working tree before the guard runs, so for the few seconds until
the guard verdict the on-disk code is newer than what the bot has loaded. That is
harmless because freqtrade reads the strategy only at startup, and the only thing
that restarts the bot is step 6 — after the guard has passed.

### Why `git pull` rather than upstream's `wget` sidecar

Upstream ships an `nfi-updater` service (`docker/updater/*.sh`) that downloads
`${STRATEGY}.py`, the blacklist and one pairlist over `raw.githubusercontent.com`,
plus a 60-second ETag watcher on the blacklist. It is not usable here:

- The README claims the service is "already defined in `docker-compose.yml`". It was
  added on 2026-04-06 (`f2cc3d5e7`) and **removed on 2026-04-11** by `ba1befa3f`
  ("Docker compose: quick fix if multiple instances running"). Only the README and
  the shell scripts remain; there is no such service at HEAD.
- It fetches `${STRATEGY}.py` from GitHub. Here `STRATEGY=NFIX7Risk`, which does not
  exist upstream — a 404 every run, and `NostalgiaForInfinityX7.py`, the file that
  actually needs updating, would never be fetched.

Pulling the whole repo makes the blacklist and pairlists arrive for free, so the
separate ETag watcher, `NFI_PAIRLIST_FILE`, and per-exchange hash state files all
disappear. Restarting by container name (`docker restart bottrade`) also removes
upstream's `COMPOSE_PROJECT_NAME` coupling, the thing its own commit message blames
for breaking with multiple instances.

Git history is also the rollback mechanism: `last_good_sha` plus `reset --hard` is a
one-command revert to the exact code that was trading.

### The guard

```
docker exec $TARGET_CONTAINER python3 -c \
  "import sys; sys.path.insert(0,'/freqtrade/user_data/strategies'); import NFIX7Risk"
```

Importing inside the running container validates the syntax of the 3MB generated
`NostalgiaForInfinityX7.py`, the operator's subclass, and that both class bodies
execute — using the exact Python and freqtrade version that will run the code.
`py_compile` was rejected: it writes `__pycache__` next to the source, which is on a
`:ro` mount, so it would fail for the wrong reason. Import failure to write bytecode
cache is ignored silently by Python, so the read-only mount is not a problem there.

The guard is only meaningful if it can fail, so the test plan (§9) runs the same
command against a deliberately broken file and asserts a non-zero exit.

### `WATCH_PATHS`

Restricted to files the bot actually loads: `NostalgiaForInfinityX7.py` and the five
`configs/` files `config.json` includes (`trading_mode-futures.json`,
`pairlist-volume-binance-usdt.json`, `blacklist-binance.json`, `exampleconfig.json`,
`exampleconfig_secret.json`). Without this filter, every unrelated upstream commit
would restart the bot; `configs/` alone holds ~70 pairlist files for other exchanges.

At `UPDATE_INTERVAL=900` the realistic rate is a few restarts per day. Each restart
is ~30-60s with no exit monitoring, acceptable on a 5m timeframe. Raise the interval
to reduce it.

### Accepted cost

Mounting `/var/run/docker.sock` gives the updater container root-equivalent
authority on the host. Accepted because the sidecar approach was chosen deliberately
over a host systemd timer, and because the container installs nothing at runtime.

## 7. Configuration changes

`user_data/config.json`: `add_config_files` entries change from `../configs/...` to
`../NostalgiaForInfinity/configs/...`, since `configs/` now lives inside the upstream
clone. This removes a mount and makes the paths self-describing. All risk values are
unchanged (`tradable_balance_ratio: 0.90`, `nfi_parameters.futures_mode_leverage: 2.0`
and the two rebuy/grind variants, `strategy: NFIX7Risk`). The ~60 lines of inline
reasoning are compressed to one or two lines per setting, with the full analysis
preserved in this document's §4 and in `NFIX7Risk.py`'s docstring.

`.env`: carried over from `backupRepoOld/.env` minus `FREQTRADE__LOGFILE`, plus
`UPDATE_INTERVAL`, `WATCH_PATHS`, `TARGET_CONTAINER`, `UPDATER_DRY_RUN`.
`COMPOSE_PROJECT_NAME` is set so compose commands work from any directory.

`.env.example` mirrors it with placeholders and `FREQTRADE__DRY_RUN=true`, so a fresh
machine cannot accidentally trade real money on first boot.

## 8. Portability

`setup.sh`, idempotent, safe to re-run:

1. Clone NFI if absent:
   `git clone --filter=blob:none --single-branch -b main https://github.com/iterativv/NostalgiaForInfinity`
2. `cp .env.example .env` if `.env` is absent, then print which variables need values
   and exit (so a half-configured stack never starts).
3. `mkdir -p user_data/logs user_data/data updater/state`
4. `docker compose up -d --build`

New machine: `git clone <this repo> && cd money-generator && ./setup.sh`, fill
`.env`, `./setup.sh` again.

`--filter=blob:none`: NFI's `.git` is 295MB because its history holds thousands of
revisions of multi-megabyte generated `.py` files, against a ~17MB working tree. A
partial clone fetches only the blobs the current checkout needs while keeping the
full commit graph, so the updater's `git diff OLD NEW` still works (missing blobs are
fetched on demand). Shallow `--depth` was rejected for exactly that reason: it breaks
diffs between successive fetches, which is what the updater depends on.

Moving the *live* bot to another machine additionally means copying the sqlite
database and the profit-target cache, i.e. the §9 cutover steps.

## 9. Cutover and verification

### Pre-cutover — live bot untouched

`docker compose run --rm` does not publish ports, so it cannot collide with the
running bot, and neither command below opens the database.

- `docker compose run --rm freqtrade list-strategies --userdir /freqtrade/user_data`
  → must list `NFIX7Risk`. Proves the symlink and the subclass import work in-container.
- `docker compose run --rm freqtrade show-config --config user_data/config.json`
  → must show `strategy=NFIX7Risk`, `tradable_balance_ratio=0.90`,
  `max_open_trades=12`, `timeframe=5m`, `dry_run=false`. Proves `add_config_files`
  resolves against the new paths and the deep merge lands as intended.
- Updater happy path: `git -C NostalgiaForInfinity reset --hard HEAD~5`, then
  `docker compose run --rm -e UPDATER_DRY_RUN=1 -e UPDATER_RUN_ONCE=1 updater`
  → must advance to `origin/main`, report the changed files, and log a would-restart
  decision without restarting anything.
- Updater guard: run the import command against a file with deliberate broken syntax
  → must exit non-zero.

### Cutover — ~1 minute of downtime

```bash
# 1. snapshot open positions BEFORE stopping — this is the acceptance test
#    ($API_USER/$API_PASS from .env; do not use $USER, the shell already owns it)
curl -s -u "$API_USER:$API_PASS" localhost:8989/api/v1/status  > before-status.json
curl -s -u "$API_USER:$API_PASS" localhost:8989/api/v1/balance > before-balance.json

# 2. clean stop: SIGTERM lets freqtrade checkpoint the WAL and close the DB
docker stop bottrade bottrade-watchdog
docker rm   bottrade bottrade-watchdog        # the new container reuses the name

# 3. carry state over (the live mount followed the rename into backupRepoOld)
cp backupRepoOld/user_data/khoakomlem_binance_futures-tradesv3.sqlite* user_data/
cp backupRepoOld/user_data/nfix7-profit_max-khoakomlem-binance-USDT.json user_data/

# 4. start
docker compose up -d

# 5. re-query status/balance; open trades and positions must match before-*.json
```

Stopping before copying is required: copying a sqlite file with an active WAL can
yield an inconsistent snapshot.

`nfix7-profit_max-khoakomlem-binance-USDT.json` is X7's per-pair profit-target cache
for open trades; without it, open positions recompute their targets from scratch.

### Post-cutover

Bot logs must show the open trades restored, protections registered, and no exchange
authentication error. `backupRepoOld/` stays untouched for a few days as the fallback,
then gets deleted.

## 10. Rescued artifacts

Recovered from the running container and the `nfi-watchdog:local` image before any
change, and verified byte-identical to the `backups/2026-07-30-pre-symlink/` copies
where those existed:

- `NFIX7Risk.py` (4169 bytes) — adopted into `user_data/strategies/`.
- `user_data/config.json` (4581 bytes) — adopted.
- `nfi_watchdog/` (6 modules, 736 lines: REST + log-file collector, rule engine,
  Telegram notifier, Prometheus `/metrics` on 9101) — **not** integrated. Its only
  durable copy is the `nfi-watchdog:local` image, so it is archived to
  `backups/rescued-nfi-watchdog/` rather than left dependent on an image surviving.

## 11. Operational rules

- `NostalgiaForInfinity/` is upstream-only. Local edits there are destroyed by
  `git reset --hard`.
- `.env` is never committed; `.env.example` is the tracked template.
- The updater writes only to `/nfi` and `/state`. It never touches `user_data/` or
  the database.
- `rollback.sh`: `git -C NostalgiaForInfinity reset --hard $(cat updater/state/previous_sha)`
  then `docker restart bottrade`. For when a new version imports cleanly but trades
  badly — the case the automated guard cannot detect. It rewinds exactly one update:
  `previous_sha` is overwritten on every applied update, so rolling back further means
  picking a commit by hand from `git -C NostalgiaForInfinity log`. Note that the
  updater will pull forward again on its next iteration, so a rollback meant to stick
  needs `docker stop bottrade-updater` alongside it.
- A `git fetch` failure (network) is not an error: the loop retries next interval.

## 12. Risks accepted

- **Untested upstream code reaches live money.** Inherent to the full-auto choice,
  made knowingly. Mitigated only by the import guard and one-command rollback; the
  guard catches broken code, not unprofitable code.
- **`docker.sock` exposure** (§6).
- **~30-60s blind window per restart**, bounded by `UPDATE_INTERVAL` and `WATCH_PATHS`.
