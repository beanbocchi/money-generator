# ADR-0001: Own project, upstream NFI as a replaceable dependency

- Status: accepted
- Date: 2026-07-30
- Code: `docker-compose.yml`, `user_data/`, `.gitignore`
- Source: `docs/superpowers/specs/2026-07-30-freqtrade-nfi-project-design.md`

## Context

Until 2026-07-30 the live bot ran out of a clone of `iterativv/NostalgiaForInfinity`
with the operator's own files inside it. `git pull` was risky, and three of the
operator's files (`NFIX7Risk.py`, a compose override, a 736-line watchdog) existed only
inside the running container. Moving that directory left the bot running but unable to
restart, because its bind-mount sources no longer existed.

## Decision

This repository owns everything that is ours: `user_data/config.json`,
`user_data/strategies/NFIX7Risk.py`, the compose stack and the sidecars.
`NostalgiaForInfinity/` is a gitignored clone that only the updater writes to (see
ADR-0002) and is mounted read-only into the bot as a directory. Risk settings are
overrides in `config.json` or attributes on `NFIX7Risk`, which subclasses
`NostalgiaForInfinityX7` and never edits it.

The trade database filename is derived from `FREQTRADE__BOT_NAME`,
`FREQTRADE__EXCHANGE__NAME` and `FREQTRADE__TRADING_MODE` and must stay
`khoakomlem_binance_futures-tradesv3.sqlite`; changing any of the three starts the bot
against an empty database.

## Consequences

Editing `NostalgiaForInfinityX7.py` directly would conflict with almost every upstream
commit, so all local behaviour goes through the subclass. Anything edited inside
`NostalgiaForInfinity/` is destroyed by the updater's `git reset --hard`.
