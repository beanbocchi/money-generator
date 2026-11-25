# ADR-0002: Track upstream automatically behind an import guard and a config guard

- Status: accepted
- Date: 2026-07-30; config guard added 2026-08-05; interrupted deploys handled 2026-11-25
- Code: `internal/updater` (`cycle.go`: import and config guards, `deploying` record; `rollback.go`)
- Tests: `go test ./internal/updater`
- Related: ADR-0010 adds the fuse on top of this

## Context

Upstream pushes to `NostalgiaForInfinityX7.py` several times a day. Upstream's own
`nfi-updater` sidecar was removed from its compose file on 2026-04-11 and fetches
`${STRATEGY}.py` by name, which 404s for `NFIX7Risk`.

## Decision

A sidecar container fetches `origin/main` every `UPDATE_INTERVAL` (900s). It restarts
the bot only when a file in `WATCH_PATHS` changed: the strategy file and the five
`configs/` files `config.json` includes. Before restarting it runs two checks inside
the live container:

- importing `NFIX7Risk` with `python3 -P` and asserting the module came from
  `/freqtrade/user_data/strategies/`; without `-P` a stray file in `/freqtrade` once
  satisfied the import;
- `freqtrade show-config --config user_data/config.json`, which exercises the whole
  config merge chain in about two seconds (added 2026-08-05).

A failed check resets the clone to the old commit and leaves the bot alone. A passing
check records `previous_sha`; `bin/rollback.sh` pins back to it and stops the updater.

## Consequences

The guards catch code that does not load, not code that trades badly. The updater
mounts `/var/run/docker.sock`, which is root-equivalent on the host. Each restart costs
301-331s of the bot not processing (measured across 24 restarts).

## Interrupted deploys (added 2026-11-25)

On 2026-11-25 at 10:54 UTC the updater moved the clone to `3e3b6612` and started the
import guard. The guard's `docker exec` hung, and 25 minutes later the host was
rebooted. The restarted updater found the clone already on `3e3b6612` and logged "up to
date". That commit's `configs/pairlist-volume-binance-usdt.json` uses
`PairInformationFilter`, which freqtrade 2026.1 does not have. From 12:08 UTC, after
another reboot, the bot failed its config check on every start until freqtrade was
upgraded to 2026.10 at 15:13 UTC.

`deploy` now writes the running commit to `state/deploying`, fsyncs it, and only then
moves the clone. It removes the file when the update has been rolled back or the bot has
been restarted into it. At the start of each cycle, a `deploying` file means the last
deploy did not finish: the updater resets the clone to the commit in the file, removes
the file, pages, and handles the update again from there. A file that names no commit
stops every cycle until someone fixes the clone by hand. `bin/mg rollback` removes the
file after it pins the clone.

Both guards together must finish within `GUARD_TIMEOUT` seconds (default 300). A guard
that runs longer counts as failed.
