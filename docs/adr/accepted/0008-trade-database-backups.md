# ADR-0008: Snapshot the trade database every 6 hours

- Status: accepted
- Date: 2026-07-31
- Code: `services/dbbackup/backup.sh`
- Tests: `tests/test_dbbackup.sh`

## Context

The sqlite file is the only record of which real positions are open. Without it
freqtrade starts believing it holds nothing while the exchange account holds futures
positions. The project shipped without any backup until 2026-07-31.

## Decision

A sidecar takes a snapshot every 6h with sqlite's online backup API, since the bot
writes in WAL mode and a `cp` can catch the main file and `-wal` disagreeing. Each
snapshot passes `integrity_check` in a private temp directory and is renamed into
`backups/db/` only afterwards. 30 are kept. `user_data` is mounted read-only. Restore by
stopping the bot and copying a snapshot back into `user_data/`.

## Evidence

The first real snapshot restored to 211 trades with open #91 and #211, matching the live
database. The test suite caught a bug where a failed run in the same second as a good
snapshot deleted the good one.
