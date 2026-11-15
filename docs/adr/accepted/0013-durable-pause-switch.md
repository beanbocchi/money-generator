# ADR-0013: Pause entries with initial_state and /stopentry together

- Status: accepted
- Date: 2026-10-30
- Code: `user_data/config.json` `initial_state`

## Context

Entries were paused from 2026-10-30 to 2026-11-05 during the Binance hack, and resumed on
2026-11-05 16:12 UTC at the operator's request.

## Decision

`initial_state` is pinned in `config.json` instead of inherited from upstream. To pause
durably, set it to "paused" and call `POST /api/v1/stopentry`; `/start` resumes. The API
call alone is lost on the next restart, and the updater restarts the bot up to 4 times a
day. "paused" blocks new entries and grinding add-ons, and NFI still takes profit,
derisks and exits.

## Consequences

After a restart the API reports state "stopped" for 5-10 minutes before `initial_state`
applies. That is startup, not a fault.
