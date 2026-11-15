# Freqtrade + NFI Project Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn `/home/beanbocchi/other/money-generator` into a project the operator owns, which tracks upstream NostalgiaForInfinity automatically, and move the live bot onto it without losing trade state.

**Architecture:** The operator's repo holds compose, config, and a strategy subclass; upstream NFI is a gitignored partial clone that only an updater sidecar writes to. The sidecar polls `git fetch`, restarts the bot only when a file the bot actually loads has changed, and guards each update by importing the strategy inside the running container before restarting.

**Tech Stack:** Docker Compose, `freqtradeorg/freqtrade:stable`, `docker:cli` + bash for the sidecar, git partial clone. No Python packaging, no test framework — shell tests with stubbed `docker`/`git` binaries.

**Spec:** `docs/superpowers/specs/2026-07-30-freqtrade-nfi-project-design.md`

## Global Constraints

- **Do NOT use a git worktree for this plan.** The deliverable is this working directory: docker bind mounts resolve to absolute paths inside it, and Task 6 touches the live bot. A worktree would produce a stack that mounts the wrong directory.
- **Never run `docker compose up` before Task 6.** A second freqtrade against the same API keys and the same database is the worst possible failure here. Before cutover, only `docker compose run --rm <service> <read-only command>` is allowed. `container_name: bottrade` collides with the live container, which is a safety net, not a licence to try.
- **Any updater run before cutover must set `DRY_RUN=1`**, or it can restart the live bot.
- `NostalgiaForInfinity/` is upstream-only. Nothing but the updater writes there; the updater's `git reset --hard` destroys local edits.
- `.env` is never committed. `.gitignore` must also cover `backups/` — `backups/rescued-nfi-watchdog/container-env.txt` contains the exchange API secret and Telegram token.
- The trade database filename must stay exactly `khoakomlem_binance_futures-tradesv3.sqlite`. It is derived from `FREQTRADE__BOT_NAME=khoakomlem`, `FREQTRADE__EXCHANGE__NAME=binance`, `FREQTRADE__TRADING_MODE=futures`; changing any of those three starts the bot against an empty database.
- Risk values are carried over unchanged: `tradable_balance_ratio: 0.90`, all three `nfi_parameters` leverages `2.0`, `strategy: NFIX7Risk`, `FREQTRADE__MAX_OPEN_TRADES=12`, long 9 / short 4.
- KISS: no runtime dependency beyond `docker:cli` + `git` + `bash` + `curl` in the sidecar image. No bats, no pytest, no Python packaging.
- Shell scripts take their paths and their `docker` binary from environment variables with production defaults, so tests can point them at temp directories and stubs. This is the only reason they are testable at all — do not hardcode `/nfi`, `/state`, or `docker`.

---

## File Structure

| File | Responsibility |
|---|---|
| `.gitignore` | Keep secrets, upstream clone, DB, and backups out of git |
| `.env.example` | Tracked template; `FREQTRADE__DRY_RUN=true` |
| `.env` | Real secrets + tuning (gitignored) |
| `docker-compose.yml` | Two services: `freqtrade`, `updater` |
| `user_data/config.json` | Risk overrides + `add_config_files` into the upstream clone |
| `user_data/strategies/NFIX7Risk.py` | Risk-overlay subclass (`protections`) |
| `user_data/strategies/NostalgiaForInfinityX7.py` | Symlink into the upstream clone |
| `updater/Dockerfile` | `docker:cli` + git/bash/curl |
| `updater/update.sh` | Fetch → filter → guard → restart loop |
| `setup.sh` | Bootstrap a machine: clone, dirs, `.env`, up |
| `rollback.sh` | Pin NFI to `previous_sha`, stop updater, restart bot |
| `compare-status.sh` | Cutover acceptance: open trades before vs after |
| `tests/test_gitignore.sh` | Asserts secrets are ignored |
| `tests/test_config_paths.sh` | Asserts every `add_config_files` path exists |
| `tests/test_updater.sh` | Five behaviours of `update.sh` against stubs |
| `tests/test_setup.sh` | `setup.sh` bootstrap + idempotency against stubs |
| `tests/test_rollback.sh` | `rollback.sh` order of operations against stubs |
| `tests/test_compare_status.sh` | Match / mismatch detection |
| `README.md` | Operating instructions and the cutover runbook |

---

### Task 1: Repo skeleton, ignore rules, environment

**Files:**
- Create: `.gitignore`, `.env.example`, `.env`
- Test: `tests/test_gitignore.sh`

**Interfaces:**
- Consumes: nothing.
- Produces: `.env` with all `FREQTRADE__*` variables the compose file interpolates (`FREQTRADE__BOT_NAME`, `FREQTRADE__EXCHANGE__NAME`, `FREQTRADE__TRADING_MODE`, `FREQTRADE__API_SERVER__LISTEN_PORT`), plus updater knobs `UPDATE_INTERVAL`, `TARGET_CONTAINER`, `WATCH_PATHS`, `DRY_RUN`, `RUN_ONCE`, `HOST_UID`, `HOST_GID`, `DOCKER_GID`. Tasks 3-6 depend on `.env` existing.

- [ ] **Step 1: Write the failing test**

Create `tests/test_gitignore.sh`:

```bash
#!/usr/bin/env bash
# Asserts the ignore rules that keep secrets and bulk out of git.
set -uo pipefail
cd "$(dirname "$0")/.."
PASS=0; FAIL=0
ok() { echo "  ok: $1"; PASS=$((PASS+1)); }
no() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

must_ignore() {
  if git check-ignore -q "$1"; then ok "ignored: $1"; else no "NOT ignored: $1"; fi
}
must_track() {
  if git check-ignore -q "$1"; then no "wrongly ignored: $1"; else ok "trackable: $1"; fi
}

must_ignore .env
must_ignore NostalgiaForInfinity/
must_ignore backups/rescued-nfi-watchdog/container-env.txt
must_ignore backupRepoOld/
must_ignore updater/state/previous_sha
must_ignore user_data/khoakomlem_binance_futures-tradesv3.sqlite
must_ignore user_data/logs/bot.log
must_ignore user_data/data/
must_ignore user_data/nfix7-profit_max-khoakomlem-binance-USDT.json

must_track .env.example
must_track docker-compose.yml
must_track user_data/config.json
must_track user_data/strategies/NFIX7Risk.py
must_track updater/update.sh

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `bash tests/test_gitignore.sh`
Expected: FAIL — every `must_ignore` fails because `.gitignore` does not exist yet.

- [ ] **Step 3: Write `.gitignore`**

```gitignore
# secrets
.env

# upstream clone — owned by the updater, never by git
/NostalgiaForInfinity/

# old bot + rescued artifacts (backups/rescued-nfi-watchdog/container-env.txt has secrets)
/backups/
/backupRepoOld/

# updater runtime state
/updater/state/

# live trading state
user_data/*.sqlite
user_data/*.sqlite-shm
user_data/*.sqlite-wal
user_data/nfi*-profit_max-*.json
user_data/nfi-hold-trades.json
user_data/logs/
user_data/data/
user_data/backtest_results/
user_data/hyperopt_results/
user_data/plot/

__pycache__/
*.py[cod]
```

- [ ] **Step 4: Write `.env.example`**

```env
# Copy to .env and fill in. ./setup.sh does this for you.
COMPOSE_PROJECT_NAME=bottrade

FREQTRADE__BOT_NAME=changeme
FREQTRADE__MAX_OPEN_TRADES=12
FREQTRADE__FUTURES_MAX_OPEN_TRADES_LONG=9
FREQTRADE__FUTURES_MAX_OPEN_TRADES_SHORT=4

FREQTRADE__TRADING_MODE=futures
FREQTRADE__MARGIN_MODE=isolated
FREQTRADE__STRATEGY=NFIX7Risk

FREQTRADE__EXCHANGE__NAME=binance
FREQTRADE__EXCHANGE__KEY=
FREQTRADE__EXCHANGE__SECRET=

FREQTRADE__TELEGRAM__ENABLED=false
FREQTRADE__TELEGRAM__TOKEN=
FREQTRADE__TELEGRAM__CHAT_ID=

FREQTRADE__API_SERVER__ENABLED=true
FREQTRADE__API_SERVER__LISTEN_PORT=8989
FREQTRADE__API_SERVER__USERNAME=
FREQTRADE__API_SERVER__PASSWORD=
FREQTRADE__API_SERVER__JWT_SECRET_KEY=
FREQTRADE__API_SERVER__WS_TOKEN=

TZ=Asia/Ho_Chi_Minh

# true on a fresh machine. Set false only when you mean real money.
FREQTRADE__DRY_RUN=true

# --- updater ---
UPDATE_INTERVAL=900
TARGET_CONTAINER=bottrade
WATCH_PATHS=NostalgiaForInfinityX7.py configs/trading_mode-futures.json configs/pairlist-volume-binance-usdt.json configs/blacklist-binance.json configs/exampleconfig.json configs/exampleconfig_secret.json
DRY_RUN=0
RUN_ONCE=0

# Host identity for the updater container: it must run as the user that owns the
# NostalgiaForInfinity clone, and be in the docker group to reach the socket.
# On a new machine: id -u / id -g / getent group docker | cut -d: -f3
HOST_UID=1000
HOST_GID=1000
DOCKER_GID=968
```

- [ ] **Step 5: Create the real `.env` from the old bot**

```bash
cp backupRepoOld/.env .env
```

Then edit `.env` to match `.env.example`'s variable set:
- Delete the line `FREQTRADE__LOGFILE=user_data/logs/freqtrade.log` — `--log-file` in compose supersedes it.
- Change `COMPOSE_PROJECT_NAME=bottrade` (already correct in the old file — verify, don't assume).
- Append the `# --- updater ---` block and the `HOST_UID`/`HOST_GID`/`DOCKER_GID` block from `.env.example` verbatim, keeping `DRY_RUN=0`. Fill the three host values from this machine: `id -u`, `id -g`, `getent group docker | cut -d: -f3`.
- Leave `FREQTRADE__DRY_RUN=false` — this machine trades for real.
- Leave `FREQTRADE__STRATEGY=NFIX7Risk`, `FREQTRADE__BOT_NAME=khoakomlem`, `TZ=Asia/Ho_Chi_Minh` and every credential untouched.

Verify no variable was lost:

```bash
diff <(grep -o '^[A-Z_]*=' backupRepoOld/.env | sort) <(grep -o '^[A-Z_]*=' .env | sort)
```

Expected: only `FREQTRADE__LOGFILE=` removed and the five updater variables added.

- [ ] **Step 6: Run the test to verify it passes**

Run: `bash tests/test_gitignore.sh`
Expected: `passed=14 failed=0`, exit 0.

- [ ] **Step 7: Commit**

```bash
git add .gitignore .env.example tests/test_gitignore.sh
git status --short   # confirm .env is NOT staged
git commit -m "chore: ignore rules, env template, and a test that secrets stay out of git"
```

---

### Task 2: user_data — config, strategy subclass, symlink

**Files:**
- Create: `user_data/config.json`, `user_data/strategies/NFIX7Risk.py`, `user_data/strategies/NostalgiaForInfinityX7.py` (symlink)
- Test: `tests/test_config_paths.sh`

**Interfaces:**
- Consumes: `NostalgiaForInfinity/` must already be cloned (it is — verify with `git -C NostalgiaForInfinity rev-parse HEAD`).
- Produces: strategy class `NFIX7Risk` discoverable at `user_data/strategies/`, and a `config.json` whose `add_config_files` resolve relative to `user_data/`. Task 3's compose file mounts `./user_data` and `./NostalgiaForInfinity` such that these paths hold inside the container.

- [ ] **Step 1: Write the failing test**

Create `tests/test_config_paths.sh`. This catches the one substantive change to `config.json` — the `../configs/` → `../NostalgiaForInfinity/configs/` rewrite — and the symlink, which is the piece most likely to be silently broken.

```bash
#!/usr/bin/env bash
# Every add_config_files path in user_data/config.json must resolve, and the
# strategy symlink must point at a real upstream file.
set -uo pipefail
cd "$(dirname "$0")/.."
PASS=0; FAIL=0
ok() { echo "  ok: $1"; PASS=$((PASS+1)); }
no() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

# config.json uses // comments (freqtrade parses it with rapidjson in comment mode),
# so strip them before feeding it to python's json.
INCLUDES=$(sed 's://.*::' user_data/config.json | python3 -c '
import json,sys
print("\n".join(json.load(sys.stdin)["add_config_files"]))')

[ -n "$INCLUDES" ] && ok "config.json parses and lists includes" || no "config.json unparsable or has no add_config_files"

while IFS= read -r inc; do
  [ -z "$inc" ] && continue
  # paths are relative to the directory holding config.json
  if [ -f "user_data/$inc" ]; then ok "include resolves: $inc"; else no "include MISSING: $inc"; fi
done <<< "$INCLUDES"

STRAT_VALUE=$(sed 's://.*::' user_data/config.json | python3 -c '
import json,sys; print(json.load(sys.stdin).get("strategy",""))')
[ "$STRAT_VALUE" = "NFIX7Risk" ] && ok "strategy=NFIX7Risk" || no "strategy is '$STRAT_VALUE', expected NFIX7Risk"

LINK=user_data/strategies/NostalgiaForInfinityX7.py
[ -L "$LINK" ] && ok "$LINK is a symlink" || no "$LINK is not a symlink"
[ -e "$LINK" ] && ok "symlink resolves on host" || no "symlink dangling"
[ "$(readlink "$LINK")" = "../../NostalgiaForInfinity/NostalgiaForInfinityX7.py" ] \
  && ok "symlink is relative to the project root" \
  || no "symlink target is '$(readlink "$LINK")', expected ../../NostalgiaForInfinity/NostalgiaForInfinityX7.py"

python3 -c "import ast,sys; ast.parse(open('user_data/strategies/NFIX7Risk.py').read())" 2>/dev/null \
  && ok "NFIX7Risk.py is syntactically valid" || no "NFIX7Risk.py has a syntax error"

grep -q 'def protections' user_data/strategies/NFIX7Risk.py \
  && ok "NFIX7Risk defines protections" || no "NFIX7Risk lost its protections property"

# leverage must stay at 2.0 on all three keys
for k in futures_mode_leverage futures_mode_leverage_rebuy_mode futures_mode_leverage_grind_mode; do
  V=$(sed 's://.*::' user_data/config.json | python3 -c "
import json,sys; print(json.load(sys.stdin)['nfi_parameters']['$k'])")
  [ "$V" = "2.0" ] && ok "$k=2.0" || no "$k is $V, expected 2.0"
done

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `bash tests/test_config_paths.sh`
Expected: FAIL — `user_data/config.json` does not exist, so the `sed`/`python3` pipeline produces nothing.

- [ ] **Step 3: Create `user_data/config.json`**

```jsonc
{
  "add_config_files": [
    "../NostalgiaForInfinity/configs/trading_mode-futures.json",
    "../NostalgiaForInfinity/configs/pairlist-volume-binance-usdt.json",
    "../NostalgiaForInfinity/configs/blacklist-binance.json",
    "../NostalgiaForInfinity/configs/exampleconfig.json",
    "../NostalgiaForInfinity/configs/exampleconfig_secret.json"
  ],

  // Keys below override the files above (main config wins in deep_merge_dicts).
  // Risk settings live HERE so that pulling upstream never clobbers them.
  // Full reasoning: docs/superpowers/specs/2026-07-30-freqtrade-nfi-project-design.md

  // 0.90 rather than 0.99 leaves headroom for NFI's grind mode, which can demand
  // several times a slot's stake. At 267 USDT over 12 slots this gives a ~20 USDT
  // stake, which keeps BTC/USDT tradeable (minNotional 50 => min stake 25 at 2x,
  // and freqtrade tops a stake up when the gap is <=30%).
  "tradable_balance_ratio": 0.90,

  // Was 3.0. At 3x a -33% adverse move wipes the trade's margin, and NFI's doom
  // stop (0.20) can be gapped straight through on a 5m alt candle -- that is how
  // trades #52 and #13 reached liquidation. 2x doubles the gap headroom and scales
  // realised P&L down by ~1/3. Revert to 3.0 to undo.
  "nfi_parameters": {
    "futures_mode_leverage": 2.0,
    "futures_mode_leverage_rebuy_mode": 2.0,
    "futures_mode_leverage_grind_mode": 2.0
  },

  // Per-pair cooldown after a pair has repeatedly hurt the account. It is declared
  // on the strategy class in strategies/NFIX7Risk.py, not here: this freqtrade
  // version hard-rejects "protections" in the config file.
  "strategy": "NFIX7Risk"
}
```

- [ ] **Step 4: Restore `NFIX7Risk.py` unchanged**

```bash
mkdir -p user_data/strategies
cp backups/2026-07-30-pre-symlink/NFIX7Risk.py user_data/strategies/NFIX7Risk.py
```

This copy was verified byte-identical to the one inside the running container. Change nothing in it — its `sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))` already resolves to the directory the symlink from Step 5 lives in, so `from NostalgiaForInfinityX7 import NostalgiaForInfinityX7` works under the new layout with no edit.

- [ ] **Step 5: Create the symlink**

```bash
ln -s ../../NostalgiaForInfinity/NostalgiaForInfinityX7.py \
      user_data/strategies/NostalgiaForInfinityX7.py
```

Relative, not absolute: it has to resolve both on the host and inside the container, where the project root is `/freqtrade`.

- [ ] **Step 6: Run the test to verify it passes**

Run: `bash tests/test_config_paths.sh`
Expected: `passed=15 failed=0`, exit 0. If an include is reported missing, the filename differs from upstream's `configs/` — list it with `ls NostalgiaForInfinity/configs/ | grep <name>` and fix the path in `config.json` rather than inventing the file.

- [ ] **Step 7: Commit**

```bash
git add user_data/config.json user_data/strategies/NFIX7Risk.py \
        user_data/strategies/NostalgiaForInfinityX7.py tests/test_config_paths.sh
git commit -m "feat: user_data config and NFIX7Risk overlay pointing at the upstream clone"
```

---

### Task 3: docker-compose.yml, verified inside the container

**Files:**
- Create: `docker-compose.yml`
- Modify: none

**Interfaces:**
- Consumes: `.env` (Task 1), `user_data/` (Task 2), `updater/` (Task 4 — declared here, built there).
- Produces: services `freqtrade` (container `bottrade`) and `updater` (container `bottrade-updater`); mount points `/freqtrade/user_data` and `/freqtrade/NostalgiaForInfinity`; `/nfi` and `/state` inside the updater.

Because Task 4 has not built `updater/` yet, `docker compose config` will validate but `docker compose build updater` will fail until then. That is expected — only the `freqtrade` service is exercised in this task.

- [ ] **Step 1: Write `docker-compose.yml`**

```yaml
---
services:
  freqtrade:
    image: freqtradeorg/freqtrade:stable
    container_name: bottrade
    restart: unless-stopped
    env_file:
      - .env
    ports:
      - "${FREQTRADE__API_SERVER__LISTEN_PORT:-8989}:${FREQTRADE__API_SERVER__LISTEN_PORT:-8989}"
    volumes:
      - ./user_data:/freqtrade/user_data
      # directory-level, never file-level: git replaces files by rename, and a
      # single-file bind mount would keep serving the old inode
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

  updater:
    build: ./updater
    container_name: bottrade-updater
    restart: unless-stopped
    # Run as the host user that owns the clone, not root. As root, git refuses the
    # bind-mounted repo ("detected dubious ownership in repository at '/nfi'") and
    # every file it did write would come out root-owned, which then breaks
    # ./rollback.sh when you run it from the host. group_add is the docker group's
    # GID, needed to reach the socket as a non-root user.
    user: "${HOST_UID:-1000}:${HOST_GID:-1000}"
    group_add:
      - "${DOCKER_GID:-968}"
    env_file:
      - .env
    environment:
      - HOME=/tmp
      - NFI_DIR=/nfi
      - STATE_DIR=/state
      - TARGET_CONTAINER=${TARGET_CONTAINER:-bottrade}
      - UPDATE_INTERVAL=${UPDATE_INTERVAL:-900}
      - WATCH_PATHS=${WATCH_PATHS:-NostalgiaForInfinityX7.py configs/}
      # Names match what update.sh reads, deliberately. An earlier revision mapped
      # DRY_RUN=${UPDATER_DRY_RUN:-0}, which made `docker compose run -e
      # UPDATER_DRY_RUN=1` silently ineffective: compose resolves the right-hand side
      # from .env at parse time, so -e set a variable the script never reads and the
      # "dry" run restarted the bot for real. One name, no indirection.
      - DRY_RUN=${DRY_RUN:-0}
      - RUN_ONCE=${RUN_ONCE:-0}
      - STRATEGY_MODULE=${FREQTRADE__STRATEGY:-NFIX7Risk}
    volumes:
      - ./NostalgiaForInfinity:/nfi
      - ./updater/state:/state
      - /var/run/docker.sock:/var/run/docker.sock
```

`HOST_UID`, `HOST_GID` and `DOCKER_GID` come from `.env` (Task 1). On this machine they
are `1000`, `1000`, `968`. On another machine: `id -u`, `id -g`,
`getent group docker | cut -d: -f3`.

- [ ] **Step 2: Validate interpolation**

Run: `docker compose config | grep -E 'db-url|container_name|source:'`
Expected: the db-url reads `sqlite:////freqtrade/user_data/khoakomlem_binance_futures-tradesv3.sqlite` — exactly the live filename. If any of the three variables is empty the name will be malformed (`__-tradesv3.sqlite`); fix `.env` before continuing, because a wrong name means an empty database.

- [ ] **Step 3: Verify the strategy loads inside the container**

Run:

```bash
docker compose run --rm freqtrade list-strategies --userdir /freqtrade/user_data
```

Expected: output contains `NFIX7Risk` **and** `NostalgiaForInfinityX7`. This is the real test of Task 2's symlink and subclass — it proves that inside the container `import NostalgiaForInfinityX7` resolves through the symlink and that the subclass body executes under the actual freqtrade version.

`run --rm` does not publish ports and does not trade, so it cannot collide with the live bot.

If it fails with `ModuleNotFoundError: NostalgiaForInfinityX7`, the symlink did not resolve in-container: check that `docker compose config` shows the `./NostalgiaForInfinity` mount at `/freqtrade/NostalgiaForInfinity` exactly.

- [ ] **Step 4: Verify the merged configuration**

Run:

```bash
docker compose run --rm freqtrade show-config --config user_data/config.json
```

Expected, all four in one output: `"strategy": "NFIX7Risk"`, `"tradable_balance_ratio": 0.9`, `"max_open_trades": 12`, `"timeframe": "5m"`. `timeframe` comes from upstream's `exampleconfig.json` and must be `5m` — NFI requires it. `dry_run` will show `false`; that is correct for this machine and harmless here because `show-config` makes no exchange calls.

This step is what proves the `../NostalgiaForInfinity/configs/...` rewrite works through freqtrade's own loader, not just through the host-side check in Task 2.

- [ ] **Step 5: Commit**

```bash
git add docker-compose.yml
git commit -m "feat: compose stack mounting upstream NFI read-only beside owned user_data"
```

---

### Task 4: Updater sidecar

**Files:**
- Create: `updater/update.sh`, `updater/Dockerfile`
- Test: `tests/test_updater.sh`

**Interfaces:**
- Consumes: env `NFI_DIR` (default `/nfi`), `STATE_DIR` (`/state`), `DOCKER` (`docker`), `TARGET_CONTAINER` (`bottrade`), `STRATEGY_MODULE` (`NFIX7Risk`), `WATCH_PATHS`, `UPDATE_INTERVAL` (900), `DRY_RUN` (0), `RUN_ONCE` (0), and optionally `FREQTRADE__TELEGRAM__TOKEN` / `FREQTRADE__TELEGRAM__CHAT_ID`.
- Produces: `$STATE_DIR/previous_sha` — the commit that was checked out *before* the applied update. `rollback.sh` (Task 5) reads exactly this file and name.

- [ ] **Step 1: Write the failing test**

Create `tests/test_updater.sh`. It builds a throwaway upstream repo plus a clone and puts a fake `docker` on the path, so all five behaviours are exercised with no containers and no network.

```bash
#!/usr/bin/env bash
# Hermetic tests for updater/update.sh: temp git repos + a stubbed docker binary.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT="$HERE/../updater/update.sh"
PASS=0; FAIL=0
ok() { echo "  ok: $1"; PASS=$((PASS+1)); }
no() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

setup() {                       # echoes a fresh tmpdir with upstream/ + nfi/ + state/ + bin/
  local t; t=$(mktemp -d)
  git init -q -b main "$t/upstream"
  git -C "$t/upstream" config user.email t@example.com
  git -C "$t/upstream" config user.name  test
  git -C "$t/upstream" config commit.gpgsign false
  mkdir -p "$t/upstream/configs"
  echo "class X: pass"  > "$t/upstream/NostalgiaForInfinityX7.py"
  echo '{"a":1}'        > "$t/upstream/configs/blacklist-binance.json"
  echo "docs"           > "$t/upstream/README.md"
  git -C "$t/upstream" add -A
  git -C "$t/upstream" commit -qm init
  git clone -q "$t/upstream" "$t/nfi"
  mkdir -p "$t/state" "$t/bin"
  echo "$t"
}

stub_docker() {                 # $1=tmpdir  $2=exit code for `docker exec` (the guard)
  cat > "$1/bin/docker" <<STUB
#!/usr/bin/env bash
echo "\$@" >> "$1/docker.log"
[ "\$1" = "exec" ] && exit $2
exit 0
STUB
  chmod +x "$1/bin/docker"
  : > "$1/docker.log"
}

upstream_commit() {             # $1=tmpdir  $2=file  $3=content
  echo "$3" > "$1/upstream/$2"
  git -C "$1/upstream" add -A
  git -C "$1/upstream" commit -qm "change $2"
}

run_once() {                    # $1=tmpdir, rest=extra env assignments
  local t=$1; shift
  env NFI_DIR="$t/nfi" STATE_DIR="$t/state" DOCKER="$t/bin/docker" \
      TARGET_CONTAINER=bottrade STRATEGY_MODULE=NFIX7Risk \
      WATCH_PATHS="NostalgiaForInfinityX7.py configs/" \
      RUN_ONCE=1 UPDATE_INTERVAL=1 FREQTRADE__TELEGRAM__TOKEN="" \
      "$@" bash "$SCRIPT" 2>&1
}
head_of() { git -C "$1/nfi" rev-parse HEAD; }

echo "case 1: no upstream change -> no restart"
T=$(setup); stub_docker "$T" 0
run_once "$T" >/dev/null
grep -q restart "$T/docker.log" && no "restarted with nothing to do" || ok "no restart"
rm -rf "$T"

echo "case 2: watched file changed -> guard, restart, previous_sha"
T=$(setup); stub_docker "$T" 0
OLD=$(head_of "$T")
upstream_commit "$T" NostalgiaForInfinityX7.py "class X: pass  # v2"
run_once "$T" >/dev/null
grep -q "restart bottrade" "$T/docker.log" && ok "restarted the bot" || no "did not restart"
grep -q "^exec bottrade" "$T/docker.log" && ok "ran the import guard" || no "skipped the guard"
[ "$(cat "$T/state/previous_sha" 2>/dev/null)" = "$OLD" ] \
  && ok "previous_sha records the pre-update commit" \
  || no "previous_sha is '$(cat "$T/state/previous_sha" 2>/dev/null)', expected $OLD"
[ "$(head_of "$T")" != "$OLD" ] && ok "working tree advanced" || no "working tree did not advance"
rm -rf "$T"

echo "case 3: only an unwatched file changed -> advance, no restart"
T=$(setup); stub_docker "$T" 0
OLD=$(head_of "$T")
upstream_commit "$T" README.md "docs v2"
run_once "$T" >/dev/null
grep -q restart "$T/docker.log" && no "restarted for an unwatched file" || ok "no restart"
[ "$(head_of "$T")" != "$OLD" ] && ok "still advanced the clone" || no "clone did not advance"
rm -rf "$T"

echo "case 4: guard fails -> roll back, never restart"
T=$(setup); stub_docker "$T" 1
OLD=$(head_of "$T")
upstream_commit "$T" NostalgiaForInfinityX7.py "def broken("
run_once "$T" >/dev/null
grep -q restart "$T/docker.log" && no "restarted despite a failing guard" || ok "no restart"
[ "$(head_of "$T")" = "$OLD" ] && ok "rolled the clone back" || no "left the bad commit checked out"
[ -f "$T/state/previous_sha" ] && no "wrote previous_sha on a failed update" || ok "no previous_sha written"
rm -rf "$T"

echo "case 5: DRY_RUN=1 -> decide, do not act"
T=$(setup); stub_docker "$T" 0
upstream_commit "$T" NostalgiaForInfinityX7.py "class X: pass  # v3"
OUT=$(run_once "$T" DRY_RUN=1)
grep -q restart "$T/docker.log" && no "restarted in DRY_RUN" || ok "no restart in DRY_RUN"
echo "$OUT" | grep -qi "would restart" && ok "logged the would-restart decision" || no "no decision logged"
[ -f "$T/state/previous_sha" ] && no "wrote previous_sha in DRY_RUN" || ok "no previous_sha in DRY_RUN"
rm -rf "$T"

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `bash tests/test_updater.sh`
Expected: FAIL — `updater/update.sh` does not exist, so every case reports failures.

- [ ] **Step 3: Write `updater/update.sh`**

```bash
#!/usr/bin/env bash
# Tracks upstream NostalgiaForInfinity and restarts the bot only when a file the
# bot actually loads has changed, and only if the new code imports cleanly.
set -uo pipefail

NFI_DIR="${NFI_DIR:-/nfi}"
STATE_DIR="${STATE_DIR:-/state}"
DOCKER="${DOCKER:-docker}"
TARGET_CONTAINER="${TARGET_CONTAINER:-bottrade}"
STRATEGY_MODULE="${STRATEGY_MODULE:-NFIX7Risk}"
UPDATE_INTERVAL="${UPDATE_INTERVAL:-900}"
DRY_RUN="${DRY_RUN:-0}"
RUN_ONCE="${RUN_ONCE:-0}"
WATCH_PATHS="${WATCH_PATHS:-NostalgiaForInfinityX7.py configs/}"

log() { echo "[updater $(date -u +%FT%TZ)] $*"; }

notify() {
  [ -n "${FREQTRADE__TELEGRAM__TOKEN:-}" ] || return 0
  curl -sf -m 10 -X POST \
    "https://api.telegram.org/bot${FREQTRADE__TELEGRAM__TOKEN}/sendMessage" \
    -d chat_id="${FREQTRADE__TELEGRAM__CHAT_ID:-}" \
    -d text="$1" >/dev/null || log "telegram notify failed"
}

# Importing inside the running container validates the syntax of upstream's 3MB
# generated strategy, our subclass, and that both class bodies execute -- under the
# exact python and freqtrade version that will run the code. py_compile is not used:
# it writes __pycache__ next to the source, which is on a read-only mount, so it
# would fail for the wrong reason. Import failures to write bytecode are ignored
# silently by python, so read-only is fine here.
#
# python3 -P and the __file__ assertion are both load-bearing. Without -P, python
# prepends the process's cwd (/freqtrade) to sys.path, so a stray NostalgiaForInfinity
# checkout or a leftover strategy file sitting at /freqtrade/ can satisfy the import
# and the guard passes having validated a file the bot will never load. Observed for
# real against the pre-cutover container, which had exactly such a stray file.
guard() {
  "$DOCKER" exec "$TARGET_CONTAINER" python3 -P -c \
    "import sys; sys.path.insert(0,'/freqtrade/user_data/strategies'); import ${STRATEGY_MODULE} as m; assert m.__file__.startswith('/freqtrade/user_data/strategies/'), 'guard imported the wrong file: '+str(m.__file__)"
}

one_cycle() {
  if ! git -C "$NFI_DIR" fetch -q origin main; then
    log "fetch failed (network?), retrying next cycle"
    return 0
  fi

  local old new changed
  old=$(git -C "$NFI_DIR" rev-parse HEAD)
  new=$(git -C "$NFI_DIR" rev-parse origin/main)
  if [ "$old" = "$new" ]; then
    log "up to date at ${old:0:8}"
    return 0
  fi

  # WATCH_PATHS is deliberately unquoted: it is a space-separated pathspec list.
  changed=$(git -C "$NFI_DIR" diff --name-only "$old" "$new" -- $WATCH_PATHS)

  # Advance unconditionally so the clone never drifts from upstream. Safe while the
  # bot is running: freqtrade reads the strategy only at startup, and the only thing
  # that restarts it is below, after the guard has passed.
  git -C "$NFI_DIR" reset --hard -q origin/main

  if [ -z "$changed" ]; then
    log "advanced ${old:0:8}->${new:0:8}, no watched file changed, not restarting"
    return 0
  fi
  log "watched files changed: $(echo $changed | tr '\n' ' ')"

  if ! guard; then
    git -C "$NFI_DIR" reset --hard -q "$old"
    log "guard FAILED for ${new:0:8}, rolled back to ${old:0:8}, bot untouched"
    notify "NFI ${new:0:8} failed to import - rolled back, bot untouched"
    return 0
  fi

  if [ "$DRY_RUN" = "1" ]; then
    log "DRY_RUN: would restart $TARGET_CONTAINER for ${old:0:8}->${new:0:8}"
    return 0
  fi

  mkdir -p "$STATE_DIR"
  echo "$old" > "$STATE_DIR/previous_sha"
  if "$DOCKER" restart "$TARGET_CONTAINER"; then
    log "restarted $TARGET_CONTAINER at ${new:0:8}"
    notify "NFI ${old:0:8}->${new:0:8}, bot restarted"
  else
    log "restart FAILED after applying ${new:0:8}"
    notify "NFI ${new:0:8} applied but restart FAILED - check the host"
  fi
}

log "started: interval=${UPDATE_INTERVAL}s target=$TARGET_CONTAINER dry_run=$DRY_RUN watch='$WATCH_PATHS'"
while true; do
  one_cycle
  [ "$RUN_ONCE" = "1" ] && break
  sleep "$UPDATE_INTERVAL"
done
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `bash tests/test_updater.sh`
Expected: `passed=13 failed=0`, exit 0.

- [ ] **Step 5: Write `updater/Dockerfile`**

```dockerfile
FROM docker:cli
# bash for the script, git for the pull, curl for Telegram notifications
RUN apk add --no-cache bash git curl
COPY update.sh /usr/local/bin/update.sh
RUN chmod +x /usr/local/bin/update.sh
ENTRYPOINT ["/usr/local/bin/update.sh"]
```

- [ ] **Step 6: Build it and confirm the entrypoint starts**

`TARGET_CONTAINER=bottrade` (the `.env` default) must not be used for this rehearsal:
`bottrade` is the pre-existing live container with the old `user_data` layout, not this
project's, so the guard would fail for the wrong reason (`ModuleNotFoundError`) and look
broken when it isn't. Point the updater at a disposable probe container with the new
layout instead:

```bash
cd /home/beanbocchi/other/money-generator
docker run -d --rm --name guard-probe --entrypoint sleep \
  -v "$PWD/user_data:/freqtrade/user_data" \
  -v "$PWD/NostalgiaForInfinity:/freqtrade/NostalgiaForInfinity:ro" \
  freqtradeorg/freqtrade:stable 900
```

`--entrypoint sleep` is required: the image's baked-in `ENTRYPOINT ["freqtrade"]` otherwise
takes `sleep 900` as its own argv and exits 2 with a usage error.

```bash
docker compose build updater
docker compose run --rm -e TARGET_CONTAINER=guard-probe -e DRY_RUN=1 \
  -e RUN_ONCE=1 \
  updater
```

`-e DRY_RUN=1` names the variable `update.sh` actually reads, and that is the point. An
earlier revision of this plan had `.env` carry `UPDATER_DRY_RUN` which compose mapped onto
`DRY_RUN`; because compose substitutes the right-hand side from `.env` at parse time,
`-e UPDATER_DRY_RUN=1` set a variable nothing read and the supposedly dry run restarted the
target for real. The indirection is gone — one name end to end.

Two other things this rehearsal depends on, both already handled in `docker-compose.yml`:
the updater runs as `${HOST_UID}:${HOST_GID}` with the docker group added, because as root
git rejects the bind-mounted clone ("detected dubious ownership in repository at '/nfi'")
and any file it wrote would come out root-owned, which then breaks host-side
`./rollback.sh`. And `updater/state/` must already exist and be owned by you before the
first compose run: compose creates a missing bind source as root, which is exactly how you
end up with a `previous_sha` you cannot delete. `setup.sh`'s `mkdir -p` (Task 5) is
load-bearing for that reason, not cosmetic.

Expected: a `[updater ...] started:` line, then either `up to date at <sha>` or an advance-plus-`DRY_RUN: would restart` line. Clean up with `docker rm -f guard-probe` once done with Step 7.

- [ ] **Step 7: End-to-end rehearsal against a real rewind**

Still against `guard-probe`, never `bottrade`. Run the happy path both ways so the guard
pass → `previous_sha` write → `docker restart` chain is exercised for real, not only
through the stubbed tests:

```bash
git -C NostalgiaForInfinity rev-parse HEAD > /tmp/nfi_head_before
REWIND=$(git -C NostalgiaForInfinity rev-parse HEAD~5)
git -C NostalgiaForInfinity reset --hard -q "$REWIND"
docker compose run --rm -e TARGET_CONTAINER=guard-probe -e DRY_RUN=1 \
  -e RUN_ONCE=1 \
  updater
# dry run only decides and advances the clone; rewind again for the real pass
git -C NostalgiaForInfinity reset --hard -q "$REWIND"
docker compose run --rm -e TARGET_CONTAINER=guard-probe -e DRY_RUN=0 \
  -e RUN_ONCE=1 \
  updater
git -C NostalgiaForInfinity rev-parse HEAD   # must equal /tmp/nfi_head_before
```

Expected: the dry pass reports the watched files that changed and logs
`DRY_RUN: would restart`; the real pass writes `updater/state/previous_sha` and actually
`docker restart`s `guard-probe` (its `StartedAt` moves forward). Both leave the clone at
`/tmp/nfi_head_before`. This exercises the real `git fetch` over the partial clone,
proving blob-on-demand fetching does not break `git diff`.

- [ ] **Step 8: Prove the guard can actually fail**

A guard that never fails is decoration. Run it against deliberately broken input:

```bash
docker compose run --rm --entrypoint sh freqtrade -c \
  'printf "def broken(\n" > /tmp/broken_mod.py; \
   python3 -c "import sys; sys.path.insert(0,\"/tmp\"); import broken_mod"; \
   echo "exit=$?"'
```

Expected: a `SyntaxError` and `exit=1`. Combined with test case 4 (which stubs this exit code), this closes the loop: the command reports failure on bad code, and `update.sh` refuses to restart when it does.

- [ ] **Step 9: Commit**

```bash
git add updater/update.sh updater/Dockerfile tests/test_updater.sh
git commit -m "feat: updater sidecar that pulls upstream and restarts only behind an import guard"
```

---

### Task 5: Bootstrap, rollback, README

**Files:**
- Create: `setup.sh`, `rollback.sh`, `README.md`
- Test: `tests/test_setup.sh`, `tests/test_rollback.sh`

**Interfaces:**
- Consumes: `$STATE_DIR/previous_sha` written by Task 4's `update.sh`; `.env.example` from Task 1; `docker-compose.yml` from Task 3.
- Produces: `./setup.sh` as the single entry point on a new machine; `./rollback.sh` as the manual escape hatch.

- [ ] **Step 1: Write the failing tests**

Create `tests/test_setup.sh`:

```bash
#!/usr/bin/env bash
# setup.sh against stubbed git and docker compose, in a throwaway project dir.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/.." && pwd)
PASS=0; FAIL=0
ok() { echo "  ok: $1"; PASS=$((PASS+1)); }
no() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

setup() {                        # throwaway project containing only what setup.sh needs
  local t; t=$(mktemp -d)
  cp "$ROOT/setup.sh" "$ROOT/.env.example" "$t/"
  mkdir -p "$t/bin"
  cat > "$t/bin/git" <<STUB
#!/usr/bin/env bash
echo "\$@" >> "$t/git.log"
# emulate a clone by creating the directory it would produce
if [ "\$1" = "clone" ]; then mkdir -p "\${@: -1}/.git"; fi
exit 0
STUB
  cat > "$t/bin/compose" <<STUB
#!/usr/bin/env bash
echo "\$@" >> "$t/compose.log"
exit 0
STUB
  chmod +x "$t/bin/git" "$t/bin/compose"
  : > "$t/git.log"; : > "$t/compose.log"
  echo "$t"
}
run_setup() { local t=$1; (cd "$t" && env GIT="$t/bin/git" COMPOSE="$t/bin/compose" bash ./setup.sh 2>&1); }

echo "case 1: no .env -> create it, explain, and refuse to start"
T=$(setup)
OUT=$(run_setup "$T"); RC=$?
[ "$RC" -ne 0 ] && ok "exited non-zero" || no "exited 0 with an unconfigured .env"
[ -f "$T/.env" ] && ok "created .env" || no "did not create .env"
grep -q clone "$T/git.log" && ok "cloned upstream" || no "did not clone"
[ -s "$T/compose.log" ] && no "started the stack without configuration" || ok "did not start the stack"
echo "$OUT" | grep -q "FREQTRADE__EXCHANGE__KEY" && ok "named the empty variables" || no "did not say what to fill in"
[ -d "$T/user_data/logs" ] && ok "created user_data/logs" || no "missing user_data/logs"
[ -d "$T/updater/state" ] && ok "created updater/state" || no "missing updater/state"
rm -rf "$T"

echo "case 2: configured .env -> start the stack"
T=$(setup)
sed 's/^\([A-Z_]*\)=$/\1=filled/' "$T/.env.example" > "$T/.env"
OUT=$(run_setup "$T"); RC=$?
[ "$RC" -eq 0 ] && ok "exited 0" || no "exited $RC with a configured .env: $OUT"
grep -q "up -d" "$T/compose.log" && ok "brought the stack up" || no "did not bring the stack up"
rm -rf "$T"

echo "case 3: clone already present -> do not re-clone"
T=$(setup)
mkdir -p "$T/NostalgiaForInfinity/.git"
sed 's/^\([A-Z_]*\)=$/\1=filled/' "$T/.env.example" > "$T/.env"
run_setup "$T" >/dev/null
grep -q clone "$T/git.log" && no "re-cloned over an existing checkout" || ok "skipped the clone"
rm -rf "$T"

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
```

Create `tests/test_rollback.sh`:

```bash
#!/usr/bin/env bash
# rollback.sh must stop the updater BEFORE pinning, or the updater pulls forward again.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/.." && pwd)
PASS=0; FAIL=0
ok() { echo "  ok: $1"; PASS=$((PASS+1)); }
no() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

setup() {
  local t; t=$(mktemp -d)
  cp "$ROOT/rollback.sh" "$t/"
  git init -q -b main "$t/nfi"
  git -C "$t/nfi" config user.email t@example.com
  git -C "$t/nfi" config user.name test
  git -C "$t/nfi" config commit.gpgsign false
  echo v1 > "$t/nfi/f"; git -C "$t/nfi" add -A; git -C "$t/nfi" commit -qm v1
  echo v2 > "$t/nfi/f"; git -C "$t/nfi" add -A; git -C "$t/nfi" commit -qm v2
  mkdir -p "$t/state" "$t/bin"
  cat > "$t/bin/docker" <<STUB
#!/usr/bin/env bash
echo "\$@" >> "$t/docker.log"
exit 0
STUB
  chmod +x "$t/bin/docker"; : > "$t/docker.log"
  echo "$t"
}
run_rb() { local t=$1; (cd "$t" && env NFI_DIR="$t/nfi" STATE_DIR="$t/state" \
  DOCKER="$t/bin/docker" TARGET_CONTAINER=bottrade UPDATER_CONTAINER=bottrade-updater \
  bash ./rollback.sh 2>&1); }

echo "case 1: no previous_sha -> refuse"
T=$(setup)
run_rb "$T" >/dev/null; RC=$?
[ "$RC" -ne 0 ] && ok "refused without previous_sha" || no "proceeded with nothing to roll back to"
[ -s "$T/docker.log" ] && no "touched docker anyway" || ok "did not touch docker"
rm -rf "$T"

echo "case 2: rolls back, stopping the updater first"
T=$(setup)
FIRST=$(git -C "$T/nfi" rev-list --max-parents=0 HEAD)
echo "$FIRST" > "$T/state/previous_sha"
run_rb "$T" >/dev/null; RC=$?
[ "$RC" -eq 0 ] && ok "exited 0" || no "exited $RC"
[ "$(git -C "$T/nfi" rev-parse HEAD)" = "$FIRST" ] && ok "pinned to previous_sha" || no "did not pin"
[ "$(cat "$T/nfi/f")" = "v1" ] && ok "working tree is the old content" || no "working tree not rewound"
grep -q "stop bottrade-updater" "$T/docker.log" && ok "stopped the updater" || no "left the updater running"
grep -q "restart bottrade" "$T/docker.log" && ok "restarted the bot" || no "did not restart the bot"
[ "$(grep -n 'stop bottrade-updater' "$T/docker.log" | cut -d: -f1)" -lt \
  "$(grep -n 'restart bottrade' "$T/docker.log" | cut -d: -f1)" ] \
  && ok "stopped the updater before restarting the bot" \
  || no "wrong order: the updater could pull forward mid-rollback"
rm -rf "$T"

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
```

- [ ] **Step 2: Run both to make sure they fail**

Run: `bash tests/test_setup.sh; bash tests/test_rollback.sh`
Expected: both FAIL — `setup.sh` and `rollback.sh` do not exist yet.

- [ ] **Step 3: Write `setup.sh`**

```bash
#!/usr/bin/env bash
# Bootstrap this project on a machine. Idempotent: safe to re-run.
set -euo pipefail
cd "$(dirname "$0")"

NFI_REPO="${NFI_REPO:-https://github.com/iterativv/NostalgiaForInfinity}"
GIT="${GIT:-git}"
COMPOSE="${COMPOSE:-docker compose}"

if [ ! -d NostalgiaForInfinity/.git ]; then
  echo "==> cloning NostalgiaForInfinity"
  # Partial clone: upstream's .git is ~295MB because its history holds thousands of
  # revisions of multi-megabyte generated .py files, against a ~17MB working tree.
  # blob:none fetches only the blobs the checkout needs and keeps the full commit
  # graph, so the updater's `git diff OLD NEW` still works. Shallow --depth is NOT
  # usable: it breaks exactly that diff between successive fetches.
  $GIT clone --filter=blob:none --single-branch -b main "$NFI_REPO" NostalgiaForInfinity
else
  echo "==> NostalgiaForInfinity already present, skipping clone"
fi

mkdir -p user_data/logs user_data/data updater/state

if [ ! -f .env ]; then
  cp .env.example .env
  echo
  echo "==> created .env from .env.example. Fill these in, then re-run ./setup.sh:"
  grep -n '=$' .env || true
  echo
  echo "    FREQTRADE__DRY_RUN is true. Set it to false only when you mean real money."
  echo "    To move a live bot here, also copy its *.sqlite* and nfi*-profit_max-*.json"
  echo "    into user_data/ before starting (see README.md)."
  exit 1
fi

echo "==> starting stack"
$COMPOSE up -d --build
$COMPOSE ps
```

- [ ] **Step 4: Write `rollback.sh`**

```bash
#!/usr/bin/env bash
# Pin upstream back one update, for when a new version imports fine but trades badly
# -- the case the updater's guard cannot detect.
set -euo pipefail
cd "$(dirname "$0")"

NFI_DIR="${NFI_DIR:-NostalgiaForInfinity}"
STATE_DIR="${STATE_DIR:-updater/state}"
DOCKER="${DOCKER:-docker}"
TARGET_CONTAINER="${TARGET_CONTAINER:-bottrade}"
UPDATER_CONTAINER="${UPDATER_CONTAINER:-bottrade-updater}"

if [ ! -f "$STATE_DIR/previous_sha" ]; then
  echo "no $STATE_DIR/previous_sha - the updater has not applied an update yet."
  echo "To pin a specific commit by hand:"
  echo "  $DOCKER stop $UPDATER_CONTAINER"
  echo "  git -C $NFI_DIR reset --hard <sha>   # pick one from git -C $NFI_DIR log"
  echo "  $DOCKER restart $TARGET_CONTAINER"
  exit 1
fi
SHA=$(cat "$STATE_DIR/previous_sha")

# Order matters: stop the updater FIRST. Otherwise its next cycle pulls forward again
# and silently undoes the rollback.
echo "==> stopping $UPDATER_CONTAINER so it cannot pull forward"
"$DOCKER" stop "$UPDATER_CONTAINER" || true

echo "==> pinning $NFI_DIR to $SHA"
git -C "$NFI_DIR" reset --hard -q "$SHA"

echo "==> restarting $TARGET_CONTAINER"
"$DOCKER" restart "$TARGET_CONTAINER"

echo
echo "Bot now runs ${SHA:0:8}. The updater is STOPPED, so this sticks."
echo "Re-enable auto-updates with: $DOCKER start $UPDATER_CONTAINER"
echo "(it will pull to latest again on its next cycle)"
```

- [ ] **Step 5: Run both tests to verify they pass**

Run: `bash tests/test_setup.sh && bash tests/test_rollback.sh`
Expected: `passed=10 failed=0` then `passed=8 failed=0`, both exit 0.

- [ ] **Step 6: Write `README.md`**

```markdown
# money-generator

Freqtrade running NostalgiaForInfinityX7 with a risk overlay, tracking upstream
automatically.

Design and rationale: `docs/superpowers/specs/2026-07-30-freqtrade-nfi-project-design.md`

## Layout

- `user_data/` — **yours**: `config.json` (risk overrides), `strategies/NFIX7Risk.py`
  (the `protections` overlay), the trade database, logs.
- `NostalgiaForInfinity/` — **upstream, not yours**. A gitignored partial clone that
  only the updater writes to. Local edits there are destroyed by `git reset --hard`.
- `updater/` — the sidecar that pulls upstream and restarts the bot.

## Setup on a new machine

```bash
git clone <this repo> money-generator && cd money-generator
./setup.sh          # clones upstream, creates .env, tells you what to fill in
$EDITOR .env
./setup.sh          # starts the stack
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

## How auto-updates work

The `updater` container loops every `UPDATE_INTERVAL` seconds (default 900):

1. `git fetch origin main`; nothing new → sleep.
2. Did any path in `WATCH_PATHS` change? Those are the strategy file and the five
   `configs/` files `config.json` includes. Anything else → advance the clone,
   do not restart.
3. Import the strategy inside the running bot container. Fails → roll the clone back,
   notify, leave the bot alone.
4. Passes → record the previous commit, `docker restart`, notify.

Follow it with `docker compose logs -f updater`.

The guard catches broken code, not unprofitable code. For a version that loads fine
but trades badly:

```bash
./rollback.sh       # pins one update back and stops the updater so it sticks
docker start bottrade-updater   # re-enable auto-updates
```

## Everyday commands

```bash
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
```

- [ ] **Step 7: Commit**

```bash
git add setup.sh rollback.sh README.md tests/test_setup.sh tests/test_rollback.sh
chmod +x setup.sh rollback.sh
git update-index --chmod=+x setup.sh rollback.sh
git commit -m "feat: portable bootstrap, one-step rollback, and operating docs"
```

---

### Task 6: Cutover from the live bot

**Files:**
- Create: `compare-status.sh`
- Test: `tests/test_compare_status.sh`

**Interfaces:**
- Consumes: everything above, all tests green.
- Produces: the live bot running from this project.

**This task moves real money.** Do not start it until Tasks 1-5 are committed and every test passes. Read all steps before running any.

- [ ] **Step 1: Write the failing test**

Create `tests/test_compare_status.sh`:

```bash
#!/usr/bin/env bash
# compare-status.sh is the cutover acceptance gate: identical open trades before and
# after, or it must fail loudly.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT="$HERE/../compare-status.sh"
PASS=0; FAIL=0
ok() { echo "  ok: $1"; PASS=$((PASS+1)); }
no() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

T=$(mktemp -d)
cat > "$T/a.json" <<'JSON'
[{"trade_id": 2, "pair": "ETH/USDT:USDT", "amount": 0.5, "is_short": false},
 {"trade_id": 1, "pair": "BTC/USDT:USDT", "amount": 0.01, "is_short": false}]
JSON
# same trades, different order -- must still match
cat > "$T/b.json" <<'JSON'
[{"trade_id": 1, "pair": "BTC/USDT:USDT", "amount": 0.01, "is_short": false},
 {"trade_id": 2, "pair": "ETH/USDT:USDT", "amount": 0.5, "is_short": false}]
JSON
# a trade has vanished
cat > "$T/c.json" <<'JSON'
[{"trade_id": 1, "pair": "BTC/USDT:USDT", "amount": 0.01, "is_short": false}]
JSON
# same ids, different size
cat > "$T/d.json" <<'JSON'
[{"trade_id": 1, "pair": "BTC/USDT:USDT", "amount": 0.02, "is_short": false},
 {"trade_id": 2, "pair": "ETH/USDT:USDT", "amount": 0.5, "is_short": false}]
JSON

bash "$SCRIPT" "$T/a.json" "$T/b.json" >/dev/null 2>&1 \
  && ok "matches regardless of ordering" || no "reported a mismatch for identical trades"
bash "$SCRIPT" "$T/a.json" "$T/c.json" >/dev/null 2>&1 \
  && no "missed a vanished trade" || ok "detects a missing trade"
bash "$SCRIPT" "$T/a.json" "$T/d.json" >/dev/null 2>&1 \
  && no "missed a changed amount" || ok "detects a changed amount"
bash "$SCRIPT" "$T/a.json" "$T/missing.json" >/dev/null 2>&1 \
  && no "succeeded on a missing file" || ok "fails on a missing file"
rm -rf "$T"

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `bash tests/test_compare_status.sh`
Expected: FAIL — `compare-status.sh` does not exist.

- [ ] **Step 3: Write `compare-status.sh`**

```bash
#!/usr/bin/env bash
# Cutover gate: are the open trades identical before and after?
# Usage: ./compare-status.sh before-status.json after-status.json
set -euo pipefail

[ $# -eq 2 ] || { echo "usage: $0 before-status.json after-status.json"; exit 2; }
for f in "$1" "$2"; do [ -f "$f" ] || { echo "missing file: $f"; exit 2; }; done

extract() {
  python3 -c '
import json, sys
d = json.load(open(sys.argv[1]))
rows = d if isinstance(d, list) else d.get("trades", [])
for t in sorted(rows, key=lambda r: r["trade_id"]):
    print(t["trade_id"], t["pair"], t["amount"], t.get("is_short"))
' "$1"
}

if diff -u <(extract "$1") <(extract "$2"); then
  echo "MATCH: open trades are identical ($(extract "$1" | wc -l) trades)"
else
  echo
  echo "MISMATCH: open trades differ. Do NOT leave the bot running."
  echo "Stop it, restore the database from backupRepoOld/user_data/, and investigate."
  exit 1
fi
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `bash tests/test_compare_status.sh`
Expected: `passed=4 failed=0`, exit 0.

- [ ] **Step 5: Run every test one final time**

```bash
for t in tests/test_*.sh; do echo "== $t"; bash "$t" || { echo "STOP: $t failed"; break; }; done
```

Expected: all six suites pass. Do not proceed on any failure — after Step 7 the old bot no longer exists to fall back to.

- [ ] **Step 6: Snapshot the live bot**

```bash
mkdir -p cutover
API_USER=$(grep '^FREQTRADE__API_SERVER__USERNAME=' .env | cut -d= -f2-)
API_PASS=$(grep '^FREQTRADE__API_SERVER__PASSWORD=' .env | cut -d= -f2-)
curl -s -u "$API_USER:$API_PASS" localhost:8989/api/v1/status  > cutover/before-status.json
curl -s -u "$API_USER:$API_PASS" localhost:8989/api/v1/balance > cutover/before-balance.json
python3 -c "import json;d=json.load(open('cutover/before-status.json'));print('open trades:',len(d))"
```

Expected: a trade count, not an error. `$API_USER` deliberately, never `$USER` — the shell already owns that name. If curl returns nothing, the live bot's API is down and you must resolve that before cutting over blind.

- [ ] **Step 7: Stop and remove the old containers**

```bash
docker stop bottrade bottrade-watchdog
docker rm   bottrade bottrade-watchdog
```

`docker stop` sends SIGTERM, which lets freqtrade checkpoint the sqlite WAL and close the database cleanly — required before Step 8. `docker rm` is required because the new stack reuses the name `bottrade`. The watchdog is descoped; its source is archived in `backups/rescued-nfi-watchdog/`.

- [ ] **Step 8: Carry the trading state over**

```bash
cp backupRepoOld/user_data/khoakomlem_binance_futures-tradesv3.sqlite*  user_data/
cp backupRepoOld/user_data/nfix7-profit_max-khoakomlem-binance-USDT.json user_data/
ls -la user_data/*.sqlite* user_data/nfix7-profit_max-*.json
```

`backupRepoOld/user_data` is where the live database actually lives: the container's bind mount followed the inode when the directory was renamed. The `*` glob takes the `-shm` and `-wal` files along with the main database.

`nfix7-profit_max-...json` is X7's per-pair profit-target cache for open trades; without it, open positions recompute their targets from scratch.

- [ ] **Step 9: Start the new stack**

```bash
docker compose up -d
docker compose ps
docker compose logs -f freqtrade   # watch until it reports the open trades, then Ctrl-C
```

Expected in the logs: the strategy resolving as `NFIX7Risk`, the open trades restored, no exchange authentication error. This is the first `docker compose up` allowed by this plan.

- [ ] **Step 10: Run the acceptance gate**

```bash
sleep 60   # let the API come up
curl -s -u "$API_USER:$API_PASS" localhost:8989/api/v1/status  > cutover/after-status.json
curl -s -u "$API_USER:$API_PASS" localhost:8989/api/v1/balance > cutover/after-balance.json
./compare-status.sh cutover/before-status.json cutover/after-status.json
```

Expected: `MATCH: open trades are identical (N trades)`.

On `MISMATCH`: `docker compose down`, restore `user_data/*.sqlite*` from
`backupRepoOld/user_data/`, and investigate before restarting anything. The old bot's
containers are gone, but its database is intact.

- [ ] **Step 11: Confirm the updater is live and behaving**

```bash
docker compose logs --tail 30 updater
```

Expected: `started: interval=900s target=bottrade dry_run=0` followed by `up to date at <sha>`. `dry_run=0` matters — a leftover `DRY_RUN=1` in `.env` would leave auto-updates permanently inert.

- [ ] **Step 12: Commit and record the cutover**

```bash
echo "cutover/" >> .gitignore   # the snapshots hold account balances
git add .gitignore compare-status.sh tests/test_compare_status.sh
git commit -m "feat: cutover acceptance gate comparing open trades before and after"
```

- [ ] **Step 13: Leave the fallback in place**

Do **not** delete `backupRepoOld/` now. Keep it for a few days as the only copy of the pre-cutover database. Once the new stack has traded uneventfully and you are satisfied, delete it and this plan's job is done.

---

## Self-Review

**Spec coverage:**

| Spec section | Task |
|---|---|
| §4 layout, symlink rationale, NFIX7Risk kept | 2 |
| §5 freqtrade service | 3 |
| §6 updater, guard, WATCH_PATHS, DRY_RUN/RUN_ONCE | 4 |
| §7 config.json path rewrite, `.env` / `.env.example` | 1, 2 |
| §8 portability, partial clone, `setup.sh` | 5 |
| §9 pre-cutover verification | 3 (steps 3-4), 4 (steps 7-8) |
| §9 cutover + acceptance | 6 |
| §10 rescued artifacts archived | done before this plan; `.gitignore` covers the secret in it (Task 1) |
| §11 operational rules, `rollback.sh` incl. stop-updater-first | 5 |
| §12 risks accepted | README + this plan's Global Constraints |

**Placeholder scan:** no TBDs; every step carries the literal file content or command. The only judgement calls left to the implementer are Task 1 Step 5's hand-edit of `.env` (with a `diff` check to verify it) and Task 2 Step 6's fallback if an upstream `configs/` filename has changed.

**Type/name consistency:** `previous_sha` is written by `update.sh` (Task 4) and read by `rollback.sh` (Task 5) and `tests/test_rollback.sh` — one name throughout, and the earlier `last_good_sha` from the design discussion appears nowhere. `NFI_DIR` / `STATE_DIR` / `DOCKER` / `TARGET_CONTAINER` / `UPDATER_CONTAINER` / `STRATEGY_MODULE` / `WATCH_PATHS` / `DRY_RUN` / `RUN_ONCE` are spelled identically in `update.sh`, `rollback.sh`, `docker-compose.yml`, and every test. `DRY_RUN` and `RUN_ONCE` use one name from `.env` through compose into the script: the earlier two-tier `UPDATER_DRY_RUN` → `DRY_RUN` mapping made `docker compose run -e UPDATER_DRY_RUN=1` silently ineffective, because compose resolves the right-hand side from `.env` at parse time. A bare `DRY_RUN` reaching the freqtrade container is harmless — freqtrade reads only `FREQTRADE__*`.
