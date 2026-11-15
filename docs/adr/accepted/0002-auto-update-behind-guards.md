# ADR-0002: Track upstream automatically behind an import guard and a config guard

- Status: accepted
- Date: 2026-07-30; config guard added 2026-08-05
- Code: `services/updater/update.sh` (`guard`, `config_guard`), `bin/rollback.sh`
- Tests: `tests/test_updater.sh`, `tests/test_rollback.sh`
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
